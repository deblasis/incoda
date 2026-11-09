package cli

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatchLimitsWarnsOnceWhenCPUIsUnreadable(t *testing.T) {
	g := newAbortGate()
	stop := make(chan struct{})
	var mu sync.Mutex
	var warned []string
	warn := func(s string) { mu.Lock(); warned = append(warned, s); mu.Unlock() }
	done := watchLimits(0, 200*time.Millisecond, func() (time.Duration, bool) { return 0, false },
		5*time.Millisecond, time.Now(), g, stop, warn)
	time.Sleep(900 * time.Millisecond)
	close(stop)
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(warned) != 1 {
		t.Fatalf("an unreadable counter warns exactly once, got %q", warned)
	}
	if _, _, fired := g.Fired(); fired {
		t.Fatal("an unreadable counter must never kill")
	}
}

func TestWatchLimitsFiresOnceTheCounterRecovers(t *testing.T) {
	g := newAbortGate()
	stop := make(chan struct{})
	defer close(stop)
	start := time.Now()
	var warns atomic.Int32
	cpu := func() (time.Duration, bool) {
		if time.Since(start) < 300*time.Millisecond {
			return 0, false
		}
		return 5 * time.Second, true // readable, and never moving
	}
	watchLimits(0, 200*time.Millisecond, cpu, 5*time.Millisecond, start, g, stop, func(string) { warns.Add(1) })
	select {
	case <-g.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("a recovered counter that stays flat is a stall")
	}
	if _, limit, _ := g.Fired(); limit != limitStall {
		t.Fatalf("limit = %q", limit)
	}
	if warns.Load() > 1 {
		t.Fatalf("at most one warning, got %d", warns.Load())
	}
}

func TestArmStall(t *testing.T) {
	live := func() error { return nil }
	dead := func() error { return errors.New("the counter did not move") }
	cases := []struct {
		name      string
		stall     time.Duration
		goos      string
		calibrate func() error
		want      time.Duration
		warns     string
	}{
		{"no stall asks for nothing", 0, "linux", dead, 0, ""},
		{"supported and live", time.Minute, "linux", live, time.Minute, ""},
		{"unsupported platform", time.Minute, "plan9", live, 0, "plan9"},
		{"a dead counter never arms", time.Minute, "windows", dead, 0, "did not move"},
	}
	for _, c := range cases {
		got, warn := armStall(c.stall, c.goos, c.calibrate)
		if got != c.want {
			t.Errorf("%s: stall = %v, want %v", c.name, got, c.want)
		}
		if (c.warns == "") != (warn == "") || !strings.Contains(warn, c.warns) {
			t.Errorf("%s: warning %q, want one containing %q", c.name, warn, c.warns)
		}
	}
}

func TestCalibrationIsNotRunWhenStallIsOff(t *testing.T) {
	called := false
	armStall(0, "linux", func() error { called = true; return nil })
	if called {
		t.Fatal("no stall, no calibration spawn")
	}
}

func TestStallFloor(t *testing.T) {
	if stallFloorError(500*time.Millisecond) == nil {
		t.Error("a --stall under one second cannot be sampled and is refused")
	}
	if stallFloorError(0) != nil || stallFloorError(time.Second) != nil {
		t.Error("zero and one second are accepted")
	}
	if w := stallWarning(10 * time.Second); !strings.Contains(w, "under 30s") {
		t.Errorf("a short stall warns, got %q", w)
	}
	if stallWarning(30*time.Second) != "" || stallWarning(0) != "" {
		t.Error("30s and zero do not warn")
	}
}
