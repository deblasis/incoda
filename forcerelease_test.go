package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/machine"
)

// TestForceReleaseLiveRefusedDuringTheUpgrade: while machine.json is
// absent, force-release --live would hide an older run from the upgrade;
// it refuses with one stop line per live ticket and deletes nothing. Plain
// force-release still works.
func TestForceReleaseLiveRefusedDuringTheUpgrade(t *testing.T) {
	incoda, _ := binaries(t)
	t.Run("before the fence", func(t *testing.T) {
		state := t.TempDir()
		release := holdOldTicket(t, filepath.Join(state, "queues"), "held", 999999, "zig", "build")
		out, code := runIncoda(t, incoda, state, "force-release", "--queue", "held", "--live")
		want := "incoda: upgrade-pending: force-release --live would hide a running job from the upgrade; ask the user before stopping another session's job; they can run:\n" +
			"incoda:   incoda kill --queue held --pid 999999 --reason 'incoda upgrade'\n"
		if code != 120 || out != want {
			t.Fatalf("want exit 120 and\n%s\ngot %d:\n%s", want, code, out)
		}
		if countTicketsIn(filepath.Join(state, "queues", "held")) != 1 {
			t.Fatal("a refused force-release must delete nothing")
		}
		release()
		// Plain force-release still works: its scan reaps the dead ticket.
		out, code = runIncoda(t, incoda, state, "force-release", "--queue", "held")
		if code != 0 || countTicketsIn(filepath.Join(state, "queues", "held")) != 0 {
			t.Fatalf("plain force-release on a dead ticket: %d\n%s", code, out)
		}
	})
	t.Run("behind the fence", func(t *testing.T) {
		state := t.TempDir()
		// Row 7 of the recovery table: lanes/, the fence and
		// migration.json, no machine.json.
		holdOldTicket(t, filepath.Join(state, "lanes"), "slip", 999998, "make")
		if err := os.WriteFile(filepath.Join(state, "queues"), []byte(machine.FenceText), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, "migration.json"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, code := runIncoda(t, incoda, state, "force-release", "--queue", "slip", "--live")
		if code != 120 || !strings.HasSuffix(out, "incoda:   incoda kill --queue slip --pid 999998 --reason 'incoda upgrade'\n") {
			t.Fatalf("want exit 120 with the stop line, got %d:\n%s", code, out)
		}
	})
}

// TestPlainForceReleaseDeletesStaleOrphanRecords: a record whose job has
// fully exited holds nothing; plain force-release on its key deletes it
// and leaves other keys' records alone.
func TestPlainForceReleaseDeletesStaleOrphanRecords(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "builds"); code != 0 {
		t.Fatalf("config: %d\n%s", code, out)
	}
	if err := os.MkdirAll(machine.OrphansDir(state), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string]string{"999990-1.orphan": "builds", "999991-2.orphan": "tests"} {
		body := `{"key":"` + key + `","pid":999990,"descendants":[],"groups":[]}`
		if err := os.WriteFile(filepath.Join(machine.OrphansDir(state), name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, code := runIncoda(t, incoda, state, "force-release", "--queue", "builds")
	if code != 0 || !strings.Contains(out, `queue "builds": removed 1 stale orphan record(s)`) {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if left, _, _ := machine.ReadOrphans(state); len(left) != 1 || left[0].Key != "tests" {
		t.Fatalf("left: %+v", left)
	}
}
