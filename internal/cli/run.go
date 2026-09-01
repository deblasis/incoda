package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/deblasis/incoda/internal/child"
	"github.com/deblasis/incoda/internal/lane"
)

func cmdRun(args []string, _, stderr io.Writer) error {
	fs := newFlagSet("run", stderr)
	queue := fs.String("queue", "", "queue key (defaults to $INCODA_QUEUE)")
	slots := fs.Int("slots", 1, "concurrent holders permitted on this queue")
	reason := fs.String("reason", "", "free-text note shown in status")
	poll := fs.Duration("poll", 500*time.Millisecond, "how often to re-check position while queued")
	quiet := fs.Bool("quiet", false, "suppress lane chatter on stderr")
	noColor := fs.Bool("no-color", false, "never emit ANSI color, even on a terminal (the NO_COLOR environment variable does the same)")
	wait := &waitValue{d: 30 * time.Minute}
	fs.Var(wait, "wait", "max time to queue: a Go duration (30m) or bare seconds (1800); 0 fails immediately, negative waits forever")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: incoda run --queue KEY [--slots N] [--wait DUR] [--reason TEXT] [--] <cmd...>\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for run"}
	}
	argv := fs.Args()
	if len(argv) == 0 {
		return usagef("run needs a command; try: incoda run --queue KEY -- zig build")
	}
	if *slots < 1 {
		return usagef("--slots must be at least 1, got %d", *slots)
	}
	p := paletteFor(stderr, *noColor)
	key, err := resolveKey(*queue)
	if err != nil {
		return err
	}
	dir, err := stateDir()
	if err != nil {
		return err
	}

	q, err := lane.Open(dir, key)
	if err != nil {
		return exitWith(ExitState, "%v", err)
	}
	defer q.Close()

	host, _ := os.Hostname()
	cwd, _ := os.Getwd()
	en, err := q.Enroll(lane.Ticket{
		Slots:    *slots,
		Command:  argv,
		Reason:   *reason,
		Hostname: host,
		Dir:      cwd,
	})
	if err != nil {
		return exitWith(ExitState, "cannot enter queue %q: %v", key, err)
	}

	// From here on the ticket must be released on every exit path. The OS lock
	// covers the paths we cannot reach (SIGKILL, power loss); this covers the
	// ones we can, so the next caller does not wait a poll interval for nothing.
	rc := ExitOK
	released := false
	release := func() {
		if !released {
			en.Release(rc)
			released = true
		}
	}
	defer release()

	ctx, stop := signal.NotifyContext(context.Background(), interruptSignals()...)
	defer stop()

	acqErr := en.Acquire(ctx, lane.AcquireOptions{
		Wait:   wait.d,
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
			q.Logf("queue=%s event=giveup pid=%d waited=%s", key, os.Getpid(), wait.d)
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

	stop() // hand interrupt handling to the child supervisor
	code, runErr := child.Run(argv, os.Stdin, os.Stdout, os.Stderr)
	if runErr != nil {
		rc = ExitSpawn
		return exitWith(ExitSpawn, "cannot run %q: %v", argv[0], runErr)
	}
	rc = code
	release()
	if code != 0 {
		return &exitCode{code: code}
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
