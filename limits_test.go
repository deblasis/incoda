package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Exit codes of the two limits, distinct from every other incoda code.
const (
	exitMaxRun = 131
	exitStall  = 132
)

var (
	limitsOnce sync.Once
	limitsBin  string
	limitsErr  error
)

// limitsBinary builds internal/testprog/limits next to the incoda test binary.
func limitsBinary(t *testing.T) string {
	t.Helper()
	incoda, _ := binaries(t)
	limitsOnce.Do(func() {
		name := "limits"
		if strings.HasSuffix(incoda, ".exe") {
			name += ".exe"
		}
		limitsBin = filepath.Join(filepath.Dir(incoda), name)
		cmd := exec.Command("go", "build", "-o", limitsBin, "./internal/testprog/limits")
		cmd.Env = append(os.Environ(), "GOTOOLCHAIN=auto")
		if out, err := cmd.CombinedOutput(); err != nil {
			limitsErr = fmt.Errorf("build limits: %v\n%s", err, out)
		}
	})
	if limitsErr != nil {
		t.Fatal(limitsErr)
	}
	return limitsBin
}

func laneLog(t *testing.T, state, key string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(laneDir(state, key), "lane.log"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// logLine returns the last lane.log line of the given event.
func logLine(log, event string) string {
	last := ""
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, "event="+event+" ") {
			last = l
		}
	}
	return last
}

// timedRun runs incoda and reports its output, exit code and wall time.
func timedRun(t *testing.T, incoda, state string, args ...string) (string, int, time.Duration) {
	t.Helper()
	start := time.Now()
	out, code := runIncoda(t, incoda, state, args...)
	return out, code, time.Since(start)
}

// TestMaxRunKillsAJobPastItsLimit: a job that keeps its slot longer than
// --max-run is killed, run exits with its own code, says which limit fired,
// the lane is free and the release line names the limit.
func TestMaxRunKillsAJobPastItsLimit(t *testing.T) {
	incoda, _ := binaries(t)
	lim := limitsBinary(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "mr")

	out, code, el := timedRun(t, incoda, state, "run", "--queue", "mr", "--poll", "50ms", "--max-run", "1s",
		"--", lim, "sleep", "30000")
	if code != exitMaxRun {
		t.Fatalf("a job past --max-run exits %d, got %d:\n%s", exitMaxRun, code, out)
	}
	if el > 15*time.Second {
		t.Fatalf("the limit took %s to act", el)
	}
	if !strings.Contains(out, "--max-run") {
		t.Fatalf("stderr must name the limit that fired:\n%s", out)
	}
	if n := countTickets(t, state, "mr"); n != 0 {
		t.Fatalf("%d ticket(s) left after the limit fired", n)
	}
	log := laneLog(t, state, "mr")
	rel := logLine(log, "release")
	if !strings.Contains(rel, "limit=max-run") || !strings.Contains(rel, fmt.Sprintf("rc=%d", exitMaxRun)) {
		t.Fatalf("the release line must record the limit and rc:\n%s", log)
	}
	if kl := logLine(log, "kill"); !strings.Contains(kl, "limit=max-run") {
		t.Fatalf("the kill event must record the limit:\n%s", log)
	}
	// The sidecar beside the capture carries the same code the run exits
	// with: a limit kill never produced the job's own.
	exits, err := filepath.Glob(filepath.Join(laneDir(state, "mr"), "jobs", "*.exit"))
	if err != nil || len(exits) != 1 {
		t.Fatalf("want one exit sidecar beside the capture, got %v (%v)", exits, err)
	}
	if b, err := os.ReadFile(exits[0]); err != nil || strings.TrimSpace(string(b)) != fmt.Sprint(exitMaxRun) {
		t.Fatalf("the sidecar must record the run's code %d, got %q (%v)", exitMaxRun, b, err)
	}
}

