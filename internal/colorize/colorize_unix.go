//go:build !windows

package colorize

import (
	"io"
	"os"
)

// isTerminal reports whether w is a character device, which for our purposes
// is "a terminal the user is looking at".
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// prepare is a no-op on Unix: flock-side terminals have understood SGR
// sequences for forty years.
func prepare(io.Writer) bool { return true }
