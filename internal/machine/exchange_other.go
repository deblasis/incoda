//go:build !darwin && !linux

package machine

// exchange is not available here (Windows, the BSDs): M4 always takes the
// rename fallback.
func exchange(string, string) error { return errNoExchange }
