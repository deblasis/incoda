//go:build !windows

package machine

// notIdleError is false off Windows: a POSIX rename does not care about
// open files, so a refused rename is a real failure.
func notIdleError(error) bool { return false }
