//go:build windows

package child

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The OSC 7501 support reply must reach the job's console input without ever
// reaching its output: a console in cooked mode (line input with echo, the
// default — readline, input(), any fgets) echoes injected keys into the very
// stream the job's log carries, and ConPTY's input pipe offers no way around
// that (bytes written to it are parsed as VT input and swallowed, or encoded
// as win32-input-mode key events and delivered — and a cooked reader echoes
// them). So the reply is injected by a short-lived copy of incoda that
// attaches to the job's console — the supervisor cannot attach in place,
// AttachConsole would detach it from its own console, where its Ctrl-C
// handling lives — turns the console's echo off, writes the reply as key
// events with WriteConsoleInput, waits for the job to read them, and
// removes whatever was left unread before echo comes back. No reply byte is
// echoed whatever mode the console is in; what a job never reads is dropped,
// which is the silence of a terminal that did not answer.

// ConinjCommand is the hidden CLI entry the injector runs.
const ConinjCommand = "__coninj"

// How long the injector waits for the job to consume the reply events once
// their count stops falling (the job may read one event per loop turn), and
// the longest it stays at all.
const (
	coninjSettle = 500 * time.Millisecond
	coninjWait   = 2 * time.Second
)

// consoleInjector writes to a job's console input: one helper process per
// Write, so the supervisor's own console is never touched. The helper's
// exit code is the delivery verdict: exit 3 means the bytes were written
// but the job never read them, which for an owner's answer is a failure
// with its own name rather than a generic one.
type consoleInjector struct{ pid uint32 }

func (c consoleInjector) Write(b []byte) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(exe, ConinjCommand, strconv.FormatUint(uint64(c.pid), 10),
		base64.StdEncoding.EncodeToString(b))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return 0, verdictError(ee.ExitCode())
		}
		return 0, fmt.Errorf("console input injection failed: %w", err)
	}
	return len(b), nil
}

type keyEventRecord struct {
	KeyDown         int32
	RepeatCount     uint16
	VirtualKeyCode  uint16
	VirtualScanCode uint16
	Rune            uint16
	ControlKeyState uint32
}

type inputRecord struct {
	EventType uint16
	_         uint16
	Event     keyEventRecord
}

// keyEvents turns bytes into the down/up key-event pairs a console read
// consumes as typed characters. The console input buffer is UTF-16, so a
// non-BMP rune travels as its surrogate halves: truncating to one uint16
// would hand the job mojibake and no way to know.
func keyEvents(b []byte) []inputRecord {
	units := utf16.Encode([]rune(string(b)))
	recs := make([]inputRecord, 0, 2*len(units))
	for _, u := range units {
		down := inputRecord{EventType: windows.KEY_EVENT}
		down.Event = keyEventRecord{KeyDown: 1, RepeatCount: 1, Rune: u}
		recs = append(recs, down)
		up := down
		up.Event.KeyDown = 0
		recs = append(recs, up)
	}
	return recs
}

var k32 = windows.NewLazySystemDLL("kernel32.dll")

