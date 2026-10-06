//go:build !windows

package machine

import (
	"errors"

	"golang.org/x/sys/unix"
)

// groupHasMembers reports whether process group g has any member:
// kill(-g, 0) answering anything but ESRCH. EPERM counts as members (macOS
// answers it for a group whose members are all zombies).
func groupHasMembers(g int) bool {
	if g <= 1 {
		return false
	}
	return !errors.Is(unix.Kill(-g, 0), unix.ESRCH)
}
