package sysinfo

import "testing"

func TestCPUUsagePct(t *testing.T) {
	a := cpuTotals{total: 1000, idle: 800}
	b := cpuTotals{total: 2000, idle: 1550} // 25% busy over the interval
	pct, ok := cpuUsagePct(a, b)
	if !ok {
		t.Fatal("expected usage between two samples")
	}
	if pct != 25 {
		t.Fatalf("usage = %v, want 25", pct)
	}
}

func TestCPUUsagePctRejectsBadDelta(t *testing.T) {
	a := cpuTotals{total: 1000, idle: 500}
	b := cpuTotals{total: 1000, idle: 600} // idle grew more than total
	if _, ok := cpuUsagePct(a, b); ok {
		t.Fatal("expected reject when idle delta exceeds total delta")
	}
}
