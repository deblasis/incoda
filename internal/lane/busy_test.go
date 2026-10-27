package lane

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

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
// the test ends, the way a stopped incoda keeps a registry lock.
func holdLockElsewhere(t *testing.T, path string) {
	t.Helper()
	release := holdLockElsewhereUntil(t, path)
	t.Cleanup(release)
}

// holdLockElsewhereUntil is holdLockElsewhere with the hold on a switch: the
// returned func ends it, safely more than once, so a test can prove the lane is
// readable again once the holder is gone.
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

// within fails unless fn returns within limit.
func within(t *testing.T, what string, limit time.Duration, fn func()) {
	t.Helper()
	start := time.Now()
	fn()
	if d := time.Since(start); d > limit {
		t.Fatalf("%s took %s, more than %s", what, d, limit)
	}
}

// TestProbesAreBoundedWhenAnotherProcessHoldsTheRegistry: a registry lock
// another process keeps (a stopped incoda) costs every probe at most its
// deadline. ProbeLane reports the lane live with a cannot-tell entry,
// ProbeTicket a probe error, RemoveIfIdle deletes nothing, ObserveBy fails
// with ErrRegistryBusy.
func TestProbesAreBoundedWhenAnotherProcessHoldsTheRegistry(t *testing.T) {
	root := t.TempDir()
	q, err := OpenIn(root, "busy", Create)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	en, err := q.Enroll(Ticket{Slots: 1})
	if err != nil {
		t.Fatal(err)
	}
	name := en.Name()
	holdLockElsewhere(t, RegistryLockPath(q.Dir))
	dl := func() time.Time { return time.Now().Add(200 * time.Millisecond) }
	const limit = 2 * time.Second

	within(t, "ProbeLane", limit, func() {
		live, err := ProbeLane(q.Dir, dl())
		if err != nil || len(live) != 1 || !live[0].Live || !live[0].CannotTell() || live[0].PID() != 0 || !AnyCannotTell(live) {
			t.Fatalf("ProbeLane must report one cannot-tell live entry: %+v %v", live, err)
		}
	})
	within(t, "ProbeTicket", limit, func() {
		p := ProbeTicket(q.Dir, name, dl())
		if !p.CannotTell() || p.Live {
			t.Fatalf("ProbeTicket must fail with ErrRegistryBusy: %+v", p)
		}
	})
	within(t, "RemoveIfIdle", limit, func() {
		kept := false
		ok, err := RemoveIfIdle(q.Dir, dl(), func(string) { kept = true })
		if ok || err != nil || kept || !ExistsIn(root, "busy") {
			t.Fatalf("RemoveIfIdle must delete nothing: %v %v %v", ok, err, kept)
		}
	})
	within(t, "ObserveBy", limit, func() {
		snap, err := q.ObserveBy(0, dl())
		if err != nil {
			t.Fatalf("ObserveBy must answer from the tickets, not fail: %v", err)
		}
		if !snap.Lockless || len(snap.Holders) != 1 || snap.Holders[0].File != name {
			t.Fatalf("ObserveBy must report the live holder from a lockless read: %+v", snap)
		}
	})
	within(t, "a past deadline", 200*time.Millisecond, func() {
		if live, _ := ProbeLane(q.Dir, time.Time{}); !AnyCannotTell(live) {
			t.Fatal("a zero deadline tries once and reports cannot tell")
		}
	})
	if ErrRegistryBusy.Error() != "cannot tell: registry lock held by another process" {
		t.Fatalf("text: %q", ErrRegistryBusy)
	}
}

