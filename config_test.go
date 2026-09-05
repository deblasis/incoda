package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func runIncoda(t *testing.T, incoda, state string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(incoda, args...)
	cmd.Env = laneEnv(state)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		code = exitCodeOf(err)
	}
	return string(out), code
}

// TestQueueConfigSuppliesSlots: callers should not have to agree on --slots
// by hand. The queue's own config is the default, and three runs that never
// mention slots overlap exactly as the config allows.
func TestQueueConfigSuppliesSlots(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	if out, code := runIncoda(t, incoda, state, "config", "cfgslots", "--slots", "2", "--description", "CPU and RAM"); code != 0 {
		t.Fatalf("config: exit %d\n%s", code, out)
	}
	out, code := runIncoda(t, incoda, state, "config", "cfgslots")
	if code != 0 || !strings.Contains(out, "slots: 2") || !strings.Contains(out, "CPU and RAM") {
		t.Fatalf("config should print what it holds, exit %d:\n%s", code, out)
	}

	const n = 6
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			label := fmt.Sprintf("c%d", i)
			cmd := exec.Command(incoda, "run", "--queue", "cfgslots", "--wait", "60s", "--poll", "50ms", "--quiet",
				"--", stamp, filepath.Join(stamps, label+".txt"), label, "400")
			cmd.Env = laneEnv(state)
			if out, err := cmd.CombinedOutput(); err != nil {
				errs[i] = fmt.Errorf("child %d: %v\n%s", i, err, out)
			}
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var ivs []interval
	for i := 0; i < n; i++ {
		iv, ok := readInterval(t, filepath.Join(stamps, fmt.Sprintf("c%d.txt", i)))
		if !ok {
			t.Fatalf("child %d left no stamp", i)
		}
		ivs = append(ivs, iv)
	}
	if got, w := maxOverlap(ivs); got != 2 {
		t.Fatalf("max concurrent holders = %d, want exactly 2 from the queue config; %v", got, w)
	}

	// The report carries the config so a watcher can show it.
	rep := statusJSON(t, incoda, state, "cfgslots")
	var raw map[string]any
	cmd := exec.Command(incoda, "status", "--json", "--queue", "cfgslots")
	cmd.Env = laneEnv(state)
	b, _ := cmd.Output()
	_ = json.Unmarshal(b, &raw)
	if rep.EffectiveSlots() != 2 || !strings.Contains(string(b), `"description": "CPU and RAM"`) {
		t.Fatalf("status --json should report the configured slots and description:\n%s", b)
	}
}

func (r report) EffectiveSlots() int {
	if len(r.Queues) == 0 {
		return 0
	}
	return r.Queues[0].EffectiveSlots
}

