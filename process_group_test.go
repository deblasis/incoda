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
)

var (
	treeOnce sync.Once
	treeBin  string
	treeErr  error
)

// treeBinary builds internal/testprog/tree next to the incoda test binary.
func treeBinary(t *testing.T) string {
	t.Helper()
	incoda, _ := binaries(t)
	treeOnce.Do(func() {
		treeBin = filepath.Join(filepath.Dir(incoda), "tree")
		cmd := exec.Command("go", "build", "-o", treeBin, "./internal/testprog/tree")
		cmd.Env = append(os.Environ(), "GOTOOLCHAIN=auto")
		if out, err := cmd.CombinedOutput(); err != nil {
			treeErr = fmt.Errorf("build tree: %v\n%s", err, out)
		}
	})
	if treeErr != nil {
		t.Fatal(treeErr)
	}
	return treeBin
}

type treeInfo struct{ pid, ppid, pgid, grandchild int }

// readTree waits for the tree program's report file and parses it.
func readTree(t *testing.T, path string) treeInfo {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		b, err := os.ReadFile(path)
		if err == nil {
			var ti treeInfo
			for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
				k, v, _ := strings.Cut(line, " ")
				n, _ := strconv.Atoi(v)
				switch k {
				case "pid":
					ti.pid = n
				case "ppid":
					ti.ppid = n
				case "pgid":
					ti.pgid = n
				case "grandchild":
					ti.grandchild = n
				}
			}
			return ti
		}
		if time.Now().After(deadline) {
			t.Fatalf("tree never wrote %s", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// gone reports whether pid no longer exists, waiting up to d.
func gone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// killPid signals pid if it is positive. A zero or negative pid is a tree
// that never got reported, not something to signal.
func killPid(pid int) {
	if pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// killTreeSafe tears a reported tree down for test cleanup. It only signals
// the process group when pgid is confirmed to be the child's own (pgid ==
// pid and positive): a kill(-pgid) run against any other value, including
// the exact bug this file guards against, where the child never left its
// parent's group, would signal a foreign group. In that case the group is
// shared with whatever started the test binary itself, so it falls back to
// signalling the reported pids individually instead.
func killTreeSafe(ti treeInfo) {
	if ti.pgid > 0 && ti.pgid == ti.pid {
		_ = syscall.Kill(-ti.pgid, syscall.SIGKILL)
		return
	}
	killPid(ti.pid)
	killPid(ti.grandchild)
}

// TestTopLevelChildOwnsItsGroup: a top-level run puts its child in a new
// process group, so the child's pgid is its own pid.
func TestTopLevelChildOwnsItsGroup(t *testing.T) {
	incoda, _ := binaries(t)
	tree := treeBinary(t)
	state := t.TempDir()
	out := filepath.Join(t.TempDir(), "tree.txt")

	holder := exec.Command(incoda, "run", "--queue", "pg", "--quiet", "--poll", "50ms", "--", tree, out)
	holder.Env = laneEnv(state)
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Process.Kill(); _ = holder.Wait() }()

	ti := readTree(t, out)
	defer func() { killTreeSafe(ti) }()
	if ti.pgid != ti.pid {
		t.Fatalf("a top-level run's child must lead its own process group: pid %d pgid %d", ti.pid, ti.pgid)
	}
}

// TestKillReachesGrandchild is the regression test for the shipped bug:
// `incoda kill` on a holder whose child spawned a grandchild ends both.
func TestKillReachesGrandchild(t *testing.T) {
	incoda, _ := binaries(t)
	tree := treeBinary(t)
	state := t.TempDir()
	out := filepath.Join(t.TempDir(), "tree.txt")

	holder := exec.Command(incoda, "run", "--queue", "pgk", "--quiet", "--poll", "50ms", "--", tree, out)
	holder.Env = laneEnv(state)
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	// If the test fails before the kill step below reaps the holder itself
	// (readTree times out, the kill command fails), this cleans it up. Once
	// the kill step has called holder.Wait() itself, holderWaited skips this:
	// a second Wait on the same *exec.Cmd is an error, not a safety net.
	holderWaited := false
	defer func() {
		if holderWaited {
			return
		}
		_ = holder.Process.Kill()
		_ = holder.Wait()
	}()
	ti := readTree(t, out)
	defer func() { killPid(ti.grandchild) }()

	msg, code := runIncoda(t, incoda, state, "kill", "--queue", "pgk", "--pid", strconv.Itoa(holder.Process.Pid), "--reason", "test")
	if code != 0 {
		t.Fatalf("kill: exit %d\n%s", code, msg)
	}
	waitErr := holder.Wait()
	holderWaited = true
	if got := exitCodeOf(waitErr); got != 124 {
		t.Fatalf("killed holder exits 124, got %d", got)
	}
	if !gone(ti.pid, 5*time.Second) {
		t.Fatalf("child pid %d survived the kill", ti.pid)
	}
	if !gone(ti.grandchild, 5*time.Second) {
		t.Fatalf("grandchild pid %d survived the kill: the child was not in its own process group", ti.grandchild)
	}
}

// TestNestedChildStaysInOuterGroup: a nested run whose outer incoda is alive
// keeps its child in the outer group, so the outer tree kill reaches it.
func TestNestedChildStaysInOuterGroup(t *testing.T) {
	incoda, _ := binaries(t)
	tree := treeBinary(t)
	state := t.TempDir()
	out := filepath.Join(t.TempDir(), "tree.txt")
	cmd := exec.Command(incoda, "run", "--queue", "outer", "--quiet", "--",
		incoda, "run", "--queue", "inner", "--quiet", "--", tree, out)
	cmd.Env = laneEnv(state)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	ti := readTree(t, out)
	defer func() { killTreeSafe(ti) }()
	// The inner incoda is tree's parent and leads the group the outer run
	// opened; tree must sit in that group, not in one of its own.
	if ti.pgid != ti.ppid {
		t.Fatalf("nested child should stay in the outer group (pgid %d) but has pgid %d", ti.ppid, ti.pgid)
	}
}