// TestObserveByListsEveryoneWhileTheRegistryLockIsHeld: a lane whose registry
// lock another process holds (a stopped incoda) is still described, not
// reported as unreadable. Liveness is per ticket, so the holder and every
// waiter are listed and the snapshot says the read was taken without the lock.
// Nothing is reaped: a ticket that looks unlocked may be one Enroll is between
// creating and locking, so a lockless read must never delete it.
func TestObserveByListsEveryoneWhileTheRegistryLockIsHeld(t *testing.T) {
	root := t.TempDir()
	q, err := OpenIn(root, "queued", Create)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	var ens []*Enrollment
	for i := 0; i < 3; i++ {
		en, err := q.Enroll(Ticket{Slots: 1, Command: []string{"job", strconv.Itoa(i)}})
		if err != nil {
			t.Fatal(err)
		}
		ens = append(ens, en)
	}

	// A ticket file no process locks stands in for one Enroll has created but
	// not yet locked, or for a holder that died: the lockless read must leave
	// it alone for the next lock-holding scan to decide.
	dead := TicketFilePath(q.Dir, ticketName(time.Now().UnixNano(), 1))
	if err := os.WriteFile(dead, []byte(`{"pid":1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	releaseLock := holdLockElsewhereUntil(t, RegistryLockPath(q.Dir))
	snap, err := q.ObserveBy(0, time.Now().Add(20*time.Millisecond))
	if err != nil {
		t.Fatalf("a held registry lock must not fail the view: %v", err)
	}
	releaseLock()
	for _, en := range ens {
		en.Release(0)
	}
	if !snap.Lockless {
		t.Fatal("the snapshot must say it was read without the registry lock")
	}
	if len(snap.Holders) != 1 || len(snap.Waiting) != 2 {
		t.Fatalf("want 1 holder and 2 waiters, got %d/%d: %+v", len(snap.Holders), len(snap.Waiting), snap)
	}
	if snap.Holders[0].File != ens[0].Name() || snap.Waiting[0].File != ens[1].Name() || snap.Waiting[1].File != ens[2].Name() {
		t.Fatalf("arrival order must be kept: holders=%v waiting=%v",
			snap.Holders[0].File, []string{snap.Waiting[0].File, snap.Waiting[1].File})
	}
	if _, err := os.Stat(dead); err != nil {
		t.Fatalf("a lockless read must reap nothing: %v", err)
	}
}

// TestRegistryWaitsAreBounded: every lane operation of this binary waits
// for a registry lock another process keeps (a stopped incoda) only within
// the handle's budget, never for ever. Enroll, Position, RequestKill,
// WaitGone, ForceRelease, UpdateConfig, SaveConfig and LockAll fail with
// ErrRegistryBusy; MarkAcquired gives up silently; Release frees the
// ticket without the lock; Acquire reads a busy poll as "not admitted" and
// times out within its wait.
func TestRegistryWaitsAreBounded(t *testing.T) {
	saved := registryFloor
	registryFloor = 100 * time.Millisecond
	defer func() { registryFloor = saved }()
	root := t.TempDir()
	q, err := OpenIn(root, "bounded", Create)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	en, err := q.Enroll(Ticket{Slots: 1, Command: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	holdLockElsewhere(t, RegistryLockPath(q.Dir))
	q.SetBudget(time.Now(), 200*time.Millisecond)
	const limit = 1500 * time.Millisecond
	busy := func(what string, fn func() error) {
		t.Helper()
		within(t, what, limit, func() {
			if err := fn(); !errors.Is(err, ErrRegistryBusy) {
				t.Fatalf("%s: want ErrRegistryBusy, got %v", what, err)
			}
		})
	}
	busy("Enroll", func() error { _, err := q.Enroll(Ticket{Slots: 1}); return err })
	busy("Position", func() error { _, _, _, err := en.Position(); return err })
	busy("RequestKill", func() error { _, err := q.RequestKill(os.Getpid(), KillRequest{By: "t", Reason: "r"}); return err })
	busy("WaitGone", func() error { _, err := q.WaitGone(os.Getpid(), 0, 10*time.Millisecond); return err })
	busy("ForceRelease", func() error { _, err := q.ForceRelease(true); return err })
	busy("UpdateConfig", func() error { _, err := q.UpdateConfig(func(*Config) error { return nil }); return err })
	busy("SaveConfig", func() error { return q.SaveConfig(Config{Slots: 2}) })
	busy("LockAll", func() error { _, err := LockAll([]*Queue{q}, time.Now().Add(100*time.Millisecond)); return err })
	within(t, "MarkAcquired", limit, func() { en.MarkAcquired() })
	within(t, "Acquire", limit+time.Second, func() {
		if err := en.Acquire(context.Background(), AcquireOptions{Wait: 300 * time.Millisecond, Poll: 20 * time.Millisecond}); !errors.Is(err, ErrTimeout) {
			t.Fatalf("Acquire must time out within its wait: %v", err)
		}
	})
	within(t, "Release", releaseRegistryWait+time.Second, func() { en.Release(0) })
	if _, err := os.Stat(TicketFilePath(q.Dir, en.Name())); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Release must remove the ticket without the lock: %v", err)
	}
}

// TestRegistryDeadline: the budget end, capped by limit, floored by
// registryFloor; no budget gives defaultRegistryWait; a negative wait has
// no deadline unless a limit caps it.
func TestRegistryDeadline(t *testing.T) {
	q := &Queue{}
	near := func(got time.Time, want time.Duration) bool {
		d := time.Until(got)
		return d > want-200*time.Millisecond && d <= want+50*time.Millisecond
	}
	if dl := q.registryDeadline(0); !near(dl, defaultRegistryWait) {
		t.Fatalf("no budget: %s", time.Until(dl))
	}
	q.SetBudget(time.Now(), 10*time.Second)
	if dl := q.registryDeadline(0); !near(dl, 10*time.Second) {
		t.Fatalf("budget end: %s", time.Until(dl))
	}
	if dl := q.registryDeadline(PollProbeWait); !near(dl, registryFloor) {
		t.Fatalf("a limit below the floor gets the floor: %s", time.Until(dl))
	}
	q.SetBudget(time.Now().Add(-time.Hour), time.Minute)
	if dl := q.registryDeadline(0); !near(dl, registryFloor) {
		t.Fatalf("a spent budget gets the floor: %s", time.Until(dl))
	}
	q.SetBudget(time.Now(), -1)
	if dl := q.registryDeadline(0); !dl.IsZero() {
		t.Fatalf("a negative wait has no deadline: %s", time.Until(dl))
	}
	if dl := q.registryDeadline(3 * time.Second); !near(dl, 3*time.Second) {
		t.Fatalf("a limit caps a negative wait: %s", time.Until(dl))
	}
}

func TestProbeDeadline(t *testing.T) {
	now := time.Now()
	if d := ProbeDeadline(time.Time{}, time.Second); d.Sub(now) < 900*time.Millisecond || d.Sub(now) > 2*time.Second {
		t.Fatalf("no budget end: about the limit from now, got %s", d.Sub(now))
	}
	if d := ProbeDeadline(now.Add(200*time.Millisecond), time.Second); d.Sub(now) > 300*time.Millisecond {
		t.Fatalf("never past the budget end, got %s", d.Sub(now))
	}
	if d := ProbeDeadline(now.Add(-time.Hour), time.Second); d.Before(now.Add(probeFloor - 5*time.Millisecond)) {
		t.Fatalf("a spent budget still gets the floor, got %s", d.Sub(now))
	}
}
