package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestReentrantRunPassesThrough is what lets a recipe own its lane: `just fuzz`
// may take the queue itself, and an agent that wrapped `just fuzz` in
// `incoda run` on the same key must not deadlock against its own child. The
// inner run sees INCODA_HELD from the parent and passes straight through.
func TestReentrantRunPassesThrough(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()
	marker := filepath.Join(stamps, "inner.txt")

	// --wait 2: without re-entrancy the inner run queues behind its own
	// parent, times out, and exits 121. With it the stamp runs at once.
	cmd := exec.Command(incoda, "run", "--queue", "re", "--wait", "2", "--poll", "50ms",
		"--", incoda, "run", "--queue", "re", "--wait", "2", "--poll", "50ms",
		"--", stamp, marker, "inner", "10")
	cmd.Env = laneEnv(state)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("nested run on the same key must pass through, got %v\n%s", err, out)
	}
	if _, ok := readInterval(t, marker); !ok {
		t.Fatal("the inner command never ran")
	}
	if !strings.Contains(string(out), "already held") {
		t.Fatalf("the inner run should say it is riding its parent's lane, got:\n%s", out)
	}
	log, err := os.ReadFile(filepath.Join(state, "queues", "re", "lane.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "event=reenter") {
		t.Fatalf("lane.log should record the re-entry:\n%s", log)
	}

	// A different key is not held by the parent and must still queue normally.
	other := filepath.Join(stamps, "other.txt")
	cmd = exec.Command(incoda, "run", "--queue", "re", "--quiet",
		"--", incoda, "run", "--queue", "other", "--quiet", "--", stamp, other, "other", "10")
	cmd.Env = laneEnv(state)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("nested run on another key: %v\n%s", err, out)
	}
	if countTickets(t, state, "other") != 0 {
		t.Fatal("the other queue's ticket was not released")
	}
}

// TestReleaseRecordsJobStats: the slot count for a build queue should come
// from measured peaks, not from a guess, so every release line carries what
// the job tree actually used.
func TestReleaseRecordsJobStats(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	cmd := exec.Command(incoda, "run", "--queue", "acct", "--quiet", "--owner", "test-session",
		"--", stamp, filepath.Join(stamps, "a.txt"), "a", "50")
	cmd.Env = laneEnv(state)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	log, err := os.ReadFile(filepath.Join(state, "queues", "acct", "lane.log"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(log)
	if !strings.Contains(s, "event=release") || !strings.Contains(s, "peak_mem=") || !strings.Contains(s, "cpu=") {
		t.Fatalf("release line should carry peak_mem and cpu:\n%s", s)
	}
	if !strings.Contains(s, "owner=test-session") {
		t.Fatalf("enqueue line should name the owner:\n%s", s)
	}
}
