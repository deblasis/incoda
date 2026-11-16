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
