package lane

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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
		if _, err := q.ObserveBy(0, dl()); !errors.Is(err, ErrRegistryBusy) {
			t.Fatalf("ObserveBy must fail with ErrRegistryBusy: %v", err)
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
