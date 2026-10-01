//go:build !incoda_crashpoints

package machine

// crashpoint is a no-op in every build without the incoda_crashpoints tag
// (see crash_on.go).
func crashpoint(string) {}

// noExchange is always false outside test builds.
func noExchange() bool { return false }
