package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/lockfile"
	"github.com/deblasis/incoda/internal/machine"
)

// TestHelperHoldLock is not a test of its own. Run as a helper process
// with INCODA_TEST_HOLD_LOCK set, it takes that file's lock, prints
// "held" and keeps the lock until its stdin closes.
func TestHelperHoldLock(t *testing.T) {
	path := os.Getenv("INCODA_TEST_HOLD_LOCK")
	if path == "" {
		t.Skip("helper process only")
	}
	f, err := lockfile.Open(path)
	if err != nil {
		os.Exit(2)
	}
	if err := f.Lock(); err != nil {
		os.Exit(3)
	}
	fmt.Println("held")
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

// holdRegistryElsewhere makes dir a lane and starts a helper process that
// holds its registry lock until the test ends, the way a stopped incoda
// does.
func holdRegistryElsewhere(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	holdLockElsewhere(t, lane.RegistryLockPath(dir))
}

// holdLockElsewhere starts a helper process that holds the lock of the
// file at path until the test ends, and waits until it holds it.
func holdLockElsewhere(t *testing.T, path string) {
	t.Helper()
	release := holdLockElsewhereUntil(t, path)
	t.Cleanup(release)
}

// holdLockElsewhereUntil is holdLockElsewhere with the hold on a switch: the
// returned func ends it, safely more than once, so a test can prove what a
// view shows while the lock is held and then let the lane run again.
func holdLockElsewhereUntil(t *testing.T, path string) (release func()) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldLock$")
	cmd.Env = append(os.Environ(), "INCODA_TEST_HOLD_LOCK="+path)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	released := false
	release = func() {
		if released {
			return
		}
		released = true
		in.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
	t.Cleanup(release)
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "held\n" {
		t.Fatalf("helper did not take the lock: %q %v", line, err)
	}
	return release
}

// TestOwnLaneOperationsAreBoundedByTheirWait: a registry lock another
// process keeps on a lane of this layout (a stopped incoda of this binary)
// costs a run, a config and a force-release on that lane at most their
// --wait plus the 2s floor: the run times out with 121, config and
// force-release fail closed with 122, and none of them hangs.
func TestOwnLaneOperationsAreBoundedByTheirWait(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "seed"); code != 0 {
		t.Fatalf("migrate: %d\n%s", code, out)
	}
	holdRegistryElsewhere(t, laneDir(state, "builds"))
	const cannot = "cannot tell: registry lock held by another process"
	timed := func(limit time.Duration, args ...string) (string, int) {
		t.Helper()
		start := time.Now()
		out, code := runIncoda(t, incoda, state, args...)
		if d := time.Since(start); d > limit {
			t.Fatalf("incoda %s took %s:\n%s", strings.Join(args, " "), d, out)
		}
		return out, code
	}
	out, code := timed(8*time.Second, "run", "--queue", "builds", "--wait", "1s", "--poll", "50ms", "--",
		stamp, filepath.Join(t.TempDir(), "s.txt"), "x", "1")
	if code != 121 || !strings.Contains(out, `cannot enter queue "builds" within --wait: registry lock: `+cannot) {
		t.Fatalf("run must time out within its wait: %d\n%s", code, out)
	}
	out, code = timed(8*time.Second, "config", "builds", "--slots", "2", "--wait", "1s")
	if code != 122 || !strings.Contains(out, cannot) {
		t.Fatalf("config must fail closed within its wait: %d\n%s", code, out)
	}
	out, code = timed(10*time.Second, "force-release", "--queue", "builds")
	if code != 122 || !strings.Contains(out, cannot) {
		t.Fatalf("force-release must fail closed within its bound: %d\n%s", code, out)
	}
}

