package machine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/deblasis/incoda/internal/lockfile"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/textsafe"
)

const (
	lockName = "machine.lock"
	// minLockWait is the floor of a machine.lock wait: its normal holds
	// last milliseconds, so even --wait 0 gets this long (spec 3.1).
	minLockWait = 2 * time.Second
	// lockNotify is how often a waiter repeats its waiting line.
	lockNotify = 60 * time.Second
)

// LockPath is <state>/machine.lock, a file no released incoda knows about.
func LockPath(stateDir string) string { return filepath.Join(stateDir, lockName) }

// Lock is a held machine.lock. It is taken for migration and its recovery,
// re-fencing and registry writes, and never while waiting for user input.
type Lock struct {
	f     *lockfile.File
	op    string
	since time.Time
}

// LockOptions says how long to wait and where to say so.
type LockOptions struct {
	// Op names the holder's operation in the note: migrate, refence,
	// recover, rebuild-registry.
	Op string
	// Start and Wait are the caller's --wait budget, which runs from the
	// start of the command; a negative Wait waits forever.
	Start time.Time
	Wait  time.Duration
	// Poll is the retry interval; zero means 500ms.
	Poll time.Duration
	// Chain is this process's parent chain, computed once by the caller. A
	// waiter whose chain contains a blocker named in the holder's note is
	// upgrade-blocked at once (Unix; Windows chains are Skip).
	Chain  procinfo.Chain
	Stderr io.Writer
	// Ctx, when set, ends the wait early: once it is done AcquireLock
	// returns ErrInterrupted between polls. Nil waits as before.
	Ctx context.Context
}

// ErrInterrupted is AcquireLock's error when LockOptions.Ctx ended the
// wait (an interrupt or a termination signal) before machine.lock was
// taken. Nothing is held or written then.
var ErrInterrupted = errors.New("interrupted while waiting for machine.lock")

// lockDeadline is when a machine.lock wait gives up: the end of the --wait
// budget, but never less than minLockWait from now. The zero time means
// never.
func lockDeadline(start time.Time, wait time.Duration, now time.Time) time.Time {
	if wait < 0 {
		return time.Time{}
	}
	if start.IsZero() {
		start = now
	}
	d := start.Add(wait).Sub(now)
	if d < minLockWait {
		d = minLockWait
	}
	return now.Add(d)
}

// AcquireLock takes machine.lock with TryLock and a poll, never the
// blocking Lock (spec 3.1). Waiters are not FIFO. While it waits it reads
// the holder's note on every poll, prints "waiting for machine.lock: ..."
// after its first failed poll and then every 60s, and refuses with
// upgrade-blocked when a blocker in the note is its own ancestor. On budget
// expiry it returns a Timeout: machine-lock-timeout. When o.Ctx ends first
// it returns ErrInterrupted.
func AcquireLock(stateDir string, o LockOptions) (*Lock, error) {
	stderr := o.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	poll := o.Poll
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	f, err := lockfile.Open(LockPath(stateDir))
	if err != nil {
		return nil, stateErrorf("cannot open %s: %s", textsafe.Escape(LockPath(stateDir)), esc(err))
	}
	deadline := lockDeadline(o.Start, o.Wait, time.Now())
	var done <-chan struct{}
	if o.Ctx != nil {
		done = o.Ctx.Done()
	}
	var printedAt time.Time
	for attempt := 0; ; attempt++ {
		if o.Ctx != nil && o.Ctx.Err() != nil {
			f.Close()
			return nil, ErrInterrupted
		}
		ok, err := f.TryLock()
		if err != nil {
			f.Close()
			return nil, stateErrorf("machine.lock: %s", esc(err))
		}
		if ok {
			l := &Lock{f: f, op: o.Op, since: time.Now()}
			if err := l.SetBlockers(nil); err != nil {
				l.Release()
				return nil, stateErrorf("cannot write the machine.lock note: %s", esc(err))
			}
			return l, nil
		}
		note, noted := ReadNote(stateDir)
		if noted && !o.Chain.Skip {
			for _, b := range note.Blockers {
				if o.Chain.Contains(b.PID) {
					f.Close()
					return nil, upgradeBlocked(b.PID, b.Key)
				}
			}
		}
		now := time.Now()
		if !deadline.IsZero() && !now.Before(deadline) {
			f.Close()
			return nil, lockTimeout(note, noted)
		}
		if attempt > 0 && (printedAt.IsZero() || now.Sub(printedAt) >= lockNotify) {
			fmt.Fprintf(stderr, "incoda: %s\n", waitingLine(note, noted))
			printedAt = now
		}
		t := time.NewTimer(poll)
		select {
		case <-done:
			t.Stop()
			f.Close()
			return nil, ErrInterrupted
		case <-t.C:
		}
	}
}

func waitingLine(n Note, ok bool) string {
	if !ok {
		return "waiting for machine.lock (its holder has not written its note yet)"
	}
	s := fmt.Sprintf("waiting for machine.lock: pid %d %s since %s", n.PID, n.Op, n.Since.UTC().Format(time.RFC3339))
	if len(n.Blockers) > 0 {
		s += "; waiting for older runs: " + blockerList(n.Blockers)
	}
	return s
}

func lockTimeout(n Note, ok bool) *Timeout {
	if !ok {
		return &Timeout{Msg: "machine-lock-timeout: held by a process that left no note"}
	}
	return &Timeout{Msg: fmt.Sprintf("machine-lock-timeout: held by pid %d (%s)", n.PID, n.Op)}
}

// SetBlockers rewrites the note (Truncate) with this holder's pid, op and
// start time, plus the older runs it waits for.
func (l *Lock) SetBlockers(bs []Blocker) error {
	if l == nil || l.f == nil {
		return errors.New("machine.lock is not held")
	}
	return l.f.Truncate([]byte(Note{PID: os.Getpid(), Op: l.op, Since: l.since, Blockers: bs}.String()))
}

// Release clears the note and drops the lock. It is safe to call more than
// once. A holder that dies instead leaves its note behind; the kernel still
// frees the lock, and readers check the pid (status) or the lock itself
// (waiters) before trusting it.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = l.f.Truncate(nil)
	_ = l.f.Close()
	l.f = nil
}