// TestMaxRunLeavesAShortJobAlone: finishing inside the limit is a plain run.
func TestMaxRunLeavesAShortJobAlone(t *testing.T) {
	incoda, _ := binaries(t)
	lim := limitsBinary(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "mrok")

	out, code := runIncoda(t, incoda, state, "run", "--queue", "mrok", "--poll", "50ms", "--max-run", "60s",
		"--", lim, "sleep", "200")
	if code != 0 {
		t.Fatalf("a job inside --max-run exits 0, got %d:\n%s", code, out)
	}
	if rel := logLine(laneLog(t, state, "mrok"), "release"); strings.Contains(rel, "limit=") {
		t.Fatalf("no limit fired, the release line must not say so:\n%s", rel)
	}
}

// TestStallKillsAnIdleJob: a job tree that uses no CPU for --stall is killed
// with the stall exit code, however short its wall time so far.
func TestStallKillsAnIdleJob(t *testing.T) {
	requireStallCounter(t)
	incoda, _ := binaries(t)
	lim := limitsBinary(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "st")

	out, code, el := timedRun(t, incoda, state, "run", "--queue", "st", "--poll", "50ms", "--stall", "1s",
		"--", lim, "sleep", "30000")
	if code != exitStall {
		t.Fatalf("an idle job past --stall exits %d, got %d:\n%s", exitStall, code, out)
	}
	if el > 15*time.Second {
		t.Fatalf("the limit took %s to act", el)
	}
	if !strings.Contains(out, "--stall") {
		t.Fatalf("stderr must name the limit that fired:\n%s", out)
	}
	rel := logLine(laneLog(t, state, "st"), "release")
	if !strings.Contains(rel, "limit=stall") || !strings.Contains(rel, fmt.Sprintf("rc=%d", exitStall)) {
		t.Fatalf("the release line must record the limit and rc:\n%s", rel)
	}
}

// TestStallSparesABusyJob: a job that keeps using CPU for longer than
// --stall is not stalled. Without this the limit would be a second max-run.
func TestStallSparesABusyJob(t *testing.T) {
	incoda, _ := binaries(t)
	lim := limitsBinary(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "stb")

	out, code := runIncoda(t, incoda, state, "run", "--queue", "stb", "--poll", "50ms", "--stall", "1s",
		"--", lim, "spin", "3500")
	if code != 0 {
		t.Fatalf("a busy job must not be killed by --stall, got exit %d:\n%s", code, out)
	}
	if rel := logLine(laneLog(t, state, "stb"), "release"); strings.Contains(rel, "limit=") {
		t.Fatalf("no limit fired:\n%s", rel)
	}
}

// TestStallCountsDescendants: the process the run started idles while its
// grandchild burns CPU. The tree is busy, so no stall; the control, a tree
// whose grandchild sleeps too, is stalled.
func TestStallCountsDescendants(t *testing.T) {
	requireStallCounter(t)
	incoda, _ := binaries(t)
	lim := limitsBinary(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "std")

	out, code := runIncoda(t, incoda, state, "run", "--queue", "std", "--poll", "50ms", "--stall", "1s",
		"--", lim, "tree", "spin", "3500")
	if code != 0 {
		t.Fatalf("CPU burned by a grandchild counts for the tree, got exit %d:\n%s", code, out)
	}
	out, code = runIncoda(t, incoda, state, "run", "--queue", "std", "--poll", "50ms", "--stall", "1s",
		"--", lim, "tree", "sleep", "30000")
	if code != exitStall {
		t.Fatalf("a tree that is idle throughout is stalled, got exit %d:\n%s", code, out)
	}
}

