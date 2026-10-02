//go:build windows

package machine

import (
	"errors"

	"golang.org/x/sys/windows"
)

// notIdleError reports a directory rename that Windows refused because a
// file inside is still open (ERROR_SHARING_VIOLATION) or because a handle
// without delete sharing is open on it (ERROR_ACCESS_DENIED): an older run
// is still in there, so the caller waits. Any other error is a real
// failure.
func notIdleError(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
