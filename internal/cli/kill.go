package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/proc"
)

// cmdKill addresses a kill request to one participant and reports whether it
// was honoured. The request is cooperative first: the participant's own
// incoda notices within a poll interval, tells its owner who killed it and
// why, takes its job tree down and exits 124. --force is for the participant
// that never answers.
func cmdKill(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("kill", stderr)
	queue := fs.String("queue", "", "queue key (defaults to $INCODA_QUEUE)")
	pid := fs.Int("pid", 0, "pid of the holder or waiter, as status shows it")
	reason := fs.String("reason", "", "why; the killed job's owner reads this on their stderr and the log keeps it")
	wait := fs.Duration("wait", 5*time.Second, "how long to give the participant to acknowledge before giving up, or terminating it with --force")
	force := fs.Bool("force", false, "terminate the participant's process if it does not acknowledge within --wait")
	noColor := fs.Bool("no-color", false, "never emit ANSI color, even on a terminal (the NO_COLOR environment variable does the same)")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: incoda kill --queue KEY --pid N --reason TEXT [--wait 5s] [--force]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for kill"}
	}
	if *pid <= 0 {
		return usagef("kill needs --pid, the participant's pid as `incoda status` shows it")
	}
	if strings.TrimSpace(*reason) == "" {
		return usagef("kill needs --reason: the killed job's owner is told why on their own stderr, and the log keeps it")
	}
	p := paletteFor(stdout, *noColor)
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

	req := lane.KillRequest{By: whoami(), ByPID: os.Getpid(), Reason: *reason}
	entry, err := q.RequestKill(*pid, req)
	if errors.Is(err, lane.ErrNoParticipant) {
		return usagef("%v", err)
	}
	if err != nil {
		return exitWith(ExitState, "cannot request the kill: %v", err)
	}
	role := "waiter"
	if entry.Holding {
		role = "holder"
	}
	fmt.Fprintf(stdout, "%s pid %d (%s) on queue %q: %s\n", p.Yellow("kill requested:"), *pid, role, key, entry.Ticket.CommandString())

	gone, err := q.WaitGone(*pid, *wait, 100*time.Millisecond)
	if err != nil {
		return exitWith(ExitState, "%v", err)
	}
	if gone {
		fmt.Fprintf(stdout, "%s\n", p.Green(fmt.Sprintf("pid %d released the lane", *pid)))
		return nil
	}
	if !*force {
		return exitWith(ExitKillPending,
			"pid %d has not acknowledged after %s. It may be an incoda from before kill existed, or wedged. "+
				"Rerun with --force to terminate it; the kernel frees the lane when it dies", *pid, *wait)
	}
	if err := proc.Terminate(*pid, ExitKilled); err != nil {
		return exitWith(ExitState, "cannot terminate pid %d: %v", *pid, err)
	}
	q.Logf("queue=%s event=kill pid=%d by=%s reason=%q forced=true", key, *pid, req.By, req.Reason)
	gone, err = q.WaitGone(*pid, 5*time.Second, 100*time.Millisecond)
	if err != nil {
		return exitWith(ExitState, "%v", err)
	}
	if !gone {
		return exitWith(ExitState, "pid %d was terminated but its ticket is still held; check `incoda status --queue %s`", *pid, key)
	}
	fmt.Fprintf(stdout, "%s\n", p.Green(fmt.Sprintf("pid %d terminated; the kernel released the lane", *pid)))
	return nil
}

// whoami names the killer as user@host, which is what the killed job's owner
// wants to know first.
func whoami() string {
	name := "unknown"
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	host, _ := os.Hostname()
	if host == "" {
		return name
	}
	return name + "@" + host
}
