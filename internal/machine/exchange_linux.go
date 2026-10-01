//go:build linux

package machine

import (
	"errors"

	"golang.org/x/sys/unix"
)

// exchange swaps a and b atomically with renameat2(RENAME_EXCHANGE)
// (golang.org/x/sys/unix.Renameat2). A filesystem without it answers
// EINVAL or ENOTSUP, and a kernel older than 3.15 ENOSYS; all three are
// errNoExchange, so M4 takes the rename fallback.
func exchange(a, b string) error {
	if noExchange() {
		return errNoExchange
	}
	err := unix.Renameat2(unix.AT_FDCWD, a, unix.AT_FDCWD, b, unix.RENAME_EXCHANGE)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.ENOSYS) {
		return errNoExchange
	}
	return err
}
