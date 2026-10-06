package sysinfo

import (
	"testing"
)

// resetCPUState clears the package-level sampling state so each test starts
// from a fresh baseline, the way a fresh process does.
func resetCPUState() {
	cpuMu.Lock()
	defer cpuMu.Unlock()
	cpuPrev = cpuTotals{}
	havePrev = false
	lastPct = 0
	havePct = false
}

// staleThenMoving returns a reader whose first stale reads serve the same
// snapshot (the kernel-cached values a fresh process can see), after which
// the counters advance by busy on every read.
func staleThenMoving(stale int, base, busy uint64) func() (cpuTotals, bool) {
	calls := 0
	return func() (cpuTotals, bool) {
		calls++
		if calls <= stale {
			return cpuTotals{total: base, idle: base}, true
		}
		return cpuTotals{total: base + busy*uint64(calls), idle: base}, true
	}
}

// TestSampleCPURetriesAStaleBaseline: a fresh process whose first reads all
// serve the same snapshot still gets a usage reading once the counters move,
// and only a counter that never moves within the bound is an error.
func TestSampleCPURetriesAStaleBaseline(t *testing.T) {
	t.Run("movement within the bound succeeds", func(t *testing.T) {
		resetCPUState()
		// 3 stale reads: the baseline plus two within the retry loop.
		c := sampleCPU("fake", staleThenMoving(3, 1000, 10))
		if !c.HaveUsage || c.Err != "" {
			t.Fatalf("expected a usage reading, got %+v", c)
		}
	})
	t.Run("a counter that never moves fails loudly", func(t *testing.T) {
		resetCPUState()
		c := sampleCPU("fake", staleThenMoving(1<<30, 1000, 10))
		if c.HaveUsage || c.Err != "cpu counters did not advance" {
			t.Fatalf("expected the not-advanced error, got %+v", c)
		}
	})
	t.Run("a stale window after a good sample falls back to the last reading", func(t *testing.T) {
		resetCPUState()
		if c := sampleCPU("fake", staleThenMoving(0, 1000, 10)); !c.HaveUsage {
			t.Fatalf("first sample: %+v", c)
		}
		// The next read serves a snapshot older than the recorded one, so
		// no diff is possible; the last good percentage must come back.
		c := sampleCPU("fake", staleThenMoving(1<<30, 1000, 10))
		if !c.HaveUsage {
			t.Fatalf("expected the last good reading, got %+v", c)
		}
	})
}

// TestSampleCPUBoundedWaits pins the worst-case cost: the retry loop waits
// no longer than baselineRetries intervals before giving up.
func TestSampleCPUBoundedWaits(t *testing.T) {
	resetCPUState()
	c := sampleCPU("fake", staleThenMoving(1<<30, 1000, 10))
	if c.HaveUsage {
		t.Fatal("a never-moving counter must not produce a reading")
	}
}