func TestClosedQueueRefusesRuns(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	msg := "retired: use wintty-build for builds and wintty-desktop for harnesses"
	if out, code := runIncoda(t, incoda, state, "config", "old", "--close", msg); code != 0 {
		t.Fatalf("config --close: exit %d\n%s", code, out)
	}
	out, code := runIncoda(t, incoda, state, "run", "--queue", "old", "--", stamp, filepath.Join(stamps, "x.txt"), "x", "10")
	if code != 120 {
		t.Fatalf("run on a closed queue should exit 120, got %d\n%s", code, out)
	}
	if !strings.Contains(out, msg) {
		t.Fatalf("the refusal must carry the closing message, got:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(stamps, "x.txt")); !os.IsNotExist(err) {
		t.Fatal("the command must not have run")
	}
	if out, code := runIncoda(t, incoda, state, "config", "old", "--open"); code != 0 {
		t.Fatalf("config --open: exit %d\n%s", code, out)
	}
	if out, code := runIncoda(t, incoda, state, "run", "--queue", "old", "--quiet", "--", stamp, filepath.Join(stamps, "y.txt"), "y", "10"); code != 0 {
		t.Fatalf("reopened queue should run, exit %d\n%s", code, out)
	}
}

func TestRequireReason(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	if out, code := runIncoda(t, incoda, state, "config", "strict", "--require-reason"); code != 0 {
		t.Fatalf("config: exit %d\n%s", code, out)
	}
	out, code := runIncoda(t, incoda, state, "run", "--queue", "strict", "--", stamp, filepath.Join(stamps, "a.txt"), "a", "10")
	if code != 120 || !strings.Contains(out, "--reason") {
		t.Fatalf("a run without --reason on a strict queue should exit 120 naming the flag, got %d:\n%s", code, out)
	}
	if out, code := runIncoda(t, incoda, state, "run", "--queue", "strict", "--reason", "because", "--quiet",
		"--", stamp, filepath.Join(stamps, "b.txt"), "b", "10"); code != 0 {
		t.Fatalf("with a reason it runs, exit %d\n%s", code, out)
	}
}

// TestMultiKeyAcquiresEveryQueue: `--queue b,a` holds both keys for the
// duration, so a job that needs the desktop AND the build capacity can say so
// in one run, and a plain run on either key waits behind it.
func TestMultiKeyAcquiresEveryQueue(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	holder := exec.Command(incoda, "run", "--queue", "mk-b,mk-a", "--wait", "60s", "--poll", "50ms", "--quiet",
		"--", stamp, filepath.Join(stamps, "both.txt"), "both", "4000")
	holder.Env = laneEnv(state)
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Process.Kill(); _ = holder.Wait() }()
	waitFor(t, incoda, state, "mk-a", func(q queueReport) bool { return len(q.Holders) == 1 })
	waitFor(t, incoda, state, "mk-b", func(q queueReport) bool { return len(q.Holders) == 1 })

	for _, key := range []string{"mk-a", "mk-b"} {
		out, code := runIncoda(t, incoda, state, "run", "--queue", key, "--wait", "0",
			"--", stamp, filepath.Join(stamps, key+".txt"), key, "10")
		if code != 121 {
			t.Fatalf("a run on %s should wait behind the multi-key holder (121), got %d\n%s", key, code, out)
		}
	}
	if err := holder.Wait(); err != nil {
		t.Fatalf("multi-key holder failed: %v", err)
	}
	if countTickets(t, state, "mk-a")+countTickets(t, state, "mk-b") != 0 {
		t.Fatal("multi-key tickets were not all released")
	}
	// The same key twice, and a key with a bad name, are usage errors.
	if _, code := runIncoda(t, incoda, state, "run", "--queue", "mk-a,mk-a", "--", stamp, filepath.Join(stamps, "d.txt"), "d", "10"); code != 120 {
		t.Fatalf("duplicate keys should be a usage error, got %d", code)
	}
	if _, code := runIncoda(t, incoda, state, "run", "--queue", "mk-a,bad key", "--", stamp, filepath.Join(stamps, "e.txt"), "e", "10"); code != 120 {
		t.Fatalf("an invalid key in the list should be a usage error, got %d", code)
	}
}

// TestExclusiveRunWaitsForAnEmptyQueue proves --exclusive from the outside:
// a two-slot queue with one holder would admit a plain run, and refuses the
// exclusive one until the holder leaves.
func TestExclusiveRunWaitsForAnEmptyQueue(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "excl", "--slots", "2"); code != 0 {
		t.Fatalf("config: %s", out)
	}
	holder := exec.Command(incoda, "run", "--queue", "excl", "--quiet", "--poll", "50ms",
		"--", stamp, filepath.Join(stamps, "h.txt"), "h", "2500")
	holder.Env = laneEnv(state)
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Wait() }()
	waitFor(t, incoda, state, "excl", func(q queueReport) bool { return len(q.Holders) == 1 })

	out, code := runIncoda(t, incoda, state, "run", "--queue", "excl", "--exclusive", "--wait", "0",
		"--", stamp, filepath.Join(stamps, "x.txt"), "x", "10")
	if code != 121 {
		t.Fatalf("exclusive run should not join a holder, got %d\n%s", code, out)
	}
	out, code = runIncoda(t, incoda, state, "run", "--queue", "excl", "--exclusive", "--wait", "30s", "--poll", "50ms", "--quiet",
		"--", stamp, filepath.Join(stamps, "x.txt"), "x", "10")
	if code != 0 {
		t.Fatalf("exclusive run should acquire once the queue drains, got %d\n%s", code, out)
	}
	hv, _ := readInterval(t, filepath.Join(stamps, "h.txt"))
	xv, _ := readInterval(t, filepath.Join(stamps, "x.txt"))
	if xv.enter < hv.exit {
		t.Fatalf("exclusive job entered at %d before the holder left at %d", xv.enter, hv.exit)
	}
	_ = strconv.Itoa
}
