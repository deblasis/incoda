package lane

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lockfile"
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

// TestEnrollStampsAfterEveryLiveTicket: arrival order is the admission order,
// and the wall clock can repeat a value (its steps are coarse on Windows) or
// step back. A ticket that sorted ahead of one already live could start beside
// a running exclusive, because a ticket that already acquired is never
// revoked, so enrollment stamps past every live ticket.
func TestEnrollStampsAfterEveryLiveTicket(t *testing.T) {
	q, err := Open(t.TempDir(), "order")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	future := time.Now().Add(time.Hour).UnixNano()
	name := ticketName(future, 4242)
	path := ticketPath(q.Dir, name)
	lf, err := lockfile.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := lf.TryLock(); err != nil || !ok {
		t.Fatalf("hold: ok=%v err=%v", ok, err)
	}
	t.Cleanup(func() {
		_ = lf.Close()
		_ = os.Remove(path)
	})
	payload := `{"pid":4242,"slots":1,"arrival_nano":` + strconv.FormatInt(future, 10) + `}`
	if err := lf.Truncate([]byte(payload)); err != nil {
		t.Fatal(err)
	}

	en, err := q.Enroll(Ticket{Slots: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer en.Release(0)
	if en.ticket.ArrivalNano <= future {
		t.Fatalf("stamp %d did not move past live ticket %d", en.ticket.ArrivalNano, future)
	}
}

// TestEnrollStampsPastADeadFutureName: the order is the filename, so a ticket
// file the lane cannot open still bounds the next arrival. A name at the
// maximum stamp saturates instead of wrapping negative.
func TestEnrollStampsPastADeadFutureName(t *testing.T) {
	q, err := Open(t.TempDir(), "dead-name")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	future := time.Now().Add(time.Hour).UnixNano()
	if err := os.WriteFile(ticketPath(q.Dir, ticketName(future, 4242)), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	en, err := q.Enroll(Ticket{Slots: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer en.Release(0)
	if en.ticket.ArrivalNano <= future {
		t.Fatalf("stamp %d did not move past dead ticket name %d", en.ticket.ArrivalNano, future)
	}
}

func TestEnrollSaturatesAtTheMaximumStamp(t *testing.T) {
	q, err := Open(t.TempDir(), "max-name")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if err := os.WriteFile(ticketPath(q.Dir, ticketName(math.MaxInt64, 4242)), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	en, err := q.Enroll(Ticket{Slots: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer en.Release(0)
	if en.ticket.ArrivalNano != math.MaxInt64 {
		t.Fatalf("stamp %d did not saturate at the maximum", en.ticket.ArrivalNano)
	}
}

// TestAcquireStartsThroughAnUnreadableOwnPayload: the owner of a ticket whose
// payload was lost still starts when admission says so, because it has the
// ticket in memory. Everyone else reads that ticket as a barrier.
func TestAcquireStartsThroughAnUnreadableOwnPayload(t *testing.T) {
	q, err := Open(t.TempDir(), "torn")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	en, err := q.Enroll(Ticket{Slots: 1, Command: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	defer en.Release(0)
	if err := en.lock.Truncate([]byte("{")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := en.Acquire(ctx, AcquireOptions{Poll: 50 * time.Millisecond}); err != nil {
		t.Fatalf("the owner could not start through a torn payload: %v", err)
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
	if got := effectiveSlots(live, 0); got != 2 {
		t.Fatalf("effectiveSlots = %d, want 2 (the most restrictive participant wins)", got)
	}
	if !SlotsDisagree(live) {
		t.Fatal("SlotsDisagree should report a mixed set")
	}
	if SlotsDisagree([]Entry{{Ticket: Ticket{Slots: 3}}, {Ticket: Ticket{Slots: 3}}}) {
		t.Fatal("SlotsDisagree should not fire on an agreeing set")
	}
	if got := effectiveSlots(nil, 0); got != 1 {
		t.Fatalf("effectiveSlots(nil) = %d, want 1", got)
	}
	// A configured count is a clamp, not a target: every ticket counts as at
	// least the configured width, so the same mixed set on a 6-slot queue
	// resolves to 6...
	if got := effectiveSlots(live, 6); got != 6 {
		t.Fatalf("effectiveSlots with a configured floor of 6 = %d, want 6", got)
	}
	// ...and a configured 1 caps the same set at 1, so a ticket written
	// before the config was narrowed cannot keep the queue wide.
	if got := effectiveSlots(live, 1); got != 1 {
		t.Fatalf("effectiveSlots with a configured cap of 1 = %d, want 1", got)
	}
}

func TestNextArrivalSaturatesAtTheMaximum(t *testing.T) {
	now := int64(1_700_000_000_000_000_000)
	dir := t.TempDir()
	write := func(nano int64) {
		name := fmt.Sprintf("%020d-1.ticket", nano)
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(now)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := nextArrival(now, entries); got != now+1 {
		t.Fatalf("nextArrival = %d, want one past %d", got, now)
	}
	write(math.MaxInt64)
	entries, err = os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := nextArrival(now, entries); got != math.MaxInt64 {
		t.Fatalf("nextArrival = %d, want the maximum stamp, not a wrapped one", got)
	}
}

func TestEffectiveSlotsIgnoresAnUnreadableTicket(t *testing.T) {
	// An unreadable payload has no width to vote; letting its zero value
	// floor the lane at one would idle the slots of a wider unconfigured
	// queue, while the barrier it already is stops everything behind it.
	live := []Entry{
		{Ticket: Ticket{Slots: 4}},
		{PayloadError: "parse: unexpected end of JSON input"},
	}
	if got := effectiveSlots(live, 0); got != 4 {
		t.Fatalf("an unreadable ticket narrowed the lane to %d, want 4", got)
	}
	if got := effectiveSlots([]Entry{{PayloadError: "parse"}}, 0); got != 1 {
		t.Fatalf("an unreadable-only set still floors at one slot, got %d", got)
	}
	if got := effectiveSlots(live, 2); got != 2 {
		t.Fatalf("the configured clamp still applies, got %d, want 2", got)
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
