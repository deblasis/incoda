//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/procinfo"
)

var (
	holderOnce sync.Once
	holderBin  string
	holderErr  error
)

// oldHolderBinary builds internal/testprog/oldholder next to the incoda
// test binary.
func oldHolderBinary(t *testing.T) string {
	t.Helper()
	incoda, _ := binaries(t)
	holderOnce.Do(func() {
		holderBin = filepath.Join(filepath.Dir(incoda), "oldholder")
		cmd := exec.Command("go", "build", "-o", holderBin, "./internal/testprog/oldholder")
		cmd.Env = append(os.Environ(), "GOTOOLCHAIN=auto")
		if out, err := cmd.CombinedOutput(); err != nil {
			holderErr = fmt.Errorf("build oldholder: %v\n%s", err, out)
		}
	})
	if holderErr != nil {
		t.Fatal(holderErr)
	}
	return holderBin
}

// runnerSentinel starts a process in the test runner's own process group
// and, at the end of the test, fails unless it is still alive: a kill test
// must never signal the runner's group or anything outside its own tree.
func runnerSentinel(t *testing.T) {
	t.Helper()
	s := exec.Command("sleep", "120")
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !alive(s.Process.Pid) {
			t.Errorf("the sentinel in the test runner's process group (pid %d) was signalled", s.Process.Pid)
		}
		_ = s.Process.Kill()
		_ = s.Wait()
	})
}

// alive reports whether pid exists and is not a zombie.
func alive(pid int) bool {
	p, err := procinfo.Lookup(pid)
	return err == nil && p.State != 'Z'
}

// stopped reports whether pid is in the stopped state.
func stopped(pid int) bool {
	s, _ := procinfo.Stopped(pid)
	return s
}

func waitGone(t *testing.T, what string, pids ...int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for _, pid := range pids {
		for alive(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: pid %d is still running", what, pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// startInGroup starts bin with args in a new process group of its own
// (never the runner's), with INCODA_DIR set to state. Cleanup resumes and
// kills that whole group, which is safe because the group's id is the
// started process's pid by construction and the process is not reaped
// before the signal.
func startInGroup(t *testing.T, state string, bin string, args ...string) *exec.Cmd {
	t.Helper()
	c := exec.Command(bin, args...)
	c.Env = laneEnv(state)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-c.Process.Pid, syscall.SIGCONT)
		_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		_ = c.Wait()
	})
	return c
}

// shJob is a command whose shell starts a grandchild (sleep 60), writes
// "<shell pid> <sleep pid>" to file, and waits for it.
func shJob(file string) []string {
	return []string{"sh", "-c", `sleep 60 & echo "$$ $!" > "$0.tmp" && mv "$0.tmp" "$0"; wait`, file}
}

// readJob waits for shJob's file and returns the shell and sleep pids.
func readJob(t *testing.T, file string) (int, int) {
	t.Helper()
	waitForFile(t, file)
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(b))
	if len(f) != 2 {
		t.Fatalf("job file %q", b)
	}
	sh, _ := strconv.Atoi(f[0])
	sl, _ := strconv.Atoi(f[1])
	return sh, sl
}

// strayOldRun migrates a state directory, deletes the fence, starts an
// older incoda run of shJob on key (in its own group), then re-places the
// fence with a config, which moves the run's lane to strays/: an unpooled
// holder no kill request can reach.
func strayOldRun(t *testing.T, tag, key string) (state string, old *exec.Cmd, sh, sl int) {
	t.Helper()
	incoda, _ := binaries(t)
	bin := oldBinary(t, tag)
	state = t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "seed"); code != 0 {
		t.Fatalf("migrate: %d\n%s", code, out)
	}
	if err := os.Remove(filepath.Join(state, "queues")); err != nil {
		t.Fatal(err)
	}
	job := filepath.Join(t.TempDir(), "job")
	old = startInGroup(t, state, bin, append([]string{"run", "--queue", key, "--poll", "50ms", "--quiet", "--"}, shJob(job)...)...)
	sh, sl = readJob(t, job)
	if out, code := runIncoda(t, incoda, state, "config", "other"); code != 0 {
		t.Fatalf("re-fence: %d\n%s", code, out)
	}
	if !machine.FencePlaced(state) {
		t.Fatal("config must re-place the fence")
	}
	return state, old, sh, sl
}