// TestLimitLaneDefaultsAndOverrides: config sets a lane default; a run with
// no flag takes it, a flag overrides it either way (0 turns it off), and
// config shows and clears it.
func TestLimitLaneDefaultsAndOverrides(t *testing.T) {
	requireStallCounter(t)
	incoda, _ := binaries(t)
	lim := limitsBinary(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "ld")

	if out, code := runIncoda(t, incoda, state, "config", "ld", "--max-run", "1s", "--stall", "45m"); code != 0 {
		t.Fatalf("config --max-run: %d\n%s", code, out)
	}
	if out, code := runIncoda(t, incoda, state, "config", "ld"); code != 0 || !strings.Contains(out, "1s") || !strings.Contains(out, "45m") {
		t.Fatalf("config must show both limits (%d):\n%s", code, out)
	}

	// No flag: the lane default applies.
	out, code := runIncoda(t, incoda, state, "run", "--queue", "ld", "--poll", "50ms", "--", lim, "sleep", "30000")
	if code != exitMaxRun {
		t.Fatalf("the lane's --max-run default applies when the flag is absent, got exit %d:\n%s", code, out)
	}
	// A longer flag overrides the default.
	out, code = runIncoda(t, incoda, state, "run", "--queue", "ld", "--poll", "50ms", "--max-run", "60s", "--", lim, "sleep", "2500")
	if code != 0 {
		t.Fatalf("--max-run 60s overrides a 1s default, got exit %d:\n%s", code, out)
	}
	// 0 turns the default off.
	out, code = runIncoda(t, incoda, state, "run", "--queue", "ld", "--poll", "50ms", "--max-run", "0", "--", lim, "sleep", "2500")
	if code != 0 {
		t.Fatalf("--max-run 0 disables the default, got exit %d:\n%s", code, out)
	}
	// The stall default is a separate setting and applies by itself.
	if out, code := runIncoda(t, incoda, state, "config", "ld", "--max-run", "0", "--stall", "1s"); code != 0 {
		t.Fatalf("config swap: %d\n%s", code, out)
	}
	out, code = runIncoda(t, incoda, state, "run", "--queue", "ld", "--poll", "50ms", "--", lim, "sleep", "30000")
	if code != exitStall {
		t.Fatalf("the lane's --stall default applies, got exit %d:\n%s", code, out)
	}
	// Clearing both leaves a plain lane.
	if out, code := runIncoda(t, incoda, state, "config", "ld", "--max-run", "0", "--stall", "0"); code != 0 {
		t.Fatalf("config clear: %d\n%s", code, out)
	}
	out, code = runIncoda(t, incoda, state, "run", "--queue", "ld", "--poll", "50ms", "--", lim, "sleep", "2500")
	if code != 0 {
		t.Fatalf("a cleared lane has no limits, got exit %d:\n%s", code, out)
	}
}

// TestLimitFlagsAreValidated: a bad duration is a usage error, never a
// silently ignored limit.
func TestLimitFlagsAreValidated(t *testing.T) {
	incoda, _ := binaries(t)
	lim := limitsBinary(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "lv")
	for _, args := range [][]string{
		{"run", "--queue", "lv", "--max-run", "soon", "--", lim, "sleep", "1"},
		{"run", "--queue", "lv", "--max-run", "-5s", "--", lim, "sleep", "1"},
		{"run", "--queue", "lv", "--stall", "-1", "--", lim, "sleep", "1"},
		{"config", "lv", "--max-run", "soon"},
		{"config", "lv", "--stall", "-5s"},
	} {
		if out, code := runIncoda(t, incoda, state, args...); code != 120 {
			t.Fatalf("%v: a bad limit is a usage error (120), got %d:\n%s", args, code, out)
		}
	}
}

// TestNestedRunDoesNotKillItsParent: a nested run on a lane its parent holds
// has no ticket of its own. A limit it was given ends its own child and
// nothing else; the parent, which asked for no limit, sees an ordinary
// non-zero child and records no limit on its release line.
func TestNestedRunDoesNotKillItsParent(t *testing.T) {
	requireStallCounter(t)
	incoda, _ := binaries(t)
	lim := limitsBinary(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "nest")

	out, code := runIncoda(t, incoda, state, "run", "--queue", "nest", "--poll", "50ms",
		"--", incoda, "run", "--queue", "nest", "--poll", "50ms", "--stall", "1s",
		"--", lim, "sleep", "30000")
	if code != exitStall {
		t.Fatalf("the inner stall limit ends the inner child with %d, passed through the parent, got %d:\n%s", exitStall, code, out)
	}
	log := laneLog(t, state, "nest")
	rel := logLine(log, "release")
	if strings.Contains(rel, "limit=") {
		t.Fatalf("the parent set no limit and must not record one:\n%s", log)
	}
	if strings.Count(out, "killed by incoda") != 1 {
		t.Fatalf("exactly one run was killed by a limit (the inner), stderr:\n%s", out)
	}
}

