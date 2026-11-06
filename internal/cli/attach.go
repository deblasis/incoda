package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/joblog"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/textsafe"
)

// cmdAttach streams a pty-mode job's captured output live to a second
// terminal: the job file from its start, then follows it until the job's run
// process is gone. Read-only: nothing typed here reaches the job.
//
// Only the job's owner may attach. The owner identity is the same string the
// run put on its ticket (--owner / $INCODA_OWNER), so on a shared machine one
// session cannot watch another's job without saying whose it is. Like kill's
// owner rule this is lane etiquette between cooperating sessions, not a
// security boundary: the job file sits in the state directory next to
// everything else that is already per-user.
func cmdAttach(args []string, stdout, stderr io.Writer) error {
	start := time.Now()
	fs := newFlagSet("attach", stderr)
	queue := fs.String("queue", "", "queue key (defaults to $INCODA_QUEUE)")
	pid := fs.Int("pid", 0, "pid of the pty-mode job's run process, as status shows it")
	owner := fs.String("owner", os.Getenv("INCODA_OWNER"), "the identity to attach as; must match the job's owner (defaults to $INCODA_OWNER)")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: incoda attach --queue KEY --pid N [--owner WHO]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for attach"}
	}
	if *pid <= 0 {
		return usagef("attach needs --pid, the job's run pid as `incoda status` shows it")
	}
	key, err := resolveKey(*queue)
	if err != nil {
		return err
	}
	_, v, err := readState()
	if err != nil {
		return err
	}
	// Attach, like kill, addresses the layout it finds: it never creates a
	// lane, never migrates and never takes machine.lock.
	q, err := lane.OpenIn(v.Root, key, lane.Existing)
	if errors.Is(err, os.ErrNotExist) {
		return usagef("queue %q has no state on this machine", key)
	}
	if err != nil {
		return exitWith(ExitState, "%v", err)
	}
	defer q.Close()
	q.SetBudget(start, 5*time.Second)

	snap, err := q.ObserveBy(0, time.Now().Add(5*time.Second))
	if err != nil {
		return exitWith(ExitState, "%v", err)
	}
	entry, holding := findEntry(snap, *pid)
	if entry == nil {
		return usagef("queue %q has no live participant with pid %d: %v", key, *pid, lane.ErrNoParticipant)
	}
	if err := mayAttach(entry.Ticket, *owner); err != nil {
		return err
	}
	if !entry.Ticket.PTY {
		return usagef("pid %d on queue %q is not running in pty mode; its output goes to its run's stdout, and there is no job file to attach to", *pid, key)
	}
	if !holding {
		return usagef("pid %d on queue %q is still waiting for the lane; attach again once it holds and the job starts", *pid, key)
	}

	path, err := joblog.Path(q.Dir, entry.File)
	if err != nil {
		return exitWith(ExitState, "%v", err)
	}
	// The run creates the job file between acquiring the lane and starting
	// the child; a ticket seen as holding in this snapshot can still be
	// inside that window.
	f, err := openJobFile(path, 5*time.Second)
	if err != nil {
		return exitWith(ExitState, "job output for pid %d is not there yet: %v", *pid, err)
	}
	defer f.Close()

	fmt.Fprintf(stderr, "attach: streaming pid %d on queue %q from the start; Ctrl-C to detach (read-only)\n", *pid, key)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := streamJobFile(ctx, f, *pid, stdout); err != nil {
		return exitWith(ExitState, "%v", err)
	}
	if ctx.Err() != nil {
		fmt.Fprintf(stderr, "attach: detached\n")
	}
	return nil
}

// findEntry returns the participant with this pid, and whether it holds the
// lane (a waiter is the same run before its turn).
func findEntry(snap *lane.Snapshot, pid int) (*lane.Entry, bool) {
	for i := range snap.Holders {
		if snap.Holders[i].Ticket.PID == pid {
			return &snap.Holders[i], true
		}
	}
	for i := range snap.Waiting {
		if snap.Waiting[i].Ticket.PID == pid {
			return &snap.Waiting[i], false
		}
	}
	return nil, false
}

// mayAttach is the owner gate: fail closed on a missing identity, a ticket
// with no recorded owner, and a mismatch, each named. The identity string is
// trimmed the way the run trims --owner, so a trailing space in either place
// cannot fail a match that is real.
func mayAttach(t lane.Ticket, me string) error {
	me = strings.TrimSpace(me)
	if me == "" {
		return usagef("attach needs --owner or $INCODA_OWNER: attach is limited to the job's own owner")
	}
	if t.Owner == "" {
		return usagef("job pid %d on queue %q recorded no owner; attach is refused. Runs that want to be attachable must pass --owner", t.PID, t.Queue)
	}
	if t.Owner != me {
		return usagef("attach refused: job pid %d on queue %q belongs to owner %q; you are %q. Only the job's owner may attach",
			t.PID, t.Queue, textsafe.Escape(t.Owner), textsafe.Escape(me))
	}
	return nil
}

// openJobFile waits briefly for the run to create the job file, then opens it
// read-only.
func openJobFile(path string, wait time.Duration) (*os.File, error) {
	deadline := time.Now().Add(wait)
	for {
		f, err := os.Open(path)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, os.ErrNotExist) || time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// streamJobFile copies new bytes from the job file to out as they appear,
// from the start, and returns when the run process is gone and the file is
// drained, or on interrupt.
func streamJobFile(ctx context.Context, f *os.File, pid int, out io.Writer) error {
	buf := make([]byte, 32*1024)
	var offset int64
	for {
		for {
			st, err := f.Stat()
			if err != nil {
				return fmt.Errorf("job file stat: %w", err)
			}
			if st.Size() <= offset {
				break
			}
			n, err := f.ReadAt(buf[:min(int64(len(buf)), st.Size()-offset)], offset)
			if n > 0 {
				if _, werr := out.Write(buf[:n]); werr != nil {
					return werr
				}
				offset += int64(n)
			}
			if err != nil {
				break
			}
		}
		if !procinfo.Alive(pid) {
			// One more drain: bytes can land between the last read and the
			// death check.
			if n, err := f.ReadAt(buf, offset); err == nil || n > 0 {
				if _, werr := out.Write(buf[:n]); werr != nil {
					return werr
				}
				offset += int64(n)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(150 * time.Millisecond):
		}
	}
}
