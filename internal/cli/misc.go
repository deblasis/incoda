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

	"github.com/deblasis/incoda/internal/colorize"
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
	noColor := fs.Bool("no-color", false, "never emit ANSI color, even on a terminal (the NO_COLOR environment variable does the same)")
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for watch"}
	}
	if *interval <= 0 {
		return usagef("--interval must be positive")
	}
	p := paletteFor(stdout, *noColor)
	for {
		rep, err := buildReport(*queue, *all, *events)
		if err != nil {
			return err
		}
		if !*once {
			clearScreen(stdout)
		}
		fmt.Fprintf(stdout, "%s  %s\n\n", p.Bold("incoda watch"), p.Dim(time.Now().Format("15:04:05")))
		renderReport(stdout, p, rep)
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
	noColor := fs.Bool("no-color", false, "never emit ANSI color, even on a terminal (the NO_COLOR environment variable does the same)")
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for queues"}
	}
	dir, err := stateDir()
	if err != nil {
		return err
	}
	p := paletteFor(stdout, *noColor)
	keys, err := lane.ListQueues(dir)
	if err != nil {
		return exitWith(ExitState, "cannot list queues: %v", err)
	}
	sort.Strings(keys)
	fmt.Fprintf(stdout, "%s %s  %s\n", p.Dim("state dir:"), dir, p.Dim("("+stateDirSource()+")"))
	if len(keys) == 0 {
		fmt.Fprintln(stdout, p.Dim("no queues have state on this machine yet"))
		return nil
	}
	for _, k := range keys {
		q, err := lane.Open(dir, k)
		if err != nil {
			fmt.Fprintf(stdout, "  %s %s\n", fmt.Sprintf("%-24s", k), p.Red(fmt.Sprintf("(unreadable: %v)", err)))
			continue
		}
		snap, err := q.Observe(0)
		q.Close()
		if err != nil {
			fmt.Fprintf(stdout, "  %s %s\n", fmt.Sprintf("%-24s", k), p.Red(fmt.Sprintf("(unreadable: %v)", err)))
			continue
		}
		state := p.BoldGreen("free")
		if len(snap.Holders) > 0 {
			state = p.BoldYellow(fmt.Sprintf("%d/%d held, %d waiting", len(snap.Holders), snap.EffectiveSlots, len(snap.Waiting)))
		}
		fmt.Fprintf(stdout, "  %s %s\n", fmt.Sprintf("%-24s", k), state)
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
	noColor := fs.Bool("no-color", false, "never emit ANSI color, even on a terminal (the NO_COLOR environment variable does the same)")
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for doctor"}
	}
	p := paletteFor(stdout, *noColor)

	v, c, d := versionInfo()
	fmt.Fprintf(stdout, "incoda %s (commit %s, built %s)\n", p.Bold(v), c, d)
	fmt.Fprintf(stdout, "%s %s  %s/%s\n", p.Dim("go:       "), runtime.Version(), runtime.GOOS, runtime.GOARCH)
	host, _ := os.Hostname()
	fmt.Fprintf(stdout, "%s %s\n", p.Dim("host:     "), host)

	dir, err := lane.StateDir()
	if err != nil {
		fmt.Fprintf(stdout, "%s %s\n", p.Dim("state dir:"), p.BoldRed("UNRESOLVED: "+err.Error()))
		return exitWith(ExitState, "cannot resolve the state directory")
	}
	fmt.Fprintf(stdout, "%s %s\n", p.Dim("state dir:"), dir)
	fmt.Fprintf(stdout, "  %s %s\n", p.Dim("source: "), stateDirSource())
	if src := stateDirSource(); src == "INCODA_DIR" {
		// A per-project override is the one configuration mistake that breaks
		// the whole model quietly: every fragment looks like a healthy, empty
		// lane while the jobs it was meant to serialise run side by side.
		fmt.Fprintf(stdout, "  %s INCODA_DIR is set. It is a MACHINE-level override, not a per-project one.\n", p.BoldYellow("WARNING:"))
		fmt.Fprintln(stdout, "           If some callers have it set and others do not, they will use different")
		fmt.Fprintln(stdout, "           state directories, form separate lanes, and stop serialising each other.")
	}
	fmt.Fprintf(stdout, "  %s %s (state is never derived from the working directory)\n", p.Dim("cwd-independent:"), p.Green("yes"))
	if cwd, err := os.Getwd(); err == nil {
		fmt.Fprintf(stdout, "  %s %s\n", p.Dim("current cwd (not used for resolution):"), cwd)
	}

	if err := os.MkdirAll(lane.QueuesDir(dir), 0o755); err != nil {
		fmt.Fprintf(stdout, "  %s %s\n", p.Dim("writable:"), p.BoldRed(fmt.Sprintf("NO (%v)", err)))
		return exitWith(ExitState, "state directory is not usable")
	}
	fmt.Fprintf(stdout, "  %s %s\n", p.Dim("writable:"), p.Green("yes"))

	if err := probeLocking(dir, stdout, p); err != nil {
		return exitWith(ExitState, "OS file locking is not usable: %v", err)
	}

	keys, err := lane.ListQueues(dir)
	if err == nil {
		sort.Strings(keys)
		if len(keys) == 0 {
			fmt.Fprintln(stdout, p.Dim("queues:    none yet"))
		} else {
			fmt.Fprintf(stdout, "%s %s\n", p.Dim("queues:   "), strings.Join(keys, ", "))
		}
	}
	if k := strings.TrimSpace(os.Getenv("INCODA_QUEUE")); k != "" {
		note := p.Green("ok")
		if err := lane.ValidateKey(k); err != nil {
			note = p.BoldRed("INVALID: " + err.Error())
		}
		fmt.Fprintf(stdout, "%s %s (%s)\n", p.Dim("INCODA_QUEUE:"), k, note)
	} else {
		fmt.Fprintf(stdout, "%s %s\n", p.Dim("INCODA_QUEUE:"), p.Dim("unset (run needs --queue)"))
	}
	fmt.Fprintf(stdout, "%s\n", p.Dim(sysinfo.ReadMemory().String()))
	return nil
}

// probeLocking proves the OS lock is actually enforced rather than merely not
// erroring. It takes an exclusive lock and then, through a second independent
// handle, checks that the lock is refused. A filesystem that silently ignores
// locks (some network mounts do) fails here instead of failing later as two
// heavy builds running at once.
func probeLocking(dir string, w io.Writer, p colorize.Palette) error {
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
	fmt.Fprintf(w, "  %s %s (%s)\n", p.Dim("locking:"), p.Green("enforced"), lockMechanism)
	return nil
}
