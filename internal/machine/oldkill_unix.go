//go:build !windows

package machine

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"time"

	"golang.org/x/sys/unix"

	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/textsafe"
)

// Seams for tests; production never changes them.
var (
	signalFn = unix.Kill
	listFn   = procinfo.List
	lookupFn = procinfo.Lookup
	// walkSettle bounds how long the descendant walk waits for every
	// stopped process to show the stopped state.
	walkSettle = 5 * time.Second
)

// KillOldHolder force-ends an old-layout holder and its whole job (spec
// 3.2, steps 0 to 5): refuse an ancestor, or a parent chain that cannot be
// verified; SIGSTOP the old incoda; re-check that it still holds its
// ticket; walk and SIGSTOP its descendants until the walk is stable; write
// the orphan record; SIGKILL the recorded groups, the recorded descendants
// and the old incoda (start times re-checked); then wait until the tree is
// empty and the ticket free, and delete the record. From the first SIGSTOP
// to the last SIGKILL SIGINT, SIGTERM and SIGHUP are ignored, no registry
// lock is taken, and every abort path resumes everything it stopped.
func KillOldHolder(stateDir string, t KillTarget, chain procinfo.Chain) (OldKillResult, error) {
	pid := t.PID
	if chain.Contains(pid) {
		return OldKillResult{}, ancestorRefusal(pid)
	}
	if reason := chainIncomplete(chain); reason != "" {
		return OldKillResult{}, chainRefusal(reason)
	}
	self := os.Getpid()
	exclude := map[int]bool{self: true}
	for _, a := range chain.PIDs {
		exclude[a] = true
	}
	// The old incoda's start time, so its SIGKILL in step 5 cannot reach a
	// new process that reused its pid.
	rootProc, err := lookupFn(pid)
	if errors.Is(err, procinfo.ErrNoProcess) {
		return OldKillResult{}, noLongerHolds(pid, t.Key)
	}
	if err != nil {
		return OldKillResult{}, listingRefusal(pid, err)
	}
	crashpoint("kill-before-stop")

	// The protected window. Signals that would end kill are taken off the
	// default action (delivered to a channel nobody reads) until the old
	// incoda has been sent SIGKILL.
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, unix.SIGINT, unix.SIGTERM, unix.SIGHUP)
	windowOpen := true
	closeWindow := func() {
		if windowOpen {
			signal.Stop(sigs)
			windowOpen = false
		}
	}
	defer closeWindow()
	// Every return before step 5, a panic included, resumes everything
	// stopped so far. Deferred after closeWindow, so it runs first, inside
	// the window.
	var stopped []int
	killing := false
	defer func() {
		if !killing {
			for _, p := range stopped {
				_ = signalFn(p, unix.SIGCONT)
			}
		}
	}()

	// Step 1. A stopped old incoda cannot start or reap a child.
	if err := signalFn(pid, unix.SIGSTOP); errors.Is(err, unix.ESRCH) {
		return OldKillResult{}, noLongerHolds(pid, t.Key)
	} else if err != nil {
		return OldKillResult{}, stopRefusal(pid, err)
	}
	stopped = append(stopped, pid)
	// Step 2.
	held, err := ticketHeldFn(t)
	if err != nil {
		return OldKillResult{}, recheckFailed(pid, err)
	}
	if !held {
		return OldKillResult{}, noLongerHolds(pid, t.Key)
	}
	// Step 3.
	desc, groups, err := walkTree(pid, exclude, &stopped)
	if err != nil {
		var se *stopError
		switch {
		case errors.Is(err, errRootAncestor):
			return OldKillResult{}, ancestorRefusal(pid)
		case errors.Is(err, errRootGone):
			return OldKillResult{}, noLongerHolds(pid, t.Key)
		case errors.Is(err, errKeptChanging):
			return OldKillResult{}, keptChanging(pid)
		case errors.As(err, &se):
			return OldKillResult{}, stopRefusal(se.pid, se.err)
		}
		return OldKillResult{}, listingRefusal(pid, err)
	}
	crashpoint("kill-stopped")
	// Step 4. Older binaries never read orphans/.
	rec := &Orphan{Key: t.Key, PID: pid, Command: t.Command, Descendants: desc, Groups: groups, ByPID: self}
	path, err := writeOrphanFn(stateDir, rec)
	if err != nil {
		return OldKillResult{}, recordFailed(pid, err)
	}
	// Step 5. The job is frozen: a graceful SIGTERM would buy nothing, and
	// a resumed job could fork outside the walk. Every pid is re-checked by
	// start time first; a group whose leader is gone is still signalled
	// (its id stays reserved while it has members).
	killing = true
	starts := make(map[int]uint64, len(desc))
	for _, d := range desc {
		starts[d.PID] = d.Start
	}
	for _, g := range groups {
		p, err := lookupFn(g)
		if errors.Is(err, procinfo.ErrNoProcess) || (err == nil && p.Start == starts[g]) {
			_ = signalFn(-g, unix.SIGKILL)
		}
	}
	for _, d := range desc {
		if p, err := lookupFn(d.PID); err == nil && p.Start == d.Start {
			_ = signalFn(d.PID, unix.SIGKILL)
		}
	}
	if p, err := lookupFn(pid); err == nil && p.Start == rootProc.Start {
		_ = signalFn(pid, unix.SIGKILL)
	}
	closeWindow()

	if ok, left := waitTreeGone(t, rec); !ok {
		return OldKillResult{}, treeNotGone(t, left)
	}
	_ = os.Remove(path)
	return OldKillResult{Processes: len(desc)}, nil
}

