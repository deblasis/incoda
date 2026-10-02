package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/deblasis/incoda/internal/machine"
)

var (
	crashOnce sync.Once
	crashBin  string
	crashErr  error
)

// crashBinary builds incoda with the incoda_crashpoints tag next to the
// regular test binary. Release builds never carry the tag.
func crashBinary(t *testing.T) string {
	t.Helper()
	incoda, _ := binaries(t)
	crashOnce.Do(func() {
		crashBin = filepath.Join(filepath.Dir(incoda), "incoda-crash"+filepath.Ext(incoda))
		cmd := exec.Command("go", "build", "-tags", "incoda_crashpoints", "-o", crashBin, ".")
		cmd.Env = append(os.Environ(), "GOTOOLCHAIN=auto")
		if out, err := cmd.CombinedOutput(); err != nil {
			crashErr = fmt.Errorf("build the crashpoint binary: %v\n%s", err, out)
		}
	})
	if crashErr != nil {
		t.Fatal(crashErr)
	}
	return crashBin
}

// describeLayout names what lstat finds at the five names the migration
// uses, in the words of the recovery table.
func describeLayout(state string) string {
	kind := func(name string) string {
		fi, err := os.Lstat(filepath.Join(state, name))
		switch {
		case err != nil:
			return "-"
		case fi.IsDir():
			return "dir"
		case fi.Mode().IsRegular():
			return "file"
		default:
			return "other"
		}
	}
	return fmt.Sprintf("queues=%s queues.new=%s lanes=%s migration.json=%s machine.json=%s",
		kind("queues"), kind("queues.new"), kind("lanes"), kind("migration.json"), kind("machine.json"))
}

func runWithEnv(t *testing.T, bin, state string, extra []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(laneEnv(state), extra...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), exitCodeOf(err)
	}
	return string(out), 0
}

// TestCrashAtEveryStepRecovers: the crash binary dies right after a step;
// the state is the recovery-table row for that step; the next ordinary
// mutating command finishes the migration.
func TestCrashAtEveryStepRecovers(t *testing.T) {
	bin := crashBinary(t)
	incoda, _ := binaries(t)
	for _, tc := range []struct {
		name       string
		seeded     bool
		step       string
		noExchange bool
		swapOnly   bool
		want       string
	}{
		{name: "row 1 not started", seeded: true, step: "locked",
			want: "queues=dir queues.new=- lanes=- migration.json=- machine.json=-"},
		{name: "row 2 crashed in M3", seeded: true, step: "M3",
			want: "queues=dir queues.new=- lanes=- migration.json=file machine.json=-"},
		{name: "row 2 crashed in M4 before the fence", seeded: true, step: "M4-new",
			want: "queues=dir queues.new=file lanes=- migration.json=file machine.json=-"},
		{name: "row 3 crashed between swap and rename", seeded: true, step: "M4-swapped", swapOnly: true,
			want: "queues=file queues.new=dir lanes=- migration.json=file machine.json=-"},
		{name: "row 4 fallback crashed before the fence", seeded: true, step: "M4-moved", noExchange: true,
			want: "queues=- queues.new=file lanes=dir migration.json=file machine.json=-"},
		{name: "row 4 empty-dir path crashed before the fence", step: "M4-lanes",
			want: "queues=- queues.new=file lanes=dir migration.json=file machine.json=-"},
		{name: "row 5 empty-dir path crashed after M3", step: "M3",
			want: "queues=- queues.new=- lanes=- migration.json=file machine.json=-"},
		{name: "row 5 empty-dir path crashed in M4", step: "M4-new",
			want: "queues=- queues.new=file lanes=- migration.json=file machine.json=-"},
		{name: "row 7 crashed after the fence", seeded: true, step: "M4",
			want: "queues=file queues.new=- lanes=dir migration.json=file machine.json=-"},
		{name: "row 7 crashed in M5", seeded: true, step: "M5",
			want: "queues=file queues.new=- lanes=dir migration.json=file machine.json=-"},
		{name: "row 7 crashed in M6", seeded: true, step: "M6",
			want: "queues=file queues.new=- lanes=dir migration.json=file machine.json=-"},
		{name: "row 7 crashed in M7", seeded: true, step: "M7",
			want: "queues=file queues.new=- lanes=dir migration.json=file machine.json=-"},
		{name: "row 8 crashed after commit", seeded: true, step: "M8-registry",
			want: "queues=file queues.new=- lanes=dir migration.json=file machine.json=file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.swapOnly && runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
				t.Skip("no atomic exchange on " + runtime.GOOS + ", so no swap to crash after")
			}
			state := t.TempDir()
			if tc.seeded {
				seedOldLayout(t, state)
			}
			env := []string{"INCODA_TEST_CRASH_AT=" + tc.step}
			if tc.noExchange {
				env = append(env, "INCODA_TEST_NO_EXCHANGE=1")
			}
			out, code := runWithEnv(t, bin, state, env, "config", "alpha", "--slots", "2")
			if code == 0 && tc.swapOnly {
				t.Skip("this filesystem has no atomic exchange; the migration took the fallback")
			}
			if code != 97 {
				t.Fatalf("want the crash exit 97 at %s, got %d:\n%s", tc.step, code, out)
			}
			if got := describeLayout(state); got != tc.want {
				t.Fatalf("after a crash at %s:\n got %s\nwant %s", tc.step, got, tc.want)
			}
			out, code = runIncoda(t, incoda, state, "config", "alpha")
			if code != 0 {
				t.Fatalf("recovery: exit %d\n%s", code, out)
			}
			assertLayout2(t, state, tc.seeded)
		})
	}
}

