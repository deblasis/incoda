package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/child"
	"github.com/deblasis/incoda/internal/colorize"
	"github.com/deblasis/incoda/internal/fixline"
	"github.com/deblasis/incoda/internal/held"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/runplan"
	"github.com/deblasis/incoda/internal/textsafe"
)

// lanePart is one lane of a run: its place in the plan, its queue handle
// and, once enrolled, the ticket. A single-key run without links is the
// list with one element.
type lanePart struct {
	l   runplan.Lane
	key string
	q   *lane.Queue
	en  *lane.Enrollment
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
	pool := &poolsValue{}
	fs.Var(pool, "pool", "take only these of each named project key's linked pools (comma-separated); on an unlinked key, a set equal to its suggestion becomes its first link")
	fs.Var(pool, "pools", "alias of --pool")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: incoda run --queue KEY[,KEY...] [--pool P,P] [--slots N] [--exclusive] [--wait DUR] [--reason TEXT] [--owner WHO] [--] <cmd...>\n\n")
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

	// The plan (runplan) is the lane set in its total order, with every
	// rule checked before any ticket exists and before any lane is opened,
	// so a closed or reason-requiring lane refuses with nothing to undo and
	// a typo leaves nothing behind. Lanes already held by a parent incoda
	// are skipped (re-entrancy): a recipe that takes its own lane must not
	// deadlock when an agent wraps the whole recipe in run from outside.
	// The parent says which lanes it holds through INCODA_HELD, and a
	// nested run on one of them rides the parent's ticket instead of
	// queueing behind it.
	//
	// Inherited lanes come from the environment incoda was started with.
	// Each entry is probed: dead and malformed ones are dropped, live ones
	// count for ordering and the process group (L), and only those held by
	// a verified ancestor are passed through (P).
	inherited := held.Verify(dir, startGetenv("INCODA_HELD"), chain)
	reportDropped(dir, inherited, *quiet, stderr, p)
	pass := inherited.PassKeys()
	live := inherited.LiveKeys()
	here, _ := os.Getwd()
	req := runplan.Request{
		Named: keys, Slots: *slots, Exclusive: *exclusive, Reason: *reason, Held: pass,
		Fix:       fixline.Run{Flags: carriedFlags(fs, wait), Argv: argv, Dir: here, Here: here},
		WaitGiven: wait.set,
	}
	if pool.set {
		req.Pool = pool.keys
	}
	plan, err := planWithFirstLinks(dir, reg, req, machine.Options{
		Start: start, Wait: wait.d, Poll: *poll, Chain: chain, Stderr: stderr,
	}, *quiet, stderr, p)
	if err != nil {
		return machineExit(err)
	}
	var parts, toTake []*lanePart
	defer func() {
		for _, pt := range parts {
			pt.q.Close()
		}
	}()
	for _, l := range plan.Lanes {
		if pass[l.Key] {
			if !*quiet {
				fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
					p.Dim(fmt.Sprintf("queue %q is already held by a parent incoda; running inside its lane", l.Key)))
			}
			lane.AppendLog(lane.LaneDir(dir, l.Key), "queue=%s event=reenter pid=%d cmd=%s", l.Key, os.Getpid(), textsafe.LogValue(lane.Ticket{Command: argv}.CommandString()))
			continue
		}
		q, err := lane.Open(dir, l.Key)
		if err != nil {
			return exitWith(ExitState, "%v", err)
		}
		// Every registry lock wait of this run stays inside its --wait
		// budget: a stopped incoda keeping a registry lock costs this run
		// its budget, never more.
		q.SetBudget(start, wait.d)
		pt := &lanePart{l: l, key: l.Key, q: q}
		parts = append(parts, pt)
		toTake = append(toTake, pt)
	}
	// The total order that makes multi-lane runs deadlock-free stops at a
	// nested run: a parent holding "b" whose recipe now takes "a" is
	// acquiring out of order, and two such parents can each wait on the
	// other's lane until --wait expires. It cannot be prevented from here
	// (the parent's lane is already held), so it is said out loud.
	for _, pt := range toTake {
		for h := range live {
			if runplan.Less(pt.l, runplan.Lane{Key: h, Pool: reg.IsPool(h)}) && !*quiet {
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
	cwd := here

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

	// Lanes are taken one at a time in the plan's total order: project
	// lanes sorted by key, then pools sorted by key (spec 2.4). Every run
	// orders the same way, so two of them can never each hold what the
	// other waits for: the classic lock-ordering argument. A lane is
	// enrolled only once the one before it is held; enrolling them all and
	// then waiting would let a waiting ticket block later arrivals. The
	// budget started with the command, so machine.lock and migration waits
	// above have already spent part of it.
	for _, pt := range toTake {
		// Enroll's wait for the registry lock ends on an interrupt too, and
		// says once why it waits when another process keeps that lock (a
		// stopped incoda), so the wait is never silent.
		busy := func() {
			if *quiet {
				return
			}
			limit := "no time limit"
			if wait.d >= 0 {
				limit = "limit " + wait.d.String()
			}
			fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
				p.Yellow(fmt.Sprintf("queue %q: registry lock held by another process; waiting (%s)", pt.key, textsafe.Escape(limit))))
		}
		t := lane.Ticket{
			// --exclusive holds a lane the run names; it does not
			// propagate to the pools a link brings (spec 2.4).
			Exclusive: *exclusive && (pt.l.Named || !pt.l.Pool),
			Command:   argv,
			Reason:    *reason,
			Owner:     *owner,
			Hostname:  host,
			Dir:       cwd,
			Via:       pt.l.Via,
			Wait:      wait.raw,
		}
		if !pt.l.Pool {
			// --slots applies to named project lanes only. A pool ticket
			// carries the pool's configured count; the plan has already
			// refused a --slots that disagrees with a named pool.
			t.Slots = *slots
		}
		en, err := pt.q.EnrollContext(ctx, t, busy)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				rc = ExitInterrupt
				return exitWith(ExitInterrupt, "interrupted while queueing on %q", pt.key)
			}
			// The queue's config can change between the pre-check above and
			// this enrollment; the refusal is the same caller mistake either
			// way, so it gets the same message and the same usage exit
			// rather than masquerading as unusable state.
			var sd *lane.SlotsDisagreement
			var ce *lane.ClosedError
			var re *lane.ReasonRequiredError
			var ns *lane.NewerSchemaError
			switch {
			case errors.As(err, &sd), errors.As(err, &ce), errors.As(err, &re):
				rc = ExitUsage
				return usagef("%v", err)
			case errors.As(err, &ns):
				rc = ExitState
				return exitWith(ExitState, "machine-state: %v", err)
			}
			if errors.Is(err, lane.ErrRegistryBusy) {
				rc = ExitTimeout
				return exitWith(ExitTimeout, "cannot enter queue %q within --wait: %v. Check `incoda status --queue %s`. Do NOT bypass the lane; surface the wait and coordinate instead", pt.key, err, pt.key)
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
		role := pt.l.Role()
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
			// A lane closed while this run waits on it ends the wait
			// (spec 2.5); the config is re-read without a lock (every
			// write is a rename). A config written by a newer incoda
			// fails closed.
			Check: func() error { return waitingCheck(pt.q.Dir, key) },
			OnWait: func(pos, effSlots int, live []lane.Entry, waited time.Duration) {
				if *quiet {
					return
				}
				if live == nil && effSlots == 0 {
					// The poll could not read the lane: another process
					// kept its registry lock (a stopped incoda).
					fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
						p.Yellow(fmt.Sprintf("queue %q busy (%s), waited %s%s",
							key, lane.ErrRegistryBusy, waited.Round(time.Second), waitBudget(wait.d))))
					return
				}
				ahead := pos
				if ahead < 0 {
					ahead = len(live)
				}
				// A pool's busy line names its role (spec 5.1); a project
				// lane's line is unchanged.
				rolePart := ""
				if role != "" {
					rolePart = role + "; "
				}
				fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
					p.Yellow(fmt.Sprintf("queue %q busy (%s%d slot(s), %d ahead of you), waited %s%s",
						key, rolePart, effSlots, ahead, waited.Round(time.Second), waitBudget(wait.d))))
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
						p.Dim(fmt.Sprintf("holder pid %d in %s: %s%s", e.Ticket.PID, textsafe.Escape(e.Ticket.Dir), textsafe.Escape(e.Ticket.CommandString()), viaText(e.Ticket.Via))))
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
				named := fmt.Sprintf("queue %q", key)
				if role != "" {
					named += " (" + role + ")"
				}
				return exitWith(ExitTimeout,
					"%s still busy after %s. Check `incoda status --queue %s`. Do NOT bypass the lane; surface the wait and coordinate instead",
					named, wait.d, pt.l.StatusKey())
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
			if pt.l.Cfg.Slots > 0 {
				inForce = fmt.Sprintf("the configured %d is in force", pt.l.Cfg.Slots)
			}
			fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
				p.Yellow(fmt.Sprintf("warning: participants on queue %q disagree about --slots; %s", key, inForce)))
		}
		if !*quiet {
			what := fmt.Sprintf("acquired queue %q (pid %d)", key, os.Getpid())
			if role != "" {
				what = fmt.Sprintf("acquired queue %q (%s; pid %d)", key, role, os.Getpid())
			}
			fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"), p.Green(what))
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

// planWithFirstLinks plans the run and, when its --pool set equals an
// unlinked key's suggestion, writes that first link (spec 4.2) and plans
// again. Each first link is a compare-and-set under machine.lock, then the
// key's registry lock, before any ticket: still unlinked, it is written
// and logged event=link by=run and the run says linked:; already linked to
// the same set (another run won the race), the run goes on; linked to a
// different set, it refuses with link-conflict.
func planWithFirstLinks(dir string, reg *machine.Registry, req runplan.Request, o machine.Options, quiet bool, stderr io.Writer, p colorize.Palette) (*runplan.Plan, error) {
	for {
		plan, err := runplan.Make(dir, reg, req)
		if err != nil {
			return nil, err
		}
		if len(plan.FirstLinks) == 0 {
			if !quiet {
				for _, n := range plan.Notes {
					fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"), p.Dim(n))
				}
			}
			return plan, nil
		}
		for _, fl := range plan.FirstLinks {
			beforeFirstLink(dir, fl.Key)
			res, err := machine.WriteLink(dir, fl.Key, "run", o, func(_ *machine.Registry, c *lane.Config) error {
				switch {
				case len(c.Pools) == 0:
					c.Pools, c.QuietMachine = fl.Pools, c.QuietMachine || fl.Quiet
					return nil
				case machine.SameSet(c.Pools, fl.Pools):
					return lane.ErrNoChange
				}
				by := "another process"
				if pid, ok := machine.LastLinker(dir, fl.Key); ok {
					by = fmt.Sprintf("pid %d", pid)
				}
				return &machine.Refusal{Msg: fmt.Sprintf("link-conflict: %q was just linked to %s by %s; rerun without --pool", fl.Key, machine.SetText(c.Pools), by)}
			})
			if err != nil {
				return nil, err
			}
			if res.Changed {
				// Printed even with --quiet: it records a stored change.
				fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"), p.Green(fmt.Sprintf("linked: %s (stored; every later run on %s takes these pools)",
					machine.LinkedLine(fl.Key, res.New.Pools, res.New.QuietMachine), fl.Key)))
			}
		}
	}
}

