//go:build darwin || linux

package machine

import (
	"fmt"
	"testing"

	"golang.org/x/sys/unix"
)

// TestIsNoExchange: errors that mean "this filesystem or sandbox has no
// atomic exchange" send M4 to the rename fallback; any other error is a
// real failure of the rename.
func TestIsNoExchange(t *testing.T) {
	for _, e := range []error{unix.EINVAL, unix.ENOTSUP, unix.EPERM, unix.EXDEV, fmt.Errorf("renamex: %w", unix.EXDEV)} {
		if !isNoExchange(e) {
			t.Fatalf("%v must take the fallback", e)
		}
	}
	for _, e := range []error{nil, unix.EACCES, unix.ENOENT, unix.EBUSY} {
		if isNoExchange(e) {
			t.Fatalf("%v is not a missing exchange", e)
		}
	}
}
