//go:build windows

package colorize

import (
	"io"
	"os"

	"golang.org/x/sys/windows"
)

// isTerminal uses the console mode rather than the file mode: on Windows the
// character-device test also matches things like NUL, which are not screens.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}

// prepare turns on virtual terminal processing so the SGR sequences render
// instead of leaking to the screen. Windows 10+ consoles support it; if the
// console refuses, color stays off rather than printing escapes it cannot
// draw.
func prepare(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return false
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
