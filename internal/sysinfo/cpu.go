package sysinfo

import "time"

// sampleInterval is long enough for /proc/stat (and its cousins) to move,
// short enough that status and watch do not feel sluggish.
const sampleInterval = 100 * time.Millisecond

// CPU is a best-effort whole-machine utilization readout.
type CPU struct {
	UsagePct  float64 `json:"usage_pct,omitempty"`
	HaveUsage bool    `json:"have_usage"`
	Source    string  `json:"source"`
	Err       string  `json:"error,omitempty"`
}

// ReadCPU returns current CPU utilization as a percentage of all cores.
func ReadCPU() CPU { return readCPU() }
