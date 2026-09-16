package sysinfo

import "testing"

func TestMachineLineIncludesCPU(t *testing.T) {
	line := MachineLine(Memory{HaveTotal: true, TotalBytes: 100, HaveAvailable: true, AvailableBytes: 50, Source: "test"}, CPU{UsagePct: 42, HaveUsage: true, Source: "test"})
	if want := "cpu 42%"; !contains(line, want) {
		t.Fatalf("MachineLine() = %q; want substring %q", line, want)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
