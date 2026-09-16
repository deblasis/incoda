//go:build darwin

package sysinfo

import "testing"

func TestReadMemoryDarwin(t *testing.T) {
	mem := ReadMemory()
	if !mem.HaveTotal || mem.TotalBytes == 0 {
		t.Fatalf("expected total memory, got %+v", mem)
	}
	if !mem.HaveAvailable {
		t.Fatalf("expected available memory via mach, got %+v", mem)
	}
	if mem.AvailableBytes > mem.TotalBytes {
		t.Fatalf("available (%d) > total (%d)", mem.AvailableBytes, mem.TotalBytes)
	}
}

func TestReadCPUDarwin(t *testing.T) {
	cpu := ReadCPU()
	if !cpu.HaveUsage {
		t.Fatalf("expected cpu usage via mach, got %+v", cpu)
	}
	if cpu.UsagePct < 0 || cpu.UsagePct > 100 {
		t.Fatalf("cpu pct out of range: %v", cpu.UsagePct)
	}
}
