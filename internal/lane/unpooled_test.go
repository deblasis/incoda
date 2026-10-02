package lane

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestAcquireCountsUnpooledHolders: holders without a ticket here (an
// older incoda's unpooled run counted on a pool) hold slots ahead of every
// waiter. A free lane with one such holder admits nobody until it goes;
// an error from the count ends the wait as is.
func TestAcquireCountsUnpooledHolders(t *testing.T) {
	q, err := Open(t.TempDir(), "builds")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	en, err := q.Enroll(Ticket{Slots: 1, Command: []string{"new"}})
	if err != nil {
		t.Fatal(err)
	}
	defer en.Release(0)

	var unpooled atomic.Int32
	unpooled.Store(1)
	waits := 0
	opt := AcquireOptions{Wait: 200 * time.Millisecond, Poll: 20 * time.Millisecond,
		Unpooled: func() (int, error) { return int(unpooled.Load()), nil },
		OnWait:   func(int, int, []Entry, time.Duration) { waits++ }}
	if err := en.Acquire(context.Background(), opt); err != ErrTimeout {
		t.Fatalf("one unpooled holder on a one-slot lane: want ErrTimeout, got %v", err)
	}
	if waits == 0 {
		t.Fatal("a run held off by an unpooled holder is waiting and must say so")
	}

	go func() { time.Sleep(100 * time.Millisecond); unpooled.Store(0) }()
	opt.Wait = 5 * time.Second
	if err := en.Acquire(context.Background(), opt); err != nil {
		t.Fatalf("once the unpooled holder is gone: %v", err)
	}

	boom := errors.New("upgrade-blocked")
	en2, err := q.Enroll(Ticket{Slots: 1, Command: []string{"other"}})
	if err != nil {
		t.Fatal(err)
	}
	defer en2.Release(0)
	opt.Unpooled = func() (int, error) { return 0, boom }
	if err := en2.Acquire(context.Background(), opt); !errors.Is(err, boom) {
		t.Fatalf("the count's error ends the wait: %v", err)
	}
}

// TestRemoveIfIdle: a stray lane with only dead tickets is deleted under
// its registry lock after its log is handed on; one with a live ticket
// is kept; a missing directory is no error.
func TestRemoveIfIdle(t *testing.T) {
	root := t.TempDir()
	q, err := OpenIn(root, "dead", Create)
	if err != nil {
		t.Fatal(err)
	}
	en, err := q.Enroll(Ticket{Slots: 1, Command: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	en.lock.Close() // dies without Release: a dead ticket stays behind
	q.Close()
	var kept []string
	keep := func(p string) { kept = append(kept, p) }
	if ok, err := RemoveIfIdle(q.Dir, keep); !ok || err != nil {
		t.Fatalf("dead lane: %v %v", ok, err)
	}
	if ExistsIn(root, "dead") || len(kept) != 1 || kept[0] != LogPath(q.Dir) {
		t.Fatalf("deleted=%v kept=%v", !ExistsIn(root, "dead"), kept)
	}

	live, err := OpenIn(root, "live", Create)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	held, err := live.Enroll(Ticket{Slots: 1, Command: []string{"y"}})
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release(0)
	if ok, err := RemoveIfIdle(live.Dir, keep); ok || err != nil || !ExistsIn(root, "live") {
		t.Fatalf("live lane: %v %v", ok, err)
	}
	if ok, err := RemoveIfIdle(filepath.Join(root, "missing"), keep); ok || err != nil {
		t.Fatalf("missing lane: %v %v", ok, err)
	}
}
