package machine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/deblasis/incoda/internal/fixline"
	"github.com/deblasis/incoda/internal/lane"
)

// TestKillLinePerShell: POSIX prints the key and pid bare (unchanged); on
// PowerShell every value is one single-quoted word, key and pid included,
// since fixline.Quote now also doubles PowerShell's smart quotes.
func TestKillLinePerShell(t *testing.T) {
	if got, want := killLineFor(fixline.POSIX, "builds", 4711, "incoda upgrade", false),
		"incoda kill --queue builds --pid 4711 --reason 'incoda upgrade'"; got != want {
		t.Fatalf("POSIX: got %s want %s", got, want)
	}
	if got, want := killLineFor(fixline.PowerShell, "builds", 4711, "incoda upgrade", false),
		"incoda kill --queue 'builds' --pid '4711' --reason 'incoda upgrade'"; got != want {
		t.Fatalf("PowerShell: got %s want %s", got, want)
	}
	if got, want := killLineFor(fixline.PowerShell, "k", 1, "it's", true),
		"incoda kill --queue 'k' --pid '1' --reason 'it''s' --force"; got != want {
		t.Fatalf("PowerShell with force and a quote: got %s want %s", got, want)
	}
}

// TestFindKillTarget: kill finds a participant wherever the layout puts
// it and tells a run of this binary from an older incoda (spec 3.2).
func TestFindKillTarget(t *testing.T) {
	find := func(t *testing.T, state, key string, pid int) KillTarget {
		t.Helper()
		v, err := Inspect(state)
		if err != nil {
			t.Fatal(err)
		}
		before := machineSnapshot(t, state)
		tg, err := FindKillTarget(state, v, key, pid, soon())
		if err != nil {
			t.Fatal(err)
		}
		if after := machineSnapshot(t, state); len(after) != len(before) {
			t.Fatal("FindKillTarget must create nothing")
		}
		return tg
	}
	t.Run("before the fence", func(t *testing.T) {
		state := t.TempDir()
		holdTicket(t, lane.QueuesDir(state), "old", 4711, "zig", "build")
		tg := find(t, state, "old", 4711)
		if tg.Kind != TargetOld || tg.Dir != filepath.Join(lane.QueuesDir(state), "old") || tg.Command[0] != "zig" || !tg.Old() {
			t.Fatalf("%+v", tg)
		}
		if tg := find(t, state, "old", 4712); tg.Kind != TargetNone {
			t.Fatalf("another pid: %+v", tg)
		}
	})
	t.Run("behind the fence, machine.json absent", func(t *testing.T) {
		state := t.TempDir()
		holdTicket(t, lane.LanesDir(state), "slip", 4711, "make")
		if err := os.WriteFile(lane.QueuesDir(state), []byte(FenceText), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := writePlan(state); err != nil {
			t.Fatal(err)
		}
		if tg := find(t, state, "slip", 4711); tg.Kind != TargetOld {
			t.Fatalf("%+v", tg)
		}
	})
	t.Run("migrated", func(t *testing.T) {
		state, _ := migrated(t)
		holdTicket(t, lane.LanesDir(state), "mine", 4711, "x")
		holdTicket(t, filepath.Join(StraysDir(state), "1"), "builds", 4712, "y")
		if tg := find(t, state, "mine", 4711); tg.Kind != TargetLane || tg.Old() {
			t.Fatalf("a ticket of this binary: %+v", tg)
		}
		if tg := find(t, state, "builds", 4712); tg.Kind != TargetOld || tg.Dir != filepath.Join(StraysDir(state), "1", "builds") {
			t.Fatalf("a stray: %+v", tg)
		}
		if err := os.Remove(lane.QueuesDir(state)); err != nil {
			t.Fatal(err)
		}
		holdTicket(t, lane.QueuesDir(state), "again", 4713, "z")
		if tg := find(t, state, "again", 4713); tg.Kind != TargetOld {
			t.Fatalf("an older run in a recreated queues/: %+v", tg)
		}
	})
}
