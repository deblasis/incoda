package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/machine"
)

type oldBuild struct {
	once sync.Once
	path string
	err  error
}

var oldBuilds sync.Map // tag -> *oldBuild

// oldBinary builds incoda at tag once per test run: `git archive <tag>`
// piped into `tar -x` in a temp directory, then `go build` there. It never
// creates a worktree and never writes to the repository's .git. A failed
// build (offline, no module cache, no git or tar) skips the test.
func oldBinary(t *testing.T, tag string) string {
	t.Helper()
	incoda, _ := binaries(t)
	v, _ := oldBuilds.LoadOrStore(tag, &oldBuild{})
	ob := v.(*oldBuild)
	ob.once.Do(func() {
		src := filepath.Join(filepath.Dir(incoda), "src-"+tag)
		if err := os.MkdirAll(src, 0o755); err != nil {
			ob.err = err
			return
		}
		archive := exec.Command("git", "archive", tag)
		untar := exec.Command("tar", "-x", "-C", src)
		pipe, err := archive.StdoutPipe()
		if err != nil {
			ob.err = err
			return
		}
		untar.Stdin = pipe
		var archiveErrs, untarErrs strings.Builder
		archive.Stderr, untar.Stderr = &archiveErrs, &untarErrs
		if err := untar.Start(); err != nil {
			ob.err = err
			return
		}
		if err := archive.Run(); err != nil {
			_ = untar.Wait()
			ob.err = fmt.Errorf("git archive %s: %v\n%s%s", tag, err, archiveErrs.String(), untarErrs.String())
			return
		}
		if err := untar.Wait(); err != nil {
			ob.err = fmt.Errorf("tar: %v\n%s%s", err, archiveErrs.String(), untarErrs.String())
			return
		}
		ob.path = filepath.Join(filepath.Dir(incoda), "incoda-"+tag+filepath.Ext(incoda))
		build := exec.Command("go", "build", "-o", ob.path, ".")
		build.Dir = src
		build.Env = append(os.Environ(), "GOTOOLCHAIN=auto", "GOFLAGS=-mod=mod")
		if out, err := build.CombinedOutput(); err != nil {
			ob.err = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if ob.err != nil {
		t.Skipf("cannot build incoda %s (offline, no module cache, or not run from a git checkout?): %v", tag, ob.err)
	}
	return ob.path
}

// refusedFence reports whether out is an older incoda refusing to work on a
// state directory whose queues/ is the migration fence. The exit code is the
// contract (122); the text is whatever the OS said about the path, which
// differs between Unix (ENOTDIR, "not a directory") and Windows
// (ERROR_PATH_NOT_FOUND, "cannot find the path").
func refusedFence(out string) bool {
	for _, s := range []string{
		"not a directory",
		"cannot find the path specified",
		"cannot find the path",
		"not a directory.",
	} {
		if strings.Contains(out, s) {
			return true
		}
	}
	return false
}

func countTicketsIn(dir string) int {
	entries, _ := os.ReadDir(dir)
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".ticket") {
			n++
		}
	}
	return n
}

func waitForTicket(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for countTicketsIn(dir) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no ticket appeared in %s", dir)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestOldBinariesStopAtTheFence: on a migrated state directory every
// command of v0.2.0 and v0.6.0 that touches state exits 122 with "not a
// directory" and writes nothing, with or without INCODA_HELD.
func TestOldBinariesStopAtTheFence(t *testing.T) {
	incoda, _ := binaries(t)
	for _, tc := range []struct {
		tag  string
		cmds [][]string
	}{
		{"v0.2.0", [][]string{
			{"run", "--queue", "seed", "--", "true"},
			{"status", "--queue", "seed"},
			{"status", "--json", "--all"},
			{"watch", "--once", "--queue", "seed"},
			{"queues"},
			{"force-release", "--queue", "seed"},
			{"doctor"},
		}},
		{"v0.6.0", [][]string{
			{"run", "--queue", "seed", "--", "true"},
			{"status", "--queue", "seed"},
			{"status", "--json", "--all"},
			{"watch", "--once", "--queue", "seed"},
			{"queues"},
			{"config", "seed", "--slots", "2"},
			{"kill", "--queue", "seed", "--pid", "1", "--reason", "r"},
			{"force-release", "--queue", "seed"},
			{"doctor"},
		}},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			old := oldBinary(t, tc.tag)
			state := t.TempDir()
			if out, code := runIncoda(t, incoda, state, "config", "seed", "--slots", "1"); code != 0 {
				t.Fatalf("migrate: %d\n%s", code, out)
			}
			before := treeState(t, state)
			for _, args := range tc.cmds {
				for _, held := range []string{"", "seed"} {
					cmd := exec.Command(old, args...)
					cmd.Env = laneEnv(state)
					if held != "" {
						cmd.Env = append(cmd.Env, "INCODA_HELD="+held)
					}
					out, err := cmd.CombinedOutput()
					if code := exitCodeOf(err); code != 122 {
						t.Fatalf("%s %v (INCODA_HELD=%q): want exit 122, got %d:\n%s", tc.tag, args, held, code, out)
					}
					// The old binary refuses because the fence is a file
					// where its layout wants a directory. Exit 122 is the
					// contract; the wording is the OS's (ENOTDIR says "not a
					// directory" on Unix, "cannot find the path" on
					// Windows), so assert the refusal, not one shell's text.
					if args[0] != "doctor" && !refusedFence(string(out)) {
						t.Fatalf("%s %v: want the not-a-directory refusal:\n%s", tc.tag, args, out)
					}
				}
			}
			if after := treeState(t, state); after != before {
				t.Fatalf("an old binary wrote to a migrated state directory:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

// TestMigrationWaitsForAnOldRun (M2): the migration waits for a live run of
// an older incoda on the old layout, and the new run's job starts only
// after the old job ended.
func TestMigrationWaitsForAnOldRun(t *testing.T) {
	incoda, stamp := binaries(t)
	for _, tag := range []string{"v0.2.0", "v0.6.0"} {
		t.Run(tag, func(t *testing.T) {
			old := oldBinary(t, tag)
			state := t.TempDir()
			stamps := t.TempDir()
			o := exec.Command(old, "run", "--queue", "oldq", "--poll", "50ms", "--quiet", "--", stamp, filepath.Join(stamps, "old.txt"), "old", "1500")
			o.Env = laneEnv(state)
			if err := o.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = o.Process.Kill(); _ = o.Wait() }()
			waitForTicket(t, filepath.Join(state, "queues", "oldq"))

			// A pool, so the run needs no link: it is the command that
			// migrates.
			out, code := runIncoda(t, incoda, state, "run", "--queue", "builds", "--wait", "60s", "--poll", "50ms", "--", stamp, filepath.Join(stamps, "new.txt"), "new", "10")
			if code != 0 {
				t.Fatalf("new run: exit %d\n%s", code, out)
			}
			for _, want := range []string{
				"incoda: upgrade-wait: state upgrade waits for 1 run(s) by an older incoda:\n",
				fmt.Sprintf("incoda:   oldq pid %d: ", o.Process.Pid),
				// The stop line is rendered for the shell the user is in,
				// so PowerShell gets quoted words (machine.KillLine).
				fmt.Sprintf("incoda:   %s\n", machine.KillLine("oldq", o.Process.Pid, machine.UpgradeReason, false)),
			} {
				if !strings.Contains(out, want) {
					t.Fatalf("missing %q in:\n%s", want, out)
				}
			}
			if err := o.Wait(); err != nil {
				t.Fatalf("the old run must finish normally: %v", err)
			}
			oldIv, ok1 := readInterval(t, filepath.Join(stamps, "old.txt"))
			newIv, ok2 := readInterval(t, filepath.Join(stamps, "new.txt"))
			if !ok1 || !ok2 || newIv.enter < oldIv.exit {
				t.Fatalf("the new job overlapped the old one: old %+v new %+v", oldIv, newIv)
			}
			if !machine.FencePlaced(state) {
				t.Fatal("the migration did not place the fence")
			}
		})
	}
}

// TestMigrationWaitsForAnOldRunThatSlipsIn (M5): an older incoda starts a
// run after the idle check and before the fence; the swap carries its
// ticket into lanes/, and the migration waits for it with its
// stop line before anything new runs.
func TestMigrationWaitsForAnOldRunThatSlipsIn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot rename queues/ while the old run has a file open in it, so the migration waits for that run in M2 instead (TestM4NotIdleGoesBackToM2)")
	}
	_, stamp := binaries(t)
	bin := crashBinary(t)
	old := oldBinary(t, "v0.6.0")
	state := t.TempDir()
	stamps := t.TempDir()
	if out, code := runIncoda(t, old, state, "run", "--queue", "seed", "--quiet", "--", stamp, filepath.Join(stamps, "seed.txt"), "seed", "1"); code != 0 {
		t.Fatalf("seed the old layout: %d\n%s", code, out)
	}

	pause := filepath.Join(t.TempDir(), "go")
	// A pool, so the run needs no link: it is the command that migrates.
	m := exec.Command(bin, "run", "--queue", "builds", "--wait", "60s", "--poll", "50ms", "--", stamp, filepath.Join(stamps, "new.txt"), "new", "10")
	m.Env = append(laneEnv(state), "INCODA_TEST_PAUSE_AT=M3", "INCODA_TEST_PAUSE_FILE="+pause)
	var mErr syncBuffer
	m.Stderr = &mErr
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Process.Kill(); _ = m.Wait() }()
	waitForFile(t, pause+".reached")

	o := exec.Command(old, "run", "--queue", "slip", "--poll", "50ms", "--quiet", "--", stamp, filepath.Join(stamps, "old.txt"), "old", "1500")
	o.Env = laneEnv(state)
	if err := o.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = o.Process.Kill(); _ = o.Wait() }()
	waitForTicket(t, filepath.Join(state, "queues", "slip"))
	if err := os.WriteFile(pause, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	waitForText(t, &mErr, fmt.Sprintf("incoda:   %s\n", machine.KillLine("slip", o.Process.Pid, machine.UpgradeReason, false)))
	if !machine.FencePlaced(state) {
		t.Fatal("M5 waits behind the fence")
	}
	_ = o.Wait()
	if err := m.Wait(); err != nil {
		t.Fatalf("the migrating run must finish: %v\n%s", err, mErr.String())
	}
	oldIv, ok1 := readInterval(t, filepath.Join(stamps, "old.txt"))
	newIv, ok2 := readInterval(t, filepath.Join(stamps, "new.txt"))
	if !ok1 || !ok2 || newIv.enter < oldIv.exit {
		t.Fatalf("the new job overlapped the slipped-in old one: old %+v new %+v", oldIv, newIv)
	}
	if _, err := os.Stat(filepath.Join(laneDir(state, "slip"), "lane.log")); err != nil {
		t.Fatalf("the slipped-in lane moved to lanes/: %v", err)
	}
}

// TestBlockedWaiterExitsUpgradeBlocked (spec 3.1): an old run's job starts
// a new-binary run while the migrator holds machine.lock waiting for that
// same old run. The new run finds its own ancestor in the note's blockers
// and refuses at once; the old run passes the 120 through and ends, and
// the migration completes.
func TestBlockedWaiterExitsUpgradeBlocked(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no ancestry walk on Windows; there the waiter times out instead")
	}
	incoda, stamp := binaries(t)
	old := oldBinary(t, "v0.6.0")
	state := t.TempDir()
	script := `while ! grep -q blockers= "$1/machine.lock" 2>/dev/null; do sleep 0.05; done; exec "$2" run --queue inner --wait 30s -- true`
	o := exec.Command(old, "run", "--queue", "outer", "--poll", "50ms", "--", "sh", "-c", script, "sh", state, incoda)
	o.Env = laneEnv(state)
	var oOut syncBuffer
	o.Stdout, o.Stderr = &oOut, &oOut
	if err := o.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = o.Process.Kill(); _ = o.Wait() }()
	waitForTicket(t, filepath.Join(state, "queues", "outer"))

	// A pool, so the run needs no link: it is the command that migrates.
	out, code := runIncoda(t, incoda, state, "run", "--queue", "builds", "--wait", "60s", "--poll", "50ms", "--", stamp, filepath.Join(t.TempDir(), "m.txt"), "m", "10")
	if code != 0 {
		t.Fatalf("the migrator must finish once the old run ends: %d\n%s", code, out)
	}
	err := o.Wait()
	if c := exitCodeOf(err); c != 120 {
		t.Fatalf("the old run passes its child's 120 through, got %d:\n%s", c, oOut.String())
	}
	want := `incoda: upgrade-blocked: an older incoda (pid ` + strconv.Itoa(o.Process.Pid) + `, an ancestor of this process) holds "outer"; rerun the outer command after it exits`
	if !strings.Contains(oOut.String(), want) {
		t.Fatalf("missing %q in:\n%s", want, oOut.String())
	}
}
