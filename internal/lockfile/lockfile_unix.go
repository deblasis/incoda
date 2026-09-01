//go:build !windows

package lockfile

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func openLockable(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
}

// flock is used rather than fcntl/POSIX record locks on purpose: POSIX locks are
// released when the process closes *any* descriptor on the file, which makes a
// stray os.Open in the same process silently drop somebody else's lock. flock
// locks live on the open file description and survive that.
func lockFile(f *os.File, block bool) (bool, error) {
	how := unix.LOCK_EX
	if !block {
		how |= unix.LOCK_NB
	}
	for {
		err := unix.Flock(int(f.Fd()), how)
		if err == nil {
			return true, nil
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EWOULDBLOCK) {
			return false, nil
		}
		return false, err
	}
}

func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
