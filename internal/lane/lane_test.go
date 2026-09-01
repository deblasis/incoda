package lane

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateKey(t *testing.T) {
	good := []string{"heavy-builds", "gui-tests", "a", "gui_tests", "v1.2", strings.Repeat("k", 64)}
	for _, k := range good {
		if err := ValidateKey(k); err != nil {
			t.Errorf("ValidateKey(%q) = %v, want nil", k, err)
		}
	}
	bad := []string{
		"", ".", "..", "../escape", "a/b", `a\b`, "with space", "nul", "NUL", "Com1",
		strings.Repeat("k", 65), "a:b", "a*b", "a\x00b", "a$b",
	}
	for _, k := range bad {
		if err := ValidateKey(k); err == nil {
			t.Errorf("ValidateKey(%q) = nil, want an error", k)
		}
	}
}

func TestStateDirIgnoresWorkingDirectory(t *testing.T) {
	// The whole model depends on the state directory being a property of the
	// machine and user, never of where the caller happens to stand. Six agents
	// in six worktrees of the same repo must land on one lane.
	base := t.TempDir()
	t.Setenv("INCODA_DIR", base)

	want, err := StateDir()
	if err != nil {
		t.Fatal(err)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	for _, sub := range []string{"worktree-a", "worktree-b", "deep/nested/checkout"} {
		dir := filepath.Join(base, "cwds", sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		got, err := StateDir()
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("StateDir() from cwd %s = %q, want %q (state must never depend on the working directory)", dir, got, want)
		}
	}
}

func TestTicketOrderIsDeterministic(t *testing.T) {
	cases := []struct{ a, b string }{
		{ticketName(100, 5), ticketName(200, 1)},
		{ticketName(100, 5), ticketName(100, 6)},
	}
	for _, c := range cases {
		oa, ok := parseTicketName(c.a)
		if !ok {
			t.Fatalf("parseTicketName(%q) failed", c.a)
		}
		ob, ok := parseTicketName(c.b)
		if !ok {
			t.Fatalf("parseTicketName(%q) failed", c.b)
		}
		if !oa.less(ob) {
			t.Errorf("%q should sort before %q", c.a, c.b)
		}
		if ob.less(oa) {
			t.Errorf("%q should not sort before %q", c.b, c.a)
		}
	}
	if _, ok := parseTicketName("not-a-ticket.txt"); ok {
		t.Error("parseTicketName accepted a non-ticket name")
	}
}

func TestSingleProcessSlotAccounting(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	a, err := q.Enroll(Ticket{Slots: 1, Command: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Acquire(context.Background(), AcquireOptions{Wait: 0}); err != nil {
		t.Fatalf("first enrollment should acquire immediately: %v", err)
	}

	snap, err := q.Observe(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Holders) != 1 || len(snap.Waiting) != 0 {
		t.Fatalf("want 1 holder 0 waiting, got %d/%d", len(snap.Holders), len(snap.Waiting))
	}

	a.Release(0)
	snap, err = q.Observe(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Holders) != 0 {
		t.Fatalf("want 0 holders after release, got %d", len(snap.Holders))
	}
}

func TestForceReleaseRefusesLiveHolder(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	en, err := q.Enroll(Ticket{Slots: 1, Command: []string{"held"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.ForceRelease(false); err == nil {
		t.Fatal("force-release must refuse while a live participant exists")
	} else if !strings.Contains(err.Error(), "--live") {
		t.Fatalf("the refusal must point at --live, got: %v", err)
	}

	n, err := q.ForceRelease(true)
	if err != nil {
		t.Fatalf("force-release --live: %v", err)
	}
	if n != 1 {
		t.Fatalf("force-release --live removed %d tickets, want 1", n)
	}
	en.Release(0)
}

func TestObserveOnFreshQueue(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "never-used")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	snap, err := q.Observe(5)
	if err != nil {
		t.Fatalf("observing an empty queue must not error: %v", err)
	}
	if len(snap.Holders) != 0 || len(snap.Waiting) != 0 {
		t.Fatal("a fresh queue must be empty")
	}
}

func TestDeadTicketIsReaped(t *testing.T) {
	// Simulates a participant whose process vanished: the ticket file is on
	// disk but nothing holds its lock. A scan must delete it rather than
	// treating it as a live holder.
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	orphan := filepath.Join(q.Dir, ticketName(time.Now().UnixNano()-1_000_000, 999999))
	if err := os.WriteFile(orphan, []byte(`{"pid":999999,"slots":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := q.Observe(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Holders) != 0 {
		t.Fatalf("an unlocked ticket must be reaped, got %d holders", len(snap.Holders))
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("the orphan ticket file should have been deleted")
	}
}

func TestAcquireTimesOut(t *testing.T) {
	dir := t.TempDir()
	q1, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q1.Close()
	holder, err := q1.Enroll(Ticket{Slots: 1, Command: []string{"holder"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.Acquire(context.Background(), AcquireOptions{Wait: 0}); err != nil {
		t.Fatal(err)
	}
	defer holder.Release(0)

	waiter, err := q1.Enroll(Ticket{Slots: 1, Command: []string{"waiter"}})
	if err != nil {
		t.Fatal(err)
	}
	defer waiter.Release(0)

	start := time.Now()
	err = waiter.Acquire(context.Background(), AcquireOptions{Wait: 300 * time.Millisecond, Poll: 50 * time.Millisecond})
	if err != ErrTimeout {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	if el := time.Since(start); el < 250*time.Millisecond {
		t.Fatalf("gave up after %s, expected to wait at least the budget", el)
	}
}

func TestEffectiveSlotsTakesTheMinimum(t *testing.T) {
	live := []Entry{
		{Ticket: Ticket{Slots: 4}},
		{Ticket: Ticket{Slots: 2}},
		{Ticket: Ticket{Slots: 7}},
	}
	if got := effectiveSlots(live); got != 2 {
		t.Fatalf("effectiveSlots = %d, want 2 (the most restrictive participant wins)", got)
	}
	if !SlotsDisagree(live) {
		t.Fatal("SlotsDisagree should report a mixed set")
	}
	if SlotsDisagree([]Entry{{Ticket: Ticket{Slots: 3}}, {Ticket: Ticket{Slots: 3}}}) {
		t.Fatal("SlotsDisagree should not fire on an agreeing set")
	}
	if got := effectiveSlots(nil); got != 1 {
		t.Fatalf("effectiveSlots(nil) = %d, want 1", got)
	}
}

func TestHeldTicketPayloadStaysReadable(t *testing.T) {
	// Regression: the first Windows implementation locked byte 0 of the ticket
	// file. Windows byte-range locks deny reads of the locked range, so every
	// observer silently failed to parse the payload, --slots was read as 0, and
	// every queue quietly degraded to a single slot. Mutual exclusion still
	// held, so only a slots test caught it. Assert the payload is readable
	// while the lock is held.
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	en, err := q.Enroll(Ticket{Slots: 3, Command: []string{"zig", "build"}, Reason: "why", Dir: "C:/somewhere"})
	if err != nil {
		t.Fatal(err)
	}
	defer en.Release(0)

	snap, err := q.Observe(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Holders) != 1 {
		t.Fatalf("want 1 holder, got %d", len(snap.Holders))
	}
	got := snap.Holders[0]
	if got.PayloadError != "" {
		t.Fatalf("the payload of a held ticket must be readable, got error: %s", got.PayloadError)
	}
	if got.Ticket.Slots != 3 {
		t.Fatalf("slots read back as %d, want 3", got.Ticket.Slots)
	}
	if snap.EffectiveSlots != 3 {
		t.Fatalf("effective slots = %d, want 3", snap.EffectiveSlots)
	}
	if got.Ticket.CommandString() != "zig build" {
		t.Fatalf("command read back as %q", got.Ticket.CommandString())
	}
	if got.Ticket.Reason != "why" || got.Ticket.Dir != "C:/somewhere" {
		t.Fatalf("reason/cwd did not round-trip: %+v", got.Ticket)
	}
	if got.Ticket.Queue != "unit" {
		t.Fatalf("queue key did not round-trip: %q", got.Ticket.Queue)
	}
}
