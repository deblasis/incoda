package main

import (
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
