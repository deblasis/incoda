package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/machine"
)

// startOldRunAfterFenceDeletion migrates a state directory, deletes the
// fence (the careless rm of spec 2.3), and starts an older incoda run on
// key, which recreates queues/<key> and holds it. It returns the run, which
// the caller waits for, and the state directory.
func startOldRunAfterFenceDeletion(t *testing.T, tag, key string, args ...string) (*exec.Cmd, string) {
	t.Helper()
	incoda, _ := binaries(t)
	old := oldBinary(t, tag)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "seed"); code != 0 {
		t.Fatalf("migrate: %d\n%s", code, out)
	}
	if err := os.Remove(filepath.Join(state, "queues")); err != nil {
		t.Fatal(err)
	}
	o := exec.Command(old, append([]string{"run", "--queue", key, "--poll", "50ms", "--quiet", "--"}, args...)...)
	o.Env = laneEnv(state)
	if err := o.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Process.Kill(); _ = o.Wait() })
	waitForTicket(t, filepath.Join(state, "queues", key))
	return o, state
}

// TestFenceDeletionWithALiveStrayIsCounted: after a careless rm of the
// fence an older incoda runs on the builds pool. The next new run re-places
// the fence (its queues/ goes to strays/), counts that run as a held slot
// on builds, names it on its busy line, and starts only after it ended;
// the dead stray lane is then deleted and its log kept.
func TestFenceDeletionWithALiveStrayIsCounted(t *testing.T) {
	incoda, stamp := binaries(t)
	stamps := t.TempDir()
	o, state := startOldRunAfterFenceDeletion(t, "v0.6.0", "builds", stamp, filepath.Join(stamps, "old.txt"), "old", "2500")

	out, code := runIncoda(t, incoda, state, "run", "--queue", "builds", "--wait", "60s", "--poll", "50ms", "--",
		stamp, filepath.Join(stamps, "new.txt"), "new", "10")
	if code != 0 {
		t.Fatalf("new run: exit %d\n%s", code, out)
	}
	if want := fmt.Sprintf("incoda:   unpooled run by an older incoda: pid %d, key builds\n", o.Process.Pid); !strings.Contains(out, want) {
		t.Fatalf("missing %q in:\n%s", want, out)
	}
	if err := o.Wait(); err != nil {
		t.Fatalf("the old run must finish normally: %v", err)
	}
	oldIv, ok1 := readInterval(t, filepath.Join(stamps, "old.txt"))
	newIv, ok2 := readInterval(t, filepath.Join(stamps, "new.txt"))
	if !ok1 || !ok2 || newIv.enter < oldIv.exit {
		t.Fatalf("the new job overlapped the unpooled old one: old %+v new %+v", oldIv, newIv)
	}
	if !machine.FencePlaced(state) {
		t.Fatal("the new run re-places the fence")
	}
	batches, _ := os.ReadDir(machine.StraysDir(state))
	for _, b := range batches {
		if _, err := os.Stat(filepath.Join(machine.StraysDir(state), b.Name(), "builds")); err == nil {
			t.Fatal("the dead stray lane must be deleted by an acquisition poll")
		}
	}
	log, _ := os.ReadFile(filepath.Join(laneDir(state, "builds"), "lane.log"))
	if !strings.Contains(string(log), fmt.Sprintf("event=acquire pid=%d", o.Process.Pid)) {
		t.Fatalf("the stray's log fragment must be appended to lanes/builds/lane.log:\n%s", log)
	}
}

// TestUnknownStrayKeyCountsOnEveryPool: an unpooled run on a key that is
// no lane of the new layout counts on every pool, and on nothing else: a
// project key does not wait for it.
func TestUnknownStrayKeyCountsOnEveryPool(t *testing.T) {
	incoda, stamp := binaries(t)
	o, state := startOldRunAfterFenceDeletion(t, "v0.6.0", "oldjob", stamp, filepath.Join(t.TempDir(), "old.txt"), "old", "30000")
	// config re-places the fence, which moves queues/ to strays/.
	if out, code := runIncoda(t, incoda, state, "config", "proj"); code != 0 {
		t.Fatalf("config: %d\n%s", code, out)
	}
	for _, pool := range []string{"builds", "computer-use", "tests", "vm"} {
		out, code := runIncoda(t, incoda, state, "run", "--queue", pool, "--wait", "300ms", "--poll", "50ms", "--", stamp, filepath.Join(t.TempDir(), "p.txt"), "p", "1")
		if code != 121 || !strings.Contains(out, fmt.Sprintf("unpooled run by an older incoda: pid %d, key oldjob", o.Process.Pid)) {
			t.Fatalf("pool %s: want exit 121 naming the unpooled run, got %d:\n%s", pool, code, out)
		}
	}
	if out, code := runIncoda(t, incoda, state, "run", "--queue", "proj", "--wait", "0", "--", stamp, filepath.Join(t.TempDir(), "q.txt"), "q", "1"); code != 0 {
		t.Fatalf("a project key is not charged: %d\n%s", code, out)
	}
}

// TestUnpooledAncestorIsUpgradeBlocked: a new run inside the job of an
// unpooled older run would wait for its own ancestor; it refuses at once.
func TestUnpooledAncestorIsUpgradeBlocked(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no ancestry walk on Windows")
	}
	incoda, stamp := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "seed"); code != 0 {
		t.Fatalf("migrate: %d\n%s", code, out)
	}
	// This test process is the new run's parent: a stray ticket it holds is
	// an unpooled older incoda that is the run's ancestor.
	holdOldTicket(t, filepath.Join(machine.StraysDir(state), "1"), "builds", os.Getpid(), "outer", "job")
	start := time.Now()
	out, code := runIncoda(t, incoda, state, "run", "--queue", "builds", "--wait", "30s", "--", stamp, filepath.Join(t.TempDir(), "s"), "s", "1")
	want := fmt.Sprintf(`incoda: upgrade-blocked: an older incoda (pid %d, an ancestor of this process) holds "builds"; rerun the outer command after it exits`, os.Getpid())
	if code != 120 || !strings.Contains(out, want) {
		t.Fatalf("want exit 120 and %q, got %d:\n%s", want, code, out)
	}
	if el := time.Since(start); el > 10*time.Second {
		t.Fatalf("refused after %s; it must refuse at once", el)
	}
}