// InjectConsoleInput is the body of the hidden `__coninj <pid> <base64>`
// entry: attach to pid's console, write the decoded bytes as key events
// with echo suppressed, and leave the console as its job owns it. The
// exit code is the verdict: 0, the bytes were written and the job read
// them; 1, nothing was written (attach, console, mode or write failure - a
// terminal that cannot answer just does not); 3, the bytes were written
// but the job never read them, so they were flushed unread - which for a
// support reply is the silence of a terminal that did not answer, and for
// an owner's answer is the honest "nobody was listening" the answerer
// needs to hear instead of a false delivered.
func InjectConsoleInput(args []string) int {
	if len(args) != 2 {
		return 1
	}
	pid, err := strconv.ParseUint(args[0], 10, 32)
	if err != nil {
		return 1
	}
	b, err := base64.StdEncoding.DecodeString(args[1])
	if err != nil || len(b) == 0 {
		return 1
	}
	k32.NewProc("FreeConsole").Call()
	if r, _, _ := k32.NewProc("AttachConsole").Call(uintptr(pid)); r == 0 {
		return 1
	}
	defer k32.NewProc("FreeConsole").Call()
	h, err := windows.CreateFile(windows.StringToUTF16Ptr("CONIN$"), windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return 1
	}
	defer windows.CloseHandle(h)
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return 1
	}
	restore := func() {}
	if mode&windows.ENABLE_ECHO_INPUT != 0 {
		if err := windows.SetConsoleMode(h, mode&^windows.ENABLE_ECHO_INPUT); err != nil {
			return 1 // cannot suppress the echo: no reply is better than a leaked one
		}
		// The restore is not a rewind: the job may rewrite its own console
		// mode while the echo is off (going raw is what a program does the
		// moment it hears a terminal answer), and that choice is the job's,
		// not ours to revert. Only a console still exactly as we left it —
		// echo off, nothing else moved — goes back to the mode we found.
		restore = func() {
			var now uint32
			if err := windows.GetConsoleMode(h, &now); err == nil && now == mode&^windows.ENABLE_ECHO_INPUT {
				_ = windows.SetConsoleMode(h, mode)
			}
		}
	}
	defer restore()
	// base is the buffer's count BEFORE this write: everything above it
	// (save one) is this payload, whatever else the buffer held. The
	// verdict is decided against it, never against movement alone.
	var base uint32
	k32.NewProc("GetNumberOfConsoleInputEvents").Call(uintptr(h), uintptr(unsafe.Pointer(&base)))
	recs := keyEvents(b)
	var written uint32
	r1, _, _ := k32.NewProc("WriteConsoleInputW").Call(
		uintptr(h), uintptr(unsafe.Pointer(&recs[0])), uintptr(len(recs)), uintptr(unsafe.Pointer(&written)))
	if r1 == 0 || written == 0 {
		return 1
	}
	// Watch the payload drain. Delivered means everything this helper
	// wrote was consumed: the count fell back to base (plus one, for the
	// up-event a cooked line read can leave behind). Partial movement is
	// NOT delivery - a raw per-key reader (a line editor, the
	// terminal-reply loop itself) drains across reads with gaps far past
	// any poll cadence, and breaking early there would flush the unread
	// remainder under a reader that is still reading and call it
	// delivered. The settle window bounds the wait for exactly that
	// reader; a reader that never comes leaves the payload past base at
	// settle, which is the honest nobody-listened refusal.
	//
	// A blocked reader consumes within microseconds of the write, so the
	// first polls are micro-polls (backing off to a few milliseconds): a
	// job that finishes on its answer kills its console milliseconds
	// later, and the delivered verdict must leave before the close can.
	wait := time.Now().Add(coninjWait)
	settledAt := time.Now()
	var last, n uint32
	k32.NewProc("GetNumberOfConsoleInputEvents").Call(uintptr(h), uintptr(unsafe.Pointer(&n)))
	last = n
	pollGap := 100 * time.Microsecond
	for n > base+1 && time.Now().Before(wait) {
		if n != last {
			last = n
			settledAt = time.Now()
		}
		if time.Since(settledAt) > coninjSettle {
			break
		}
		time.Sleep(pollGap)
		if pollGap < 2*time.Millisecond {
			pollGap *= 2
		}
		k32.NewProc("GetNumberOfConsoleInputEvents").Call(uintptr(h), uintptr(unsafe.Pointer(&n)))
	}
	consumed := n <= base+1
	if n > base {
		k32.NewProc("FlushConsoleInputBuffer").Call(uintptr(h))
	}
	// Delivered: restore the echo and leave the console NOW, inline — the
	// deferred versions would run a hair later, and a hair is a window.
	restore()
	k32.NewProc("FreeConsole").Call()
	if consumed {
		return 0 // the job read everything that was typed: it was listening
	}
	return 3
}
