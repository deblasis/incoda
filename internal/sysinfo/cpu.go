package sysinfo

import (
	"sync"
	"time"
)

// sampleInterval is long enough for counters to move on the second read.
const sampleInterval = 100 * time.Millisecond

// CPU is a best-effort whole-machine utilization readout.
type CPU struct {
	UsagePct  float64 `json:"usage_pct,omitempty"`
	HaveUsage bool    `json:"have_usage"`
	Source    string  `json:"source"`
	Err       string  `json:"error,omitempty"`
}

type cpuTotals struct {
	total uint64
	idle  uint64
}

var (
	cpuMu    sync.Mutex
	cpuPrev  cpuTotals
	havePrev bool
	lastPct  float64
	havePct  bool
)

// ReadCPU returns current CPU utilization as a percentage of all cores.
func ReadCPU() CPU { return readCPU() }

func cpuUsagePct(a, b cpuTotals) (float64, bool) {
	total := b.total - a.total
	idle := b.idle - a.idle
	if total == 0 || idle > total {
		return 0, false
	}
	return 100 * float64(total-idle) / float64(total), true
}

// sampleCPU diffs the current counters against the last sample with no sleep.
// The first call sleeps once to establish a baseline.
func sampleCPU(source string, read func() (cpuTotals, bool)) CPU {
	cur, ok := read()
	if !ok {
		return CPU{Source: source, Err: source + " read failed"}
	}

	cpuMu.Lock()
	if havePrev {
		if pct, ok := cpuUsagePct(cpuPrev, cur); ok {
			cpuPrev = cur
			lastPct, havePct = pct, true
			cpuMu.Unlock()
			return CPU{UsagePct: pct, HaveUsage: true, Source: source}
		}
	} else {
		cpuPrev = cur
		havePrev = true
	}
	cpuMu.Unlock()

	time.Sleep(sampleInterval)
	cur, ok = read()
	if !ok {
		return CPU{Source: source, Err: source + " read failed"}
	}

	cpuMu.Lock()
	defer cpuMu.Unlock()
	if pct, ok := cpuUsagePct(cpuPrev, cur); ok {
		cpuPrev = cur
		lastPct, havePct = pct, true
		return CPU{UsagePct: pct, HaveUsage: true, Source: source}
	}
	if havePct {
		return CPU{UsagePct: lastPct, HaveUsage: true, Source: source}
	}
	return CPU{Source: source, Err: "cpu counters did not advance"}
}
