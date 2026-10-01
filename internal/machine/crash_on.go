//go:build incoda_crashpoints

package machine

import (
	"os"
	"time"
)

// crashExit is the exit status of a process stopped at a crash point.
const crashExit = 97

// crashpoint is compiled only into test binaries built with
// -tags incoda_crashpoints; just dist never passes that tag, so release
// builds get the no-op in crash_off.go. With INCODA_TEST_CRASH_AT=<step> the
// process exits at once after that step, running no deferred code, which
// leaves the state directory exactly as a kill at that instant would (the
// kernel frees machine.lock with the process). With
// INCODA_TEST_PAUSE_AT=<step> it instead creates
// $INCODA_TEST_PAUSE_FILE.reached and waits until $INCODA_TEST_PAUSE_FILE
// exists, so a test can act inside the window after that step.
func crashpoint(step string) {
	if os.Getenv("INCODA_TEST_CRASH_AT") == step {
		os.Exit(crashExit)
	}
	if os.Getenv("INCODA_TEST_PAUSE_AT") == step {
		f := os.Getenv("INCODA_TEST_PAUSE_FILE")
		_ = os.WriteFile(f+".reached", nil, 0o644)
		for {
			if _, err := os.Stat(f); err == nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// noExchange forces the rename fallback of M4 (INCODA_TEST_NO_EXCHANGE=1),
// so the fallback and its race rule are tested where an exchange exists.
func noExchange() bool { return os.Getenv("INCODA_TEST_NO_EXCHANGE") == "1" }
