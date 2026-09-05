package lane

import (
	"time"

	"github.com/deblasis/incoda/internal/sysinfo"
)

// Stats is what a finished job actually cost the machine. It is recorded on
// the release line of lane.log so that a queue's slot count can be argued
// from measured peaks instead of guessed: "two zig builds fit" is a claim
// this log can either back or refute.
//
// Each field is optional on purpose. Windows accounts for the whole job tree
// through the Job Object; Unix only sees the direct child through rusage, and
// a platform with neither reports nothing rather than a confident zero.
type Stats struct {
	// PeakBytes is the peak committed memory of the job tree (Windows) or
	// the direct child's maximum resident set (Unix).
	PeakBytes uint64
	HavePeak  bool
	// CPU is user plus kernel time of the job tree (Windows) or of the
	// direct child and whatever it waited for (Unix).
	CPU     time.Duration
	HaveCPU bool
}

// logFields renders the stats as " key=value" pairs for a lane.log line, or
// "" when nothing was measured. The leading space keeps the caller's format
// string free of a dangling separator when the stats are absent.
func (s Stats) logFields() string {
	out := ""
	if s.HavePeak {
		out += " peak_mem=" + sysinfo.Human(s.PeakBytes)
	}
	if s.HaveCPU {
		out += " cpu=" + s.CPU.Round(time.Second).String()
	}
	return out
}
