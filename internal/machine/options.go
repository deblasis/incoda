package machine

import (
	"io"
	"time"

	"github.com/deblasis/incoda/internal/procinfo"
)

// Options is what a mutating command gives the migration.
type Options struct {
	// Start and Wait are the command's --wait budget: one budget, measured
	// from the start of the command, covers machine.lock waits, migration
	// waits and every lane (spec 2.4). A negative Wait waits forever.
	Start time.Time
	Wait  time.Duration
	// Poll is how often waits re-check; zero means 500ms.
	Poll time.Duration
	// Chain is this process's parent chain, computed once.
	Chain procinfo.Chain
	// By is recorded as migrated_by, for example "incoda 0.7.0".
	By string
	// Stderr receives the informational lines (migrated:, upgrade-wait:,
	// upgrade-warning:, waiting for machine.lock:).
	Stderr io.Writer
	// Path is the PATH incoda was started with, for M0; Exe is this
	// binary, "" for os.Executable.
	Path string
	Exe  string
}

func (o Options) poll() time.Duration {
	if o.Poll <= 0 {
		return 500 * time.Millisecond
	}
	return o.Poll
}

func (o Options) stderr() io.Writer {
	if o.Stderr == nil {
		return io.Discard
	}
	return o.Stderr
}

func (o Options) lockOptions(op string) LockOptions {
	return LockOptions{Op: op, Start: o.Start, Wait: o.Wait, Poll: o.Poll, Chain: o.Chain, Stderr: o.Stderr}
}

// budgetDeadline is when the --wait budget ends; the zero time means never.
// Waits for older runs get no floor: --wait 0 means "do not wait for them".
func budgetDeadline(start time.Time, wait time.Duration) time.Time {
	if wait < 0 {
		return time.Time{}
	}
	if start.IsZero() {
		start = time.Now()
	}
	return start.Add(wait)
}
