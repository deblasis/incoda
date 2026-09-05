package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/child"
	"github.com/deblasis/incoda/internal/lane"
)

// lanePart is one key of a run: its queue handle and, once enrolled, the
// ticket. A single-key run is the list with one element.
type lanePart struct {
	key string
	q   *lane.Queue
	en  *lane.Enrollment
}

func cmdRun(args []string, _, stderr io.Writer) error {
	fs := newFlagSet("run", stderr)
	queue := fs.String("queue", "", "queue key, or a comma-separated list to hold several at once (defaults to $INCODA_QUEUE)")
	slots := fs.Int("slots", 0, "concurrent holders permitted on this queue; 0 takes the queue's configured slots, else 1")
	exclusive := fs.Bool("exclusive", false, "hold the queue alone: the effective slot count is 1 while this run is live")
	reason := fs.String("reason", "", "free-text note shown in status")
	owner := fs.String("owner", os.Getenv("INCODA_OWNER"), "who queued this (a session id, a worktree name); defaults to $INCODA_OWNER")
	poll := fs.Duration("poll", 500*time.Millisecond, "how often to re-check position while queued")
	quiet := fs.Bool("quiet", false, "suppress lane chatter on stderr")
	noColor := fs.Bool("no-color", false, "never emit ANSI color, even on a terminal (the NO_COLOR environment variable does the same)")
	wait := &waitValue{d: 30 * time.Minute}
	fs.Var(wait, "wait", "max time to queue: a Go duration (30m) or bare seconds (1800); 0 fails immediately, negative waits forever")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: incoda run --queue KEY[,KEY...] [--slots N] [--exclusive] [--wait DUR] [--reason TEXT] [--owner WHO] [--] <cmd...>\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for run"}
	}
	argv := fs.Args()
	if len(argv) == 0 {
		return usagef("run needs a command; try: incoda run --queue KEY -- zig build")
	}
	if *slots < 0 {
		return usagef("--slots must be at least 1, or 0 for the queue's default, got %d", *slots)
	}
	p := paletteFor(stderr, *noColor)
	keys, err := resolveKeys(*queue)
	if err != nil {
		return err
	}
	dir, err := stateDir()
	if err != nil {
		return err
	}

	// Every queue is opened and its config checked before any ticket
	// exists, so a closed or reason-requiring key refuses with nothing to
	// undo. Keys already held by a parent incoda are skipped (re-entrancy):
	// a recipe that takes its own lane must not deadlock when an agent
	// wraps the whole recipe in run from outside. The parent says which
	// keys it holds through INCODA_HELD, and a nested run on one of them
	// rides the parent's ticket instead of queueing behind it.
	held := heldKeys()
	var parts, toTake []*lanePart
	defer func() {
		for _, pt := range parts {
			pt.q.Close()
		}
	}()
	for _, key := range keys {
		q, err := lane.Open(dir, key)
		if err != nil {
			return exitWith(ExitState, "%v", err)
		}
		pt := &lanePart{key: key, q: q}
		parts = append(parts, pt)
		cfg, err := q.LoadConfig()
		if err != nil {
			return exitWith(ExitState, "queue %q: %v", key, err)
		}
		if cfg.Closed != "" {
			return usagef("queue %q is closed: %s", key, cfg.Closed)
		}
		if cfg.RequireReason && strings.TrimSpace(*reason) == "" {
			return usagef("queue %q requires --reason: say what this job is so status can answer \"whose is that and why\"", key)
		}
		if held[key] {
			if !*quiet {
				fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
					p.Dim(fmt.Sprintf("queue %q is already held by a parent incoda; running inside its lane", key)))
			}
			q.Logf("queue=%s event=reenter pid=%d cmd=%s", key, os.Getpid(), lane.Ticket{Command: argv}.CommandString())
			continue
		}
		toTake = append(toTake, pt)
	}

	host, _ := os.Hostname()
	cwd, _ := os.Getwd()

	// From here on every ticket must be released on every exit path, in
	// reverse acquisition order. The OS lock covers the paths we cannot
	// reach (SIGKILL, power loss); this covers the ones we can, so the next
	// caller does not wait a poll interval for nothing.
	rc := ExitOK
	var stats lane.Stats
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		for i := len(toTake) - 1; i >= 0; i-- {
			if en := toTake[i].en; en != nil {
				en.Stats = stats
				en.Release(rc)
			}
		}
	}
	defer release()

	ctx, stop := signal.NotifyContext(context.Background(), interruptSignals()...)
	defer stop()

	// Keys are taken one at a time in sorted order (resolveKeys sorted
	// them). Every multi-key caller orders the same way, so two of them can
	// never each hold what the other waits for: the classic lock-ordering
	// argument, and the whole reason a list is allowed at all.
	start := time.Now()
	for _, pt := range toTake {
		en, err := pt.q.Enroll(lane.Ticket{
			Slots:     *slots,
			Exclusive: *exclusive,
			Command:   argv,
			Reason:    *reason,
			Owner:     *owner,
			Hostname:  host,
			Dir:       cwd,
		})
		if err != nil {
			rc = ExitState
			return exitWith(ExitState, "cannot enter queue %q: %v", pt.key, err)
		}
		pt.en = en

		// One --wait budget covers the whole list: a caller asked to wait
		// thirty minutes for the job, not thirty per key.
		budget := wait.d
		if budget > 0 {
			if budget -= time.Since(start); budget < 0 {
				budget = 0
			}
		}
		key := pt.key
		acqErr := en.Acquire(ctx, lane.AcquireOptions{
			Wait:   budget,
			Poll:   *poll,
			Notify: 60 * time.Second,
			OnWait: func(pos, effSlots int, live []lane.Entry, waited time.Duration) {
				if *quiet {
					return
				}
				ahead := pos
				if ahead < 0 {
					ahead = len(live)
				}
				fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
					p.Yellow(fmt.Sprintf("queue %q busy (%d slot(s), %d ahead of you), waited %s%s",
						key, effSlots, ahead, waited.Round(time.Second), waitBudget(wait.d))))
				for i, e := range live {
					if i >= effSlots {
						break
					}
					fmt.Fprintf(stderr, "%s   %s\n", p.Dim("incoda:"),
						p.Dim(fmt.Sprintf("holder pid %d in %s: %s", e.Ticket.PID, e.Ticket.Dir, e.Ticket.CommandString())))
				}
			},
		})
		if acqErr != nil {
			if errors.Is(acqErr, context.Canceled) {
				rc = ExitInterrupt
				return exitWith(ExitInterrupt, "interrupted while queueing on %q", key)
			}
			if errors.Is(acqErr, lane.ErrTimeout) {
				rc = ExitTimeout
				pt.q.Logf("queue=%s event=giveup pid=%d waited=%s", key, os.Getpid(), wait.d)
				return exitWith(ExitTimeout,
					"queue %q still busy after %s. Check `incoda status --queue %s`. Do NOT bypass the lane; surface the wait and coordinate instead",
					key, wait.d, key)
			}
			rc = ExitState
			return exitWith(ExitState, "%v", acqErr)
		}

		if _, _, live, err := en.Position(); err == nil && lane.SlotsDisagree(live) {
			fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
				p.Yellow(fmt.Sprintf("warning: participants on queue %q disagree about --slots; the smallest value is in force", key)))
		}
		if !*quiet {
			fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
				p.Green(fmt.Sprintf("acquired queue %q (pid %d)", key, os.Getpid())))
		}
	}

	stop() // hand interrupt handling to the child supervisor
	// The child inherits the environment, so this is how the held keys
	// reach a nested incoda. Set on the process rather than on the child's
	// env slice because child.Run copies os.Environ() itself.
	_ = os.Setenv("INCODA_HELD", joinHeld(held, keys))
	res, runErr := child.Run(argv, os.Stdin, os.Stdout, os.Stderr)
	if runErr != nil {
		rc = ExitSpawn
		return exitWith(ExitSpawn, "cannot run %q: %v", argv[0], runErr)
	}
	rc = res.Code
	stats = lane.Stats{PeakBytes: res.PeakBytes, HavePeak: res.HavePeak, CPU: res.CPU, HaveCPU: res.HaveCPU}
	release()
	if res.Code != 0 {
		return &exitCode{code: res.Code}
	}
	return nil
}

