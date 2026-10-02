package machine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/lockfile"
	"github.com/deblasis/incoda/internal/textsafe"
)

// OldKillResult is what an old-holder kill ended.
type OldKillResult struct {
	// Processes is how many descendants of the old incoda were recorded
	// and ended (Unix; zero on Windows, where the job object ends them).
	Processes int
}

// Seams for tests; production never changes them.
var (
	writeOrphanFn = writeOrphan
	// treeGoneWait bounds the wait, after the SIGKILLs, for the recorded
	// tree to empty and the ticket to free.
	treeGoneWait = 10 * time.Second
	// ticketHeldFn is step 2's re-check and the final wait's ticket probe.
	ticketHeldFn = ticketHeld
)

// ticketHeld is step 2 of the old-holder kill: TryLock of the ticket file
// alone, no registry lock. Still locked means the old incoda still holds
// it (ticket descriptors are close-on-exec, so no child inherited it); a
// missing or lockable ticket means it does not.
func ticketHeld(t KillTarget) (bool, error) {
	f, err := lockfile.OpenExisting(filepath.Join(t.Dir, t.Ticket))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	ok, err := f.TryLock()
	if err != nil {
		return false, err
	}
	return !ok, nil
}

// waitTreeGone waits, up to treeGoneWait, until the recorded tree is empty
// and the old incoda's ticket is free. It reports the pids still running
// when it gives up.
func waitTreeGone(t KillTarget, rec *Orphan) (bool, string) {
	deadline := time.Now().Add(treeGoneWait)
	for {
		held, err := ticketHeldFn(t)
		if err == nil && !held && !rec.Live() {
			return true, ""
		}
		if time.Now().After(deadline) {
			var left []string
			if held || err != nil {
				left = append(left, fmt.Sprintf("%d (the older incoda)", t.PID))
			}
			if rec.Live() {
				left = append(left, rec.pidList())
			}
			return false, strings.Join(left, ", ")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func ancestorRefusal(pid int) *Refusal {
	return &Refusal{Msg: fmt.Sprintf("kill: pid %d is an ancestor of this process; run kill from outside its job", pid)}
}

func noLongerHolds(pid int, key string) *Refusal {
	return &Refusal{Msg: fmt.Sprintf("kill: pid %d no longer holds %q", pid, key)}
}

func listingRefusal(pid int, err error) *Refusal {
	return &Refusal{Msg: fmt.Sprintf("kill: cannot list the job of older incoda pid %d (%s); stop its job by hand, then rerun", pid, textsafe.Escape(err.Error()))}
}

func recordFailed(pid int, err error) *StateError {
	return &StateError{Msg: fmt.Sprintf("kill: cannot write the orphan record for older incoda pid %d (%s); nothing was terminated and its job was resumed", pid, textsafe.Escape(err.Error()))}
}

func treeNotGone(t KillTarget, left string) *StateError {
	return &StateError{Msg: fmt.Sprintf("kill: older incoda pid %d was sent SIGKILL but %s still run; the orphan record keeps %q busy until they exit", t.PID, left, t.Key)}
}
