//go:build darwin

package sysinfo

import (
	"strings"
	"testing"
	"time"
	"unsafe"
)

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

func TestVMStatisticsMatchesHostVMInfoCount(t *testing.T) {
	const want = hostVMInfoCount * 4
	if got := int(unsafe.Sizeof(vmStatistics{})); got != want {
		t.Fatalf("sizeof(vmStatistics) = %d, want %d (HOST_VM_INFO_COUNT natural_t fields)", got, want)
	}
}

func TestReadMemoryDarwinNoUnavailableFallback(t *testing.T) {
	mem := ReadMemory()
	if !mem.HaveAvailable {
		t.Fatalf("expected available memory, got %+v", mem)
	}
	if strings.Contains(mem.String(), "free unavailable") {
		t.Fatalf("memory line fell back to sysctl-only text: %q", mem.String())
	}
}

func TestDarwinCPUTotalsIdleAdvances(t *testing.T) {
	// Mach may not bump HOST_CPU_LOAD_INFO on every rapid poll; wait for movement.
	time.Sleep(200 * time.Millisecond)
	a, ok := darwinCPUTotals()
	if !ok {
		t.Fatal("first mach cpu sample failed")
	}
	deadline := time.Now().Add(3 * time.Second)
	var b cpuTotals
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		b, ok = darwinCPUTotals()
		if !ok {
			t.Fatal("mach cpu sample failed")
		}
		if b.total > a.total {
			goto check
		}
	}
	t.Fatal("cpu counters did not advance within 3s")

check:
	total := b.total - a.total
	idle := b.idle - a.idle
	if idle == 0 {
		t.Fatal("idle counter at index 2 did not advance; wrong HOST_CPU_LOAD_INFO layout?")
	}
	if idle > total {
		t.Fatalf("idle delta (%d) > total delta (%d)", idle, total)
	}
}
