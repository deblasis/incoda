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
	"github.com/deblasis/incoda/internal/colorize"
	"github.com/deblasis/incoda/internal/held"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/textsafe"
)

// lanePart is one key of a run: its queue handle and, once enrolled, the
// ticket. A single-key run is the list with one element.
type lanePart struct {
	key string
	q   *lane.Queue
	en  *lane.Enrollment
	cfg lane.Config
}

func cmdRun(args []string, _, stderr io.Writer) error {
	// One --wait budget, measured from the start of the command, covers
	// the machine.lock and migration waits and every lane (spec 2.4).
	start := time.Now()
	fs := newFlagSet("run", stderr)
	queue := fs.String("queue", "", "queue key, or a comma-separated list to hold several at once (defaults to $INCODA_QUEUE)")
	slots := fs.Int("slots", 0, "concurrent holders permitted on this queue; 0 takes the queue's configured slots, else 1; on a queue whose config sets a count, a disagreeing value is refused")
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
	chain := procinfo.ParentChain()
	dir, reg, err := mutatingState(start, wait.d, *poll, chain, stderr)
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
	//
	// Inherited lanes come from the environment incoda was started with.
	// Each entry is probed: dead and malformed ones are dropped, live ones
	// count for ordering and the process group (L), and only those held by
	// a verified ancestor are passed through (P).
	inherited := held.Verify(dir, startGetenv("INCODA_HELD"), chain)
	reportDropped(dir, inherited, *quiet, stderr, p)
	pass := inherited.PassKeys()
	live := inherited.LiveKeys()
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
		var ns *lane.NewerSchemaError
		if errors.As(err, &ns) {
			return exitWith(ExitState, "machine-state: %v", err)
		}
		if err != nil {
			return exitWith(ExitState, "queue %q: %v", key, err)
		}
		pt.cfg = cfg
		if cfg.Closed != "" {
			return usagef("queue %q is closed: %s", key, textsafe.Escape(cfg.Closed))
		}
		if cfg.RequireReason && strings.TrimSpace(*reason) == "" {
			return usagef("queue %q requires --reason: say what this job is so status can answer \"whose is that and why\"", key)
		}
		if cfg.Slots > 0 && *slots >= 1 && *slots != cfg.Slots {
			return usagef("%v", lane.NewSlotsDisagreement(key, cfg.Slots, *slots, *exclusive))
		}
		if pass[key] {
			if !*quiet {
				fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
					p.Dim(fmt.Sprintf("queue %q is already held by a parent incoda; running inside its lane", key)))
			}
			q.Logf("queue=%s event=reenter pid=%d cmd=%s", key, os.Getpid(), textsafe.LogValue(lane.Ticket{Command: argv}.CommandString()))
			continue
		}
		toTake = append(toTake, pt)
	}
	// The sorted-order argument that makes multi-key runs deadlock-free
	// stops at a nested run: a parent holding "b" whose recipe now takes "a"
	// is acquiring out of order, and two such parents can each wait on the
	// other's key until --wait expires. It cannot be prevented from here
	// (the parent's key is already held), so it is said out loud.
	for _, pt := range toTake {
		for h := range live {
			if pt.key < h && !*quiet {
				fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
					p.Yellow(fmt.Sprintf("warning: taking %q while a parent incoda holds %q acquires out of sorted order; two nested runs shaped like this can wait on each other until --wait expires", pt.key, h)))
			}
		}
	}
	if len(toTake) == 0 {
		// Every key is the parent's. Nothing to enroll, nothing to watch:
		// a kill addressed to the parent takes this process with it.
		res, runErr := child.Run(argv, os.Stdin, os.Stdout, os.Stderr, nil, child.Options{
			Env:      childEnv(startEnv, held.Format(inherited.L)),
			OwnGroup: len(inherited.L) == 0,
		})
		if runErr != nil {
			return exitWith(ExitSpawn, "cannot run %q: %v", argv[0], runErr)
		}
		if res.Code != 0 {
			return &exitCode{code: res.Code}
		}
		return nil
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
	// argument, and the whole reason a list is allowed at all. The budget
	// started with the command, so machine.lock and migration waits above
	// have already spent part of it.
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
			// The queue's config can change between the pre-check above and
			// this enrollment; the refusal is the same caller mistake either
			// way, so it gets the same message and the same usage exit
			// rather than masquerading as unusable state.
			var sd *lane.SlotsDisagreement
			if errors.As(err, &sd) {
				rc = ExitUsage
				return usagef("%v", err)
			}
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
		// On a pool, unpooled runs of an older incoda (strays and orphan
		// records, spec 2.3) hold slots too. Each poll rescans them,
		// deletes stray lanes that have fully died, and refuses at once
		// when one of them is this run's own ancestor: waiting for it
		// would never end.
		var unpooled []machine.Unpooled
		var countUnpooled func() (int, error)
		if reg.IsPool(key) {
			countUnpooled = func() (int, error) {
				// The probes of one poll wait for a stray's registry
				// lock at most PollProbeWait and never past the budget:
				// a lock held for ever reads as one held slot.
				var end time.Time
				if wait.d >= 0 {
					end = start.Add(wait.d)
				}
				all, err := machine.ScanUnpooled(dir, true, lane.ProbeDeadline(end, lane.PollProbeWait))
				if err != nil {
					return 0, &machine.StateError{Msg: "machine-state: cannot scan for unpooled runs: " + textsafe.Escape(err.Error())}
				}
				mine := machine.ChargedTo(dir, reg, key, all)
				if !chain.Skip {
					for _, u := range mine {
						if !u.Unknown && chain.Contains(u.PID) {
							return 0, machine.UpgradeBlocked(u.PID, u.Key)
						}
					}
				}
				unpooled = mine
				return len(mine), nil
			}
		}
		acqErr := en.Acquire(ctx, lane.AcquireOptions{
			Wait:   budget,
			Poll:   *poll,
			Notify: 60 * time.Second,
			// The keys already held are the ones status shows as held, so
			// they are the ones a kill gets addressed to while this run
			// still queues on the next key.
			Killed: func() (lane.KillRequest, bool) {
				for _, h := range toTake {
					if h.en == nil {
						continue
					}
					if req, ok := h.en.KillRequested(); ok {
						return req, true
					}
				}
				return lane.KillRequest{}, false
			},
			Unpooled: countUnpooled,
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
					if e.File == en.Name() {
						// Held off only by unpooled runs: this run's own
						// ticket is inside the slot count.
						continue
					}
					fmt.Fprintf(stderr, "%s   %s\n", p.Dim("incoda:"),
						p.Dim(fmt.Sprintf("holder pid %d in %s: %s", e.Ticket.PID, textsafe.Escape(e.Ticket.Dir), textsafe.Escape(e.Ticket.CommandString()))))
				}
				for _, u := range unpooled {
					fmt.Fprintf(stderr, "%s   %s\n", p.Dim("incoda:"), p.Dim(u.Line()))
				}
			},
		})
		if acqErr != nil {
			if errors.Is(acqErr, context.Canceled) {
				rc = ExitInterrupt
				return exitWith(ExitInterrupt, "interrupted while queueing on %q", key)
			}
			var killed *lane.KilledError
			if errors.As(acqErr, &killed) {
				rc = ExitKilled
				logKill(toTake, killed.Request)
				return exitWith(ExitKilled, "%s", p.Red(fmt.Sprintf("cancelled while queued on %q by %s: %s",
					key, textsafe.Escape(killed.Request.By), textsafe.Escape(killed.Request.Reason))))
			}
			var rf *machine.Refusal
			var se *machine.StateError
			if errors.As(acqErr, &rf) || errors.As(acqErr, &se) {
				rc = ExitUsage
				if se != nil {
					rc = ExitState
				}
				return machineExit(acqErr)
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
			// On a configured queue every new ticket carries the configured
			// count, so a disagreement means a stale or foreign ticket; the
			// config floors the effective width regardless. On a queue with
			// no configured count the minimum still rules.
			inForce := "the smallest value is in force"
			if pt.cfg.Slots > 0 {
				inForce = fmt.Sprintf("the configured %d is in force", pt.cfg.Slots)
			}
			fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
				p.Yellow(fmt.Sprintf("warning: participants on queue %q disagree about --slots; %s", key, inForce)))
		}
		if !*quiet {
			fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
				p.Green(fmt.Sprintf("acquired queue %q (pid %d)", key, os.Getpid())))
		}
	}

	stop() // hand interrupt handling to the child supervisor

	// While the command runs, watch every held ticket for a kill request at
	// the poll interval. A request closes abort, which takes the job tree
	// down; the message is printed here, after the child is gone, so it is
	// the last thing on stderr rather than buried under the build's tail.
	abort := make(chan struct{})
	killed := make(chan lane.KillRequest, 1)
	stopWatch := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		t := time.NewTicker(*poll)
		defer t.Stop()
		for {
			select {
			case <-stopWatch:
				return
			case <-t.C:
				for _, pt := range toTake {
					if req, ok := pt.en.KillRequested(); ok {
						killed <- req
						close(abort)
						return
					}
				}
			}
		}
	}()
	// The held keys reach a nested incoda through the child's environment
	// only. Setting them on this process would make every later decision
	// that reads the environment see the child's value.
	//
	// The child's INCODA_HELD is every live inherited entry plus this run's
	// own tickets, set only in the child's environment.
	own := make([]held.Entry, 0, len(toTake))
	for _, pt := range toTake {
		own = append(own, held.Entry{Key: pt.key, Ticket: pt.en.Name()})
	}
	res, runErr := child.Run(argv, os.Stdin, os.Stdout, os.Stderr, abort, child.Options{
		Env:      childEnv(startEnv, held.Format(held.Merge(inherited.L, own))),
		OwnGroup: len(inherited.L) == 0,
	})
	close(stopWatch)
	<-watchDone
	if runErr != nil {
		rc = ExitSpawn
		return exitWith(ExitSpawn, "cannot run %q: %v", argv[0], runErr)
	}
	stats = lane.Stats{PeakBytes: res.PeakBytes, HavePeak: res.HavePeak, CPU: res.CPU, HaveCPU: res.HaveCPU}
	// A request the watcher only found after the command had already
	// finished on its own did not kill anything; the job's real exit code
	// is the truth then, and the request goes away with the ticket.
	if res.Aborted {
		req := <-killed
		rc = ExitKilled
		logKill(toTake, req)
		release()
		fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
			p.Red(fmt.Sprintf("killed by %s: %s", textsafe.Escape(req.By), textsafe.Escape(req.Reason))))
		return &exitCode{code: ExitKilled}
	}
	rc = res.Code
	release()
	if res.Code != 0 {
		return &exitCode{code: res.Code}
	}
	return nil
}

