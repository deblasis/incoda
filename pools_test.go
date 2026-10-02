package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// mustRun runs incoda and fails the test unless it exits wantCode.
func mustRun(t *testing.T, incoda, state string, wantCode int, args ...string) string {
	t.Helper()
	out, code := runIncoda(t, incoda, state, args...)
	if code != wantCode {
		t.Fatalf("incoda %s: exit %d, want %d\n%s", strings.Join(args, " "), code, wantCode, out)
	}
	return out
}

// inOrder fails unless every want appears in out, each after the one
// before it.
func inOrder(t *testing.T, out string, wants ...string) {
	t.Helper()
	at := 0
	for _, w := range wants {
		i := strings.Index(out[at:], w)
		if i < 0 {
			t.Fatalf("missing %q (in order) in:\n%s", w, out)
		}
		at += i + len(w)
	}
}

// TestRunTakesItsLinkedPools: a run on a linked project lane also takes
// its pools, one at a time in the total order, so two projects linked to
// one pool exclude each other there (spec 2.4). A pool ticket records via
// and the run's --wait (2.7); busy, holder and timeout lines name the
// pool's role (5.1); --exclusive stays on the project lane.
func TestRunTakesItsLinkedPools(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	for _, k := range []string{"kf-gate", "other-gate"} {
		mustRun(t, incoda, state, 0, "config", k, "--pool", "tests")
	}
	h := exec.Command(incoda, "run", "--queue", "kf-gate", "--exclusive", "--wait", "60s", "--poll", "50ms", "--quiet",
		"--", stamp, filepath.Join(t.TempDir(), "h.txt"), "h", "4000")
	h.Env = laneEnv(state)
	if err := h.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Process.Kill(); _ = h.Wait() }()
	waitFor(t, incoda, state, "tests", func(q queueReport) bool { return len(q.Holders) == 1 })
	pool := statusJSON(t, incoda, state, "tests").Queues[0].Holders[0].Ticket
	if strings.Join(pool.Via, ",") != "kf-gate" || pool.Wait != "60s" || pool.Exclusive || pool.PID != h.Process.Pid {
		t.Fatalf("pool ticket: %+v", pool)
	}
	if own := statusJSON(t, incoda, state, "kf-gate").Queues[0].Holders[0].Ticket; !own.Exclusive || len(own.Via) != 0 {
		t.Fatalf("project ticket: %+v", own)
	}

	out := mustRun(t, incoda, state, 121, "run", "--queue", "other-gate", "--wait", "0", "--poll", "50ms",
		"--", stamp, filepath.Join(t.TempDir(), "o.txt"), "o", "10")
	inOrder(t, out,
		`incoda: queue "tests" busy (pool, via other-gate; 1 slot(s), 1 ahead of you), waited 0s`,
		"incoda:   holder pid ", " via kf-gate\n",
		`incoda: queue "tests" (pool, via other-gate) still busy after 0s. Check `+"`incoda status --queue other-gate`")
	if countTickets(t, state, "other-gate") != 0 || countTickets(t, state, "tests") != 1 {
		t.Fatal("a timed-out run releases every lane it took")
	}
	out = mustRun(t, incoda, state, 121, "run", "--queue", "tests", "--wait", "0", "--", stamp, filepath.Join(t.TempDir(), "d.txt"), "d", "10")
	inOrder(t, out, `incoda: queue "tests" busy (pool; 1 slot(s), 1 ahead of you)`,
		`incoda: queue "tests" (pool) still busy after 0s. Check `+"`incoda status --queue tests`")
	log, _ := os.ReadFile(filepath.Join(laneDir(state, "tests"), "lane.log"))
	if !strings.Contains(string(log), " via=kf-gate ") || !strings.Contains(string(log), " via=other-gate ") {
		t.Fatalf("enqueue lines of pool tickets carry via:\n%s", log)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("holder: %v", err)
	}

	out = mustRun(t, incoda, state, 0, "run", "--queue", "kf-gate,builds", "--poll", "50ms",
		"--", stamp, filepath.Join(t.TempDir(), "b.txt"), "b", "10")
	inOrder(t, out, `acquired queue "kf-gate" (pid `, `acquired queue "builds" (pool; pid `, `acquired queue "tests" (pool, via kf-gate; pid `)
}

// TestPoolRulesBindLinkedRuns: a linked run is refused up front on a
// closed pool, and without --reason on a pool that requires one, with the
// pool's path in the text (spec 4.5); nothing is enrolled anywhere.
func TestPoolRulesBindLinkedRuns(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	mustRun(t, incoda, state, 0, "config", "cap-e2e", "--pool", "computer-use,vm")
	mustRun(t, incoda, state, 0, "config", "vm", "--close", "maintenance")
	out := mustRun(t, incoda, state, 120, "run", "--queue", "cap-e2e", "--", stamp, filepath.Join(t.TempDir(), "x.txt"), "x", "10")
	if !strings.Contains(out, `incoda: queue "vm" is closed: maintenance (pool, via cap-e2e)`) {
		t.Fatalf("closed pool:\n%s", out)
	}
	mustRun(t, incoda, state, 0, "config", "builds", "--require-reason")
	mustRun(t, incoda, state, 0, "config", "kf-build", "--pool", "builds")
	out = mustRun(t, incoda, state, 120, "run", "--queue", "kf-build", "--", stamp, filepath.Join(t.TempDir(), "y.txt"), "y", "10")
	if !strings.Contains(out, `incoda: queue "builds" requires --reason (pool, via kf-build): say what this job is`) {
		t.Fatalf("reason pool:\n%s", out)
	}
	for _, k := range []string{"cap-e2e", "computer-use", "vm", "kf-build", "builds"} {
		if n := countTickets(t, state, k); n != 0 {
			t.Fatalf("%s holds %d ticket(s) after a refusal", k, n)
		}
	}
	mustRun(t, incoda, state, 0, "run", "--queue", "kf-build", "--reason", "nightly", "--quiet",
		"--", stamp, filepath.Join(t.TempDir(), "z.txt"), "z", "10")
}
