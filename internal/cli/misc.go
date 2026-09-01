package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/lockfile"
	"github.com/deblasis/incoda/internal/sysinfo"
)

func cmdWatch(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("watch", stderr)
	queue := fs.String("queue", "", "queue key (defaults to $INCODA_QUEUE)")
	all := fs.Bool("all", false, "watch every queue with state on this machine")
	interval := fs.Duration("interval", 2*time.Second, "repaint interval")
	once := fs.Bool("once", false, "paint once and exit")
	events := fs.Int("events", 5, "how many recent log events to show")
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for watch"}
	}
	if *interval <= 0 {
		return usagef("--interval must be positive")
	}
	for {
		rep, err := buildReport(*queue, *all, *events)
		if err != nil {
			return err
		}
		if !*once {
			clearScreen(stdout)
		}
		fmt.Fprintf(stdout, "incoda watch  %s\n\n", time.Now().Format("15:04:05"))
		renderReport(stdout, rep)
		if *once {
			return nil
		}
		time.Sleep(*interval)
	}
}

// clearScreen uses the ANSI sequence rather than shelling out to cls/clear.
// Windows 10+ consoles and every Unix terminal understand it; when output is a
// pipe it is harmless noise that `--once` avoids anyway.
func clearScreen(w io.Writer) {
	fmt.Fprint(w, "\x1b[H\x1b[2J\x1b[3J")
}

func cmdQueues(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("queues", stderr)
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for queues"}
	}
	dir, err := stateDir()
	if err != nil {
		return err
	}
	keys, err := lane.ListQueues(dir)
	if err != nil {
		return exitWith(ExitState, "cannot list queues: %v", err)
	}
	sort.Strings(keys)
	fmt.Fprintf(stdout, "state dir: %s  (%s)\n", dir, stateDirSource())
	if len(keys) == 0 {
		fmt.Fprintln(stdout, "no queues have state on this machine yet")
		return nil
	}
	for _, k := range keys {
		q, err := lane.Open(dir, k)
		if err != nil {
			fmt.Fprintf(stdout, "  %-24s (unreadable: %v)\n", k, err)
			continue
		}
		snap, err := q.Observe(0)
		q.Close()
		if err != nil {
			fmt.Fprintf(stdout, "  %-24s (unreadable: %v)\n", k, err)
			continue
		}
		state := "free"
		if len(snap.Holders) > 0 {
			state = fmt.Sprintf("%d/%d held, %d waiting", len(snap.Holders), snap.EffectiveSlots, len(snap.Waiting))
		}
		fmt.Fprintf(stdout, "  %-24s %s\n", k, state)
	}
	return nil
}

func cmdForceRelease(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("force-release", stderr)
	queue := fs.String("queue", "", "queue key (defaults to $INCODA_QUEUE)")
	live := fs.Bool("live", false, "break tickets even though live participants exist")
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for force-release"}
	}
	key, err := resolveKey(*queue)
	if err != nil {
		return err
	}
	dir, err := stateDir()
	if err != nil {
		return err
	}
	if !lane.Exists(dir, key) {
		fmt.Fprintf(stdout, "queue %q has no state on this machine; nothing to release\n", key)
		return nil
	}
	q, err := lane.Open(dir, key)
	if err != nil {
		return exitWith(ExitState, "%v", err)
	}
	defer q.Close()
	removed, err := q.ForceRelease(*live)
	if err != nil {
		return exitWith(ExitUsage, "%v", err)
	}
	q.Logf("queue=%s event=force-release removed=%d live=%v by_pid=%d", key, removed, *live, os.Getpid())
	fmt.Fprintf(stdout, "queue %q: removed %d ticket(s)\n", key, removed)
	return nil
}

func cmdDoctor(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("doctor", stderr)
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for doctor"}
	}
	fmt.Fprintf(stdout, "incoda %s (commit %s, built %s)\n", Version, Commit, Date)
	fmt.Fprintf(stdout, "go:        %s  %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	host, _ := os.Hostname()
	fmt.Fprintf(stdout, "host:      %s\n", host)

	dir, err := lane.StateDir()
	if err != nil {
		fmt.Fprintf(stdout, "state dir: UNRESOLVED: %v\n", err)
		return exitWith(ExitState, "cannot resolve the state directory")
	}
	fmt.Fprintf(stdout, "state dir: %s\n", dir)
	fmt.Fprintf(stdout, "  source:  %s\n", stateDirSource())
	if src := stateDirSource(); src == "INCODA_DIR" {
		// A per-project override is the one configuration mistake that breaks
		// the whole model quietly: every fragment looks like a healthy, empty
		// lane while the jobs it was meant to serialise run side by side.
		fmt.Fprintln(stdout, "  WARNING: INCODA_DIR is set. It is a MACHINE-level override, not a per-project one.")
		fmt.Fprintln(stdout, "           If some callers have it set and others do not, they will use different")
		fmt.Fprintln(stdout, "           state directories, form separate lanes, and stop serialising each other.")
	}
	fmt.Fprintf(stdout, "  cwd-independent: yes (state is never derived from the working directory)\n")
	if cwd, err := os.Getwd(); err == nil {
		fmt.Fprintf(stdout, "  current cwd (not used for resolution): %s\n", cwd)
	}

	if err := os.MkdirAll(lane.QueuesDir(dir), 0o755); err != nil {
		fmt.Fprintf(stdout, "  writable: NO (%v)\n", err)
		return exitWith(ExitState, "state directory is not usable")
	}
	fmt.Fprintln(stdout, "  writable: yes")

	if err := probeLocking(dir, stdout); err != nil {
		return exitWith(ExitState, "OS file locking is not usable: %v", err)
	}

	keys, err := lane.ListQueues(dir)
	if err == nil {
		sort.Strings(keys)
		if len(keys) == 0 {
			fmt.Fprintln(stdout, "queues:    none yet")
		} else {
			fmt.Fprintf(stdout, "queues:    %s\n", strings.Join(keys, ", "))
		}
	}
	if k := strings.TrimSpace(os.Getenv("INCODA_QUEUE")); k != "" {
		note := "ok"
		if err := lane.ValidateKey(k); err != nil {
			note = "INVALID: " + err.Error()
		}
		fmt.Fprintf(stdout, "INCODA_QUEUE: %s (%s)\n", k, note)
	} else {
		fmt.Fprintln(stdout, "INCODA_QUEUE: unset (run needs --queue)")
	}
	fmt.Fprintf(stdout, "%s\n", sysinfo.ReadMemory().String())
	return nil
}

// probeLocking proves the OS lock is actually enforced rather than merely not
// erroring. It takes an exclusive lock and then, through a second independent
// handle, checks that the lock is refused. A filesystem that silently ignores
// locks (some network mounts do) fails here instead of failing later as two
// heavy builds running at once.
func probeLocking(dir string, w io.Writer) error {
	path := filepath.Join(dir, ".lockprobe")
	defer os.Remove(path)

	a, err := lockfile.Open(path)
	if err != nil {
		return err
	}
	defer a.Close()
	ok, err := a.TryLock()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("could not take an uncontended lock on %s", path)
	}
	free, err := lockfile.IsFree(path)
	if err != nil {
		return err
	}
	if free {
		return fmt.Errorf("a second handle was able to lock %s while it was held; this filesystem does not enforce locks and incoda cannot serialise anything on it", path)
	}
	fmt.Fprintf(w, "  locking: enforced (%s)\n", lockMechanism)
	return nil
}
