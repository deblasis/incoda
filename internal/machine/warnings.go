package machine

import (
	"fmt"

	"github.com/deblasis/incoda/internal/procinfo"
)

// ResumeReason is the --reason of the rerun line for a stopped holder.
const ResumeReason = "resume interrupted kill"

// FenceMissingLine is plain status's warning for a migrated layout whose
// fence is gone (spec 5.3).
const FenceMissingLine = "fence missing: the next run re-places it (incoda doctor)"

// StoppedLines are the lines status and doctor print for a live holder in
// the stopped state (spec 3.2): an old-holder kill was interrupted inside
// its window. Rerunning the same kill --force is safe (a SIGSTOP of a
// stopped process does nothing); kill -CONT resumes the holder instead.
func StoppedLines(key string, pid int) []string {
	return []string{
		fmt.Sprintf("stopped holder: pid %d; a kill was interrupted; rerun: %s", pid, KillLine(key, pid, ResumeReason, true)),
		fmt.Sprintf("  or resume it instead: kill -CONT %d", pid),
	}
}

// Holder names one live ticket for the stopped-state check.
type Holder struct {
	Key string
	PID int
}

// StatusWarnings is the warning block at the end of plain status (spec
// 5.3): every live unpooled holder, a missing fence on a migrated layout,
// and every holder among holders (plus the unpooled ones) that is in the
// stopped state. It only reads: no lock, no cleanup.
func StatusWarnings(stateDir string, v View, holders []Holder) []string {
	var lines []string
	if v.Migrated {
		us, err := ScanUnpooled(stateDir, false)
		if err != nil {
			lines = append(lines, fmt.Sprintf("cannot scan for unpooled runs: %s", esc(err)))
		}
		for _, u := range us {
			lines = append(lines, u.Line())
			if u.Where != "orphans" {
				holders = append(holders, Holder{Key: u.Key, PID: u.PID})
			}
		}
		if v.FenceMissing {
			lines = append(lines, FenceMissingLine)
		}
	}
	seen := map[int]bool{}
	for _, h := range holders {
		if seen[h.PID] {
			continue
		}
		seen[h.PID] = true
		if s, err := procinfo.Stopped(h.PID); err == nil && s {
			lines = append(lines, StoppedLines(h.Key, h.PID)...)
		}
	}
	return lines
}
