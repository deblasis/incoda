package machine

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/procinfo"
)

func TestNoteRoundTrip(t *testing.T) {
	n := Note{PID: 4711, Op: "migrate", Since: time.Date(2026, 10, 2, 9, 14, 3, 0, time.UTC),
		Blockers: []Blocker{{Key: "builds", PID: 4711}, {Key: "kungfoo-ui", PID: 5120}}}
	s := n.String()
	if s != "pid=4711 op=migrate since=2026-10-02T09:14:03Z blockers=4711:builds,5120:kungfoo-ui" {
		t.Fatalf("note = %q", s)
	}
	got, ok := ParseNote(s)
	if !ok || got.PID != 4711 || got.Op != "migrate" || !got.Since.Equal(n.Since) || len(got.Blockers) != 2 ||
		got.Blockers[1] != (Blocker{Key: "kungfoo-ui", PID: 5120}) {
		t.Fatalf("ParseNote = %+v %v", got, ok)
	}
	if got, ok := ParseNote("pid=7 op=refence since=2026-10-02T09:14:03Z future=1"); !ok || got.Op != "refence" {
		t.Fatalf("an unknown field from a newer incoda is ignored: %+v %v", got, ok)
	}
	for _, bad := range []string{
		"",
		"pid=x op=migrate since=2026-10-02T09:14:03Z",
		"pid=1 op=Migrate since=2026-10-02T09:14:03Z",
		"pid=1 op=migrate",
		"pid=1 op=migrate since=yesterday",
		"pid=1 op=migrate since=2026-10-02T09:14:03Z blockers=1:a/b",
		"pid=1 op=migrate since=2026-10-02T09:14:03Z blockers=x:builds",
	} {
		if _, ok := ParseNote(bad); ok {
			t.Fatalf("ParseNote(%q) must fail", bad)
		}
	}
}

func TestLockDeadline(t *testing.T) {
	now := time.Now()
	if got := lockDeadline(now, 0, now); got.Sub(now) != 2*time.Second {
		t.Fatalf("--wait 0 still gets 2s: %v", got.Sub(now))
	}
	if got := lockDeadline(now.Add(-10*time.Second), 30*time.Minute, now); got.Sub(now) != 30*time.Minute-10*time.Second {
		t.Fatalf("the budget runs from the start: %v", got.Sub(now))
	}
	if got := lockDeadline(now.Add(-time.Hour), 30*time.Minute, now); got.Sub(now) != 2*time.Second {
		t.Fatalf("a spent budget still gets 2s: %v", got.Sub(now))
	}
	if !lockDeadline(now, -1, now).IsZero() {
		t.Fatal("a negative --wait waits forever")
	}
}

func TestAcquireLockWritesAndClearsTheNote(t *testing.T) {
	state := t.TempDir()
	lk, err := AcquireLock(state, LockOptions{Op: "migrate", Start: time.Now(), Wait: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	n, ok := ReadNote(state)
	if !ok || n.PID != os.Getpid() || n.Op != "migrate" || len(n.Blockers) != 0 {
		t.Fatalf("note = %+v %v", n, ok)
	}
	if err := lk.SetBlockers([]Blocker{{Key: "builds", PID: 4711}}); err != nil {
		t.Fatal(err)
	}
	if n, _ := ReadNote(state); len(n.Blockers) != 1 || n.Blockers[0].PID != 4711 {
		t.Fatalf("blockers not in the note: %+v", n)
	}
	lk.Release()
	lk.Release() // idempotent
	if b, _ := os.ReadFile(LockPath(state)); len(b) != 0 {
		t.Fatalf("Release must clear the note, left %q", b)
	}
}

func TestAcquireLockWaitsAtLeastTwoSecondsThenTimesOut(t *testing.T) {
	state := t.TempDir()
	holder, err := AcquireLock(state, LockOptions{Op: "migrate", Start: time.Now(), Wait: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	if err := holder.SetBlockers([]Blocker{{Key: "builds", PID: 4711}}); err != nil {
		t.Fatal(err)
	}
	var errBuf bytes.Buffer
	start := time.Now()
	_, err = AcquireLock(state, LockOptions{Op: "migrate", Start: start, Wait: 0, Poll: 100 * time.Millisecond, Stderr: &errBuf})
	var to *Timeout
	if !errors.As(err, &to) || to.Msg != fmt.Sprintf("machine-lock-timeout: held by pid %d (migrate)", os.Getpid()) {
		t.Fatalf("want the machine-lock-timeout, got %v", err)
	}
	if el := time.Since(start); el < 2*time.Second {
		t.Fatalf("a --wait 0 caller still gets 2s for machine.lock, waited %v", el)
	}
	out := errBuf.String()
	want := fmt.Sprintf("incoda: waiting for machine.lock: pid %d migrate since ", os.Getpid())
	if !strings.HasPrefix(out, want) || !strings.HasSuffix(out, "; waiting for older runs: builds pid 4711\n") || strings.Count(out, "\n") != 1 {
		t.Fatalf("want one waiting line naming the holder and its blockers, got:\n%s", out)
	}
}

func TestAcquireLockRefusesWhenABlockerIsAnAncestor(t *testing.T) {
	state := t.TempDir()
	holder, err := AcquireLock(state, LockOptions{Op: "migrate", Start: time.Now(), Wait: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	if err := holder.SetBlockers([]Blocker{{Key: "builds", PID: 4711}}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = AcquireLock(state, LockOptions{Op: "migrate", Start: start, Wait: time.Minute, Poll: 50 * time.Millisecond,
		Chain: procinfo.Chain{PIDs: []int{999, 4711, 1}}})
	var rf *Refusal
	if !errors.As(err, &rf) || rf.Msg != `upgrade-blocked: an older incoda (pid 4711, an ancestor of this process) holds "builds"; rerun the outer command after it exits` {
		t.Fatalf("want upgrade-blocked, got %v", err)
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("upgrade-blocked must not wait, waited %v", el)
	}
	// Windows has no ancestry walk (Skip): the same waiter times out instead.
	_, err = AcquireLock(state, LockOptions{Op: "migrate", Start: time.Now(), Wait: 0, Poll: 50 * time.Millisecond,
		Chain: procinfo.Chain{Skip: true, PIDs: []int{4711}}})
	var to *Timeout
	if !errors.As(err, &to) {
		t.Fatalf("a Skip chain is never upgrade-blocked, got %v", err)
	}
}

func TestAcquireLockGetsTheLockOnceFreed(t *testing.T) {
	state := t.TempDir()
	holder, err := AcquireLock(state, LockOptions{Op: "refence", Start: time.Now(), Wait: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		holder.Release()
	}()
	lk, err := AcquireLock(state, LockOptions{Op: "migrate", Start: time.Now(), Wait: time.Minute, Poll: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	lk.Release()
}
