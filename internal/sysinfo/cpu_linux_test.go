//go:build linux

package sysinfo

import "testing"

func TestParseProcStatCPU(t *testing.T) {
	a, ok := parseProcStatCPU("cpu  4705 0 3843 124389 0 0 0 0 0 0")
	if !ok {
		t.Fatal("expected aggregate cpu line to parse")
	}
	b, ok := parseProcStatCPU("cpu  4705 0 3843 134389 0 0 0 0 0 0")
	if !ok {
		t.Fatal("expected second sample to parse")
	}
	pct, ok := cpuUsagePct(a, b)
	if !ok {
		t.Fatal("expected usage between two samples")
	}
	if pct <= 0 || pct >= 100 {
		t.Fatalf("usage out of range: %v", pct)
	}
}

func TestParseProcStatCPURejectsGarbage(t *testing.T) {
	if _, ok := parseProcStatCPU("ctxt 123"); ok {
		t.Fatal("non-cpu line should be rejected")
	}
}