// TestKillAV060StrayEndsItsWholeJob: a v0.6.0 run outside the pools,
// whose child spawned a grandchild, cannot see kill requests. A plain kill
// (no --force) ends the old incoda, its child and the grandchild before it
// exits 0, deletes the orphan record, and signals nothing outside the job.
func TestKillAV060StrayEndsItsWholeJob(t *testing.T) {
	runnerSentinel(t)
	incoda, _ := binaries(t)
	state, old, sh, sl := strayOldRun(t, "v0.6.0", "builds")
	pid := strconv.Itoa(old.Process.Pid)

	out, code := runIncoda(t, incoda, state, "kill", "--queue", "builds", "--pid", pid, "--reason", "test")
	if code != 0 {
		t.Fatalf("kill: exit %d\n%s", code, out)
	}
	// Every descendant is gone before kill exits 0.
	for _, p := range []int{old.Process.Pid, sh, sl} {
		if alive(p) {
			t.Fatalf("pid %d of the old job survived the kill:\n%s", p, out)
		}
	}
	_ = old.Wait()
	if all, _, _ := machine.ReadOrphans(state); len(all) != 0 {
		t.Fatalf("the orphan record must be deleted once the tree is empty: %+v", all)
	}
}

// noKillRequests fails if kill left a request file in dir: an older
// incoda is ended by the walk, never by a request.
func noKillRequests(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".kill") || strings.HasSuffix(e.Name(), ".kill.tmp") {
			t.Fatalf("kill wrote a request %s for an older incoda", e.Name())
		}
	}
}

// TestKillBeforeTheFence: on a layout not upgraded yet (queues/ still a
// directory) a plain kill writes no request and ends the older incoda with
// its whole job. A v0.6.0 holder whose child spawned a grandchild loses
// both before kill exits 0, and its lane reads free only then; a v0.2.0
// holder, which predates kill, loses its job too, its child's own process
// group included.
func TestKillBeforeTheFence(t *testing.T) {
	runnerSentinel(t)
	incoda, _ := binaries(t)
	for _, tag := range []string{"v0.6.0", "v0.2.0"} {
		t.Run(tag, func(t *testing.T) {
			state := t.TempDir()
			job := filepath.Join(t.TempDir(), "job")
			old := startInGroup(t, state, oldBinary(t, tag), append([]string{"run", "--queue", "pre", "--poll", "50ms", "--quiet", "--"}, shJob(job)...)...)
			sh, sl := readJob(t, job)
			pid := strconv.Itoa(old.Process.Pid)
			if rep := statusJSON(t, incoda, state, "pre"); len(rep.Queues[0].Holders) != 1 {
				t.Fatalf("the old run must hold the lane: %+v", rep.Queues)
			}
			out, code := runIncoda(t, incoda, state, "kill", "--queue", "pre", "--pid", pid, "--reason", "test")
			if code != 0 {
				t.Fatalf("kill: exit %d\n%s", code, out)
			}
			// When kill returns, the child and the grandchild are gone and
			// only then does the lane read free.
			for _, p := range []int{old.Process.Pid, sh, sl} {
				if alive(p) {
					t.Fatalf("pid %d survived the kill", p)
				}
			}
			if rep := statusJSON(t, incoda, state, "pre"); len(rep.Queues[0].Holders) != 0 {
				t.Fatalf("the lane must read free once the job is gone: %+v", rep.Queues)
			}
			noKillRequests(t, filepath.Join(state, "queues", "pre"))
			log, _ := os.ReadFile(filepath.Join(state, "queues", "pre", "lane.log"))
			if !strings.Contains(string(log), "event=kill pid="+pid) || !strings.Contains(string(log), "old=true processes=2") {
				t.Fatalf("lane.log must record the old-holder kill:\n%s", log)
			}
			if _, err := os.Lstat(machine.RegistryPath(state)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("kill must never migrate")
			}
		})
	}
}

