//go:build !windows

package machine

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/deblasis/incoda/internal/procinfo"
)

// fakeProcs is a process table for walkTree: SIGSTOP marks a process
// stopped, and a process can appear on a later listing (a fork that raced
// the stop).
type fakeProcs struct {
	mu      sync.Mutex
	procs   map[int]procinfo.Proc
	later   []procinfo.Proc // added on the second listing
	lists   int
	signals []string
	// stopErr fails a SIGSTOP of that pid; onList changes the table
	// before each listing; starts overrides what lookup reports.
	stopErr map[int]error
	onList  func(n int, procs map[int]procinfo.Proc)
	starts  map[int]uint64
}

func (f *fakeProcs) lookup(pid int) (procinfo.Proc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.procs[pid]
	if !ok {
		return procinfo.Proc{}, procinfo.ErrNoProcess
	}
	if st, ok := f.starts[pid]; ok {
		p.Start = st
	}
	return p, nil
}

func (f *fakeProcs) list() ([]procinfo.Proc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	if f.onList != nil {
		f.onList(f.lists, f.procs)
	}
	if f.lists == 2 {
		for _, p := range f.later {
			f.procs[p.PID] = p
		}
	}
	var out []procinfo.Proc
	for _, p := range f.procs {
		out = append(out, p)
	}
	return out, nil
}

func (f *fakeProcs) signal(pid int, sig unix.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.stopErr[pid]; err != nil && sig == unix.SIGSTOP {
		return err
	}
	f.signals = append(f.signals, fmt.Sprintf("%d:%d", pid, sig))
	if sig == unix.SIGKILL {
		delete(f.procs, pid)
		return nil
	}
	if p, ok := f.procs[pid]; ok && sig == unix.SIGSTOP {
		p.State = 'T'
		f.procs[pid] = p
	}
	return nil
}

func TestWalkTreeStopsEveryDescendantUntilStable(t *testing.T) {
	proc := func(pid, ppid, pgid int, st byte) procinfo.Proc {
		return procinfo.Proc{PID: pid, PPID: ppid, PGID: pgid, Start: uint64(pid) * 10, State: st}
	}
	f := &fakeProcs{procs: map[int]procinfo.Proc{
		1:   proc(1, 0, 1, 'R'),
		40:  proc(40, 1, 40, 'R'),     // kill's ancestor
		100: proc(100, 40, 40, 'T'),   // the old incoda, already stopped
		101: proc(101, 100, 101, 'R'), // its child, leading its own group
		60:  proc(60, 100, 60, 'R'),   // a child leading a group kill sits in
		102: proc(102, 101, 101, 'R'),
		50:  proc(50, 101, 60, 'R'),  // kill itself, inside the job's tree
		51:  proc(51, 50, 60, 'R'),   // kill's own child: never walked into
		70:  proc(70, 101, 101, 'Z'), // a zombie: nothing to stop or record
	}, later: []procinfo.Proc{proc(103, 102, 101, 'R')}}
	listFn, signalFn = f.list, f.signal
	defer func() { listFn, signalFn = procinfo.List, unix.Kill }()

	stopped := []int{100}
	desc, groups, err := walkTree(100, map[int]bool{50: true, 40: true}, &stopped)
	if err != nil {
		t.Fatal(err)
	}
	var pids []string
	for _, d := range desc {
		pids = append(pids, fmt.Sprintf("%d@%d", d.PID, d.Start))
	}
	got := strings.Join(pids, " ")
	if got != "101@1010 60@600 102@1020 103@1030" && got != "60@600 101@1010 102@1020 103@1030" {
		t.Fatalf("descendants %s", got)
	}
	if fmt.Sprint(groups) != "[101]" {
		t.Fatalf("groups %v: 101 leads a group of the job; 60 holds kill and must never be signalled as a group", groups)
	}
	for _, s := range f.signals {
		pid := strings.Split(s, ":")[0]
		if pid == "50" || pid == "51" || pid == "40" || pid == "70" {
			t.Fatalf("signalled an excluded process or a zombie: %v", f.signals)
		}
	}
	if len(stopped) != 5 || f.lists < 3 {
		t.Fatalf("stopped %v after %d listings; the walk repeats until a pass finds nothing new", stopped, f.lists)
	}
}

