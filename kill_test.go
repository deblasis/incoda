package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// startHolder launches a run that holds key for holdMS and returns the
// command plus a buffer collecting its stderr, where the kill notice lands.
func startHolder(t *testing.T, incoda, stamp, state, key, label string, holdMS int) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	cmd := exec.Command(incoda, "run", "--queue", key, "--wait", "60s", "--poll", "50ms",
		"--", stamp, filepath.Join(t.TempDir(), label+".txt"), label, strconv.Itoa(holdMS))
	cmd.Env = laneEnv(state)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd, &errBuf
}

// TestKillHolderCooperatively is the headline: `incoda kill` names a pid and
// a reason, the holder's own incoda notices within a poll, prints who killed
// it and why on its stderr, takes its job tree down, exits 124, and the lane
// is free with the event on record.
func TestKillHolderCooperatively(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()

	holder, holderErr := startHolder(t, incoda, stamp, state, "kill", "victim", 30000)
	waitFor(t, incoda, state, "kill", func(q queueReport) bool { return len(q.Holders) == 1 })

	start := time.Now()
	out, code := runIncoda(t, incoda, state, "kill", "--queue", "kill", "--pid", strconv.Itoa(holder.Process.Pid),
		"--reason", "stale build, the branch moved")
	if code != 0 {
		t.Fatalf("kill: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "released") {
		t.Fatalf("kill should report that the holder let go, got:\n%s", out)
	}
	err := holder.Wait()
	if el := time.Since(start); el > 10*time.Second {
		t.Fatalf("holder took %s to go; a cooperative kill should be a poll interval or so", el)
	}
	if got := exitCodeOf(err); got != 124 {
		t.Fatalf("a killed run exits 124, got %d; stderr:\n%s", got, holderErr.String())
	}
	s := holderErr.String()
	if !strings.Contains(s, "killed") || !strings.Contains(s, "stale build, the branch moved") {
		t.Fatalf("the holder must be told who killed it and why on stderr, got:\n%s", s)
	}
	if n := countTickets(t, state, "kill"); n != 0 {
		t.Fatalf("%d ticket(s) left after the kill", n)
	}
	log, _ := os.ReadFile(filepath.Join(state, "queues", "kill", "lane.log"))
	if !strings.Contains(string(log), "event=kill ") || !strings.Contains(string(log), "reason=") {
		t.Fatalf("lane.log should record the kill with its reason:\n%s", log)
	}
}

func TestKillWaiterCancelsIt(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()

	holder, _ := startHolder(t, incoda, stamp, state, "kw", "h", 4000)
	defer func() { _ = holder.Wait() }()
	waitFor(t, incoda, state, "kw", func(q queueReport) bool { return len(q.Holders) == 1 })
	waiter, waiterErr := startHolder(t, incoda, stamp, state, "kw", "w", 10)
	waitFor(t, incoda, state, "kw", func(q queueReport) bool { return len(q.Waiting) == 1 })

	out, code := runIncoda(t, incoda, state, "kill", "--queue", "kw", "--pid", strconv.Itoa(waiter.Process.Pid),
		"--reason", "queued by mistake")
	if code != 0 {
		t.Fatalf("kill waiter: exit %d\n%s", code, out)
	}
	if got := exitCodeOf(waiter.Wait()); got != 124 {
		t.Fatalf("a cancelled waiter exits 124, got %d; stderr:\n%s", got, waiterErr.String())
	}
	if !strings.Contains(waiterErr.String(), "queued by mistake") {
		t.Fatalf("the waiter must see the reason:\n%s", waiterErr.String())
	}
	// The holder was not touched.
	rep := statusJSON(t, incoda, state, "kw")
	if len(rep.Queues[0].Holders) != 1 || len(rep.Queues[0].Waiting) != 0 {
		t.Fatalf("holder should still hold, waiter gone: %+v", rep.Queues[0])
	}
}

func TestKillRefusals(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "kill", "--queue", "kr", "--pid", "1"); code != 120 || !strings.Contains(out, "--reason") {
		t.Fatalf("kill without a reason is a usage error naming the flag, got %d:\n%s", code, out)
	}
	if out, code := runIncoda(t, incoda, state, "kill", "--queue", "kr", "--pid", "999999", "--reason", "x"); code != 120 || !strings.Contains(out, "999999") {
		t.Fatalf("kill of a pid not in the queue is refused naming the pid, got %d:\n%s", code, out)
	}
}

// TestKillForceTerminates covers the holder that never acknowledges: an
// older incoda, or one wedged in a syscall. --force ends its process, the
// kernel frees the lock, and the reason still lands in the log.
func TestKillForceTerminates(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()

	holder, _ := startHolder(t, incoda, stamp, state, "kf", "victim", 30000)
	waitFor(t, incoda, state, "kf", func(q queueReport) bool { return len(q.Holders) == 1 })

	// --wait 0 skips the cooperative grace so the force path is what runs.
	out, code := runIncoda(t, incoda, state, "kill", "--queue", "kf", "--pid", strconv.Itoa(holder.Process.Pid),
		"--reason", "wedged", "--wait", "0", "--force")
	if code != 0 {
		t.Fatalf("kill --force: exit %d\n%s", code, out)
	}
	err := holder.Wait()
	if runtime.GOOS == "windows" {
		// TerminateProcess carries the exit code, so even a hard kill tells
		// the caller's shell it was the lane. Unix can only SIGKILL.
		if got := exitCodeOf(err); got != 124 {
			t.Fatalf("forced kill on Windows should exit 124, got %d", got)
		}
	}
	waitFor(t, incoda, state, "kf", func(q queueReport) bool { return len(q.Holders) == 0 })
	log, _ := os.ReadFile(filepath.Join(state, "queues", "kf", "lane.log"))
	if !strings.Contains(string(log), "forced=true") {
		t.Fatalf("lane.log should record the forced kill:\n%s", log)
	}
}
