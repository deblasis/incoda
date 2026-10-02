package machine

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/lane"
)

// migrated returns a migrated state directory.
func migrated(t *testing.T) (string, *Registry) {
	t.Helper()
	state := t.TempDir()
	reg, _, err := ensure(t, state)
	if err != nil {
		t.Fatal(err)
	}
	return state, reg
}

func TestChargedPools(t *testing.T) {
	state, reg := migrated(t)
	write := func(key, body string) {
		if err := os.MkdirAll(lane.LaneDir(state, key), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(lane.LaneDir(state, key), "config.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("linked", `{"schema":2,"pools":["vm","tests"]}`)
	write("unlinked", `{"schema":2,"slots":1}`)
	write("dangling", `{"schema":2,"pools":["gone"]}`)
	write("broken", `nope`)
	all := "builds,computer-use,tests,vm"
	for _, tc := range []struct{ key, want string }{
		{"builds", "builds"},
		{"linked", "tests,vm"},
		{"unlinked", all},
		{"never-seen", all},
		{"dangling", all},
		{"broken", all},
	} {
		if got := strings.Join(ChargedPools(state, reg, tc.key), ","); got != tc.want {
			t.Fatalf("ChargedPools(%s) = %s, want %s", tc.key, got, tc.want)
		}
	}
	us := []Unpooled{{Key: "linked", PID: 1}, {Key: "builds", PID: 2}, {Key: "never-seen", PID: 3}}
	pids := func(us []Unpooled) string {
		var s []string
		for _, u := range us {
			s = append(s, strconv.Itoa(u.PID))
		}
		return strings.Join(s, ",")
	}
	for _, tc := range []struct{ pool, want string }{
		{"builds", "2,3"}, {"tests", "1,3"}, {"vm", "1,3"}, {"computer-use", "3"}, {"linked", ""},
	} {
		if got := pids(ChargedTo(state, reg, tc.pool, us)); got != tc.want {
			t.Fatalf("ChargedTo(%s) = %s, want %s", tc.pool, got, tc.want)
		}
	}
}

func TestScanUnpooledCountsStraysQueuesAndOrphans(t *testing.T) {
	state, _ := migrated(t)
	batch := filepath.Join(StraysDir(state), "1700000000000000000")
	holdTicket(t, batch, "builds", 4711, "zig", "build")
	holdTicket(t, batch, "done", 4712, "x")() // dead
	if err := os.WriteFile(lane.LogPath(filepath.Join(batch, "done")), []byte("done fragment\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(lane.LaneDir(state, "done"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The fence is missing and an older incoda runs in a new queues/.
	if err := os.Remove(lane.QueuesDir(state)); err != nil {
		t.Fatal(err)
	}
	holdTicket(t, lane.QueuesDir(state), "oldjob", 5120, "just", "ui")
	// A stale orphan record (its tree is empty): not counted, swept by
	// clean.
	if _, err := writeOrphan(state, &Orphan{Key: "builds", PID: 4713}); err != nil {
		t.Fatal(err)
	}

	before := machineSnapshot(t, state)
	us, err := ScanUnpooled(state, false)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, u := range us {
		got = append(got, u.Line()+" @"+u.Where+": "+u.Command)
	}
	want := []string{
		"unpooled run by an older incoda: pid 4711, key builds @strays/1700000000000000000: zig build",
		"unpooled run by an older incoda: pid 5120, key oldjob @queues: just ui",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ScanUnpooled:\n%s", strings.Join(got, "\n"))
	}
	if after := machineSnapshot(t, state); len(after) != len(before) {
		t.Fatal("a scan without clean must delete nothing")
	}

	if _, err := ScanUnpooled(state, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(batch, "done")); !os.IsNotExist(err) {
		t.Fatal("clean deletes a fully dead stray lane")
	}
	if b, _ := os.ReadFile(lane.LogPath(lane.LaneDir(state, "done"))); string(b) != "done fragment\n" {
		t.Fatalf("its log fragment goes to lanes/done/lane.log: %q", b)
	}
	if _, err := os.Stat(filepath.Join(batch, "builds")); err != nil {
		t.Fatal("clean keeps a live stray lane")
	}
	if _, err := os.Stat(filepath.Join(lane.QueuesDir(state), "oldjob")); err != nil {
		t.Fatal("clean never touches queues/: only a re-fence moves it")
	}
	if all, _, _ := ReadOrphans(state); len(all) != 0 {
		t.Fatal("clean deletes a stale orphan record")
	}
}

func TestCleanStraysRemovesAnEmptyBatchButNotStrays(t *testing.T) {
	state, _ := migrated(t)
	batch := filepath.Join(StraysDir(state), "1")
	holdTicket(t, batch, "k", 1234, "x")()
	if err := CleanStrays(state); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(batch); !os.IsNotExist(err) {
		t.Fatal("an emptied batch is deleted")
	}
	if fi, err := os.Stat(StraysDir(state)); err != nil || !fi.IsDir() {
		t.Fatal("strays/ itself stays")
	}
}
