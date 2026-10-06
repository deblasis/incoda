//go:build linux

package machine

import (
	"errors"

	"golang.org/x/sys/unix"
)

// exchange swaps a and b atomically with renameat2(RENAME_EXCHANGE)
// (golang.org/x/sys/unix.Renameat2). Anything that refuses the exchange
// itself is reported as errNoExchange so M4 takes the rename fallback
// (isNoExchange).
func exchange(a, b string) error {
	if noExchange() {
		return errNoExchange
	}
	err := unix.Renameat2(unix.AT_FDCWD, a, unix.AT_FDCWD, b, unix.RENAME_EXCHANGE)
	if isNoExchange(err) {
		return errNoExchange
	}
	return err
}

// isNoExchange reports an error that means "no atomic exchange here", as
// opposed to a failed rename: EINVAL and ENOTSUP (a filesystem without
// RENAME_EXCHANGE), ENOSYS (a kernel older than 3.15), EPERM (a seccomp
// profile or container runtime that forbids renameat2) and EXDEV (overlayfs
// answers it for a directory that lives in a lower layer).
func isNoExchange(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.ENOSYS) ||
		errors.Is(err, unix.EPERM) || errors.Is(err, unix.EXDEV)
}