func TestWalkTreeListingFailure(t *testing.T) {
	boom := errors.New("sysctl refused")
	listFn = func() ([]procinfo.Proc, error) { return nil, boom }
	defer func() { listFn = procinfo.List }()
	if _, _, err := walkTree(100, nil, &[]int{}); !errors.Is(err, boom) {
		t.Fatalf("want the listing error, got %v", err)
	}
	if got := listingRefusal(4711, boom).Msg; got != "kill: cannot list the job of older incoda pid 4711 (sysctl refused); stop its job by hand, then rerun" {
		t.Fatalf("refusal text %q", got)
	}
}

func TestKillLineQuotesTheReason(t *testing.T) {
	if got := KillLine("builds", 4711, "incoda upgrade", false); got != "incoda kill --queue builds --pid 4711 --reason 'incoda upgrade'" {
		t.Fatal(got)
	}
	if got := KillLine("k", 1, "it's", true); got != `incoda kill --queue k --pid 1 --reason 'it'\''s' --force` {
		t.Fatal(got)
	}
}

// killSentinel starts a process in the test runner's own process group
// and, at the end of the test, fails unless it is still alive: a test of
// the kill engine must never signal the runner's group or anything outside
// its own tree.
func killSentinel(t *testing.T) {
	t.Helper()
	s := exec.Command("/bin/sleep", "120")
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if p, err := procinfo.Lookup(s.Process.Pid); err != nil || p.State == 'Z' || p.State == 'T' {
			t.Errorf("the sentinel in the test runner's process group (pid %d) was signalled", s.Process.Pid)
		}
		_ = s.Process.Kill()
		_ = s.Wait()
	})
}

// testJob is a stand-in for an older incoda and its job: a shell in a new
// process group of its own (its pid is the group id by construction) that
// starts a sleep and a subshell with a sleep of its own, writes "ready" and
// waits. Cleanup resumes and kills that group only, before Wait reaps the
// shell, so the group id cannot have been reused.
type testJob struct {
	cmd  *exec.Cmd
	root int
}

