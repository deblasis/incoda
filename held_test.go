package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runWithHeld runs incoda with INCODA_HELD set to held.
func runWithHeld(t *testing.T, incoda, state, held string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(incoda, args...)
	cmd.Env = append(laneEnv(state), "INCODA_HELD="+held)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		code = exitCodeOf(err)
	}
	return string(out), code
}

// TestDeadHeldEntryIsDropped: a stale INCODA_HELD (a shell profile, a
// leftover export) must not let a run skip its lane.
func TestDeadHeldEntryIsDropped(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "hd")
	marker := filepath.Join(t.TempDir(), "m.txt")
	// The lane must exist for the drop to be logged in it: a bogus key in
	// the environment never creates a lane directory.
	if out, code := runIncoda(t, incoda, state, "config", "hd", "--slots", "1"); code != 0 {
		t.Fatalf("config: %d\n%s", code, out)
	}
	out, code := runWithHeld(t, incoda, state, "hd=00000000000000000001-1.ticket",
		"run", "--queue", "hd", "--", stamp, marker, "m", "1")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "incoda: held-dropped: hd (dead)") {
		t.Fatalf("missing held-dropped line:\n%s", out)
	}
	if strings.Contains(out, "already held") {
		t.Fatalf("a dead entry must not pass through:\n%s", out)
	}
	log, _ := os.ReadFile(filepath.Join(laneDir(state, "hd"), "lane.log"))
	if !strings.Contains(string(log), "event=enqueue") || !strings.Contains(string(log), "event=held-dropped") {
		t.Fatalf("the run should enroll and log the drop:\n%s", log)
	}
}

// TestBareHeldKeyIsMalformed: the pre-0.7 bare-key form is not trusted.
func TestBareHeldKeyIsMalformed(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "hk")
	out, code := runWithHeld(t, incoda, state, "hk",
		"run", "--queue", "hk", "--", stamp, filepath.Join(t.TempDir(), "m"), "m", "1")
	if code != 0 || !strings.Contains(out, "incoda: held-dropped: hk (malformed)") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// TestLiveNonAncestorEntryDoesNotPassThrough: a live ticket held by a
// process that is not this run's ancestor (a leaked variable) must not let
// the run share that lane.
func TestLiveNonAncestorEntryDoesNotPassThrough(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows trusts live entries: the Job Object ends every descendant with the holder")
	}
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "na")
	holder, _ := startHolder(t, incoda, stamp, state, "na", "holder", 20000, "50ms")
	defer func() { _ = holder.Process.Kill(); _ = holder.Wait() }()
	waitFor(t, incoda, state, "na", func(q queueReport) bool { return len(q.Holders) == 1 })

	var ticket string
	entries, _ := os.ReadDir(laneDir(state, "na"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".ticket") {
			ticket = e.Name()
		}
	}
	out, code := runWithHeld(t, incoda, state, "na="+ticket,
		"run", "--queue", "na", "--wait", "1", "--poll", "50ms", "--", stamp, filepath.Join(t.TempDir(), "m"), "m", "1")
	if code != 121 {
		t.Fatalf("a non-ancestor entry must queue (and time out here), got %d\n%s", code, out)
	}
	if !strings.Contains(out, "held-dropped: na (not-ancestor; still counts for ordering)") {
		t.Fatalf("missing not-ancestor line:\n%s", out)
	}
}
