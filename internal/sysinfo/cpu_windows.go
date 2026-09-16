//go:build windows

package sysinfo

import (
	"time"

	"golang.org/x/sys/windows"
)

func readCPU() CPU {
	c := CPU{Source: "GetSystemTimes"}
	a, ok := systemTimes()
	if !ok {
		c.Err = "GetSystemTimes failed"
		return c
	}
	time.Sleep(sampleInterval)
	b, ok := systemTimes()
	if !ok {
		c.Err = "GetSystemTimes failed"
		return c
	}
	if pct, ok := cpuUsagePct(a, b); ok {
		c.UsagePct, c.HaveUsage = pct, true
	} else {
		c.Err = "cpu counters did not advance"
	}
	return c
}

type cpuTotals struct {
	total uint64
	idle  uint64
}

func systemTimes() (cpuTotals, bool) {
	var idle, kernel, user windows.Filetime
	if err := windows.GetSystemTimes(&idle, &kernel, &user); err != nil {
		return cpuTotals{}, false
	}
	idleTicks := filetimeToUint64(&idle)
	kernelTicks := filetimeToUint64(&kernel)
	userTicks := filetimeToUint64(&user)
	return cpuTotals{
		total: kernelTicks + userTicks,
		idle:  idleTicks,
	}, true
}

func filetimeToUint64(ft *windows.Filetime) uint64 {
	return uint64(ft.HighDateTime)<<32 + uint64(ft.LowDateTime)
}

func cpuUsagePct(a, b cpuTotals) (float64, bool) {
	total := b.total - a.total
	idle := b.idle - a.idle
	if total == 0 || idle > total {
		return 0, false
	}
	return 100 * float64(total-idle) / float64(total), true
}
