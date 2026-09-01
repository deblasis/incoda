//go:build darwin

package sysinfo

import (
	"encoding/binary"

	"golang.org/x/sys/unix"
)

// readMemory carries over exactly the gauge build-lane used on the Mac
// (vm.swapusage) plus hw.memsize, and is honest about the rest: free physical
// memory needs a mach host_statistics64 call, which needs cgo, and this binary
// stays pure Go. HaveAvailable therefore stays false on darwin and the renderer
// says so rather than printing zero.
func readMemory() Memory {
	m := Memory{Source: "sysctl hw.memsize + vm.swapusage"}
	if total, err := unix.SysctlUint64("hw.memsize"); err == nil {
		m.TotalBytes, m.HaveTotal = total, true
	} else {
		m.Err = err.Error()
	}
	// struct xsw_usage { uint64 total; uint64 avail; uint64 used; ... }
	if raw, err := unix.SysctlRaw("vm.swapusage"); err == nil && len(raw) >= 24 {
		m.SwapUsedBytes = binary.LittleEndian.Uint64(raw[16:24])
		m.HaveSwap = true
	}
	return m
}
