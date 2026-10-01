//go:build darwin

package machine

import (
	"errors"

	"golang.org/x/sys/unix"
)

// exchange swaps a and b atomically with renamex_np(RENAME_SWAP)
// (golang.org/x/sys/unix.RenamexNp). APFS supports it for a file and a
// directory; a filesystem that does not answers EINVAL or ENOTSUP, reported
// as errNoExchange so M4 takes the rename fallback.
func exchange(a, b string) error {
	if noExchange() {
		return errNoExchange
	}
	err := unix.RenamexNp(a, b, unix.RENAME_SWAP)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) {
		return errNoExchange
	}
	return err
}
