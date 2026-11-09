package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/deblasis/incoda/internal/joblog"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/procinfo"
)

// cmdAttach streams a pty-mode job's captured output live to a second
// terminal: the job file from its start, then follows it until the job's run
// process is gone. Read-only: nothing typed here reaches the job.
//
// Anyone may watch - the capture is a view, and on a shared machine one
// session can follow another's job without asking whose it is. Typing at
// the job is the answer door's business, and that one stays limited to the
// job's own owner. Like kill's owner rule this is lane etiquette between
// cooperating sessions, not a security boundary: the job file sits in the
// state directory next to everything else that is already per-user.
func cmdAttach(args []string, stdout, stderr io.Writer) error {
	start := time.Now()
	fs := newFlagSet("attach", stderr)
	queue := fs.String("queue", "", "queue key (defaults to $INCODA_QUEUE)")
	pid := fs.Int("pid", 0, "pid of the pty-mode job's run process, as status shows it")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: incoda attach --queue KEY --pid N\n\n")
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
	entry, holding, err := findEntry(snap, *pid)
	if err != nil {
		return usagef("queue %q: %v; refusing to pick one, since a planted ticket is indistinguishable from the real one", key, err)
	}
	if entry == nil {
		return usagef("queue %q has no live participant with pid %d: %v", key, *pid, lane.ErrNoParticipant)
	}
	if err := authorizeAttach(key, entry); err != nil {
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
		return exitWith(ExitState, "job output for pid %d is not there yet (the run may still be waiting out a load gate, or between acquiring the lane and starting the job): %v", *pid, err)
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
		return nil
	}
	// The run records the job's exit code beside the capture on its way
	// out; a run that never got there, or one from before the sidecar, has
	// none to give. A short wait covers the racing write.
	if code, ok := awaitJobExit(path, 3*time.Second); ok {
		fmt.Fprintf(stderr, "attach: pid %d exited with code %d\n", *pid, code)
	}
	return nil
}

// awaitJobExit polls for the exit sidecar the run writes beside a job's
// capture. A viewer that stops on the process's death can arrive before
// that write; an old incoda never wrote one, and then there is simply no
// code to show.
func awaitJobExit(path string, within time.Duration) (int, bool) {
	deadline := time.Now().Add(within)
	for {
		if code, ok := joblog.ReadExit(path); ok {
			return code, true
		}
		if time.Now().After(deadline) {
			return 0, false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// authorizeAttach is every gate that decides whether this entry may be
// streamed at all, ahead of any file being opened: the ticket must be fully
// readable and self-consistent (validateEntryForAttach). Viewing is open to
// everyone, so there is no owner gate here - typing at the job is answer's
// door, and that one checks. One function so the wiring has one name to
// guard: attach that skipped any of it would stream on the strength of a
// mangled ticket.
func authorizeAttach(key string, entry *lane.Entry) error {
	return validateEntryForAttach(key, entry)
}

// validateEntryForAttach refuses entries a lazy viewer alone must not
// stream. A payload that could not be fully parsed can carry a wrong owner
// field, so authorizing on it would stream on the strength of a half-read
// ticket. A file name and payload that disagree about the pid is a planted
// or mangled ticket: the file name is what findEntry matched on, the
// payload is what the viewer reads, and attach must not act where the two
// disagree.
func validateEntryForAttach(key string, entry *lane.Entry) error {
	if entry.PayloadError != "" {
		return usagef("queue %q: ticket %s is unreadable (%s); attach does not authorize on a payload it cannot fully parse", key, entry.File, entry.PayloadError)
	}
	if np, ok := lane.TicketNamePID(entry.File); ok && np != entry.Ticket.PID {
		return usagef("queue %q: ticket %s names pid %d but claims pid %d; refusing", key, entry.File, np, entry.Ticket.PID)
	}
	return nil
}

// findEntry returns the participant with this pid, and whether it holds the
// lane (a waiter is the same run before its turn). The pid must name exactly
// one live participant: ticket state lives in the shared state directory,
// so a second ticket carrying the pid, however it got there, cannot be told
// from the real one, and streaming a guess would put someone else's bytes
// under this job's name.
func findEntry(snap *lane.Snapshot, pid int) (*lane.Entry, bool, error) {
	var found *lane.Entry
	holding := false
	count := 0
	for i := range snap.Holders {
		if snap.Holders[i].Ticket.PID == pid {
			found = &snap.Holders[i]
			holding = true
			count++
		}
	}
	for i := range snap.Waiting {
		if snap.Waiting[i].Ticket.PID == pid {
			if found == nil {
				found = &snap.Waiting[i]
				holding = false
			}
			count++
		}
	}
	if count > 1 {
		return nil, false, fmt.Errorf("pid %d appears on %d live tickets", pid, count)
	}
	return found, holding, nil
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
//
// The loop checks for death BEFORE draining, in that order, so the
// post-death drain is what carries the tail: bytes that land in the sleep
// window before the run's exit are fetched by the drain after the death is
// observed, not by a racing poll. The order is what makes that drain
// load-bearing (a drain-first loop would absorb same-tick bytes itself and
// the final drain would guard nothing).
func streamJobFile(ctx context.Context, f *os.File, pid int, out io.Writer) error {
	buf := make([]byte, 32*1024)
	var offset int64
	for {
		if !procinfo.Alive(pid) {
			// The run is gone, so nothing more can land after this drain:
			// a full drain, not one read, because the exit tail can be far
			// bigger than a single buffer.
			_, err := drainToEOF(f, offset, buf, out)
			return err
		}
		var err error
		offset, err = drainToEOF(f, offset, buf, out)
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// readerAtStat is the slice of *os.File drainToEOF needs; tests inject a
// fake whose ReadAt fails to prove real read errors surface instead of
// dissolving into the poll loop.
type readerAtStat interface {
	io.ReaderAt
	Stat() (os.FileInfo, error)
}

// drainToEOF copies every byte currently in the file past offset to out and
// returns the new offset. It stops, not errors, at end-of-file: the next
// caller picks up where it left off. Any other read error is returned: a
// read that failed for a real reason must not read as silence, or the tail
// is dropped without a word.
func drainToEOF(f readerAtStat, offset int64, buf []byte, out io.Writer) (int64, error) {
	for {
		st, err := f.Stat()
		if err != nil {
			return offset, fmt.Errorf("job file stat: %w", err)
		}
		if st.Size() <= offset {
			return offset, nil
		}
		n, err := f.ReadAt(buf[:min(int64(len(buf)), st.Size()-offset)], offset)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return offset, werr
			}
			offset += int64(n)
		}
		if err == io.EOF {
			return offset, nil
		}
		if err != nil {
			return offset, fmt.Errorf("job file read: %w", err)
		}
	}
}
