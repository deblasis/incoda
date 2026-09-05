// Package sysinfo provides the portable slice of the machine-pressure readout
// that build-lane got from `sysctl vm.swapusage`.
//
// The Mac swap gauge does not generalise, so it is not pretended at: each field
// is explicitly optional and the renderer says "unavailable" rather than
// printing a confident zero.
package sysinfo

import "fmt"

// Memory is a best-effort physical-memory readout. Any field may be absent.
type Memory struct {
	TotalBytes     uint64 `json:"total_bytes,omitempty"`
	AvailableBytes uint64 `json:"available_bytes,omitempty"`
	// SwapUsedBytes is populated only where the OS exposes it cheaply
	// (Windows commit charge, macOS vm.swapusage, Linux SwapTotal-SwapFree).
	SwapUsedBytes uint64 `json:"swap_used_bytes,omitempty"`
	HaveTotal     bool   `json:"have_total"`
	HaveAvailable bool   `json:"have_available"`
	HaveSwap      bool   `json:"have_swap"`
	Source        string `json:"source"`
	Err           string `json:"error,omitempty"`
}

// ReadMemory returns the current readout for this platform.
func ReadMemory() Memory { return readMemory() }

// String renders a single human-readable line.
func (m Memory) String() string {
	if m.Err != "" {
		return "memory: unavailable (" + m.Err + ")"
	}
	out := "memory: "
	switch {
	case m.HaveTotal && m.HaveAvailable:
		used := m.TotalBytes - m.AvailableBytes
		pct := 0.0
		if m.TotalBytes > 0 {
			pct = 100 * float64(used) / float64(m.TotalBytes)
		}
		out += fmt.Sprintf("%s free of %s (%.0f%% used)", Human(m.AvailableBytes), Human(m.TotalBytes), pct)
	case m.HaveTotal:
		out += fmt.Sprintf("%s total, free unavailable on %s", Human(m.TotalBytes), m.Source)
	default:
		out += "unavailable on " + m.Source
	}
	if m.HaveSwap {
		out += fmt.Sprintf("; swap used %s", Human(m.SwapUsedBytes))
	}
	return out
}

// Human renders a byte count the way the memory line does ("3.2 GB"), so the
// per-job peaks in lane.log read in the same units as the machine gauge.
func Human(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTP"[exp])
}