func startTestJob(t *testing.T) *testJob {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready")
	c := exec.Command("/bin/sh", "-c", `/bin/sleep 60 & /bin/sh -c '/bin/sleep 60 & wait' & echo ok > "$0"; wait`, ready)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	j := &testJob{cmd: c, root: c.Process.Pid}
	t.Cleanup(func() {
		_ = syscall.Kill(-j.root, syscall.SIGCONT)
		_ = syscall.Kill(-j.root, syscall.SIGKILL)
		_ = c.Wait()
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil && len(j.members(t)) >= 4 {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("the test job never started its tree: %v", j.members(t))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// members lists the live (not zombie) processes of the job's group.
func (j *testJob) members(t *testing.T) []procinfo.Proc {
	t.Helper()
	ps, err := procinfo.List()
	if err != nil {
		t.Fatal(err)
	}
	var out []procinfo.Proc
	for _, p := range ps {
		if p.PGID == j.root && p.State != 'Z' {
			out = append(out, p)
		}
	}
	return out
}

// guardSignals routes the kill engine's signals through a guard: a signal
// for anything outside the job's group (by pid or by group) fails the test
// and is never sent. It returns the log of what was sent.
func (j *testJob) guardSignals(t *testing.T) *[]string {
	t.Helper()
	var mu sync.Mutex
	var sent []string
	signalFn = func(pid int, sig unix.Signal) error {
		mu.Lock()
		defer mu.Unlock()
		target := pid
		if pid < 0 {
			target = -pid
			if target != j.root {
				t.Errorf("refused to signal group %d (sig %d): not the test job's group %d", target, sig, j.root)
				return unix.EPERM
			}
		} else if p, err := procinfo.Lookup(pid); err != nil || p.PGID != j.root {
			t.Errorf("refused to signal pid %d (sig %d): not in the test job's group %d", pid, sig, j.root)
			return unix.EPERM
		}
		sent = append(sent, fmt.Sprintf("%d:%d", pid, sig))
		return unix.Kill(pid, sig)
	}
	t.Cleanup(func() { signalFn = unix.Kill })
	return &sent
}

// assertRunning fails unless every member of the job runs, not stopped.
func (j *testJob) assertRunning(t *testing.T, want int) {
	t.Helper()
	ms := j.members(t)
	if len(ms) != want {
		t.Fatalf("job members %v, want %d", ms, want)
	}
	for _, p := range ms {
		if p.State == 'T' {
			t.Fatalf("pid %d was left stopped: every abort path must resume what it stopped", p.PID)
		}
	}
}

func (j *testJob) target(t *testing.T) KillTarget {
	return KillTarget{Kind: TargetOld, Key: "old", Dir: t.TempDir(), Ticket: "x.ticket", PID: j.root, Command: []string{"zig", "build"}}
}

// heldUntilReaped answers step 2 and the final wait as if the job's shell
// held the ticket: held while it runs, free once it is gone or a zombie.
func (j *testJob) heldUntilReaped(KillTarget) (bool, error) {
	p, err := procinfo.Lookup(j.root)
	if errors.Is(err, procinfo.ErrNoProcess) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return p.State != 'Z', nil
}

func countSig(sent []string, sig unix.Signal) int {
	n := 0
	for _, s := range sent {
		if strings.HasSuffix(s, fmt.Sprintf(":%d", sig)) {
			n++
		}
	}
	return n
}

func orphanFiles(t *testing.T, state string) []os.DirEntry {
	t.Helper()
	es, err := os.ReadDir(OrphansDir(state))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return es
}

// TestKillOldHolderEndsTheWholeJob: steps 1 to 5 on a real tree in a
// group of its own; every process is SIGKILLed, the record is written and
// deleted, and nothing outside the job is signalled.
func TestKillOldHolderEndsTheWholeJob(t *testing.T) {
	killSentinel(t)
	j := startTestJob(t)
	sent := j.guardSignals(t)
	ticketHeldFn = j.heldUntilReaped
	defer func() { ticketHeldFn = ticketHeld }()
	state := t.TempDir()
	var recorded *Orphan
	writeOrphanFn = func(dir string, o *Orphan) (string, error) {
		cp := *o
		recorded = &cp
		for _, d := range o.Descendants {
			if p, err := procinfo.Lookup(d.PID); err != nil || p.State != 'T' {
				t.Errorf("descendant %d not stopped when the record is written: %+v %v", d.PID, p, err)
			}
		}
		return writeOrphan(dir, o)
	}
	defer func() { writeOrphanFn = writeOrphan }()

	res, err := KillOldHolder(state, j.target(t), procinfo.ParentChain())
	if err != nil {
		t.Fatal(err)
	}
	if res.Processes != 3 || recorded == nil || len(recorded.Descendants) != 3 || recorded.Key != "old" || recorded.PID != j.root || recorded.ByPID != os.Getpid() {
		t.Fatalf("result %+v, record %+v", res, recorded)
	}
	if ms := j.members(t); len(ms) != 0 {
		t.Fatalf("still running: %v", ms)
	}
	if es := orphanFiles(t, state); len(es) != 0 {
		t.Fatalf("the record must be deleted once the tree is gone: %v", es)
	}
	if countSig(*sent, unix.SIGSTOP) != 4 || countSig(*sent, unix.SIGKILL) < 4 || countSig(*sent, unix.SIGCONT) != 0 {
		t.Fatalf("signals %v", *sent)
	}
}

// TestKillOldHolderAbortsResumeEverything: every abort path sends SIGCONT
// to the old incoda and to every descendant it stopped, and terminates
// nothing (spec 3.2, the protected window).
func TestKillOldHolderAbortsResumeEverything(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(j *testJob)
		check func(t *testing.T, j *testJob, err error)
		walks bool // the abort comes after the walk stopped the descendants
	}{
		{"re-check error", func(j *testJob) {
			ticketHeldFn = func(KillTarget) (bool, error) { return false, errors.New("EIO") }
		}, func(t *testing.T, j *testJob, err error) {
			var se *StateError
			if !errors.As(err, &se) || se.Msg != fmt.Sprintf("kill: cannot re-check the ticket of older incoda pid %d (EIO); nothing was killed", j.root) {
				t.Fatalf("want the exit-122 state error, got %T %v", err, err)
			}
		}, false},
		{"no longer holds", func(j *testJob) {
			ticketHeldFn = func(KillTarget) (bool, error) { return false, nil }
		}, func(t *testing.T, j *testJob, err error) {
			var r *Refusal
			if !errors.As(err, &r) || !strings.Contains(r.Msg, `no longer holds "old"`) {
				t.Fatalf("want the exit-120 refusal, got %T %v", err, err)
			}
		}, false},
		{"listing fails", func(j *testJob) {
			ticketHeldFn = j.heldUntilReaped
			listFn = func() ([]procinfo.Proc, error) { return nil, errors.New("sysctl refused") }
		}, func(t *testing.T, j *testJob, err error) {
			var r *Refusal
			if !errors.As(err, &r) || !strings.Contains(r.Msg, "cannot list the job") {
				t.Fatalf("want the listing refusal, got %T %v", err, err)
			}
		}, false},
		{"record write fails", func(j *testJob) {
			ticketHeldFn = j.heldUntilReaped
			writeOrphanFn = func(string, *Orphan) (string, error) { return "", errors.New("disk full") }
		}, func(t *testing.T, j *testJob, err error) {
			var se *StateError
			if !errors.As(err, &se) || !strings.Contains(se.Msg, "nothing was terminated") {
				t.Fatalf("want the record state error, got %T %v", err, err)
			}
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			killSentinel(t)
			j := startTestJob(t)
			sent := j.guardSignals(t)
			defer func() { ticketHeldFn, listFn, writeOrphanFn = ticketHeld, procinfo.List, writeOrphan }()
			tc.setup(j)
			state := t.TempDir()
			_, err := KillOldHolder(state, j.target(t), procinfo.ParentChain())
			tc.check(t, j, err)
			j.assertRunning(t, 4)
			stops, conts := countSig(*sent, unix.SIGSTOP), countSig(*sent, unix.SIGCONT)
			if countSig(*sent, unix.SIGKILL) != 0 || stops != conts || (tc.walks && stops != 4) || (!tc.walks && stops != 1) {
				t.Fatalf("signals %v", *sent)
			}
			if es := orphanFiles(t, state); len(es) != 0 {
				t.Fatalf("an abort leaves no record: %v", es)
			}
		})
	}
}

// TestKillOldHolderRefusesAnAncestor: step 0 signals nothing.
func TestKillOldHolderRefusesAnAncestor(t *testing.T) {
	killSentinel(t)
	j := startTestJob(t)
	sent := j.guardSignals(t)
	_, err := KillOldHolder(t.TempDir(), j.target(t), procinfo.Chain{PIDs: []int{os.Getppid(), j.root, 1}})
	var r *Refusal
	if !errors.As(err, &r) || r.Msg != fmt.Sprintf("kill: pid %d is an ancestor of this process; run kill from outside its job", j.root) {
		t.Fatalf("got %T %v", err, err)
	}
	if len(*sent) != 0 {
		t.Fatalf("signals %v", *sent)
	}
	j.assertRunning(t, 4)
}

// fakeKill wires the kill engine to a fake process table (pids far above
// any real pid_max, so nothing real is ever signalled) and returns the
// table. The ticket reads held until the old incoda is SIGKILLed.
func fakeKill(t *testing.T, procs ...procinfo.Proc) *fakeProcs {
	t.Helper()
	killSentinel(t)
	f := &fakeProcs{procs: map[int]procinfo.Proc{}}
	for _, p := range procs {
		f.procs[p.PID] = p
	}
	root := procs[0].PID
	listFn, signalFn, lookupFn = f.list, f.signal, f.lookup
	ticketHeldFn = func(KillTarget) (bool, error) {
		_, err := f.lookup(root)
		return err == nil, nil
	}
	settle, gone := walkSettle, treeGoneWait
	walkSettle, treeGoneWait = 200*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() {
		listFn, signalFn, lookupFn, ticketHeldFn = procinfo.List, unix.Kill, procinfo.Lookup, ticketHeld
		walkSettle, treeGoneWait = settle, gone
	})
	return f
}

func fp(pid, ppid, pgid int, st byte) procinfo.Proc {
	return procinfo.Proc{PID: pid, PPID: ppid, PGID: pgid, Start: uint64(pid) * 10, State: st}
}

func fakeTarget(t *testing.T, pid int) KillTarget {
	return KillTarget{Kind: TargetOld, Key: "old", Dir: t.TempDir(), Ticket: "x.ticket", PID: pid}
}

// assertResumed fails unless every SIGSTOP was matched by a SIGCONT of the
// same pid and nothing was SIGKILLed.
func (f *fakeProcs) assertResumed(t *testing.T, wantStops int) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	stops := map[string]int{}
	n := 0
	for _, s := range f.signals {
		pid, sig, _ := strings.Cut(s, ":")
		switch sig {
		case fmt.Sprint(int(unix.SIGSTOP)):
			stops[pid]++
			n++
		case fmt.Sprint(int(unix.SIGCONT)):
			stops[pid]--
		case fmt.Sprint(int(unix.SIGKILL)):
			t.Fatalf("an abort sent SIGKILL: %v", f.signals)
		}
	}
	for pid, c := range stops {
		if c != 0 {
			t.Fatalf("pid %s stopped and not resumed: %v", pid, f.signals)
		}
	}
	if n != wantStops {
		t.Fatalf("%d SIGSTOPs, want %d: %v", n, wantStops, f.signals)
	}
}

const fr = 5000100 // the fake old incoda

// TestKillOldHolderRefusesAnUnverifiedChain: a parent chain that failed
// partway, stops short of pid 1 or is empty is refused before any signal.
func TestKillOldHolderRefusesAnUnverifiedChain(t *testing.T) {
	for _, tc := range []struct {
		chain  procinfo.Chain
		reason string
	}{
		{procinfo.Chain{PIDs: []int{77}, Err: errors.New("lookup refused")}, "lookup refused"},
		{procinfo.Chain{PIDs: []int{77, 78}}, "the parent chain stops at pid 78"},
		{procinfo.Chain{}, "no parent chain"},
	} {
		f := fakeKill(t, fp(fr, 1, fr, 'S'), fp(fr+1, fr, fr, 'S'))
		_, err := KillOldHolder(t.TempDir(), fakeTarget(t, fr), tc.chain)
		var r *Refusal
		want := "kill: cannot verify this process's ancestors (" + tc.reason + "); run kill from a plain terminal outside the job"
		if !errors.As(err, &r) || r.Msg != want {
			t.Fatalf("got %T %v, want %q", err, err, want)
		}
		if len(f.signals) != 0 || f.lists != 0 {
			t.Fatalf("signals %v, listings %d: nothing may happen before the chain is verified", f.signals, f.lists)
		}
	}
}

// TestKillOldHolderFindsAnAncestorInTheListing: the chain says nothing,
// but the listing shows the old incoda is an ancestor of kill: refused as
// in step 0, everything resumed, kill's ancestor never stopped.
func TestKillOldHolderFindsAnAncestorInTheListing(t *testing.T) {
	self := os.Getpid()
	f := fakeKill(t, fp(fr, 1, fr, 'S'), fp(fr+1, fr, fr, 'S'), fp(fr+2, fr, fr, 'S'), procinfo.Proc{PID: self, PPID: fr + 1, PGID: fr, Start: 1, State: 'R'})
	_, err := KillOldHolder(t.TempDir(), fakeTarget(t, fr), procinfo.Chain{PIDs: []int{1}})
	var r *Refusal
	if !errors.As(err, &r) || r.Msg != fmt.Sprintf("kill: pid %d is an ancestor of this process; run kill from outside its job", fr) {
		t.Fatalf("got %T %v", err, err)
	}
	f.assertResumed(t, 1)
}

// TestKillOldHolderStopFailure: a descendant that cannot be stopped
// (EPERM) aborts the walk at once with everything resumed.
func TestKillOldHolderStopFailure(t *testing.T) {
	f := fakeKill(t, fp(fr, 1, fr, 'S'), fp(fr+1, fr, fr, 'S'), fp(fr+2, fr+1, fr, 'S'))
	f.stopErr = map[int]error{fr + 2: unix.EPERM}
	_, err := KillOldHolder(t.TempDir(), fakeTarget(t, fr), procinfo.Chain{PIDs: []int{1}})
	var r *Refusal
	if !errors.As(err, &r) || r.Msg != fmt.Sprintf("kill: cannot stop pid %d (operation not permitted); stop its job by hand, then rerun", fr+2) {
		t.Fatalf("got %T %v", err, err)
	}
	f.assertResumed(t, 2)
}

// TestWalkTreeSkipsAPidThatIsGone: ESRCH on SIGSTOP neither stops nor
// records the pid; its children are still walked.
func TestWalkTreeSkipsAPidThatIsGone(t *testing.T) {
	f := fakeKill(t, fp(fr, 1, fr, 'T'), fp(fr+1, fr, fr, 'S'), fp(fr+2, fr+1, fr, 'S'))
	f.stopErr = map[int]error{fr + 1: unix.ESRCH}
	stopped := []int{fr}
	desc, _, err := walkTree(fr, map[int]bool{os.Getpid(): true}, &stopped)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(stopped) != fmt.Sprint([]int{fr, fr + 2}) || len(desc) != 1 || desc[0].PID != fr+2 {
		t.Fatalf("stopped %v, desc %v", stopped, desc)
	}
}

// TestKillOldHolderJobKeepsChanging: a tree that forks a new process on
// every listing past walkSettle is abandoned, resumed, and nothing killed.
func TestKillOldHolderJobKeepsChanging(t *testing.T) {
	f := fakeKill(t, fp(fr, 1, fr, 'S'), fp(fr+1, fr, fr, 'S'))
	f.onList = func(n int, procs map[int]procinfo.Proc) {
		p := fp(fr+1+n, fr+n, fr, 'R')
		procs[p.PID] = p
	}
	_, err := KillOldHolder(t.TempDir(), fakeTarget(t, fr), procinfo.Chain{PIDs: []int{1}})
	var se *StateError
	if !errors.As(err, &se) || se.Msg != fmt.Sprintf("kill: the job of older incoda pid %d kept changing; nothing was killed; rerun", fr) {
		t.Fatalf("got %T %v", err, err)
	}
	if f.lists < 3 {
		t.Fatalf("only %d listings", f.lists)
	}
	f.assertResumed(t, len(f.signals)/2)
}

// TestKillOldHolderRootLeavesTheListing: the old incoda vanishing during
// the walk aborts with everything resumed.
func TestKillOldHolderRootLeavesTheListing(t *testing.T) {
	f := fakeKill(t, fp(fr, 1, fr, 'S'), fp(fr+1, fr, fr, 'S'), fp(fr+2, fr+1, fr, 'S'))
	f.onList = func(n int, procs map[int]procinfo.Proc) {
		if n == 2 {
			delete(procs, fr)
		}
	}
	_, err := KillOldHolder(t.TempDir(), fakeTarget(t, fr), procinfo.Chain{PIDs: []int{1}})
	var r *Refusal
	if !errors.As(err, &r) || r.Msg != fmt.Sprintf("kill: pid %d no longer holds %q", fr, "old") {
		t.Fatalf("got %T %v", err, err)
	}
	f.assertResumed(t, 3)
}

// TestKillOldHolderRechecksStartTimes: a group leader, a descendant or the
// old incoda whose start time no longer matches is not signalled in step 5.
func TestKillOldHolderRechecksStartTimes(t *testing.T) {
	f := fakeKill(t, fp(fr, 1, fr, 'S'), fp(fr+1, fr, fr+1, 'S'), fp(fr+2, fr+1, fr+1, 'S'), fp(fr+3, fr, fr, 'S'))
	stopped := false
	f.onList = func(n int, procs map[int]procinfo.Proc) {
		if n > 1 {
			stopped = true
		}
	}
	// After the walk, pids fr+1 (a group leader) and fr+3 read as other
	// incarnations; fr+2 and the old incoda still match.
	lookupFn = func(pid int) (procinfo.Proc, error) {
		p, err := f.lookup(pid)
		if err == nil && stopped && (pid == fr+1 || pid == fr+3) {
			p.Start++
		}
		return p, err
	}
	state := t.TempDir()
	_, err := KillOldHolder(state, fakeTarget(t, fr), procinfo.Chain{PIDs: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	kills := map[string]bool{}
	for _, s := range f.signals {
		if pid, sig, _ := strings.Cut(s, ":"); sig == fmt.Sprint(int(unix.SIGKILL)) {
			kills[pid] = true
		}
	}
	want := map[string]bool{fmt.Sprint(fr + 2): true, fmt.Sprint(fr): true}
	if fmt.Sprint(kills) != fmt.Sprint(want) {
		t.Fatalf("SIGKILLs %v, want %v (signals %v)", kills, want, f.signals)
	}

	// The old incoda reused: its SIGKILL is skipped too.
	f = fakeKill(t, fp(fr, 1, fr, 'S'), fp(fr+1, fr, fr, 'S'))
	n := 0
	lookupFn = func(pid int) (procinfo.Proc, error) {
		p, err := f.lookup(pid)
		if pid == fr {
			n++
			if n > 1 {
				p.Start++
			}
		}
		return p, err
	}
	ticketHeldFn = func(KillTarget) (bool, error) { return n < 2, nil }
	if _, err := KillOldHolder(t.TempDir(), fakeTarget(t, fr), procinfo.Chain{PIDs: []int{1}}); err != nil {
		t.Fatal(err)
	}
	for _, s := range f.signals {
		if s == fmt.Sprintf("%d:%d", fr, unix.SIGKILL) {
			t.Fatalf("SIGKILLed a reused old incoda pid: %v", f.signals)
		}
	}
}

// TestKillOldHolderTreeNotGone: a tree still there after treeGoneWait
// exits 122 and keeps the record.
func TestKillOldHolderTreeNotGone(t *testing.T) {
	f := fakeKill(t, fp(fr, 1, fr, 'S'), fp(fr+1, fr, fr, 'S'))
	ticketHeldFn = func(KillTarget) (bool, error) { return true, nil }
	state := t.TempDir()
	_, err := KillOldHolder(state, fakeTarget(t, fr), procinfo.Chain{PIDs: []int{1}})
	var se *StateError
	if !errors.As(err, &se) || se.Msg != fmt.Sprintf(`kill: older incoda pid %d was sent SIGKILL but %d (the older incoda) still run; the orphan record keeps "old" busy until they exit`, fr, fr) {
		t.Fatalf("got %T %v", err, err)
	}
	if es := orphanFiles(t, state); len(es) != 1 {
		t.Fatalf("the record must stay: %v", es)
	}
	if !strings.Contains(fmt.Sprint(f.signals), fmt.Sprintf("%d:%d", fr, unix.SIGKILL)) {
		t.Fatalf("signals %v", f.signals)
	}
}