// TestParentLimitCoversNestedRun: the lane's default applies to the run that
// holds the lane; a nested run riding that ticket takes no default of its
// own (it would clock the parent's hold twice).
func TestParentLimitCoversNestedRun(t *testing.T) {
	incoda, _ := binaries(t)
	lim := limitsBinary(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "nest2")
	if out, code := runIncoda(t, incoda, state, "config", "nest2", "--max-run", "2s"); code != 0 {
		t.Fatalf("config: %d\n%s", code, out)
	}

	// The inner job is idle for 30s: only the parent's max-run (2s) can end it.
	out, code, el := timedRun(t, incoda, state, "run", "--queue", "nest2", "--poll", "50ms",
		"--", incoda, "run", "--queue", "nest2", "--poll", "50ms", "--",
		lim, "sleep", "30000")
	if code != exitMaxRun {
		t.Fatalf("the holder's max-run ends the whole tree with %d, got %d:\n%s", exitMaxRun, code, out)
	}
	if el > 20*time.Second {
		t.Fatalf("took %s", el)
	}
	if rel := logLine(laneLog(t, state, "nest2"), "release"); !strings.Contains(rel, "limit=max-run") {
		t.Fatalf("the holder's release line records the limit:\n%s", rel)
	}
}

func run(incoda, state string, t *testing.T, args ...string) (string, int) {
	t.Helper()
	return runIncoda(t, incoda, state, args...)
}

// TestLimitsOnPipes repeats the core behaviours on the pipe path, where the
// CPU probe is armed by a different piece of code than on a pseudo-console.
func TestLimitsOnPipes(t *testing.T) {
	requireStallCounter(t)
	incoda, _ := binaries(t)
	lim := limitsBinary(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "pp")
	base := []string{"run", "--queue", "pp", "--poll", "50ms", "--pty=false"}
	with := func(extra ...string) []string { return append(append([]string{}, base...), extra...) }

	if out, code := run(incoda, state, t, with("--max-run", "1s", "--", lim, "sleep", "30000")...); code != exitMaxRun {
		t.Fatalf("pipes: --max-run kill: got %d:\n%s", code, out)
	}
	if out, code := run(incoda, state, t, with("--stall", "1s", "--", lim, "sleep", "30000")...); code != exitStall {
		t.Fatalf("pipes: an idle job is stalled: got %d:\n%s", code, out)
	}
	if out, code := run(incoda, state, t, with("--stall", "1s", "--", lim, "spin", "3500")...); code != 0 {
		t.Fatalf("pipes: a busy job is spared: got %d:\n%s", code, out)
	}
	if out, code := run(incoda, state, t, with("--stall", "1s", "--", lim, "tree", "spin", "3500")...); code != 0 {
		t.Fatalf("pipes: a busy grandchild counts: got %d:\n%s", code, out)
	}
	if out, code := run(incoda, state, t, with("--stall", "1s", "--", lim, "tree", "sleep", "30000")...); code != exitStall {
		t.Fatalf("pipes: an idle tree is stalled: got %d:\n%s", code, out)
	}
}

// TestLimitKillLeavesNothingBehind: after a limit kill the lanes the run held
// are free, no ticket is left on any of them, and a grandchild that was
// running is gone, for a top-level run and for a nested one, on a
// pseudo-console and on pipes.
func TestLimitKillLeavesNothingBehind(t *testing.T) {
	incoda, _ := binaries(t)
	lim := limitsBinary(t)
	for _, mode := range []struct {
		name string
		pty  string
	}{{"pty", "--pty=true"}, {"pipes", "--pty=false"}} {
		for _, nested := range []bool{false, true} {
			name := mode.name + "/top"
			if nested {
				name = mode.name + "/nested"
			}
			t.Run(name, func(t *testing.T) {
				if mode.name == "pty" {
					requirePty(t)
				}
				state := t.TempDir()
				key := "nb"
				linkTestKeys(t, incoda, state, key)
				pidfile := filepath.Join(t.TempDir(), "gc.pid")
				args := []string{"run", "--queue", key, "--poll", "50ms", mode.pty}
				inner := []string{lim, "tree", "sleep", "60000", pidfile}
				if nested {
					args = append(args, "--", incoda, "run", "--queue", key, "--poll", "50ms", mode.pty, "--max-run", "2s", "--")
				} else {
					args = append(args, "--max-run", "2s", "--")
				}
				args = append(args, inner...)
				out, code := runIncoda(t, incoda, state, args...)
				if code != exitMaxRun {
					t.Fatalf("expected the limit exit %d, got %d:\n%s", exitMaxRun, code, out)
				}
				b, err := os.ReadFile(pidfile)
				if err != nil {
					t.Fatalf("the fixture never reported its grandchild: %v\n%s", err, out)
				}
				var pid int
				fmt.Sscan(string(b), &pid)
				deadline := time.Now().Add(10 * time.Second)
				for pidAlive(pid) && time.Now().Before(deadline) {
					time.Sleep(50 * time.Millisecond)
				}
				if pidAlive(pid) {
					t.Fatalf("grandchild %d is still running after the limit kill", pid)
				}
				for _, k := range []string{key, testPool} {
					if n := countTickets(t, state, k); n != 0 {
						t.Fatalf("%d ticket(s) left on %q", n, k)
					}
				}
			})
		}
	}
}