func waitBudget(d time.Duration) string {
	switch {
	case d < 0:
		return ", no time limit"
	case d == 0:
		return ""
	default:
		return fmt.Sprintf(", limit %s", d)
	}
}

// resolveKeys applies --queue, then INCODA_QUEUE, and accepts a
// comma-separated list. It never falls back to a shared default: two
// unrelated projects silently sharing one lane would be a worse failure than
// an error message. The result is sorted, which is what makes holding several
// keys deadlock-free (see cmdRun).
func resolveKeys(explicit string) ([]string, error) {
	raw := strings.TrimSpace(explicit)
	src := "--queue"
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("INCODA_QUEUE"))
		src = "INCODA_QUEUE"
	}
	if raw == "" {
		return nil, usagef("no queue key: pass --queue KEY or set INCODA_QUEUE. There is no default queue, because sharing one by accident is exactly the collision this tool prevents")
	}
	seen := map[string]bool{}
	var keys []string
	for _, k := range strings.Split(raw, ",") {
		k = strings.TrimSpace(k)
		if k == "" {
			return nil, usagef("empty key in the %s list %q", src, raw)
		}
		if err := lane.ValidateKey(k); err != nil {
			return nil, usagef("invalid queue key from %s: %v", src, err)
		}
		if seen[k] {
			return nil, usagef("queue key %q is listed twice in %s", k, src)
		}
		seen[k] = true
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// heldKeys parses INCODA_HELD, the comma-separated keys an ancestor incoda
// holds on this process's behalf.
func heldKeys() map[string]bool {
	held := map[string]bool{}
	for _, k := range strings.Split(os.Getenv("INCODA_HELD"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			held[k] = true
		}
	}
	return held
}

// joinHeld renders the inherited set plus this run's keys back into
// INCODA_HELD form, sorted so the value is stable for anyone who logs or
// compares it.
func joinHeld(held map[string]bool, keys []string) string {
	set := map[string]bool{}
	for k := range held {
		set[k] = true
	}
	for _, k := range keys {
		set[k] = true
	}
	all := make([]string, 0, len(set))
	for k := range set {
		all = append(all, k)
	}
	sort.Strings(all)
	return strings.Join(all, ",")
}