// TestRecoveryRow6FenceWithoutLanes: a fence file with no lanes/ and no
// machine.json is not produced by any step order; recovery creates lanes/
// and finishes from M5.
func TestRecoveryRow6FenceWithoutLanes(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, "queues"), []byte(machine.FenceText), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := runIncoda(t, incoda, state, "config", "alpha"); code != 0 {
		t.Fatalf("recovery: exit %d\n%s", code, out)
	}
	assertLayout2(t, state, false)
}

// TestRecoveryRow9RegistryLost: lanes/ with neither machine.json nor
// migration.json is a lost registry, not a crash: the fence is re-placed if
// missing, then every mutating command fails closed.
func TestRecoveryRow9RegistryLost(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "alpha"); code != 0 {
		t.Fatalf("config: %d\n%s", code, out)
	}
	if err := os.Remove(machine.RegistryPath(state)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(state, "queues")); err != nil {
		t.Fatal(err)
	}
	out, code := runIncoda(t, incoda, state, "config", "alpha")
	if code != 122 || !strings.Contains(out, "incoda: machine-state: machine.json: missing while lanes/ exists; run incoda doctor") {
		t.Fatalf("want exit 122, got %d:\n%s", code, out)
	}
	if got := describeLayout(state); got != "queues=file queues.new=- lanes=dir migration.json=- machine.json=-" {
		t.Fatalf("layout %s", got)
	}
}

// TestReleaseBuildIgnoresCrashpoints: the crash variables do nothing to a
// binary built without the tag.
func TestReleaseBuildIgnoresCrashpoints(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	seedOldLayout(t, state)
	out, code := runWithEnv(t, incoda, state, []string{"INCODA_TEST_CRASH_AT=M3", "INCODA_TEST_NO_EXCHANGE=1"}, "config", "alpha")
	if code != 0 {
		t.Fatalf("the release build must not stop at a crash point: %d\n%s", code, out)
	}
	assertLayout2(t, state, true)
}

// TestFenceRacesSendANewQueuesDirToStrays: an older incoda's first run
// creates queues/ and takes a ticket there inside the window before the
// fence is placed (the rename fallback, and the empty-dir path). The race
// rule moves that queues/ to strays/, the fence goes in, M5 waits for the
// live stray ticket with its stop line, and M6 merges it into lanes/.
func TestFenceRacesSendANewQueuesDirToStrays(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot rename a directory with an open file inside; there the migration waits for that run before placing the fence (TestPlaceFenceNotIdleWhereADirectoryCannotMove)")
	}
	bin := crashBinary(t)
	_, stamp := binaries(t)
	for _, tc := range []struct {
		name    string
		seeded  bool
		pauseAt string
	}{
		{"rename fallback", true, "M4-moved"},
		{"old and new first run on an empty directory", false, "M4-lanes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			if tc.seeded {
				seedOldLayout(t, state)
			}
			pause := filepath.Join(t.TempDir(), "go")
			cmd := exec.Command(bin, "run", "--queue", "newq", "--wait", "60s", "--poll", "50ms", "--", stamp, filepath.Join(t.TempDir(), "s"), "s", "1")
			cmd.Env = append(laneEnv(state), "INCODA_TEST_NO_EXCHANGE=1", "INCODA_TEST_PAUSE_AT="+tc.pauseAt, "INCODA_TEST_PAUSE_FILE="+pause)
			var errBuf syncBuffer
			cmd.Stderr = &errBuf
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			waitForFile(t, pause+".reached")
			release := holdOldTicket(t, filepath.Join(state, "queues"), "late", 999998, "old", "job")
			if err := os.WriteFile(pause, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			waitForText(t, &errBuf, "incoda:   incoda kill --queue late --pid 999998 --reason 'incoda upgrade'\n")
			if !machine.FencePlaced(state) {
				t.Fatal("the fence must be in place while M5 waits")
			}
			if batches, _ := os.ReadDir(machine.StraysDir(state)); len(batches) != 1 {
				t.Fatalf("want the late queues/ in one strays batch, got %v", batches)
			}
			release()
			if err := cmd.Wait(); err != nil {
				t.Fatalf("migration did not finish: %v\n%s", err, errBuf.String())
			}
			assertLayout2(t, state, tc.seeded)
			if _, err := os.Stat(filepath.Join(laneDir(state, "late"), "registry.lock")); err != nil {
				t.Fatal("M6 must merge the stray lane into lanes/late")
			}
		})
	}
}
