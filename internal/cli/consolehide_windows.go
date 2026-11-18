//go:build windows

package cli

import (
	"fmt"
	"io"
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	user32   = syscall.NewLazyDLL("user32.dll")

	procGetConsoleWindow      = kernel32.NewProc("GetConsoleWindow")
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
	procGetStdHandle          = kernel32.NewProc("GetStdHandle")
	procGetConsoleMode        = kernel32.NewProc("GetConsoleMode")
	procIsWindowVisible       = user32.NewProc("IsWindowVisible")
	procShowWindow            = user32.NewProc("ShowWindow")
)

const (
	stdInputHandle  = ^uintptr(9)  // -10
	stdOutputHandle = ^uintptr(10) // -11
	stdErrorHandle  = ^uintptr(11) // -12

	swHide = 0
)

// isConsole reports whether h is a console handle. GetConsoleMode fails for
// pipes, files and the NUL device (which GetFileType would call
// FILE_TYPE_CHAR), so it is the only reliable test.
func isConsole(h uintptr) bool {
	if h == 0 || h == ^uintptr(0) {
		return false
	}
	var mode uint32
	ok, _, _ := procGetConsoleMode.Call(h, uintptr(unsafe.Pointer(&mode)))
	return ok != 0
}

func gatherConsoleFacts() consoleFacts {
	var f consoleFacts
	hwnd, _, _ := procGetConsoleWindow.Call()
	f.HasWindow = hwnd != 0
	f.Window = hwnd
	if !f.HasWindow {
		return f
	}
	vis, _, _ := procIsWindowVisible.Call(hwnd)
	f.Visible = vis != 0

	var pids [64]uint32
	n, _, _ := procGetConsoleProcessList.Call(
		uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	f.Attached = int(n)

	in, _, _ := procGetStdHandle.Call(stdInputHandle)
	out, _, _ := procGetStdHandle.Call(stdOutputHandle)
	errh, _, _ := procGetStdHandle.Call(stdErrorHandle)
	_ = in // stdin is not part of the decision: with captured output nobody can read the window
	f.StdoutCon = isConsole(out)
	f.StderrCon = isConsole(errh)
	return f
}

// maybeHideConsole hides incoda's own console window when the predicate
// holds. A fresh console can be mapped up to ~2 s after CreateProcess under
// Windows Terminal, so a failing visibility sample retries while every other
// clause still holds. It never blocks a terminal run: a console stdout/stderr
// fails the stable clauses on the first sample and returns.
func maybeHideConsole(args []string, stderr io.Writer) {
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	if !hidableCommand(cmd) {
		return
	}
	deadline := time.Now().Add(hideRetryWindow)
	for {
		f := gatherConsoleFacts()
		if shouldHide(f, cmd) {
			procShowWindow.Call(f.Window, swHide)
			fmt.Fprint(stderr, "event=console-hide clause=captured-stdio-attached1\n")
			return
		}
		// Retry only while every stable clause still holds and the window
		// simply has not been shown yet. Anything else is a final no.
		stable := f.HasWindow && !f.Visible && f.Attached == 1 && !f.StdoutCon && !f.StderrCon && hidableCommand(cmd)
		if !stable || time.Now().After(deadline) {
			return
		}
		time.Sleep(hideRetryStep)
	}
}