// TestLimitDefaultOnAPoolLane: the default lives on a pool the project lane
// is linked to, and the lane itself has none. The run must take it, and the
// tightest of several lanes' defaults wins either way round.
func TestLimitDefaultOnAPoolLane(t *testing.T) {
	incoda, _ := binaries(t)
	lim := limitsBinary(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "pl")

	if out, code := runIncoda(t, incoda, state, "config", testPool, "--max-run", "1s"); code != 0 {
		t.Fatalf("config pool: %d\n%s", code, out)
	}
	out, code := runIncoda(t, incoda, state, "run", "--queue", "pl", "--poll", "50ms", "--", lim, "sleep", "30000")
	if code != exitMaxRun {
		t.Fatalf("a default on the linked pool applies to the run, got %d:\n%s", code, out)
	}
	// Tightest wins: lane 1s, pool 60s.
	if out, code := runIncoda(t, incoda, state, "config", testPool, "--max-run", "60s"); code != 0 {
		t.Fatalf("config pool: %d\n%s", code, out)
	}
	if out, code := runIncoda(t, incoda, state, "config", "pl", "--max-run", "1s"); code != 0 {
		t.Fatalf("config lane: %d\n%s", code, out)
	}
	if out, code := runIncoda(t, incoda, state, "run", "--queue", "pl", "--poll", "50ms", "--", lim, "sleep", "30000"); code != exitMaxRun {
		t.Fatalf("the lane's 1s beats the pool's 60s, got %d:\n%s", code, out)
	}
}

// TestLimitConfigIsReadWhenTheJobStarts: a run that queued before the lane
// got a default still takes it, because the config is read once the lane is
// held, not when the run was planned.
func TestLimitConfigIsReadWhenTheJobStarts(t *testing.T) {
	incoda, _ := binaries(t)
	lim := limitsBinary(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "fresh")

	holder := exec.Command(incoda, "run", "--queue", "fresh", "--poll", "50ms", "--", lim, "sleep", "4000")
	holder.Env = laneEnv(state)
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Wait() }()
	waitFor(t, incoda, state, "fresh", func(q queueReport) bool { return len(q.Holders) == 1 })

	waiter := exec.Command(incoda, "run", "--queue", "fresh", "--poll", "50ms", "--wait", "60s", "--", lim, "sleep", "30000")
	waiter.Env = laneEnv(state)
	var wout strings.Builder
	waiter.Stdout, waiter.Stderr = &wout, &wout
	if err := waiter.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, incoda, state, "fresh", func(q queueReport) bool { return len(q.Waiting) == 1 })
	if out, code := runIncoda(t, incoda, state, "config", "fresh", "--max-run", "1s"); code != 0 {
		t.Fatalf("config: %d\n%s", code, out)
	}
	if got := exitCodeOf(waiter.Wait()); got != exitMaxRun {
		t.Fatalf("the default set while the run queued applies when it starts, got %d:\n%s", got, wout.String())
	}
}

