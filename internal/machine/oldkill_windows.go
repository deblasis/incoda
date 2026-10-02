//go:build windows

package machine

import (
	"github.com/deblasis/incoda/internal/procinfo"
)

// KillOldHolder on Windows needs only the termination (spec 3.2): every
// release from v0.1.0 to v0.6.0 starts its child suspended and assigns it
// to a job object with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE before it runs
// (internal/child/child_windows.go, newSupervisor and afterStart), so the
// kernel ends the whole tree when the old incoda dies. The termination is
// pinned to the old incoda by a process handle (killPinned). No record is
// written.
func KillOldHolder(stateDir string, t KillTarget, chain procinfo.Chain) (OldKillResult, error) {
	return killPinned(t)
}