// TestKillAfterTheFenceDuringTheUpgrade: an older run slipped in between
// M2 and the fence and sits under lanes/ while machine.json is absent. The
// M5 stop line needs no --force: running it ends the job and the waiting
// migration completes.
func TestKillAfterTheFenceDuringTheUpgrade(t *testing.T) {
	runnerSentinel(t)
	incoda, stamp := binaries(t)
	bin := crashBinary(t)
	old := oldBinary(t, "v0.6.0")
	state := t.TempDir()
	if out, code := runIncoda(t, old, state, "run", "--queue", "seed", "--quiet", "--", stamp, filepath.Join(t.TempDir(), "seed.txt"), "seed", "1"); code != 0 {
		t.Fatalf("seed the old layout: %d\n%s", code, out)
	}
	pause := filepath.Join(t.TempDir(), "go")
	// A pool, so the run needs no link: it is the command that migrates.
	m := exec.Command(bin, "run", "--queue", "builds", "--wait", "60s", "--poll", "50ms", "--", stamp, filepath.Join(t.TempDir(), "new.txt"), "new", "10")
	m.Env = append(laneEnv(state), "INCODA_TEST_PAUSE_AT=M3", "INCODA_TEST_PAUSE_FILE="+pause)
	var mErr syncBuffer
	m.Stderr = &mErr
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Process.Kill(); _ = m.Wait() }()
	waitForFile(t, pause+".reached")
	job := filepath.Join(t.TempDir(), "job")
	o := startInGroup(t, state, old, append([]string{"run", "--queue", "slip", "--poll", "50ms", "--quiet", "--"}, shJob(job)...)...)
	sh, sl := readJob(t, job)
	if err := os.WriteFile(pause, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	pid := strconv.Itoa(o.Process.Pid)
	waitForText(t, &mErr, "incoda kill --queue slip --pid "+pid+" --reason 'incoda upgrade'\n")

	out, code := runIncoda(t, incoda, state, "kill", "--queue", "slip", "--pid", pid, "--reason", "incoda upgrade")
	if code != 0 {
		t.Fatalf("kill: exit %d\n%s", code, out)
	}
	for _, p := range []int{sh, sl} {
		if alive(p) {
			t.Fatalf("pid %d of the slipped-in job survived", p)
		}
	}
	if err := m.Wait(); err != nil {
		t.Fatalf("the migration must complete once the old job is gone: %v\n%s", err, mErr.String())
	}
	if _, err := machine.ReadRegistry(state); err != nil {
		t.Fatal(err)
	}
}

