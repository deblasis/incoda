//go:build !windows

package child

// ConinjCommand is the hidden CLI entry the pty runner's console-input
// injector runs. The injector itself is Windows-only (pty mode is
// Windows-only), so off Windows the entry exists for the CLI dispatch and
// does nothing.
const ConinjCommand = "__coninj"

// InjectConsoleInput is a no-op off Windows.
func InjectConsoleInput(args []string) {}
