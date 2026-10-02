package machine

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/lockfile"
)

// soon is a probe deadline no healthy test gets near.
func soon() time.Time { return time.Now().Add(5 * time.Second) }

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

// holdLockElsewhere starts a helper process that holds path's lock until
// the test ends, the way a stopped older incoda keeps a registry lock.
func holdLockElsewhere(t *testing.T, path string) {
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
	t.Cleanup(func() {
		in.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "held\n" {
		t.Fatalf("helper did not take the lock: %q %v", line, err)
	}
}

// busyLane makes root/key a lane whose registry lock another process
// holds for the rest of the test.
func busyLane(t *testing.T, root, key string) string {
	t.Helper()
	holdTicket(t, root, key, 4711, "zig", "build")()
	dir := filepath.Join(root, key)
	holdLockElsewhere(t, lane.RegistryLockPath(dir))
	return dir
}

func within(t *testing.T, what string, limit time.Duration, fn func()) {
	t.Helper()
	start := time.Now()
	fn()
	if d := time.Since(start); d > limit {
		t.Fatalf("%s took %s, more than %s", what, d, limit)
	}
}

func shortly() time.Time { return time.Now().Add(200 * time.Millisecond) }

const cannotTell = "cannot tell: registry lock held by another process"

// TestScanUnpooledCountsABusyStrayAsHeld: a stray lane whose registry lock
// another process keeps is one held slot on every pool its key charges, it
// is not cleaned, and the scan returns within its deadline.
func TestScanUnpooledCountsABusyStrayAsHeld(t *testing.T) {
	state, reg := migrated(t)
	dir := busyLane(t, filepath.Join(StraysDir(state), "1"), "busy")
	var us []Unpooled
	within(t, "ScanUnpooled", 2*time.Second, func() {
		var err error
		if us, err = ScanUnpooled(state, true, shortly()); err != nil {
			t.Fatal(err)
		}
	})
	if len(us) != 1 || !us[0].Unknown || us[0].Line() != "unpooled lane of an older incoda: strays/1/busy: "+cannotTell {
		t.Fatalf("want one cannot-tell holder, got %+v", us)
	}
	if n := len(ChargedTo(state, reg, reg.Pools[0], us)); n != 1 {
		t.Fatalf("the busy stray is charged to pool %s: %d", reg.Pools[0], n)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("a lane that cannot be told is never cleaned")
	}
}

// TestFindBlockersAndWaitIdleOnABusyLane: an idle check counts a lane it
// cannot tell as a blocker with no stop line and no pid in the note, and
// times out within its budget instead of hanging under machine.lock.
func TestFindBlockersAndWaitIdleOnABusyLane(t *testing.T) {
	state := t.TempDir()
	busyLane(t, lane.QueuesDir(state), "builds")
	var bs []Blocker
	within(t, "findBlockers", 2*time.Second, func() {
		var err error
		if bs, err = findBlockers(state, phaseM2, shortly()); err != nil {
			t.Fatal(err)
		}
	})
	if len(bs) != 1 || !bs[0].Unknown {
		t.Fatalf("want one unknown blocker, got %+v", bs)
	}
	lines := strings.Join(blockerLines(bs, phaseM2), "\n")
	if strings.Contains(lines, "incoda kill") || !strings.Contains(lines, "  builds: "+cannotTell) {
		t.Fatalf("blocker lines:\n%s", lines)
	}
	if n, ok := ParseNote(Note{PID: 1, Op: "migrate", Since: time.Now(), Blockers: bs}.String()); !ok || len(n.Blockers) != 0 {
		t.Fatalf("an unknown blocker leaves the note parseable and pid-free: %+v %v", n, ok)
	}
	lk := takeLock(t, state)
	within(t, "waitIdle", 3*time.Second, func() {
		err := waitIdle(state, lk, Options{Start: time.Now(), Wait: 300 * time.Millisecond, Poll: 50 * time.Millisecond, Stderr: io.Discard}, phaseM2)
		var to *Timeout
		if !errors.As(err, &to) || !strings.Contains(to.Msg, "builds: "+cannotTell) {
			t.Fatalf("want a Timeout naming the busy lane, got %v", err)
		}
	})
}

// TestFindKillTargetCannotTell: kill fails closed, promptly, when the lane
// that may hold the pid cannot be told.
func TestFindKillTargetCannotTell(t *testing.T) {
	state, _ := migrated(t)
	busyLane(t, filepath.Join(StraysDir(state), "1"), "k")
	v, err := Inspect(state)
	if err != nil {
		t.Fatal(err)
	}
	within(t, "FindKillTarget", 2*time.Second, func() {
		if _, err := FindKillTarget(state, v, "k", 4711, shortly()); !errors.Is(err, lane.ErrRegistryBusy) {
			t.Fatalf("want ErrRegistryBusy, got %v", err)
		}
	})
}

// TestReadOnlyViewsAreBounded: Diagnose and StatusWarnings return within
// their fixed bound and name the lane they cannot tell.
func TestReadOnlyViewsAreBounded(t *testing.T) {
	state, _ := migrated(t)
	busyLane(t, filepath.Join(StraysDir(state), "1"), "busy")
	busyLane(t, lane.LanesDir(state), "mine")
	within(t, "Diagnose", 3*time.Second, func() {
		h := Diagnose(state)
		got := strings.Join(h.Strays, "\n") + "\n" + strings.Join(h.Attention, "\n")
		for _, want := range []string{"1/busy: cannot probe (" + cannotTell + ")", "queue mine: " + cannotTell} {
			if !strings.Contains(got, want) {
				t.Fatalf("doctor must name %q:\n%s", want, got)
			}
		}
	})
	v, err := Inspect(state)
	if err != nil {
		t.Fatal(err)
	}
	within(t, "StatusWarnings", 3*time.Second, func() {
		lines := StatusWarnings(state, v, nil, time.Now().Add(lane.ViewProbeWait))
		if !strings.Contains(strings.Join(lines, "\n"), "unpooled lane of an older incoda: strays/1/busy: "+cannotTell) {
			t.Fatalf("status warnings: %q", lines)
		}
	})
}

// TestStrayCleanupFailureIsLoggedNotFatal: a stray lane that cannot be
// deleted is housekeeping that failed, not a state error. The scan and
// CleanStrays still succeed, and machine.log gets one cleanup-failed line.
func TestStrayCleanupFailureIsLoggedNotFatal(t *testing.T) {
	state, _ := migrated(t)
	batch := filepath.Join(StraysDir(state), "1")
	holdTicket(t, batch, "dead", 4711, "x")()
	saved := removeIfIdleFn
	t.Cleanup(func() { removeIfIdleFn = saved })
	removeIfIdleFn = func(string, time.Time, func(string)) (bool, error) {
		return false, errors.New("injected \"failure\"")
	}
	if _, err := ScanUnpooled(state, true, soon()); err != nil {
		t.Fatalf("a failed cleanup must not fail the scan: %v", err)
	}
	if err := CleanStrays(state, soon()); err != nil {
		t.Fatalf("a failed cleanup must not fail CleanStrays: %v", err)
	}
	b, _ := os.ReadFile(MachineLogPath(state))
	want := fmt.Sprintf("event=cleanup-failed pid=%d path=%s err=", os.Getpid(), filepath.Join(batch, "dead"))
	if n := strings.Count(string(b), want); n != 2 || !strings.Contains(string(b), `err="injected \"failure\""`) {
		t.Fatalf("want two escaped cleanup-failed lines, got:\n%s", b)
	}
}
