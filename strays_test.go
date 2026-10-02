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
	"github.com/deblasis/incoda/internal/procinfo"
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
// no lane of the new layout counts on every pool, so a run on a linked
// project lane waits for it through its pool too.
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
	mustRun(t, incoda, state, 0, "config", "proj", "--pool", "tests")
	out, code := runIncoda(t, incoda, state, "run", "--queue", "proj", "--wait", "300ms", "--poll", "50ms", "--", stamp, filepath.Join(t.TempDir(), "q.txt"), "q", "1")
	if code != 121 || !strings.Contains(out, fmt.Sprintf("unpooled run by an older incoda: pid %d, key oldjob", o.Process.Pid)) {
		t.Fatalf("a linked project run waits for it through its pool: %d\n%s", code, out)
	}

	// The unrelated stray's key ("oldjob") matches no project lane, so it
	// does not charge proj itself (ChargedTo only matches a non-pool key
	// against its own name, the restored half of this test): the run above
	// acquired proj at once, with no busy wait on proj itself, and only
	// then queued and gave up on tests. (status cannot show this: its
	// holder/waiter split is structural ticket position, blind to the
	// extra Unpooled count Acquire applies; see the FIFO test above.)
	if strings.Contains(out, `queue "proj" busy`) {
		t.Fatalf("the unrelated stray must not charge lane proj itself:\n%s", out)
	}
	inOrder(t, out, `incoda: acquired queue "proj"`, `incoda: queue "tests" busy`)
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

// TestFIFOWithAnUnpooledHolder: two new runs queue on a pool behind an
// unpooled run of an older incoda (a live stray ticket). Both name it on
// their busy line, neither starts while it runs, and once it is gone they
// are served in arrival order, one at a time.
func TestFIFOWithAnUnpooledHolder(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	mustRun(t, incoda, state, 0, "config", "seed")
	release := holdOldTicket(t, filepath.Join(machine.StraysDir(state), "1"), "builds", 999999, "zig", "build")
	stamps := t.TempDir()
	var outs [2]syncBuffer
	var ws [2]*exec.Cmd
	for i := range ws {
		label := fmt.Sprintf("w%d", i)
		ws[i] = exec.Command(incoda, "run", "--queue", "builds", "--wait", "60s", "--poll", "50ms",
			"--", stamp, filepath.Join(stamps, label+".txt"), label, "300")
		ws[i].Env = laneEnv(state)
		ws[i].Stdout, ws[i].Stderr = &outs[i], &outs[i]
		if err := ws[i].Start(); err != nil {
			t.Fatal(err)
		}
		defer func(c *exec.Cmd) { _ = c.Process.Kill(); _ = c.Wait() }(ws[i])
		// The busy line comes after the first poll, so the waiter is
		// enrolled before the next one starts. (status shows the first
		// waiter as a holder: it counts positions, not unpooled holders.)
		waitForText(t, &outs[i], "incoda:   unpooled run by an older incoda: pid 999999, key builds\n")
	}
	time.Sleep(300 * time.Millisecond)
	for i := range ws {
		if _, ok := readInterval(t, filepath.Join(stamps, fmt.Sprintf("w%d.txt", i))); ok {
			t.Fatalf("w%d ran beside the unpooled holder", i)
		}
	}
	freed := time.Now().UnixNano()
	release()
	for i, w := range ws {
		if err := w.Wait(); err != nil {
			t.Fatalf("w%d: %v\n%s", i, err, outs[i].String())
		}
	}
	w0, _ := readInterval(t, filepath.Join(stamps, "w0.txt"))
	w1, _ := readInterval(t, filepath.Join(stamps, "w1.txt"))
	if w0.enter < freed || w1.enter < w0.exit {
		t.Fatalf("FIFO behind the unpooled holder violated: freed %d, w0 %+v, w1 %+v", freed, w0, w1)
	}
}

// TestUnpooledHolderOnAProjectKeyCountsOnThatLane: an older incoda's run
// on project key K used K's own width, so a new run on K counts it on K
// itself before it reaches K's pools (a plan 3 decision; spec 2.3 charges
// the pools).
func TestUnpooledHolderOnAProjectKeyCountsOnThatLane(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	mustRun(t, incoda, state, 0, "config", "p-gate", "--pool", "tests")
	holdOldTicket(t, filepath.Join(machine.StraysDir(state), "1"), "p-gate", 999999, "just", "gate")
	out := mustRun(t, incoda, state, 121, "run", "--queue", "p-gate", "--wait", "300ms", "--poll", "50ms",
		"--", stamp, filepath.Join(t.TempDir(), "s.txt"), "s", "1")
	inOrder(t, out, `incoda: queue "p-gate" busy (1 slot(s), 0 ahead of you)`,
		"incoda:   unpooled run by an older incoda: pid 999999, key p-gate\n", `incoda: queue "p-gate" still busy after 300ms.`)
}

// TestOrphanRecordHoldsALaneUntilItsTreeIsGone: the record of an older
// incoda's job that a kill is ending counts as a held slot on its key's
// pool while any recorded process still runs; once the tree is gone the
// next run proceeds and the record is deleted (spec 3.2).
func TestOrphanRecordHoldsALaneUntilItsTreeIsGone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("orphan records are written by the Unix old-holder kill")
	}
	incoda, stamp := binaries(t)
	state := t.TempDir()
	mustRun(t, incoda, state, 0, "config", "seed")
	job := exec.Command("sleep", "30")
	if err := job.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = job.Process.Kill(); _ = job.Wait() }()
	p, err := procinfo.Lookup(job.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	rec := fmt.Sprintf(`{"key":"builds","pid":999999,"command":["zig","build"],"descendants":[{"pid":%d,"start":%d}],"groups":[],"by_pid":1,"at":"2026-10-02T00:00:00Z"}`, p.PID, p.Start)
	if err := os.MkdirAll(machine.OrphansDir(state), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(machine.OrphansDir(state), "999999-1.orphan")
	if err := os.WriteFile(path, []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}
	out := mustRun(t, incoda, state, 121, "run", "--queue", "builds", "--wait", "300ms", "--poll", "50ms",
		"--", stamp, filepath.Join(t.TempDir(), "a.txt"), "a", "1")
	if !strings.Contains(out, "incoda:   unpooled run by an older incoda: pid 999999, key builds\n") {
		t.Fatalf("the orphan record holds builds:\n%s", out)
	}
	_ = job.Process.Kill()
	_ = job.Wait()
	mustRun(t, incoda, state, 0, "run", "--queue", "builds", "--wait", "10s", "--poll", "50ms",
		"--", stamp, filepath.Join(t.TempDir(), "b.txt"), "b", "1")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("an acquisition deletes the record of an empty tree")
	}
}
