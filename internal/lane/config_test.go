package lane

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigRoundTripAndDefaults(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	// No file: every field at its zero value, and no error, because a
	// never-configured queue is the common case and must stay free to use.
	cfg, err := q.LoadConfig()
	if err != nil {
		t.Fatalf("missing config must not error: %v", err)
	}
	if cfg != (Config{}) {
		t.Fatalf("missing config should be zero, got %+v", cfg)
	}

	want := Config{Slots: 2, Description: "CPU and RAM", RequireReason: true, Closed: "use wintty-build"}
	if err := q.SaveConfig(want); err != nil {
		t.Fatal(err)
	}
	got, err := q.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("config did not round-trip: got %+v want %+v", got, want)
	}

	// A malformed file is an error, not a silent reset to defaults: a
	// queue that quietly forgot it was closed would let the old key back in.
	if err := os.WriteFile(filepath.Join(q.Dir, configName), []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := q.LoadConfig(); err == nil {
		t.Fatal("a corrupt config must be reported")
	}
}

func TestConfigSlotsAreTheDefaultForTickets(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if err := q.SaveConfig(Config{Slots: 3}); err != nil {
		t.Fatal(err)
	}

	// Slots 0 on the ticket means "whatever the queue says".
	a, err := q.Enroll(Ticket{Command: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release(0)
	snap, err := q.Observe(0)
	if err != nil {
		t.Fatal(err)
	}
	if snap.EffectiveSlots != 3 || snap.Holders[0].Ticket.Slots != 3 {
		t.Fatalf("config slots should apply to an unset ticket, got effective %d ticket %d", snap.EffectiveSlots, snap.Holders[0].Ticket.Slots)
	}

	// An explicit --slots still wins downward, as it always did.
	b, err := q.Enroll(Ticket{Slots: 2, Command: []string{"b"}})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Release(0)
	snap, err = q.Observe(0)
	if err != nil {
		t.Fatal(err)
	}
	if snap.EffectiveSlots != 2 {
		t.Fatalf("explicit smaller --slots must still win, got %d", snap.EffectiveSlots)
	}
	b.Release(0)

	// A larger --slots cannot widen the queue past its config: two callers
	// both passing 5 on a 3-slot queue would otherwise see 5 slots.
	c, err := q.Enroll(Ticket{Slots: 5, Command: []string{"c"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Release(0)
	if got := c.Ticket().Slots; got != 3 {
		t.Fatalf("--slots above the config should be clamped to 3, got %d", got)
	}
}

func TestAcquiredHolderStaysAHolderWhenExclusiveArrives(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if err := q.SaveConfig(Config{Slots: 2}); err != nil {
		t.Fatal(err)
	}
	var holders []*Enrollment
	for _, name := range []string{"a", "b"} {
		en, err := q.Enroll(Ticket{Command: []string{name}})
		if err != nil {
			t.Fatal(err)
		}
		if err := en.Acquire(context.Background(), AcquireOptions{Wait: 0}); err != nil {
			t.Fatal(err)
		}
		holders = append(holders, en)
	}
	x, err := q.Enroll(Ticket{Command: []string{"x"}, Exclusive: true})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Release(0)
	snap, err := q.Observe(0)
	if err != nil {
		t.Fatal(err)
	}
	// The exclusive ticket narrows the count to 1 for newcomers, but the
	// second holder is still running and must be reported as holding.
	if len(snap.Holders) != 2 || len(snap.Waiting) != 1 {
		t.Fatalf("want 2 holders and 1 waiter, got %d/%d", len(snap.Holders), len(snap.Waiting))
	}
	for _, h := range holders {
		h.Release(0)
	}
}

func TestExclusiveTicketForcesOneSlot(t *testing.T) {
	// An exclusive participant needs the queue to itself: while it is live
	// the effective slot count is 1 whatever anyone else asked for, and it
	// is not a "disagreement" worth warning about.
	live := []Entry{
		{Ticket: Ticket{Slots: 4}},
		{Ticket: Ticket{Slots: 4, Exclusive: true}},
	}
	if got := effectiveSlots(live); got != 1 {
		t.Fatalf("effectiveSlots with an exclusive ticket = %d, want 1", got)
	}
	if SlotsDisagree(live) {
		t.Fatal("an exclusive ticket is not a slots disagreement")
	}

	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if err := q.SaveConfig(Config{Slots: 2}); err != nil {
		t.Fatal(err)
	}
	a, err := q.Enroll(Ticket{Command: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Acquire(context.Background(), AcquireOptions{Wait: 0}); err != nil {
		t.Fatal(err)
	}
	x, err := q.Enroll(Ticket{Command: []string{"x"}, Exclusive: true})
	if err != nil {
		t.Fatal(err)
	}
	// Two slots, one holder: a plain ticket would acquire now. The
	// exclusive one must wait for the queue to drain.
	if err := x.Acquire(context.Background(), AcquireOptions{Wait: 0}); err != ErrTimeout {
		t.Fatalf("exclusive ticket acquired alongside a holder: %v", err)
	}
	a.Release(0)
	if err := x.Acquire(context.Background(), AcquireOptions{Wait: 0}); err != nil {
		t.Fatalf("exclusive ticket should acquire an empty queue: %v", err)
	}
	// And nobody joins it while it holds, even though the queue says 2.
	b, err := q.Enroll(Ticket{Command: []string{"b"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Acquire(context.Background(), AcquireOptions{Wait: 0}); err != ErrTimeout {
		t.Fatalf("a ticket joined an exclusive holder: %v", err)
	}
	x.Release(0)
	b.Release(0)
}

func TestEnqueueLogsExclusive(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	en, err := q.Enroll(Ticket{Command: []string{"x"}, Exclusive: true})
	if err != nil {
		t.Fatal(err)
	}
	defer en.Release(0)
	if got := lastLog(t, q); !strings.Contains(got, "exclusive=true") {
		t.Fatalf("enqueue line should say exclusive, got %q", got)
	}
}