// chainIncomplete says why chain cannot be trusted to name every ancestor
// of kill, or "" when it can: a walk that reached pid 1 without an error.
func chainIncomplete(c procinfo.Chain) string {
	switch {
	case c.Err != nil:
		return textsafe.Escape(c.Err.Error())
	case c.Skip || len(c.PIDs) == 0:
		return "no parent chain"
	case c.PIDs[len(c.PIDs)-1] != 1:
		return fmt.Sprintf("the parent chain stops at pid %d", c.PIDs[len(c.PIDs)-1])
	}
	return ""
}

// Walk outcomes that abort the kill.
var (
	errRootAncestor = errors.New("the old incoda is an ancestor of this process")
	errRootGone     = errors.New("the old incoda left the process table")
	errKeptChanging = errors.New("the job kept changing")
)

// stopError is a SIGSTOP that failed for a reason other than ESRCH.
type stopError struct {
	pid int
	err error
}

func (e *stopError) Error() string { return fmt.Sprintf("SIGSTOP %d: %v", e.pid, e.err) }

// walkTree is step 3: list every process, collect the transitive
// descendants of root by parent pid, SIGSTOP each newly found one (adding
// it to stopped), and list again until a pass finds no new descendant and
// root and every descendant show the stopped state (a stopped process
// cannot fork, so the walk converges). Processes in exclude (kill itself
// and its ancestors) are never walked into and never signalled; each pass
// also excludes every ancestor of this process that the listing shows, and
// fails with errRootAncestor if root is one of them. A descendant that
// never shows the stopped state (uninterruptible sleep) ends the wait
// after walkSettle; what was found is recorded. A pass that still finds
// new descendants after walkSettle fails with errKeptChanging; a SIGSTOP
// that fails other than with ESRCH fails with a *stopError (a pid that has
// already gone is neither stopped nor recorded); root leaving the listing
// fails with errRootGone.
//
// It returns the descendants with their start times, and every process
// group whose leader is a descendant and which contains no excluded
// process.
func walkTree(root int, exclude map[int]bool, stopped *[]int) ([]OrphanProc, []int, error) {
	excl := make(map[int]bool, len(exclude))
	for p := range exclude {
		excl[p] = true
	}
	self := os.Getpid()
	known := map[int]procinfo.Proc{}
	var order []int
	var last map[int]procinfo.Proc
	deadline := time.Now().Add(walkSettle)
	for {
		ps, err := listFn()
		if err != nil {
			return nil, nil, err
		}
		byPID := make(map[int]procinfo.Proc, len(ps))
		children := map[int][]procinfo.Proc{}
		for _, p := range ps {
			byPID[p.PID] = p
			children[p.PPID] = append(children[p.PPID], p)
		}
		last = byPID
		if r, ok := byPID[root]; !ok || r.State == 'Z' {
			return nil, nil, errRootGone
		}
		// Kill's ancestry as this listing shows it, whatever the parent
		// chain said.
		seenUp := map[int]bool{}
		for cur, ok := byPID[self]; ok && cur.PPID > 1 && !seenUp[cur.PPID]; cur, ok = byPID[cur.PPID] {
			seenUp[cur.PPID] = true
			if cur.PPID == root {
				return nil, nil, errRootAncestor
			}
			excl[cur.PPID] = true
		}
		settled := byPID[root].State == 'T'
		fresh := 0
		queue := []int{root}
		seen := map[int]bool{root: true}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, c := range children[cur] {
				if seen[c.PID] || excl[c.PID] || c.State == 'Z' {
					continue
				}
				seen[c.PID] = true
				queue = append(queue, c.PID)
				if _, ok := known[c.PID]; !ok {
					err := signalFn(c.PID, unix.SIGSTOP)
					if errors.Is(err, unix.ESRCH) {
						continue
					}
					if err != nil {
						return nil, nil, &stopError{pid: c.PID, err: err}
					}
					known[c.PID] = c
					order = append(order, c.PID)
					*stopped = append(*stopped, c.PID)
					fresh++
				}
				if c.State != 'T' {
					settled = false
				}
			}
		}
		pastDeadline := time.Now().After(deadline)
		if fresh == 0 && (settled || pastDeadline) {
			break
		}
		if pastDeadline {
			return nil, nil, errKeptChanging
		}
		time.Sleep(5 * time.Millisecond)
	}
	// A group that holds kill or one of its ancestors is never signalled
	// as a group: its members that descend from root are killed one by one.
	forbidden := map[int]bool{}
	for p := range excl {
		if pr, ok := last[p]; ok {
			forbidden[pr.PGID] = true
		}
	}
	var desc []OrphanProc
	var groups []int
	for _, p := range order {
		d := known[p]
		desc = append(desc, OrphanProc{PID: d.PID, Start: d.Start})
		if d.PGID == d.PID && !forbidden[d.PGID] {
			groups = append(groups, d.PGID)
		}
	}
	return desc, groups, nil
}
