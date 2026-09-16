//go:build darwin

package sysinfo

import (
	"encoding/binary"

	"golang.org/x/sys/unix"
)

func readCPU() CPU {
	return sampleCPU("sysctl kern.cp_time", cpTimeTotals)
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