// beforeFirstLink is a seam for tests; production never changes it.
var beforeFirstLink = func(dir, key string) {}

// carriedFlags is every flag the caller gave except --queue and --pool, in
// flag-name order, as a printed fix line repeats them (spec 2.6); --wait
// keeps the text it was given.
func carriedFlags(fs *flag.FlagSet, wait *waitValue) []fixline.Flag {
	var out []fixline.Flag
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "queue", "pool", "pools":
			return
		case "wait":
			out = append(out, fixline.Flag{Name: "wait", Value: wait.raw})
			return
		}
		b, ok := f.Value.(interface{ IsBoolFlag() bool })
		out = append(out, fixline.Flag{Name: f.Name, Value: f.Value.String(), Bool: ok && b.IsBoolFlag()})
	})
	return out
}

// viaText is the " via <keys>" a holder line of a pool ticket ends with
// (spec 5.1); empty for a ticket taken directly.
func viaText(via []string) string {
	if len(via) == 0 {
		return ""
	}
	return " via " + strings.Join(via, ",")
}

// waitingCheck is the per-poll config check of a waiting run: closed while
// waiting refuses (exit 120), a config written by a newer incoda fails
// closed (122). A config that cannot be read is left to the admission
// rule, which falls back to the safe width.
func waitingCheck(laneDir, key string) error {
	cfg, err := lane.ReadConfig(laneDir)
	var ns *lane.NewerSchemaError
	switch {
	case errors.As(err, &ns):
		return &machine.StateError{Msg: "machine-state: " + textsafe.Escape(err.Error())}
	case err == nil && cfg.Closed != "":
		return &machine.Refusal{Msg: fmt.Sprintf("closed-while-waiting: %q: %s", key, textsafe.Escape(cfg.Closed))}
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
