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
}

func (f *fakeProcs) list() ([]procinfo.Proc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
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
	f.signals = append(f.signals, fmt.Sprintf("%d:%d", pid, sig))
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
		check func(t *testing.T, err error)
		walks bool // the abort comes after the walk stopped the descendants
	}{
		{"re-check error", func(j *testJob) {
			ticketHeldFn = func(KillTarget) (bool, error) { return false, errors.New("EIO") }
		}, func(t *testing.T, err error) {
			var se *StateError
			if !errors.As(err, &se) || !strings.Contains(se.Msg, "cannot re-check the ticket") {
				t.Fatalf("want the exit-122 state error, got %T %v", err, err)
			}
		}, false},
		{"no longer holds", func(j *testJob) {
			ticketHeldFn = func(KillTarget) (bool, error) { return false, nil }
		}, func(t *testing.T, err error) {
			var r *Refusal
			if !errors.As(err, &r) || !strings.Contains(r.Msg, `no longer holds "old"`) {
				t.Fatalf("want the exit-120 refusal, got %T %v", err, err)
			}
		}, false},
		{"listing fails", func(j *testJob) {
			ticketHeldFn = j.heldUntilReaped
			listFn = func() ([]procinfo.Proc, error) { return nil, errors.New("sysctl refused") }
		}, func(t *testing.T, err error) {
			var r *Refusal
			if !errors.As(err, &r) || !strings.Contains(r.Msg, "cannot list the job") {
				t.Fatalf("want the listing refusal, got %T %v", err, err)
			}
		}, false},
		{"record write fails", func(j *testJob) {
			ticketHeldFn = j.heldUntilReaped
			writeOrphanFn = func(string, *Orphan) (string, error) { return "", errors.New("disk full") }
		}, func(t *testing.T, err error) {
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
			tc.check(t, err)
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
