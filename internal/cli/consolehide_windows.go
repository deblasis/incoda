//go:build windows

package cli

import (
	"fmt"
	"io"
	"os"
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
	procGetStartupInfoW       = kernel32.NewProc("GetStartupInfoW")
	procIsWindowVisible       = user32.NewProc("IsWindowVisible")
	procShowWindow            = user32.NewProc("ShowWindow")
)

const (
	stdInputHandle  = ^uintptr(9)  // -10
	stdOutputHandle = ^uintptr(10) // -11
	stdErrorHandle  = ^uintptr(11) // -12

	swHide = 0

	startfUseShowWindow = 0x1
)

// startupInfo is STARTUPINFOW. The explicit pad keeps the pointer fields
// 8-aligned after Cb.
type startupInfo struct {
	Cb            uint32
	_             uint32
	LpReserved    *uint16
	LpDesktop     *uint16
	LpTitle       *uint16
	Dx            uint32
	Dy            uint32
	DxSize        uint32
	DySize        uint32
	DxCountChars  uint32
	DyCountChars  uint32
	DwFill        uint32
	DwFlags       uint32
	WShowWindow   uint16
	CbReserved2   uint16
	LpReserved2   *byte
	HStdInput     syscall.Handle
	HStdOutput    syscall.Handle
	HStdError     syscall.Handle
}

// startupWantsHidden reports that the parent asked for SW_HIDE in this
// process's STARTUPINFO - Start-Process -WindowStyle Hidden and the
// shard-runner shape. Only then may a not-yet-visible console be treated as
// finally hidden: a fresh default-terminal console also reads that way for
// a moment before its window is mapped.
func startupWantsHidden() bool {
	var si startupInfo
	si.Cb = uint32(unsafe.Sizeof(si))
	procGetStartupInfoW.Call(uintptr(unsafe.Pointer(&si)))
	return si.DwFlags&startfUseShowWindow != 0 && si.WShowWindow == swHide
}

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

// consoleFactsSummary is the doctor's one-line view of the same facts the
// hide decision uses: a run whose console looks like a hide candidate and
// then does not get hidden is a bug report, and this line says why.
func consoleFactsSummary() string {
	f := gatherConsoleFacts()
	cmd := ""
	if args := os.Args[1:]; len(args) > 0 {
		cmd = args[0]
	}
	return fmt.Sprintf("window=%v visible=%v attached=%d stdout-console=%v stderr-console=%v startup-hidden=%v hide-would-fire=%v",
		f.HasWindow, f.Visible, f.Attached, f.StdoutCon, f.StderrCon, startupWantsHidden(), shouldHide(f, cmd))
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
	started := time.Now()
	deadline := started.Add(hideRetryWindow)
	spawnerHidden := startupWantsHidden()
	for {
		f := gatherConsoleFacts()
		if shouldHide(f, cmd) {
			procShowWindow.Call(f.Window, swHide)
			fmt.Fprint(stderr, "event=console-hide clause=captured-stdio-attached1\n")
			return
		}
		// The stable clauses: nobody else attached, output captured, and a
		// command we may hide. When they fail, this is a final no.
		if f.Attached != 1 || f.StdoutCon || f.StderrCon || !hidableCommand(cmd) {
			return
		}
		if f.HasWindow && !f.Visible && spawnerHidden {
			// The parent asked for a hidden window (shard-runner's shape):
			// this console will not be shown by us or by conhost, so hiding
			// it now is the end of the matter. Without that fact a console
			// can read as "exists but invisible" for ~100-300 ms before its
			// window is mapped; hiding it then and returning leaves the
			// mapped window visible (measured: vis=1 after 94/203 ms).
			procShowWindow.Call(f.Window, swHide)
			fmt.Fprint(stderr, "event=console-hide clause=spawner-hidden\n")
			return
		}
		if time.Since(started) > hideRetryWindow || time.Now().After(deadline) {
			return
		}
		time.Sleep(hideRetryStep)
	}
}
