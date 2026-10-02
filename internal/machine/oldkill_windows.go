//go:build windows

package machine

import (
	"github.com/deblasis/incoda/internal/proc"
	"github.com/deblasis/incoda/internal/procinfo"
)

// killedExit is the exit code a forced kill hands the old incoda, the 124
// of a run killed through the lane.
const killedExit = 124

// KillOldHolder on Windows needs only the termination (spec 3.2): every
// release from v0.1.0 to v0.6.0 starts its child suspended and assigns it
// to a job object with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE before it runs
// (internal/child/child_windows.go, newSupervisor and afterStart), so the
// kernel ends the whole tree when the old incoda dies. No record is
// written.
func KillOldHolder(stateDir string, t KillTarget, chain procinfo.Chain) (OldKillResult, error) {
	if err := proc.Terminate(t.PID, killedExit); err != nil {
		return OldKillResult{}, stateErrorf("kill: cannot terminate older incoda pid %d: %s", t.PID, esc(err))
	}
	rec := &Orphan{Key: t.Key, PID: t.PID}
	if ok, left := waitTreeGone(t, rec); !ok {
		return OldKillResult{}, treeNotGone(t, left)
	}
	return OldKillResult{}, nil
}
