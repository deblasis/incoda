//go:build !windows

package procinfo

import (
	"errors"

	"golang.org/x/sys/unix"
)

// Alive reports whether a process with pid exists. EPERM means it exists
// but belongs to someone else, which still counts.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := unix.Kill(pid, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}