// TestRegistryLockHeldElsewhereNeverHangs: a registry lock another process
// keeps for ever (a stray's, and a lane's of this layout) costs status,
// doctor, kill and a pool run at most their bounds. status names the lane's
// state from the tickets and says the read was lockless, doctor names the
// stray it cannot tell, kill fails closed with exit 122, and a run on a pool
// counts the stray as held and times out within its --wait.
func TestRegistryLockHeldElsewhereNeverHangs(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "seed"); code != 0 {
		t.Fatalf("migrate: %d\n%s", code, out)
	}
	holdRegistryElsewhere(t, filepath.Join(machine.StraysDir(state), "1", "oldk"))
	holdRegistryElsewhere(t, laneDir(state, "seed"))
	const cannot = "cannot tell: registry lock held by another process"
	timed := func(limit time.Duration, args ...string) (string, int) {
		t.Helper()
		start := time.Now()
		out, code := runIncoda(t, incoda, state, args...)
		if d := time.Since(start); d > limit {
			t.Fatalf("incoda %s took %s:\n%s", strings.Join(args, " "), d, out)
		}
		return out, code
	}

	out, code := timed(5*time.Second, "status", "--queue", "seed", "--no-color")
	if code != 0 || !strings.Contains(out, `queue "seed": FREE`) ||
		!strings.Contains(out, "read without the registry lock") ||
		!strings.HasSuffix(out, "\nunpooled lane of an older incoda: strays/1/oldk: "+cannot+"\n") {
		t.Fatalf("status must still describe a lane whose registry lock is held, and name the stray it cannot tell: %d\n%s", code, out)
	}

	start := time.Now()
	out, code = doctor(t, incoda, state)
	if d := time.Since(start); d > 10*time.Second || !strings.Contains(out, "1/oldk: cannot probe ("+cannot+")") {
		t.Fatalf("doctor (%s) must name the stray it cannot tell: %d\n%s", d, code, out)
	}

	out, code = timed(5*time.Second, "kill", "--queue", "oldk", "--pid", "4711", "--reason", "test", "--wait", "300ms")
	if code != 122 || !strings.Contains(out, cannot) {
		t.Fatalf("kill must fail closed: %d\n%s", code, out)
	}

	out, code = timed(8*time.Second, "run", "--queue", "builds", "--wait", "1s", "--poll", "50ms", "--",
		stamp, filepath.Join(t.TempDir(), "s.txt"), "x", "1")
	if code != 121 || !strings.Contains(out, "unpooled lane of an older incoda: strays/1/oldk: "+cannot) {
		t.Fatalf("a pool run counts the busy stray and times out: %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(machine.StraysDir(state), "1", "oldk")); err != nil {
		t.Fatal("a stray that cannot be told is never deleted")
	}
}

// TestStatusListsQueuedJobsWhileTheRegistryLockIsHeld is the reported bug: a
// lane with a live holder and a live waiter, whose registry lock another
// process holds (a stopped incoda, or one descheduled on a loaded machine),
// must still list both. The view used to give up on the registry lock and
// report the lane as "cannot tell", with no holders and no waiters in the
// JSON, so a job that was queued looked like it had never been queued.
//
// The two tickets are enrolled here, in the test process, so the test holds
// real ticket locks for the whole check and never races the runs it is
// describing.
func TestStatusListsQueuedJobsWhileTheRegistryLockIsHeld(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "seed"); code != 0 {
		t.Fatalf("migrate: %d\n%s", code, out)
	}
	q, err := lane.OpenIn(filepath.Join(state, "lanes"), "busyq", lane.Create)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	var ens []*lane.Enrollment
	for i := 0; i < 2; i++ {
		en, err := q.Enroll(lane.Ticket{Slots: 1, Command: []string{"job", strconv.Itoa(i)}})
		if err != nil {
			t.Fatal(err)
		}
		ens = append(ens, en)
	}

	releaseLock := holdLockElsewhereUntil(t, lane.RegistryLockPath(laneDir(state, "busyq")))
	defer func() {
		// Drop the helper's registry lock first: Release takes the lock when it
		// can, and while the helper holds it every Release waits its 2s bound.
		releaseLock()
		for _, en := range ens {
			en.Release(0)
		}
	}()

	out, code := runIncoda(t, incoda, state, "status", "--queue", "busyq", "--no-color")
	if code != 0 || !strings.Contains(out, "HOLDER") || !strings.Contains(out, "WAITING (arrival order):") ||
		!strings.Contains(out, "read without the registry lock") ||
		strings.Contains(out, "cannot tell: registry lock held by another process") {
		t.Fatalf("status must list both participants and name the lockless read: %d\n%s", code, out)
	}
	q2 := statusJSON(t, incoda, state, "busyq").Queues[0]
	if q2.ProbeError != "" {
		t.Fatalf("a held registry lock must not make the lane unreadable: %q", q2.ProbeError)
	}
	if len(q2.Holders) != 1 || len(q2.Waiting) != 1 {
		t.Fatalf("status --json must list the live holder and the queued job: holders=%d waiting=%d", len(q2.Holders), len(q2.Waiting))
	}
	if !q2.Lockless {
		t.Fatal("the report must say the read was taken without the registry lock")
	}
}
