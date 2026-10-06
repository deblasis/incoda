//go:build darwin

package machine

import (
	"errors"

	"golang.org/x/sys/unix"
)

// exchange swaps a and b atomically with renamex_np(RENAME_SWAP)
// (golang.org/x/sys/unix.RenamexNp). APFS supports it for a file and a
// directory; anything that refuses the swap itself is reported as
// errNoExchange so M4 takes the rename fallback (isNoExchange).
func exchange(a, b string) error {
	if noExchange() {
		return errNoExchange
	}
	err := unix.RenamexNp(a, b, unix.RENAME_SWAP)
	if isNoExchange(err) {
		return errNoExchange
	}
	return err
}

// isNoExchange reports an error that means "no atomic exchange here", as
// opposed to a failed rename: EINVAL and ENOTSUP (a filesystem without
// RENAME_SWAP), EPERM (a sandbox or a mount that forbids it) and EXDEV (the
// two names on different filesystems, which only an overlay can produce).
// The fallback's plain renames report their own errors.
func isNoExchange(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) ||
		errors.Is(err, unix.EPERM) || errors.Is(err, unix.EXDEV)
}
