package machine

import (
	"time"

	"github.com/deblasis/incoda/internal/proc"
)

// killedExit is the exit code a forced kill hands the old incoda, the 124
// of a run killed through the lane.
const killedExit = 124

// pinnedProcess is an open handle on the old incoda. On Windows the handle
// pins the process: while it is open the pid cannot name another process,
// so the re-check made after opening it and the termination through it
// address the same incoda.
type pinnedProcess interface {
	Terminate(code int) error
	Wait(d time.Duration) error
	Close() error
}

// openPinnedFn opens the handle; tests replace it.
var openPinnedFn = func(pid int) (pinnedProcess, error) {
	h, err := proc.Open(pid)
	if err != nil {
		return nil, err
	}
	return h, nil
}

// killPinned is the old-holder kill where the kernel ends the whole job
// with the old incoda (Windows, spec 3.2): open a handle on the old incoda
// first, then re-check that its ticket is still held (step 2, TryLock of
// the ticket alone), and only then terminate it through that handle and
// wait for it. A ticket no longer held means the pid may already name
// another process: nothing is terminated (exit 120).
func killPinned(t KillTarget) (OldKillResult, error) {
	h, err := openPinnedFn(t.PID)
	if err != nil {
		if held, herr := ticketHeldFn(t); herr == nil && !held {
			return OldKillResult{}, noLongerHolds(t.PID, t.Key)
		}
		return OldKillResult{}, stateErrorf("kill: cannot open older incoda pid %d: %s", t.PID, esc(err))
	}
	defer h.Close()
	held, err := ticketHeldFn(t)
	if err != nil {
		return OldKillResult{}, recheckFailed(t.PID, err)
	}
	if !held {
		return OldKillResult{}, noLongerHolds(t.PID, t.Key)
	}
	if err := h.Terminate(killedExit); err != nil {
		return OldKillResult{}, stateErrorf("kill: cannot terminate older incoda pid %d: %s", t.PID, esc(err))
	}
	_ = h.Wait(treeGoneWait)
	rec := &Orphan{Key: t.Key, PID: t.PID}
	if ok, left := waitTreeGone(t, rec); !ok {
		return OldKillResult{}, treeNotGone(t, left)
	}
	return OldKillResult{}, nil
}
