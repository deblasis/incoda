package cli

import (
	"sync"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
)

func TestParseLimit(t *testing.T) {
	ok := []struct {
		in   string
		want time.Duration
	}{
		{"30m", 30 * time.Minute},
		{"90s", 90 * time.Second},
		{"1800", 1800 * time.Second},
		{"0", 0},
		{"0s", 0},
	}
	for _, c := range ok {
		got, err := parseLimit(c.in)
		if err != nil || got != c.want {
			t.Errorf("parseLimit(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "soon", "-5s", "-1", "1x"} {
		if _, err := parseLimit(bad); err == nil {
			t.Errorf("parseLimit(%q) must fail", bad)
		}
	}
}

func TestResolveLimits(t *testing.T) {
	d := func(v time.Duration) *time.Duration { return &v }
	cfg := func(maxRun, stall time.Duration) lane.Config { return lane.Config{MaxRun: maxRun, Stall: stall} }
	cases := []struct {
		name           string
		flagRun, flagS *time.Duration
		cfgs           []lane.Config
		wantRun, wantS time.Duration
	}{
		{"nothing", nil, nil, nil, 0, 0},
		{"lane default applies without a flag", nil, nil, []lane.Config{cfg(time.Hour, 5*time.Minute)}, time.Hour, 5 * time.Minute},
		{"a flag overrides the default", d(time.Minute), d(time.Second), []lane.Config{cfg(time.Hour, 5*time.Minute)}, time.Minute, time.Second},
		{"a longer flag overrides too", d(2 * time.Hour), nil, []lane.Config{cfg(time.Hour, 0)}, 2 * time.Hour, 0},
		{"flag 0 turns the default off", d(0), d(0), []lane.Config{cfg(time.Hour, 5*time.Minute)}, 0, 0},
		{"the tightest lane wins", nil, nil, []lane.Config{cfg(time.Hour, 0), cfg(10*time.Minute, time.Minute), cfg(0, 30*time.Second)}, 10 * time.Minute, 30 * time.Second},
		{"one flag leaves the other on the default", d(time.Minute), nil, []lane.Config{cfg(time.Hour, 5*time.Minute)}, time.Minute, 5 * time.Minute},
	}
	for _, c := range cases {
		run, stall := resolveLimits(c.flagRun, c.flagS, c.cfgs)
		if run != c.wantRun || stall != c.wantS {
			t.Errorf("%s: got (%v, %v), want (%v, %v)", c.name, run, stall, c.wantRun, c.wantS)
		}
	}
}

func TestStallClock(t *testing.T) {
	t0 := time.Unix(1000, 0)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }

	t.Run("idle for the whole window fires", func(t *testing.T) {
		c := newStallClock(10*time.Second, at(0))
		for s := 0; s < 10; s++ {
			if c.Idle(at(s), time.Second, true) {
				t.Fatalf("fired early at %ds", s)
			}
		}
		if !c.Idle(at(10), time.Second, true) {
			t.Fatal("no CPU for the whole window must fire")
		}
	})
	t.Run("progress restarts the window", func(t *testing.T) {
		c := newStallClock(10*time.Second, at(0))
		c.Idle(at(5), time.Second, true)
		if c.Idle(at(9), 2*time.Second, true) { // CPU moved at 9s
			t.Fatal("fired although CPU moved")
		}
		if c.Idle(at(18), 2*time.Second, true) {
			t.Fatal("window restarted at the last progress (9s); 18s is only 9s of idle")
		}
		if !c.Idle(at(19), 2*time.Second, true) {
			t.Fatal("10s since the last progress must fire")
		}
	})
	t.Run("an unreadable counter never fires", func(t *testing.T) {
		c := newStallClock(time.Second, at(0))
		if c.Idle(at(100), 0, false) {
			t.Fatal("no reading is not evidence of a stall")
		}
		// and it does not start a clock that a later reading would inherit
		if c.Idle(at(100), time.Second, true) {
			t.Fatal("the first reading after a gap starts the window")
		}
	})
	t.Run("a counter that goes down is progress", func(t *testing.T) {
		// A member of the tree exited: the sum fell. Something happened.
		c := newStallClock(5*time.Second, at(0))
		c.Idle(at(1), 10*time.Second, true)
		if c.Idle(at(5), 7*time.Second, true) {
			t.Fatal("a changed reading is progress")
		}
	})
}

func TestAbortGateFirstWins(t *testing.T) {
	g := newAbortGate()
	if _, _, ok := g.Fired(); ok {
		t.Fatal("fresh gate must not be fired")
	}
	var wg sync.WaitGroup
	wins := make(chan string, 8)
	for _, name := range []string{"max-run", "stall", "", "x"} {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			if g.Trigger(lane.KillRequest{By: "who", Reason: n}, n) {
				wins <- n
			}
		}(name)
	}
	wg.Wait()
	close(wins)
	if len(wins) != 1 {
		t.Fatalf("exactly one trigger wins, got %d", len(wins))
	}
	select {
	case <-g.Done():
	default:
		t.Fatal("Done must be closed after a trigger")
	}
	req, limit, ok := g.Fired()
	if !ok {
		t.Fatal("Fired must report the trigger")
	}
	if req.Reason != limit {
		t.Fatalf("request and limit come from the same trigger: %+v vs %q", req, limit)
	}
}
