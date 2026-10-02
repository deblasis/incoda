package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/machine"
)

// doctorEnv is laneEnv with PATH set to path. doctor runs every incoda it
// finds on PATH (its version probe), so no test may let it see the PATH
// the tests run with.
func doctorEnv(state, path string) []string {
	var env []string
	for _, kv := range laneEnv(state) {
		if k, _, _ := strings.Cut(kv, "="); strings.EqualFold(k, "PATH") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "PATH="+path)
}

// doctor runs incoda doctor with an empty PATH.
func doctor(t *testing.T, incoda, state string, args ...string) (string, int) {
	t.Helper()
	return doctorWithPath(t, incoda, state, t.TempDir(), args...)
}

func doctorWithPath(t *testing.T, incoda, state, path string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(incoda, append([]string{"doctor", "--no-color"}, args...)...)
	cmd.Env = doctorEnv(state, path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), exitCodeOf(err)
	}
	return string(out), 0
}

func mustContain(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Fatalf("missing %q in:\n%s", w, out)
		}
	}
}

func TestDoctorReportsTheLayout(t *testing.T) {
	incoda, _ := binaries(t)
	migrated := func(t *testing.T) string {
		state := t.TempDir()
		seedOldLayout(t, state)
		if out, code := runIncoda(t, incoda, state, "config", "alpha"); code != 0 {
			t.Fatalf("config: %d\n%s", code, out)
		}
		return state
	}
	const failClosed = "incoda: machine-state: 1 problem(s) make runs fail closed; see the problem: lines above"

	t.Run("fresh", func(t *testing.T) {
		out, code := doctor(t, incoda, t.TempDir())
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		mustContain(t, out, "layout:    none yet (the next mutating incoda command creates layout 2)\n", "attention: INCODA_DIR is set")
	})
	t.Run("not upgraded", func(t *testing.T) {
		state := t.TempDir()
		seedOldLayout(t, state)
		out, code := doctor(t, incoda, state)
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		mustContain(t, out, "layout:    1 (queues/)\n", "attention: state not upgraded yet: the next mutating incoda command upgrades it\n")
		if _, err := os.Lstat(machine.RegistryPath(state)); !os.IsNotExist(err) {
			t.Fatal("doctor must never migrate")
		}
	})
	t.Run("migrated", func(t *testing.T) {
		out, code := doctor(t, incoda, migrated(t))
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		mustContain(t, out, "layout:    2 (machine.json schema 1, generation 1; pools builds, computer-use, tests, vm)\n",
			"fence:     present\n", `attention: queue "bad" has an unreadable config.json: `)
	})
	t.Run("fence missing", func(t *testing.T) {
		state := migrated(t)
		if err := os.Remove(filepath.Join(state, "queues")); err != nil {
			t.Fatal(err)
		}
		out, code := doctor(t, incoda, state)
		if code != 122 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		mustContain(t, out, "fence:     missing\n", "problem:   fence missing: ", failClosed)
		if machine.FencePlaced(state) {
			t.Fatal("doctor must never re-fence")
		}
	})
	t.Run("malformed registry", func(t *testing.T) {
		state := migrated(t)
		if err := os.WriteFile(machine.RegistryPath(state), []byte("{"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, code := doctor(t, incoda, state)
		if code != 122 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		mustContain(t, out, "problem:   machine.json: unexpected end of JSON input; run incoda doctor\n", failClosed)
	})
	t.Run("newer registry", func(t *testing.T) {
		state := migrated(t)
		if err := os.WriteFile(machine.RegistryPath(state), []byte(`{"schema":1,"layout":3,"generation":1,"pools":[]}`), 0o644); err != nil {
			t.Fatal(err)
		}
		out, code := doctor(t, incoda, state)
		if code != 122 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		mustContain(t, out, "problem:   machine.json was written by a newer incoda; upgrade this one (")
	})
	t.Run("unfinished migration", func(t *testing.T) {
		state := migrated(t)
		if err := os.Remove(machine.RegistryPath(state)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, "migration.json"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, code := doctor(t, incoda, state)
		if code != 122 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		mustContain(t, out, "problem:   migration unfinished (stopped in M5 to M7); the next mutating incoda command resumes it\n")
	})
	t.Run("plan left after the commit", func(t *testing.T) {
		state := migrated(t)
		if err := os.WriteFile(filepath.Join(state, "migration.json"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, code := doctor(t, incoda, state)
		if code != 122 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		mustContain(t, out, "problem:   migration unfinished (stopped after the commit); the next mutating incoda command deletes migration.json\n")
	})
	t.Run("registry lost", func(t *testing.T) {
		state := migrated(t)
		if err := os.Remove(machine.RegistryPath(state)); err != nil {
			t.Fatal(err)
		}
		out, code := doctor(t, incoda, state)
		if code != 122 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		mustContain(t, out, "problem:   machine.json: missing while lanes/ exists; nothing re-creates it on its own. Lanes: alpha, bad, builds, cap-gate, computer-use, old, tests, vm. A human decides which of them are pools and runs: incoda doctor --rebuild-registry POOL,POOL\n")
	})
}

func TestDoctorRebuildRegistry(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "x"); code != 0 {
		t.Fatalf("config: %d\n%s", code, out)
	}
	if err := os.Remove(machine.RegistryPath(state)); err != nil {
		t.Fatal(err)
	}
	if out, code := doctor(t, incoda, state, "--rebuild-registry", "a/b"); code != 120 || !strings.Contains(out, `incoda: rebuild-registry: queue key "a/b" contains`) {
		t.Fatalf("an invalid key is a usage refusal: %d\n%s", code, out)
	}
	release := holdOldTicket(t, filepath.Join(state, "lanes"), "x", 999997, "busy")
	if out, code := doctor(t, incoda, state, "--rebuild-registry", "builds"); code != 120 || !strings.Contains(out, `incoda: kind-busy: "x" has live tickets`) {
		t.Fatalf("a live ticket refuses the rebuild: %d\n%s", code, out)
	}
	release()
	out, code := doctor(t, incoda, state, "--rebuild-registry", "builds,tests")
	if code != 0 {
		t.Fatalf("rebuild: exit %d\n%s", code, out)
	}
	mustContain(t, out, "rebuild-registry: builds: pool\n", "rebuild-registry: computer-use: project\n",
		"rebuild-registry: tests: pool\n", "rebuild-registry: x: project\n", "rebuild-registry: wrote machine.json (generation ")
	reg, err := machine.ReadRegistry(state)
	if err != nil || strings.Join(reg.Pools, ",") != "builds,tests" {
		t.Fatalf("registry %+v %v", reg, err)
	}
}

// TestDoctorEscapesProblemsOnce guards against double escaping: textsafe.Escape
// is not idempotent (a\b -> a\\b, escaped again -> a\\\\b). The unreadable-config
// error message is already escaped once inside internal/machine; doctor must
// print it as is, not escape it a second time. A backslash in the state dir
// path is a convenient way to get a literal backslash into that message; it
// is an ordinary filename character everywhere except Windows, where it is a
// path separator, so this test is skipped there.
func TestDoctorEscapesProblemsOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a backslash is a path separator on windows")
	}
	incoda, _ := binaries(t)
	state := filepath.Join(t.TempDir(), `a\b`)
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	seedOldLayout(t, state)
	if out, code := runIncoda(t, incoda, state, "config", "alpha"); code != 0 {
		t.Fatalf("config: %d\n%s", code, out)
	}
	out, code := doctor(t, incoda, state)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !strings.Contains(out, `a\\b`) {
		t.Fatalf("expected the state dir's backslash escaped exactly once (a\\\\b) in:\n%s", out)
	}
	if strings.Contains(out, `a\\\\b`) {
		t.Fatalf("the backslash was escaped twice:\n%s", out)
	}
}

func TestDoctorRefusesBadFlagCombinations(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	if out, code := doctor(t, incoda, state, "--rebuild-registry="); code != 120 ||
		!strings.Contains(out, "incoda: rebuild-registry: name at least one pool, for example builds,computer-use,tests,vm\n") {
		t.Fatalf("an empty --rebuild-registry is refused: %d\n%s", code, out)
	}
	if out, code := doctor(t, incoda, state, "--wait", "5s"); code != 120 ||
		!strings.Contains(out, "incoda: doctor: --wait applies only to --rebuild-registry\n") {
		t.Fatalf("--wait alone is refused: %d\n%s", code, out)
	}
}

// TestDoctorReportsStraysOrphansAndUnpooledHolders: doctor deletes the
// stray lanes whose tickets all died, lists what is left and every orphan
// record, and names the live unpooled holders as attention items.
func TestDoctorReportsStraysOrphansAndUnpooledHolders(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "seed"); code != 0 {
		t.Fatalf("migrate: %d\n%s", code, out)
	}
	holdOldTicket(t, filepath.Join(machine.StraysDir(state), "1"), "builds", 999999, "zig", "build")
	holdOldTicket(t, filepath.Join(machine.StraysDir(state), "2"), "done", 999997, "x")()
	if err := os.MkdirAll(machine.OrphansDir(state), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(machine.OrphansDir(state), "999990-1.orphan"),
		[]byte(`{"key":"tests","pid":999990,"descendants":[],"groups":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(machine.OrphansDir(state), "junk.orphan"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := doctor(t, incoda, state)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	mustContain(t, out,
		"strays:    1/builds: 1 live ticket(s)\n",
		"orphans:   999990-1.orphan: key tests, older incoda pid 999990: its job has exited; incoda force-release --queue tests deletes the record\n",
		"orphans:   junk.orphan: unreadable (",
		"attention: unpooled run by an older incoda: pid 999999, key builds (strays/1): zig build; new runs on its pools wait for it\n",
		"on PATH:   none\n")
	if strings.Contains(out, "2/done") {
		t.Fatalf("doctor deletes a fully dead stray lane before reporting:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(machine.StraysDir(state), "2")); !os.IsNotExist(err) {
		t.Fatal("the dead stray batch must be gone")
	}
}