// TestMaxRunClockStartsWithTheCommand: time spent queued is not time held. A
// run that waits three seconds behind another and then needs one second
// finishes inside --max-run 2s.
func TestMaxRunClockStartsWithTheCommand(t *testing.T) {
	incoda, _ := binaries(t)
	lim := limitsBinary(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "clk")

	holder := exec.Command(incoda, "run", "--queue", "clk", "--poll", "50ms", "--", lim, "sleep", "3000")
	holder.Env = laneEnv(state)
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Wait() }()
	waitFor(t, incoda, state, "clk", func(q queueReport) bool { return len(q.Holders) == 1 })

	out, code := runIncoda(t, incoda, state, "run", "--queue", "clk", "--poll", "50ms", "--wait", "60s", "--max-run", "2s", "--", lim, "sleep", "1000")
	if code != 0 {
		t.Fatalf("queue time must not count against --max-run, got %d:\n%s", code, out)
	}
}

// TestManualKillRacingALimit: a person's kill and a limit arrive together.
// Exactly one wins, one kill event is logged, and the exit code, the release
// line and the event agree on which it was.
func TestManualKillRacingALimit(t *testing.T) {
	incoda, _ := binaries(t)
	lim := limitsBinary(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "race")

	run := exec.Command(incoda, "run", "--queue", "race", "--poll", "50ms", "--pty=false", "--max-run", "3s", "--", lim, "sleep", "30000")
	run.Env = laneEnv(state)
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, incoda, state, "race", func(q queueReport) bool { return len(q.Holders) == 1 })
	time.Sleep(2700 * time.Millisecond)
	_, _ = runIncoda(t, incoda, state, "kill", "--queue", "race", "--pid", fmt.Sprint(run.Process.Pid), "--reason", "racing the limit", "--wait", "10s")
	code := exitCodeOf(run.Wait())
	if code != exitMaxRun && code != 124 {
		t.Fatalf("the run ends by the limit (%d) or the kill (124), got %d", exitMaxRun, code)
	}
	log := laneLog(t, state, "race")
	kills := 0
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, "event=kill ") {
			kills++
		}
	}
	if kills != 1 {
		t.Fatalf("exactly one kill event, got %d:\n%s", kills, log)
	}
	rel := logLine(log, "release")
	if !strings.Contains(rel, fmt.Sprintf("rc=%d", code)) || strings.Contains(rel, "limit=") != (code == exitMaxRun) {
		t.Fatalf("release line disagrees with exit %d:\n%s", code, rel)
	}
}

// TestMaxRunNeverReportsAFinishedJob: a job that ends by itself right at its
// limit is not reported as killed. Each job writes its exit stamp before it
// exits 0; a stamp together with a limit exit is a finished job called dead.
func TestMaxRunNeverReportsAFinishedJob(t *testing.T) {
	requireStableExitWindow(t)
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "fin")
	for i := 0; i < 12; i++ {
		file := filepath.Join(t.TempDir(), "s.txt")
		_, code := runIncoda(t, incoda, state, "run", "--queue", "fin", "--poll", "10ms", "--pty=false",
			"--max-run", "300ms", "--", stamp, file, "j", "290")
		if iv, ok := readInterval(t, file); ok && iv.exit != 0 && code != 0 {
			t.Fatalf("iteration %d: the job finished (exit stamp written) yet the run exited %d", i, code)
		}
	}
}

// TestLimitFlagValuesAreRefused covers what the first validation test did
// not: empty and exponent forms, and a --stall too short to sample.
func TestLimitFlagValuesAreRefused(t *testing.T) {
	incoda, _ := binaries(t)
	lim := limitsBinary(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "lf")
	for _, args := range [][]string{
		{"run", "--queue", "lf", "--max-run", "", "--", lim, "sleep", "1"},
		{"run", "--queue", "lf", "--stall", "1e3", "--", lim, "sleep", "1"},
		{"run", "--queue", "lf", "--stall", "500ms", "--", lim, "sleep", "1"},
		{"config", "lf", "--stall", "500ms"},
	} {
		if out, code := runIncoda(t, incoda, state, args...); code != 120 {
			t.Fatalf("%v: want usage error 120, got %d:\n%s", args, code, out)
		}
	}
}