// TestKillANestedV060HolderLeavesTheOuterJob: an inner v0.6.0 run nested
// in an outer one shares the outer group (the shipped Setenv bug). Killing
// the inner holder ends the inner job and nothing else: the outer incoda,
// the outer job's shell and their group are not signalled.
func TestKillANestedV060HolderLeavesTheOuterJob(t *testing.T) {
	runnerSentinel(t)
	incoda, _ := binaries(t)
	old := oldBinary(t, "v0.6.0")
	state := t.TempDir()
	job := filepath.Join(t.TempDir(), "job")
	// The outer job is a shell that runs the inner run, then lingers.
	outer := startInGroup(t, state, old, append([]string{"run", "--queue", "outer", "--poll", "50ms", "--quiet", "--",
		"sh", "-c", `"$@"; sleep 60`, "sh", old, "run", "--queue", "inner", "--poll", "10s", "--quiet", "--"}, shJob(job)...)...)
	sh, sl := readJob(t, job)
	jobShell, err := procinfo.Lookup(sh)
	if err != nil {
		t.Fatal(err)
	}
	inner, err := procinfo.Lookup(jobShell.PPID)
	if err != nil {
		t.Fatal(err)
	}
	if jobShell.PGID != outer.Process.Pid || inner.PGID != outer.Process.Pid {
		t.Fatalf("the inner run and its job must share the outer group %d: %+v %+v", outer.Process.Pid, inner, jobShell)
	}
	outerShell := inner.PPID
	out, code := runIncoda(t, incoda, state, "kill", "--queue", "inner", "--pid", strconv.Itoa(inner.PID), "--reason", "test", "--wait", "0", "--force")
	if code != 0 {
		t.Fatalf("kill --force: exit %d\n%s", code, out)
	}
	for _, p := range []int{inner.PID, sh, sl} {
		if alive(p) {
			t.Fatalf("pid %d of the inner job survived", p)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if !alive(outer.Process.Pid) || !alive(outerShell) || stopped(outer.Process.Pid) || stopped(outerShell) {
		t.Fatal("the outer incoda and the outer job's shell must not be signalled")
	}
}

// TestKillOfAnAncestorIsRefused: kill run from inside an older incoda's
// job, naming that incoda, refuses before stopping anything.
func TestKillOfAnAncestorIsRefused(t *testing.T) {
	runnerSentinel(t)
	incoda, _ := binaries(t)
	old := oldBinary(t, "v0.6.0")
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "seed"); code != 0 {
		t.Fatalf("migrate: %d\n%s", code, out)
	}
	if err := os.Remove(filepath.Join(state, "queues")); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	goFile, result := filepath.Join(dir, "go"), filepath.Join(dir, "result")
	script := `while [ ! -e "$1" ]; do sleep 0.05; done; "$0" kill --queue anc --pid $PPID --reason test --force > "$2.tmp" 2>&1; echo "rc=$?" >> "$2.tmp"; mv "$2.tmp" "$2"`
	o := startInGroup(t, state, old, "run", "--queue", "anc", "--poll", "50ms", "--quiet", "--", "sh", "-c", script, incoda, goFile, result)
	waitForTicket(t, filepath.Join(state, "queues", "anc"))
	if out, code := runIncoda(t, incoda, state, "config", "other"); code != 0 {
		t.Fatalf("re-fence: %d\n%s", code, out)
	}
	if err := os.WriteFile(goFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, result)
	b, _ := os.ReadFile(result)
	want := fmt.Sprintf("incoda: kill: pid %d is an ancestor of this process; run kill from outside its job\nrc=120\n", o.Process.Pid)
	if !strings.HasSuffix(string(b), want) {
		t.Fatalf("want %q, got %q", want, b)
	}
	if err := o.Wait(); err != nil {
		t.Fatalf("the old run was signalled: %v", err)
	}
}

// TestKillInsideAGroupItMustNotSignal: kill runs inside the process group
// that a v0.2.0 run's child leads. That group holds kill, so it is never
// signalled as a group: the job's processes are ended one by one, and a
// sentinel in the same group survives.
func TestKillInsideAGroupItMustNotSignal(t *testing.T) {
	runnerSentinel(t)
	incoda, _ := binaries(t)
	tree := treeBinary(t)
	state := t.TempDir()
	out := filepath.Join(t.TempDir(), "tree.txt")
	old := startInGroup(t, state, oldBinary(t, "v0.2.0"), "run", "--queue", "grp", "--poll", "50ms", "--quiet", "--", tree, out)
	ti := readTree(t, out)
	if ti.pgid != ti.pid {
		t.Fatalf("v0.2.0 puts its child in its own group: %+v", ti)
	}
	inGroup := func(args ...string) *exec.Cmd {
		c := exec.Command(args[0], args[1:]...)
		c.Env = laneEnv(state)
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: ti.pgid}
		return c
	}
	sentinel := inGroup("sleep", "60")
	if err := sentinel.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sentinel.Process.Kill(); _ = sentinel.Wait() }()
	k := inGroup(incoda, "kill", "--queue", "grp", "--pid", strconv.Itoa(old.Process.Pid), "--reason", "test", "--wait", "0", "--force")
	b, err := k.CombinedOutput()
	if err != nil {
		t.Fatalf("kill: %v\n%s", err, b)
	}
	waitGone(t, "the job", ti.pid, ti.grandchild, old.Process.Pid)
	if !alive(sentinel.Process.Pid) {
		t.Fatal("the group that holds kill was signalled")
	}
}

