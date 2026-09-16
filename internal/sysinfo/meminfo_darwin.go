//go:build darwin

package sysinfo

import (
	"encoding/binary"

	"golang.org/x/sys/unix"
)

// readMemory uses hw.memsize + vm.swapusage for totals/swap and mach
// host_statistics(HOST_VM_INFO) for available physical memory.
func readMemory() Memory {
	m := Memory{Source: "mach HOST_VM_INFO + sysctl"}
	if total, err := unix.SysctlUint64("hw.memsize"); err == nil {
		m.TotalBytes, m.HaveTotal = total, true
	} else {
		m.Err = err.Error()
	}
	if avail, ok := darwinAvailableBytes(); ok {
		m.AvailableBytes, m.HaveAvailable = avail, true
	} else if m.Err == "" {
		m.Err = "mach host_statistics(HOST_VM_INFO) failed"
	}
	// struct xsw_usage { uint64 total; uint64 avail; uint64 used; ... }
	if raw, err := unix.SysctlRaw("vm.swapusage"); err == nil && len(raw) >= 24 {
		m.SwapUsedBytes = binary.LittleEndian.Uint64(raw[16:24])
		m.HaveSwap = true
	}
	return m
}
