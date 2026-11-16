package lane

import (
	"testing"
	"time"
)

func TestFillClampsFutureStampsToZero(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	ahead := now.Add(time.Hour)
	e := Entry{Ticket: Ticket{ArrivalNano: ahead.UnixNano(), AcquireNano: ahead.UnixNano()}}
	e.fill(now)
	if e.WaitingSeconds != 0 {
		t.Fatalf("a ticket stamped ahead of the wall clock is not waiting a negative time, got %v", e.WaitingSeconds)
	}
	if e.HeldSeconds != 0 {
		t.Fatalf("a ticket acquired ahead of the wall clock is not held a negative time, got %v", e.HeldSeconds)
	}
}

func TestEntryHolderPredicateCoversUnreadable(t *testing.T) {
	cases := []struct {
		name string
		e    Entry
		want bool
	}{
		{"acquired", Entry{Holding: true}, true},
		{"waiting", Entry{Holding: false}, false},
		{"unreadable", Entry{PayloadError: "parse: unexpected end of JSON input"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.e.CountsAsHolder(); got != c.want {
				t.Fatalf("CountsAsHolder() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestHasExclusiveHolderRequiresAReadableStartedHolder: the exclusive bit is
// only believed on a payload that read whole and on a ticket that started.
// A torn payload can decode the bit before it fails, and a waiter that asked
// for the queue alone has not been admitted yet. The torn case sets Holding
// by hand, which scan never does, so the readability clause is exercised on
// its own.
func TestHasExclusiveHolderRequiresAReadableStartedHolder(t *testing.T) {
	cases := []struct {
		name string
		e    Entry
		want bool
	}{
		{"started and exclusive", Entry{Holding: true, Ticket: Ticket{Exclusive: true}}, true},
		{"started, plain", Entry{Holding: true}, false},
		{"waiting and exclusive", Entry{Ticket: Ticket{Exclusive: true}}, false},
		{"torn payload claims exclusive", Entry{Holding: true, PayloadError: "parse: unexpected end of JSON input", Ticket: Ticket{Exclusive: true}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.e.ExclusiveHolder(); got != c.want {
				t.Fatalf("ExclusiveHolder() = %v, want %v", got, c.want)
			}
			if got := HasExclusiveHolder([]Entry{c.e}); got != c.want {
				t.Fatalf("HasExclusiveHolder() = %v, want %v", got, c.want)
			}
		})
	}
}