// TestShortStallWarns: a --stall under 30s is allowed and says what it risks.
func TestShortStallWarns(t *testing.T) {
	requireStallCounter(t)
	incoda, _ := binaries(t)
	lim := limitsBinary(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "sw")
	out, code := runIncoda(t, incoda, state, "run", "--queue", "sw", "--poll", "50ms", "--stall", "5s", "--", lim, "sleep", "100")
	if code != 0 || !strings.Contains(out, "warning: --stall 5s is under 30s") {
		t.Fatalf("a short --stall warns (exit %d):\n%s", code, out)
	}
	out, _ = runIncoda(t, incoda, state, "run", "--queue", "sw", "--poll", "50ms", "--stall", "45s", "--", lim, "sleep", "100")
	if strings.Contains(out, "warning") {
		t.Fatalf("45s is no cause for a warning:\n%s", out)
	}
}

// TestBadLimitInConfigDoesNotBrickTheLane: a hand-edited limit that is not a
// duration is reported and ignored, status still works, a run still runs, and
// the obvious repair command fixes it.
func TestBadLimitInConfigDoesNotBrickTheLane(t *testing.T) {
	incoda, _ := binaries(t)
	lim := limitsBinary(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "bad")
	path := filepath.Join(laneDir(state, "bad"), "config.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(b), "{", `{"max_run":"soon",`, 1)
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := runIncoda(t, incoda, state, "status", "--all"); code != 0 {
		t.Fatalf("status must still work: %d\n%s", code, out)
	}
	out, code := runIncoda(t, incoda, state, "run", "--queue", "bad", "--poll", "50ms", "--", lim, "sleep", "100")
	if code != 0 || !strings.Contains(out, "max_run") {
		t.Fatalf("a run goes ahead, without the bad limit, and says so (exit %d):\n%s", code, out)
	}
	if out, code := runIncoda(t, incoda, state, "config", "bad", "--max-run", "0"); code != 0 {
		t.Fatalf("the repair command must work: %d\n%s", code, out)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "max_run") {
		t.Fatalf("the repair left the bad value:\n%s", b)
	}
}

// TestLimitsAppearInStatusJSON pins the two keys scripts read, and that a
// lane with a limit is stamped with the schema older binaries refuse.
func TestLimitsAppearInStatusJSON(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "js")
	if out, code := runIncoda(t, incoda, state, "config", "js", "--max-run", "5m", "--stall", "2m"); code != 0 {
		t.Fatalf("config: %d\n%s", code, out)
	}
	out, code := runIncoda(t, incoda, state, "status", "--json", "--queue", "js")
	if code != 0 {
		t.Fatalf("status --json: %d\n%s", code, out)
	}
	var rep struct {
		Queues []struct {
			Config map[string]any `json:"config"`
		} `json:"queues"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil || len(rep.Queues) != 1 {
		t.Fatalf("bad status json: %v\n%s", err, out)
	}
	c := rep.Queues[0].Config
	if c["max_run"] != "5m0s" || c["stall"] != "2m0s" {
		t.Fatalf("status --json config must carry max_run and stall: %v", c)
	}
	raw, _ := os.ReadFile(filepath.Join(laneDir(state, "js"), "config.json"))
	if !strings.Contains(string(raw), `"schema": 3`) {
		t.Fatalf("a limited lane is stamped schema 3:\n%s", raw)
	}
	if out, code := runIncoda(t, incoda, state, "config", "js", "--max-run", "0", "--stall", "0"); code != 0 {
		t.Fatalf("clear: %d\n%s", code, out)
	}
	raw, _ = os.ReadFile(filepath.Join(laneDir(state, "js"), "config.json"))
	if !strings.Contains(string(raw), `"schema": 2`) {
		t.Fatalf("a lane with no limit goes back to schema 2:\n%s", raw)
	}
}

// TestCPUSpinHelper: the hidden command the stall calibration runs.
func TestCPUSpinHelper(t *testing.T) {
	incoda, _ := binaries(t)
	if out, code := runIncoda(t, incoda, t.TempDir(), "__cpuspin", "100ms"); code != 0 {
		t.Fatalf("__cpuspin: %d\n%s", code, out)
	}
}
