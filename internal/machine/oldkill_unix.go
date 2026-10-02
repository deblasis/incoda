//go:build !windows

package machine

import (
	"os"
	"os/signal"
	"time"

	"golang.org/x/sys/unix"

	"github.com/deblasis/incoda/internal/procinfo"
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
// 3.2, steps 0 to 5): refuse an ancestor; SIGSTOP the old incoda; re-check
// that it still holds its ticket; walk and SIGSTOP its descendants until
// the walk is stable; write the orphan record; SIGKILL the recorded groups,
// the recorded descendants (start time re-checked) and the old incoda;
// then wait until the tree is empty and the ticket free, and delete the
// record. From the first SIGSTOP to the last SIGKILL SIGINT, SIGTERM and
// SIGHUP are ignored, no registry lock is taken, and every abort path
// resumes everything it stopped.
func KillOldHolder(stateDir string, t KillTarget, chain procinfo.Chain) (OldKillResult, error) {
	pid := t.PID
	if !chain.Skip && chain.Contains(pid) {
		return OldKillResult{}, ancestorRefusal(pid)
	}
	self := os.Getpid()
	exclude := map[int]bool{self: true}
	for _, a := range chain.PIDs {
		exclude[a] = true
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
	stopped := []int{pid}
	resume := func() {
		for _, p := range stopped {
			_ = signalFn(p, unix.SIGCONT)
		}
	}

	// Step 1. A stopped old incoda cannot start or reap a child.
	_ = signalFn(pid, unix.SIGSTOP)
	// Step 2.
	held, err := ticketHeldFn(t)
	if err != nil {
		resume()
		return OldKillResult{}, stateErrorf("kill: cannot re-check the ticket of pid %d: %s", pid, esc(err))
	}
	if !held {
		resume()
		return OldKillResult{}, noLongerHolds(pid, t.Key)
	}
	// Step 3.
	desc, groups, err := walkTree(pid, exclude, &stopped)
	if err != nil {
		resume()
		return OldKillResult{}, listingRefusal(pid, err)
	}
	crashpoint("kill-stopped")
	// Step 4. Older binaries never read orphans/.
	rec := &Orphan{Key: t.Key, PID: pid, Command: t.Command, Descendants: desc, Groups: groups, ByPID: self}
	path, err := writeOrphanFn(stateDir, rec)
	if err != nil {
		resume()
		return OldKillResult{}, recordFailed(pid, err)
	}
	// Step 5. The job is frozen: a graceful SIGTERM would buy nothing, and
	// a resumed job could fork outside the walk.
	for _, g := range groups {
		_ = signalFn(-g, unix.SIGKILL)
	}
	for _, d := range desc {
		if p, err := lookupFn(d.PID); err == nil && p.Start == d.Start {
			_ = signalFn(d.PID, unix.SIGKILL)
		}
	}
	_ = signalFn(pid, unix.SIGKILL)
	closeWindow()

	if ok, left := waitTreeGone(t, rec); !ok {
		return OldKillResult{}, treeNotGone(t, left)
	}
	_ = os.Remove(path)
	return OldKillResult{Processes: len(desc)}, nil
}

// walkTree is step 3: list every process, collect the transitive
// descendants of root by parent pid, SIGSTOP each newly found one (adding
// it to stopped), and list again until a pass finds no new descendant and
// root and every descendant show the stopped state (a stopped process
// cannot fork, so the walk converges). Processes in exclude (kill itself
// and its ancestors) are never walked into and never signalled. A
// descendant that never shows the stopped state (uninterruptible sleep)
// ends the wait after walkSettle; what was found is recorded.
//
// It returns the descendants with their start times, and every process
// group whose leader is a descendant and which contains no excluded
// process.
func walkTree(root int, exclude map[int]bool, stopped *[]int) ([]OrphanProc, []int, error) {
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
		settled := byPID[root].State == 'T'
		fresh := 0
		queue := []int{root}
		seen := map[int]bool{root: true}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, c := range children[cur] {
				if seen[c.PID] || exclude[c.PID] || c.State == 'Z' {
					continue
				}
				seen[c.PID] = true
				queue = append(queue, c.PID)
				if _, ok := known[c.PID]; !ok {
					known[c.PID] = c
					order = append(order, c.PID)
					_ = signalFn(c.PID, unix.SIGSTOP)
					*stopped = append(*stopped, c.PID)
					fresh++
				}
				if c.State != 'T' {
					settled = false
				}
			}
		}
		if fresh == 0 && (settled || time.Now().After(deadline)) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	// A group that holds kill or one of its ancestors is never signalled
	// as a group: its members that descend from root are killed one by one.
	forbidden := map[int]bool{}
	for p := range exclude {
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