// oldHolder starts the oldholder test program on key under root and
// returns it with its pid and its child's pid.
func oldHolder(t *testing.T, state, root, key string) (c *exec.Cmd, pid, child int, release string) {
	t.Helper()
	dir := t.TempDir()
	ready, release := filepath.Join(dir, "ready"), filepath.Join(dir, "release")
	c = startInGroup(t, state, oldHolderBinary(t), filepath.Join(root, key), key, ready, release)
	waitForFile(t, ready)
	b, _ := os.ReadFile(ready)
	var p, ch int
	if _, err := fmt.Sscanf(string(b), "pid %d\nchild %d\n", &p, &ch); err != nil {
		t.Fatalf("ready file %q: %v", b, err)
	}
	return c, p, ch, release
}

// TestKillAbortsWhenTheTargetReleasedBeforeTheStop: the holder lets go of
// its ticket between kill finding it and kill's SIGSTOP. The re-check sees
// the ticket free: kill resumes it and refuses; nothing is terminated.
func TestKillAbortsWhenTheTargetReleasedBeforeTheStop(t *testing.T) {
	runnerSentinel(t)
	bin := crashBinary(t)
	state := t.TempDir()
	h, pid, child, release := oldHolder(t, state, filepath.Join(state, "queues"), "rel")
	pause := filepath.Join(t.TempDir(), "go")
	k := exec.Command(bin, "kill", "--queue", "rel", "--pid", strconv.Itoa(pid), "--reason", "test", "--wait", "0", "--force")
	k.Env = append(laneEnv(state), "INCODA_TEST_PAUSE_AT=kill-before-stop", "INCODA_TEST_PAUSE_FILE="+pause)
	var kOut syncBuffer
	k.Stdout, k.Stderr = &kOut, &kOut
	if err := k.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = k.Process.Kill(); _ = k.Wait() }()
	waitForFile(t, pause+".reached")
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, release+".done")
	if err := os.WriteFile(pause, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	err := k.Wait()
	want := fmt.Sprintf("incoda: kill: pid %d no longer holds \"rel\"\n", pid)
	if exitCodeOf(err) != 120 || !strings.Contains(kOut.String(), want) {
		t.Fatalf("want exit 120 and %q, got %v:\n%s", want, err, kOut.String())
	}
	time.Sleep(100 * time.Millisecond)
	if !alive(pid) || !alive(child) || stopped(pid) || stopped(child) {
		t.Fatalf("the holder and its child must be running and resumed: pid %v/%v child %v/%v", alive(pid), stopped(pid), alive(child), stopped(child))
	}
	_ = h
}

// TestKillWindowIgnoresSIGINTAndSIGTERM: SIGINT and SIGTERM sent to kill
// while the job is frozen do not stop it halfway: kill completes and the
// job is gone.
func TestKillWindowIgnoresSIGINTAndSIGTERM(t *testing.T) {
	runnerSentinel(t)
	bin := crashBinary(t)
	state := t.TempDir()
	_, pid, child, _ := oldHolder(t, state, filepath.Join(state, "queues"), "sig")
	pause := filepath.Join(t.TempDir(), "go")
	k := exec.Command(bin, "kill", "--queue", "sig", "--pid", strconv.Itoa(pid), "--reason", "test", "--wait", "0", "--force")
	k.Env = append(laneEnv(state), "INCODA_TEST_PAUSE_AT=kill-stopped", "INCODA_TEST_PAUSE_FILE="+pause)
	var kOut syncBuffer
	k.Stdout, k.Stderr = &kOut, &kOut
	if err := k.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = k.Process.Kill(); _ = k.Wait() }()
	waitForFile(t, pause+".reached")
	if !stopped(pid) || !stopped(child) {
		t.Fatal("inside the window the old incoda and its child are stopped")
	}
	for _, s := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		if err := k.Process.Signal(s); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if err := os.WriteFile(pause, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := k.Wait(); err != nil {
		t.Fatalf("kill must complete despite SIGINT and SIGTERM: %v\n%s", err, kOut.String())
	}
	waitGone(t, "the job", pid, child)
}