// logKill records the kill on every queue the run held, next to the
// request the killer left, so the history reads request then outcome.
func logKill(parts []*lanePart, req lane.KillRequest) {
	for _, pt := range parts {
		pt.q.Logf("queue=%s event=kill pid=%d by=%s reason=%s", pt.key, os.Getpid(), textsafe.LogValue(req.By), textsafe.LogValue(req.Reason))
	}
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

// reportDropped prints and logs every inherited entry that is not passed
// through. A log line is written only to a lane that already exists, so a
// bogus key in the environment never creates a directory.
func reportDropped(dir string, r held.Result, quiet bool, stderr io.Writer, p colorize.Palette) {
	for _, d := range r.Dropped {
		name := d.Key
		if name == "" {
			name = d.Raw
		}
		why := string(d.Why)
		if d.Live {
			why += "; still counts for ordering"
		}
		if !quiet {
			fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
				p.Yellow(fmt.Sprintf("held-dropped: %s (%s)", textsafe.Escape(name), why)))
		}
		if d.Key == "" || !lane.Exists(dir, d.Key) {
			continue
		}
		q, err := lane.Open(dir, d.Key)
		if err != nil {
			continue
		}
		q.Logf("queue=%s event=held-dropped pid=%d ticket=%s why=%s", d.Key, os.Getpid(), textsafe.LogValue(d.Ticket), d.Why)
		q.Close()
	}
}
