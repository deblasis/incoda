//go:build windows

package child

import (
	"encoding/base64"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
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
// Write, so the supervisor's own console is never touched.
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
		return 0, err
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
// consumes as typed characters.
func keyEvents(b []byte) []inputRecord {
	recs := make([]inputRecord, 0, 2*len(b))
	for _, r := range string(b) {
		down := inputRecord{EventType: windows.KEY_EVENT}
		down.Event = keyEventRecord{KeyDown: 1, RepeatCount: 1, Rune: uint16(r)}
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
// with echo suppressed, and leave the console exactly as it was found. It
// is silent about every failure: it plays a terminal answering a query, and
// a terminal that cannot answer just does not.
func InjectConsoleInput(args []string) {
	if len(args) != 2 {
		return
	}
	pid, err := strconv.ParseUint(args[0], 10, 32)
	if err != nil {
		return
	}
	b, err := base64.StdEncoding.DecodeString(args[1])
	if err != nil || len(b) == 0 {
		return
	}
	k32.NewProc("FreeConsole").Call()
	if r, _, _ := k32.NewProc("AttachConsole").Call(uintptr(pid)); r == 0 {
		return
	}
	defer k32.NewProc("FreeConsole").Call()
	h, err := windows.CreateFile(windows.StringToUTF16Ptr("CONIN$"), windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return
	}
	if mode&windows.ENABLE_ECHO_INPUT != 0 {
		if err := windows.SetConsoleMode(h, mode&^windows.ENABLE_ECHO_INPUT); err != nil {
			return // cannot suppress the echo: no reply is better than a leaked one
		}
		defer windows.SetConsoleMode(h, mode)
	}
	recs := keyEvents(b)
	var written uint32
	r1, _, _ := k32.NewProc("WriteConsoleInputW").Call(
		uintptr(h), uintptr(unsafe.Pointer(&recs[0])), uintptr(len(recs)), uintptr(unsafe.Pointer(&written)))
	if r1 == 0 || written == 0 {
		return
	}
	// Wait for the job to read what it was sent. Whatever is still there
	// once it settles is flushed: an unread reply sitting in the buffer
	// would be echoed whenever a cooked read finally found it.
	wait := time.Now().Add(coninjWait)
	settledAt := time.Now()
	var last, n uint32
	k32.NewProc("GetNumberOfConsoleInputEvents").Call(uintptr(h), uintptr(unsafe.Pointer(&n)))
	last = n
	for n > 0 && time.Now().Before(wait) {
		time.Sleep(20 * time.Millisecond)
		k32.NewProc("GetNumberOfConsoleInputEvents").Call(uintptr(h), uintptr(unsafe.Pointer(&n)))
		if n != last {
			last = n
			settledAt = time.Now()
		}
		if time.Since(settledAt) > coninjSettle {
			break
		}
	}
	if n > 0 {
		k32.NewProc("FlushConsoleInputBuffer").Call(uintptr(h))
	}
}
