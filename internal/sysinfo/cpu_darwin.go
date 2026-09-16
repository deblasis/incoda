//go:build darwin

package sysinfo

import (
	"encoding/binary"
	"time"

	"golang.org/x/sys/unix"
)

func readCPU() CPU {
	c := CPU{Source: "sysctl kern.cp_time"}
	a, ok := cpTimeTotals()
	if !ok {
		c.Err = "could not read kern.cp_time"
		return c
	}
	time.Sleep(sampleInterval)
	b, ok := cpTimeTotals()
	if !ok {
		c.Err = "could not re-read kern.cp_time"
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

// cpTimeTotals reads aggregate CPU ticks from kern.cp_time. The layout is
// {user, nice, sys, idle, ...}; only idle is treated as non-busy.
func cpTimeTotals() (cpuTotals, bool) {
	raw, err := unix.SysctlRaw("kern.cp_time")
	if err != nil || len(raw) < 16 {
		return cpuTotals{}, false
	}
	vals := cpTimeValues(raw)
	if len(vals) < 4 {
		return cpuTotals{}, false
	}
	var total uint64
	for _, v := range vals {
		total += v
	}
	return cpuTotals{total: total, idle: vals[3]}, true
}

func cpTimeValues(raw []byte) []uint64 {
	switch len(raw) {
	case 16, 20, 24, 32, 40:
		if len(raw)%4 == 0 && len(raw) < 32 {
			out := make([]uint64, 0, len(raw)/4)
			for i := 0; i+4 <= len(raw); i += 4 {
				out = append(out, uint64(binary.LittleEndian.Uint32(raw[i:i+4])))
			}
			return out
		}
		out := make([]uint64, 0, len(raw)/8)
		for i := 0; i+8 <= len(raw); i += 8 {
			out = append(out, binary.LittleEndian.Uint64(raw[i:i+8]))
		}
		return out
	default:
		return nil
	}
}

func cpuUsagePct(a, b cpuTotals) (float64, bool) {
	total := b.total - a.total
	idle := b.idle - a.idle
	if total == 0 || idle > total {
		return 0, false
	}
	return 100 * float64(total-idle) / float64(total), true
}
