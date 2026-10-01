package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/machine"
)

func doctor(t *testing.T, incoda, state string, args ...string) (string, int) {
	t.Helper()
	return runIncoda(t, incoda, state, append([]string{"doctor", "--no-color"}, args...)...)
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
		mustContain(t, out, "problem:   machine.json: missing while lanes/ exists; nothing re-creates it on its own. A human decides which lanes are pools and runs: incoda doctor --rebuild-registry builds,computer-use,tests,vm\n")
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
