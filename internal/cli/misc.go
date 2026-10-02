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
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/sysinfo"
	"github.com/deblasis/incoda/internal/textsafe"
	"github.com/deblasis/incoda/internal/tui"
)

// cmdWatch is the live view. On a terminal it is the interactive screen:
// the overview of every queue when no key is given, or one queue's holders
// and waiters with --queue, with kill behind a prompt. On a pipe, or with
// --once or --plain, it repaints the plain status text the way it always
// did, so scripts and logs keep working.
func cmdWatch(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("watch", stderr)
	queue := fs.String("queue", "", "queue key to open; with no key the overview of every queue")
	all := fs.Bool("all", false, "plain mode: watch every queue with state on this machine")
	interval := fs.Duration("interval", 2*time.Second, "repaint interval")
	once := fs.Bool("once", false, "paint the plain view once and exit")
	plain := fs.Bool("plain", false, "repaint the plain status text instead of the interactive screen; implied by --once or a non-terminal stdout")
	events := fs.Int("events", 5, "how many recent log events to show")
	noColor := fs.Bool("no-color", false, "never emit ANSI color, even on a terminal (the NO_COLOR environment variable does the same)")
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for watch"}
	}
	if *interval <= 0 {
		return usagef("--interval must be positive")
	}
	if !*once && !*plain && !*all && colorize.IsTerminal(stdout) {
		dir, _, err := readState()
		if err != nil {
			return err
		}
		// --queue, else INCODA_QUEUE, else the overview: the same rule as
		// plain mode, so a session that set the variable opens its queue.
		key := strings.TrimSpace(*queue)
		if key == "" {
			key = strings.TrimSpace(os.Getenv("INCODA_QUEUE"))
		}
		if key != "" {
			if err := lane.ValidateKey(key); err != nil {
				return usagef("invalid queue key: %v", err)
			}
		}
		if err := tui.Run(tui.Options{Dir: dir, Version: Version, Key: key, Interval: *interval, Events: max(*events, 8), NoColor: *noColor}); err != nil {
			return exitWith(ExitState, "watch: %v", err)
		}
		return nil
	}
	// Plain mode with no key at all is the overview too; INCODA_QUEUE still
	// narrows it, because a script that set the variable meant one queue.
	if strings.TrimSpace(*queue) == "" && strings.TrimSpace(os.Getenv("INCODA_QUEUE")) == "" {
		*all = true
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
		if rep.Banner != "" {
			fmt.Fprintf(stdout, "%s\n\n", p.Yellow("incoda: "+rep.Banner))
		}
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
	dir, v, err := readState()
	if err != nil {
		return err
	}
	printBanner(stderr, v.Banner)
	p := paletteFor(stdout, *noColor)
	keys, err := lane.ListIn(v.Root)
	if err != nil {
		return exitWith(ExitState, "cannot list queues: %v", err)
	}
	sort.Strings(keys)
	fmt.Fprintf(stdout, "%s %s  %s\n", p.Dim("state dir:"), dir, p.Dim("("+stateDirSource()+")"))
	if len(keys) == 0 {
		fmt.Fprintln(stdout, p.Dim("no queues have state on this machine yet"))
		return nil
	}
	mode := lane.Existing
	if !v.Migrated {
		mode = lane.ReadOnly
	}
	for _, k := range keys {
		q, err := lane.OpenIn(v.Root, k, mode)
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
		if snap.Config.Closed != "" {
			state = p.BoldRed("closed") + " " + p.Dim(snap.Config.Closed)
		}
		desc := ""
		if snap.Config.Description != "" {
			desc = "  " + p.Dim(snap.Config.Description)
		}
		fmt.Fprintf(stdout, "  %s %s%s\n", fmt.Sprintf("%-24s", k), state, desc)
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
	dir, v, err := readState()
	if err != nil {
		return err
	}
	if !lane.ExistsIn(v.Root, key) {
		fmt.Fprintf(stdout, "queue %q has no state on this machine; nothing to release\n", key)
		return nil
	}
	if *live && !v.Migrated {
		if err := upgradePending(v, key); err != nil {
			return err
		}
	}
	q, err := lane.OpenIn(v.Root, key, lane.Existing)
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
	if !*live {
		// Records of old-holder kills whose job has fully exited (spec
		// 3.2): they hold nothing, so plain force-release clears them.
		if n, err := machine.SweepOrphans(dir, key); err == nil && n > 0 {
			fmt.Fprintf(stdout, "queue %q: removed %d stale orphan record(s)\n", key, n)
		}
	}
	return nil
}

// upgradePending refuses force-release --live while machine.json is absent
// (spec 3.2): deleting a live ticket of an older incoda would empty the
// upgrade's idle check while that job keeps running, so the upgrade would
// overlap it. It prints one stop line per live ticket instead (kill ends
// an older incoda with its whole job). With no live ticket it refuses
// nothing.
func upgradePending(v machine.View, key string) error {
	live, err := lane.ProbeLane(filepath.Join(v.Root, key))
	if err != nil {
		return exitWith(ExitState, "cannot probe queue %q: %s", key, textsafe.Escape(err.Error()))
	}
	if len(live) == 0 {
		return nil
	}
	lines := []string{"upgrade-pending: force-release --live would hide a running job from the upgrade; ask the user before stopping another session's job; they can run:"}
	for _, p := range live {
		lines = append(lines, "  "+machine.KillLine(key, p.PID(), machine.UpgradeReason, false))
	}
	return exitWith(ExitUsage, "%s", strings.Join(lines, "\nincoda: "))
}

func cmdDoctor(args []string, stdout, stderr io.Writer) error {
	start := time.Now()
	fs := newFlagSet("doctor", stderr)
	noColor := fs.Bool("no-color", false, "never emit ANSI color, even on a terminal (the NO_COLOR environment variable does the same)")
	rebuild := fs.String("rebuild-registry", "", "a human decision after machine.json was lost or broken: write a new one naming exactly these pools (comma-separated)")
	wait := &waitValue{d: time.Minute}
	fs.Var(wait, "wait", "with --rebuild-registry: how long to wait for machine.lock")
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
	fmt.Fprintf(stdout, "  %s %s (state is never derived from the working directory)\n", p.Dim("cwd-independent:"), p.Green("yes"))
	if cwd, err := os.Getwd(); err == nil {
		fmt.Fprintf(stdout, "  %s %s\n", p.Dim("current cwd (not used for resolution):"), cwd)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(stdout, "  %s %s\n", p.Dim("writable:"), p.BoldRed(fmt.Sprintf("NO (%v)", err)))
		return exitWith(ExitState, "state directory is not usable")
	}
	fmt.Fprintf(stdout, "  %s %s\n", p.Dim("writable:"), p.Green("yes"))

	if err := probeLocking(dir, stdout, p); err != nil {
		return exitWith(ExitState, "OS file locking is not usable: %v", err)
	}

	if *rebuild != "" {
		var pools []string
		for _, k := range strings.Split(*rebuild, ",") {
			if k = strings.TrimSpace(k); k != "" {
				pools = append(pools, k)
			}
		}
		reg, err := machine.Rebuild(dir, pools, machine.Options{
			Start: start, Wait: wait.d, Poll: 200 * time.Millisecond, Chain: procinfo.ParentChain(),
			By: "incoda " + v, Stderr: stderr,
		}, func(key, kind string) { fmt.Fprintf(stdout, "rebuild-registry: %s: %s\n", key, kind) })
		if err != nil {
			return machineExit(err)
		}
		fmt.Fprintf(stdout, "rebuild-registry: wrote machine.json (generation %d)\n", reg.Generation)
	}

	if view, err := machine.Inspect(dir); err == nil {
		keys, err := lane.ListIn(view.Root)
		if err == nil {
			sort.Strings(keys)
			if len(keys) == 0 {
				fmt.Fprintln(stdout, p.Dim("queues:    none yet"))
			} else {
				fmt.Fprintf(stdout, "%s %s\n", p.Dim("queues:   "), strings.Join(keys, ", "))
			}
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

	h := machine.Diagnose(dir)
	fmt.Fprintf(stdout, "%s %s\n", p.Dim("layout:   "), textsafe.Escape(h.Layout))
	if h.Fence != "" {
		fmt.Fprintf(stdout, "%s %s\n", p.Dim("fence:    "), h.Fence)
	}
	attention := h.Attention
	if stateDirSource() == "INCODA_DIR" {
		attention = append(attention, "INCODA_DIR is set: it is a MACHINE-level override, not a per-project one; it splits pools across state directories, so a caller without it set uses a different state directory, forms separate lanes, and stops serialising against this one")
	}
	for _, a := range attention {
		fmt.Fprintf(stdout, "%s %s\n", p.BoldYellow("attention:"), a)
	}
	for _, pr := range h.Problems {
		fmt.Fprintf(stdout, "%s %s\n", p.BoldRed("problem:  "), pr)
	}
	fmt.Fprintf(stdout, "%s\n", p.Dim(sysinfo.MachineLine(sysinfo.ReadMemory(), sysinfo.ReadCPU())))
	if len(h.Problems) > 0 {
		return exitWith(ExitState, "machine-state: %d problem(s) make runs fail closed; see the problem: lines above", len(h.Problems))
	}
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