// TestKillFailedRecordWriteResumesEverything: when the orphan record
// cannot be written, nothing is terminated and every process kill stopped
// is resumed.
func TestKillFailedRecordWriteResumesEverything(t *testing.T) {
	runnerSentinel(t)
	incoda, _ := binaries(t)
	state := t.TempDir()
	_, pid, child, _ := oldHolder(t, state, filepath.Join(state, "queues"), "rec")
	if err := os.WriteFile(machine.OrphansDir(state), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runIncoda(t, incoda, state, "kill", "--queue", "rec", "--pid", strconv.Itoa(pid), "--reason", "test", "--wait", "0", "--force")
	want := fmt.Sprintf("incoda: kill: cannot write the orphan record for older incoda pid %d (", pid)
	if code != 122 || !strings.Contains(out, want) || !strings.Contains(out, "); nothing was terminated and its job was resumed\n") {
		t.Fatalf("want exit 122 and %q, got %d:\n%s", want, code, out)
	}
	time.Sleep(100 * time.Millisecond)
	if !alive(pid) || !alive(child) || stopped(pid) || stopped(child) {
		t.Fatalf("everything kill stopped must be resumed: pid %v/%v child %v/%v", alive(pid), stopped(pid), alive(child), stopped(child))
	}
}

// TestStoppedHolderShownAndRecoveredByRerun: a kill that dies inside its
// window (here at the kill-stopped crash point) leaves the old incoda and
// its job stopped, still holding the lane. status flags it with the exact
// rerun line, and rerunning kill --force ends it.
func TestStoppedHolderShownAndRecoveredByRerun(t *testing.T) {
	runnerSentinel(t)
	incoda, _ := binaries(t)
	bin := crashBinary(t)
	state := t.TempDir()
	_, pid, child, _ := oldHolder(t, state, filepath.Join(state, "queues"), "stp")
	p := strconv.Itoa(pid)
	out, code := runWithEnv(t, bin, state, []string{"INCODA_TEST_CRASH_AT=kill-stopped"},
		"kill", "--queue", "stp", "--pid", p, "--reason", "test", "--wait", "0", "--force")
	if code != 97 {
		t.Fatalf("the crash binary must die inside the window: %d\n%s", code, out)
	}
	if !stopped(pid) || !stopped(child) {
		t.Fatal("the interrupted kill leaves the old incoda and its job stopped")
	}
	out, code = runIncoda(t, incoda, state, "status", "--queue", "stp", "--no-color")
	want := "stopped holder: pid " + p + "; a kill was interrupted; rerun: incoda kill --queue stp --pid " + p + " --reason 'resume interrupted kill' --force\n" +
		"  or resume it instead: kill -CONT " + p + "\n"
	if code != 0 || !strings.HasSuffix(out, want) {
		t.Fatalf("want %q at the end of status, got %d:\n%s", want, code, out)
	}
	out, code = doctor(t, incoda, state)
	if code != 0 || !strings.Contains(out, "attention: "+strings.Split(want, "\n")[0]+"\n") ||
		!strings.Contains(out, "attention:   or resume it instead: kill -CONT "+p+"\n") {
		t.Fatalf("doctor must flag the stopped holder, got %d:\n%s", code, out)
	}
	out, code = runIncoda(t, incoda, state, "kill", "--queue", "stp", "--pid", p, "--reason", "resume interrupted kill", "--wait", "0", "--force")
	if code != 0 {
		t.Fatalf("the rerun must recover: %d\n%s", code, out)
	}
	waitGone(t, "the stopped job", pid, child)
}
