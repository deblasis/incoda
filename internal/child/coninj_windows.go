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
			switch ee.ExitCode() {
			case 3:
				return 0, errors.New("the job did not read what was typed at its console")
			case 0xC000013A:
				// STATUS_CONTROL_C_EXIT: the console closed underneath the
				// helper - the job ended in the same instant the answer
				// was typed. The bytes were written before the close.
				return 0, ErrConsoleClosedAnswer
			}
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
	recs := keyEvents(b)
	var written uint32
	r1, _, _ := k32.NewProc("WriteConsoleInputW").Call(
		uintptr(h), uintptr(unsafe.Pointer(&recs[0])), uintptr(len(recs)), uintptr(unsafe.Pointer(&written)))
	if r1 == 0 || written == 0 {
		return 1
	}
	// Watch the input buffer drain. A blocked reader consumes the events
	// within microseconds of the write, so the loop polls before it
	// sleeps: an answer often ends the job that read it, and every
	// millisecond spent attached to a console that is about to close is a
	// window for the close to kill this helper before its verdict leaves.
	//
	// Consumption, not emptiness, is the read signal: a cooked line read
	// can leave the completing key's up-event behind, so a buffer that
	// settles at one straggler was still read from. Only a count that
	// never moved means nobody was listening.
	wait := time.Now().Add(coninjWait)
	settledAt := time.Now()
	var last, n uint32
	k32.NewProc("GetNumberOfConsoleInputEvents").Call(uintptr(h), uintptr(unsafe.Pointer(&n)))
	last = n
	for n > 0 && time.Now().Before(wait) {
		if n != last {
			last = n
			settledAt = time.Now()
		}
		if time.Since(settledAt) > coninjSettle {
			break
		}
		time.Sleep(2 * time.Millisecond)
		k32.NewProc("GetNumberOfConsoleInputEvents").Call(uintptr(h), uintptr(unsafe.Pointer(&n)))
		if n == 0 {
			break
		}
	}
	consumed := n == 0 || n < written
	if n > 0 {
		k32.NewProc("FlushConsoleInputBuffer").Call(uintptr(h))
	}
	// Delivered: restore the echo and leave the console NOW, inline — the
	// deferred versions would run a hair later, and a hair is a window.
	restore()
	k32.NewProc("FreeConsole").Call()
	if consumed {
		return 0 // the job read from its console: it was listening
	}
	return 3
}
