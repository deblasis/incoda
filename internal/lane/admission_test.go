package lane

import (
	"context"
	"sync/atomic"
	"testing"
)

// TestExclusiveTicketIsABarrierAtItsTurn: --exclusive needs the queue empty
// when it starts and nobody alongside it while it runs. It does not pause the
// jobs that arrived before it: they take every free slot until the queue is
// drained, and only then does the exclusive job start.
func TestExclusiveTicketIsABarrierAtItsTurn(t *testing.T) {
	q, err := Open(t.TempDir(), "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if err := q.SaveConfig(Config{Slots: 4}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	a, err := q.Enroll(Ticket{Command: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Acquire(ctx, AcquireOptions{Wait: 0}); err != nil {
		t.Fatal(err)
	}
	defer a.Release(0)

	// One holder, three slots free: a plain job and an exclusive one arrive.
	e, err := q.Enroll(Ticket{Command: []string{"e"}})
	if err != nil {
		t.Fatal(err)
	}
	x, err := q.Enroll(Ticket{Command: []string{"x"}, Exclusive: true})
	if err != nil {
		t.Fatal(err)
	}

	// The plain job is ahead of the exclusive one and a slot is free. It
	// starts now: the exclusive request is a barrier at its turn, not a
	// queue-wide pause.
	if err := e.Acquire(ctx, AcquireOptions{Wait: 0}); err != nil {
		t.Fatalf("plain job ahead of an exclusive one was held back: %v", err)
	}
	// The exclusive one still needs the queue empty: the first holder and
	// the plain job are both live.
	if err := x.Acquire(ctx, AcquireOptions{Wait: 0}); err != ErrTimeout {
		t.Fatalf("exclusive ticket acquired with live participants: %v", err)
	}
	a.Release(0)
	if err := x.Acquire(ctx, AcquireOptions{Wait: 0}); err != ErrTimeout {
		t.Fatalf("exclusive ticket acquired while the job ahead of it still holds: %v", err)
	}
	e.Release(0)
	if err := x.Acquire(ctx, AcquireOptions{Wait: 0}); err != nil {
		t.Fatalf("exclusive ticket should acquire the drained queue: %v", err)
	}

	// And it is a barrier while it holds: a job that arrives now waits
	// even though the queue is configured for four slots.
	f, err := q.Enroll(Ticket{Command: []string{"f"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Acquire(ctx, AcquireOptions{Wait: 0}); err != ErrTimeout {
		t.Fatalf("a ticket joined an exclusive holder: %v", err)
	}
	x.Release(0)
	if err := f.Acquire(ctx, AcquireOptions{Wait: 0}); err != nil {
		t.Fatalf("ticket behind an exclusive holder should acquire after it releases: %v", err)
	}
	f.Release(0)
}

// TestStatusShowsAJobAdmittedAheadOfAnExclusive: status and the wait loop read
// the same admission rule. A plain ticket ahead of an exclusive one that can
// start now is a holder in the report, not a waiter behind the request; the
// exclusive ticket and the ones after it are waiting.
func TestStatusShowsAJobAdmittedAheadOfAnExclusive(t *testing.T) {
	q, err := Open(t.TempDir(), "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if err := q.SaveConfig(Config{Slots: 4}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	a, err := q.Enroll(Ticket{Command: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Acquire(ctx, AcquireOptions{Wait: 0}); err != nil {
		t.Fatal(err)
	}
	defer a.Release(0)
	if _, err := q.Enroll(Ticket{Command: []string{"e"}}); err != nil {
		t.Fatal(err)
	}
	x, err := q.Enroll(Ticket{Command: []string{"x"}, Exclusive: true})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Release(0)
	f, err := q.Enroll(Ticket{Command: []string{"f"}})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Release(0)

	snap, err := q.Observe(0)
	if err != nil {
		t.Fatal(err)
	}
	if snap.EffectiveSlots != 4 {
		t.Fatalf("status narrowed the queue to %d slot(s) for a waiting exclusive ticket; want the configured 4", snap.EffectiveSlots)
	}
	cmds := func(es []Entry) []string {
		out := make([]string, len(es))
		for i, e := range es {
			out[i] = e.Ticket.CommandString()
		}
		return out
	}
	if got := cmds(snap.Holders); len(got) != 2 || got[0] != "a" || got[1] != "e" {
		t.Fatalf("holders = %v, want [a e]: the plain job ahead of the exclusive one starts now", got)
	}
	if got := cmds(snap.Waiting); len(got) != 2 || got[0] != "x" || got[1] != "f" {
		t.Fatalf("waiting = %v, want [x f]: the exclusive job and the job behind it wait for the drain", got)
	}
}

// TestExclusiveTicketWaitsForUnpooledHolders: an exclusive ticket must not
// start beside a slot held by a run with no ticket on this lane. The same
// unpooled count that admits a plain waiter preserves the empty queue the
// exclusive ticket needs.
func TestExclusiveTicketWaitsForUnpooledHolders(t *testing.T) {
	q, err := Open(t.TempDir(), "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	x, err := q.Enroll(Ticket{Command: []string{"x"}, Exclusive: true})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Release(0)

	var unpooled atomic.Int32
	unpooled.Store(1)
	opt := AcquireOptions{
		Wait:     0,
		Unpooled: func() (int, error) { return int(unpooled.Load()), nil },
	}
	if err := x.Acquire(context.Background(), opt); err != ErrTimeout {
		t.Fatalf("exclusive ticket acquired beside an unpooled holder: %v", err)
	}
	unpooled.Store(0)
	if err := x.Acquire(context.Background(), opt); err != nil {
		t.Fatalf("exclusive ticket should acquire once the unpooled holder is gone: %v", err)
	}
}
