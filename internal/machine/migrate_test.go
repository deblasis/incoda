package machine

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
)

// seedOld writes the queues/ layout an older incoda leaves behind.
func seedOld(t *testing.T, state string) {
	t.Helper()
	write := func(rel, body string) {
		p := filepath.Join(state, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("queues/alpha/config.json", `{"slots":2}`)
	write("queues/alpha/lane.log", "2026-09-30 10:00:00 queue=alpha event=release pid=1 rc=0\n")
	write("queues/builds/config.json", `{"require_reason":true,"description":"heavy builds"}`)
	write("queues/bad/config.json", "nope")
	write("queues/cap-gate/lane.log", "")
	write("queues/old/config.json", `{"closed":"retired"}`)
}

func ensure(t *testing.T, state string) (*Registry, string, error) {
	t.Helper()
	var errBuf bytes.Buffer
	reg, err := Ensure(state, Options{Start: time.Now(), Wait: 10 * time.Second, Poll: 20 * time.Millisecond, By: "incoda test", Stderr: &errBuf})
	return reg, errBuf.String(), err
}

// assertMigrated checks the layout-2 invariants after migrating seedOld
// (seeded) or an empty state directory.
func assertMigrated(t *testing.T, state string, seeded bool) {
	t.Helper()
	if b, err := os.ReadFile(lane.QueuesDir(state)); err != nil || string(b) != FenceText {
		t.Fatalf("fence: %q %v", b, err)
	}
	for _, p := range []string{fenceNewPath(state), planPath(state), StraysDir(state)} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s left behind", p)
		}
	}
	reg, err := ReadRegistry(state)
	if err != nil || reg.Schema != 1 || reg.Layout != 2 || reg.Generation != 1 || strings.Join(reg.Pools, ",") != "builds,computer-use,tests,vm" {
		t.Fatalf("registry %+v %v", reg, err)
	}
	for _, p := range []string{"computer-use", "tests", "vm"} {
		cfg, err := lane.ReadConfig(lane.LaneDir(state, p))
		if err != nil || cfg.Schema != lane.ConfigSchema || cfg.Slots != 1 {
			t.Fatalf("pool %s config %+v %v", p, cfg, err)
		}
	}
	cfg, err := lane.ReadConfig(lane.LaneDir(state, "builds"))
	if err != nil || cfg.Schema != lane.ConfigSchema || cfg.Slots != 1 {
		t.Fatalf("builds config %+v %v", cfg, err)
	}
	if !seeded {
		return
	}
	if !cfg.RequireReason || cfg.Description != "heavy builds" {
		t.Fatalf("builds lost its fields: %+v", cfg)
	}
	if b, _ := os.ReadFile(filepath.Join(lane.LaneDir(state, "alpha"), "config.json")); string(b) != `{"slots":2}` {
		t.Fatalf("the migration must not rewrite a project lane's config: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(lane.LaneDir(state, "bad"), "config.json")); string(b) != "nope" {
		t.Fatal("a malformed config is never rewritten")
	}
	log, _ := os.ReadFile(lane.LogPath(lane.LaneDir(state, "alpha")))
	if !strings.Contains(string(log), "queue=alpha event=release pid=1") ||
		!strings.Contains(string(log), "queue=alpha event=migrate pid=") || !strings.Contains(string(log), "kind=project") {
		t.Fatalf("alpha lane.log:\n%s", log)
	}
	if log, _ := os.ReadFile(lane.LogPath(lane.LaneDir(state, "builds"))); !strings.Contains(string(log), "kind=pool") {
		t.Fatalf("builds lane.log:\n%s", log)
	}
}

const seededLines = "incoda: migrated: pools builds, computer-use, tests, vm; 2 queues need a link before they run again\n" +
	"incoda: ask the user to run incoda init: it shows each queue's suggested pools and asks (1 have one)\n" +
	"incoda: 1 queue(s) have an unreadable config.json: see incoda doctor\n"

func TestEnsureMigratesAnOldLayout(t *testing.T) {
	state := t.TempDir()
	seedOld(t, state)
	reg, out, err := ensure(t, state)
	if err != nil {
		t.Fatal(err)
	}
	if reg.MigratedBy != "incoda test" {
		t.Fatalf("migrated_by %q", reg.MigratedBy)
	}
	if out != seededLines {
		t.Fatalf("migrated lines:\n%s\nwant:\n%s", out, seededLines)
	}
	assertMigrated(t, state, true)
}

func TestEnsureMigratesAFreshDirectory(t *testing.T) {
	state := filepath.Join(t.TempDir(), "never-used")
	if err := os.Mkdir(state, 0o755); err != nil {
		t.Fatal(err)
	}
	_, out, err := ensure(t, state)
	if err != nil {
		t.Fatal(err)
	}
	if out != "incoda: migrated: pools builds, computer-use, tests, vm; 0 queues need a link before they run again\n" {
		t.Fatalf("migrated lines:\n%s", out)
	}
	assertMigrated(t, state, false)
}

func TestEnsureFastPathTakesNoLock(t *testing.T) {
	state := t.TempDir()
	if _, _, err := ensure(t, state); err != nil {
		t.Fatal(err)
	}
	takeLock(t, state) // held until the test ends
	start := time.Now()
	reg, out, err := ensure(t, state)
	if err != nil || reg.Generation != 1 || out != "" {
		t.Fatalf("second Ensure: %+v %q %v", reg, out, err)
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("the fast path must not wait for machine.lock, took %v", el)
	}
}

func TestEnsureKeepsAPoolLaneSlotsAndRegistersAMalformedPool(t *testing.T) {
	state := t.TempDir()
	for rel, body := range map[string]string{
		"queues/builds/config.json": `{"slots":3}`,
		"queues/tests/config.json":  "nope",
	} {
		p := filepath.Join(state, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, out, err := ensure(t, state)
	if err != nil {
		t.Fatal(err)
	}
	if cfg, err := lane.ReadConfig(lane.LaneDir(state, "builds")); err != nil || cfg.Slots != 3 || cfg.Schema != lane.ConfigSchema {
		t.Fatalf("builds keeps its slots: %+v %v", cfg, err)
	}
	if b, _ := os.ReadFile(filepath.Join(lane.LaneDir(state, "tests"), "config.json")); string(b) != "nope" || !reg.IsPool("tests") {
		t.Fatal("a malformed pool config is registered and left in place")
	}
	if !strings.Contains(out, "incoda: 1 queue(s) have an unreadable config.json: see incoda doctor\n") {
		t.Fatalf("missing the unreadable line:\n%s", out)
	}
}

func TestEnsureWaitsForAnOldTicketThenMigrates(t *testing.T) {
	state := t.TempDir()
	release := holdTicket(t, lane.QueuesDir(state), "held", 999999, "zig", "build")
	go func() {
		time.Sleep(300 * time.Millisecond)
		release()
	}()
	_, out, err := ensure(t, state)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "incoda: upgrade-wait: state upgrade waits for 1 run(s) by an older incoda:\n") || !strings.Contains(out, "incoda: migrated: ") {
		t.Fatalf("output:\n%s", out)
	}
	assertMigrated(t, state, false)
	if !lane.Exists(state, "held") {
		t.Fatal("the waited-for lane moves to lanes/ like any other")
	}
}

func TestEnsureFailsClosedOnABadRegistry(t *testing.T) {
	state := t.TempDir()
	if _, _, err := ensure(t, state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(RegistryPath(state), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := ensure(t, state)
	var se *StateError
	if !errors.As(err, &se) || se.Msg != "machine-state: machine.json: unexpected end of JSON input; run incoda doctor" {
		t.Fatalf("want a machine-state refusal, got %v", err)
	}
	if _, err := Inspect(state); !errors.As(err, &se) {
		t.Fatalf("Inspect fails closed the same way, got %v", err)
	}
}

func TestEnsureRefencesAMigratedLayout(t *testing.T) {
	state := t.TempDir()
	if _, _, err := ensure(t, state); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lane.QueuesDir(state)); err != nil {
		t.Fatal(err)
	}
	// An older incoda ran on builds after the fence was deleted and is
	// gone (a dead ticket and its log); another one still runs on busy.
	if err := os.MkdirAll(filepath.Join(lane.QueuesDir(state), "builds"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lane.QueuesDir(state), "builds", "lane.log"), []byte("old fragment\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	holdTicket(t, lane.QueuesDir(state), "builds", 999995, "done")()
	holdTicket(t, lane.QueuesDir(state), "busy", 999996, "make")
	// Inspect reports the missing fence and changes nothing.
	v, err := Inspect(state)
	if err != nil || !v.Migrated || !v.FenceMissing {
		t.Fatalf("Inspect = %+v %v", v, err)
	}
	if FencePlaced(state) {
		t.Fatal("Inspect must never re-fence")
	}
	if _, _, err := ensure(t, state); err != nil {
		t.Fatal(err)
	}
	if !FencePlaced(state) {
		t.Fatal("Ensure re-places the fence")
	}
	batches, _ := os.ReadDir(StraysDir(state))
	if len(batches) != 1 {
		t.Fatalf("want the queues/ dir in one strays batch, got %v", batches)
	}
	// The re-fence deletes the dead stray lane and passes its log on; the
	// live one stays, for acquisitions to count.
	if _, err := os.Stat(filepath.Join(StraysDir(state), batches[0].Name(), "busy")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(StraysDir(state), batches[0].Name(), "builds")); !os.IsNotExist(err) {
		t.Fatal("the dead stray lane must be deleted")
	}
	if b, _ := os.ReadFile(lane.LogPath(lane.LaneDir(state, "builds"))); !strings.Contains(string(b), "old fragment") {
		t.Fatalf("lanes/builds/lane.log:\n%s", b)
	}
	log, _ := os.ReadFile(MachineLogPath(state))
	if !strings.Contains(string(log), "event=refence pid=") || !strings.Contains(string(log), "strays="+batches[0].Name()) {
		t.Fatalf("machine.log:\n%s", log)
	}
}

func TestRecoveryRows(t *testing.T) {
	plan := func(t *testing.T, s string) {
		if err := writePlan(s); err != nil {
			t.Fatal(err)
		}
	}
	mv := func(t *testing.T, s, from, to string) {
		if err := os.Rename(filepath.Join(s, from), filepath.Join(s, to)); err != nil {
			t.Fatal(err)
		}
	}
	fence := func(t *testing.T, s string) {
		if err := os.WriteFile(lane.QueuesDir(s), []byte(FenceText), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name   string
		seeded bool
		setup  func(t *testing.T, s string)
		row    Row
		extra  func(t *testing.T, s string)
	}{
		{name: "not started", seeded: true, setup: func(*testing.T, string) {}, row: RowNotStarted},
		{name: "fresh directory", setup: func(*testing.T, string) {}, row: RowNotStarted},
		{name: "planned with a leftover queues.new", seeded: true, row: RowPlanned, setup: func(t *testing.T, s string) {
			plan(t, s)
			if err := writeFenceNew(s); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "swapped, not renamed", seeded: true, row: RowSwapped, setup: func(t *testing.T, s string) {
			plan(t, s)
			mv(t, s, "queues", "queues.new")
			fence(t, s)
		}},
		{name: "fallback before the fence", seeded: true, row: RowFenceMissing, setup: func(t *testing.T, s string) {
			plan(t, s)
			mv(t, s, "queues", "lanes")
		}},
		{name: "fallback with a recreated queues/", seeded: true, row: RowFenceMissing, setup: func(t *testing.T, s string) {
			plan(t, s)
			mv(t, s, "queues", "lanes")
			if err := os.MkdirAll(filepath.Join(s, "queues", "late"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(s, "queues", "late", "lane.log"), []byte("late run\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, extra: func(t *testing.T, s string) {
			if b, _ := os.ReadFile(lane.LogPath(lane.LaneDir(s, "late"))); !strings.Contains(string(b), "late run") {
				t.Fatal("the recreated queues/ went to strays/ and M6 merged it into lanes/")
			}
		}},
		{name: "empty-dir path after M3", setup: plan, row: RowEmptyPlanned},
		{name: "a fence without lanes/", setup: fence, row: RowFenceNoLanes},
		{name: "resume at M5", seeded: true, row: RowResume, setup: func(t *testing.T, s string) {
			plan(t, s)
			mv(t, s, "queues", "lanes")
			fence(t, s)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			if tc.seeded {
				seedOld(t, state)
			}
			tc.setup(t, state)
			if got := scanLayout(state).row(); got != tc.row {
				t.Fatalf("classified as %v (%d), want %v (%d)", got, got, tc.row, tc.row)
			}
			if _, _, err := ensure(t, state); err != nil {
				t.Fatal(err)
			}
			assertMigrated(t, state, tc.seeded)
			if tc.extra != nil {
				tc.extra(t, state)
			}
		})
	}
}

func TestRecoveryCommittedDeletesThePlan(t *testing.T) {
	state := t.TempDir()
	seedOld(t, state)
	if _, _, err := ensure(t, state); err != nil {
		t.Fatal(err)
	}
	if err := writePlan(state); err != nil {
		t.Fatal(err)
	}
	_, out, err := ensure(t, state)
	if err != nil || out != "" {
		t.Fatalf("recovery after the commit: %q %v", out, err)
	}
	assertMigrated(t, state, true)
}

func TestRecoveryRegistryLostFailsClosedAndKeepsTheFence(t *testing.T) {
	state := t.TempDir()
	if _, _, err := ensure(t, state); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(RegistryPath(state)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lane.QueuesDir(state)); err != nil {
		t.Fatal(err)
	}
	if got := scanLayout(state).row(); got != RowRegistryLost {
		t.Fatalf("classified as %v", got)
	}
	want := "machine-state: machine.json: missing while lanes/ exists; run incoda doctor"
	var se *StateError
	if _, err := Inspect(state); !errors.As(err, &se) || se.Msg != want {
		t.Fatalf("Inspect: %v", err)
	}
	if FencePlaced(state) {
		t.Fatal("Inspect must not place the fence")
	}
	if _, _, err := ensure(t, state); !errors.As(err, &se) || se.Msg != want {
		t.Fatalf("Ensure: %v", err)
	}
	if !FencePlaced(state) {
		t.Fatal("Ensure places the fence before failing closed")
	}
	if _, err := os.Lstat(RegistryPath(state)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("nothing re-bootstraps a lost registry")
	}
}

// TestM4NotIdleGoesBackToM2: on Windows a directory with an open file
// inside cannot be renamed; the migration goes back to M2 and tries again.
func TestM4NotIdleGoesBackToM2(t *testing.T) {
	state := t.TempDir()
	seedOld(t, state)
	calls := 0
	exchangeFn = func(string, string) error { return errNoExchange }
	renameDir = func(from, to string) error {
		calls++
		if calls == 1 {
			return errors.New("sharing violation")
		}
		return os.Rename(from, to)
	}
	isNotIdle = func(error) bool { return true }
	defer func() {
		exchangeFn, renameDir, isNotIdle = exchange, os.Rename, notIdleError
	}()
	if _, _, err := ensure(t, state); err != nil {
		t.Fatal(err)
	}
	if calls < 2 {
		t.Fatalf("queues/ was renamed %d time(s); the refused rename must be retried", calls)
	}
	assertMigrated(t, state, true)
}

// TestFallbackRaceSendsTheNewQueuesDirToStrays: an old binary's MkdirAll
// recreates queues/ between the two renames of the fallback.
func TestFallbackRaceSendsTheNewQueuesDirToStrays(t *testing.T) {
	state := t.TempDir()
	seedOld(t, state)
	exchangeFn = func(string, string) error { return errNoExchange }
	first := true
	beforePlace = func() {
		if first {
			first = false
			if err := os.MkdirAll(filepath.Join(lane.QueuesDir(state), "late"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(lane.QueuesDir(state), "late", "lane.log"), []byte("late run\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	defer func() { exchangeFn, beforePlace = exchange, func() {} }()
	if _, _, err := ensure(t, state); err != nil {
		t.Fatal(err)
	}
	assertMigrated(t, state, true)
	if b, _ := os.ReadFile(lane.LogPath(lane.LaneDir(state, "late"))); !strings.Contains(string(b), "late run") {
		t.Fatal("the late queues/ dir must end in lanes/late")
	}
}

func machineSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		out[path] = fmt.Sprintf("%v %d %d", fi.Mode(), fi.Size(), fi.ModTime().UnixNano())
		return nil
	})
	return out
}

func TestInspect(t *testing.T) {
	state := t.TempDir()
	seedOld(t, state)
	before := machineSnapshot(t, state)
	v, err := Inspect(state)
	if err != nil || v.Migrated || v.Root != lane.QueuesDir(state) || v.Banner != "state not upgraded yet: the next mutating incoda command upgrades it" {
		t.Fatalf("unmigrated view %+v %v", v, err)
	}
	after := machineSnapshot(t, state)
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatal("Inspect wrote to an unmigrated state directory")
	}

	lk, err := AcquireLock(state, LockOptions{Op: "migrate", Start: time.Now(), Wait: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := lk.SetBlockers([]Blocker{{Key: "builds", PID: 4711}, {Key: "kungfoo-ui", PID: 5120}}); err != nil {
		t.Fatal(err)
	}
	v, _ = Inspect(state)
	want := fmt.Sprintf("state upgrade in progress by pid %d since ", os.Getpid())
	if !strings.HasPrefix(v.Banner, want) || !strings.HasSuffix(v.Banner, "; waiting for older runs: builds pid 4711, kungfoo-ui pid 5120") {
		t.Fatalf("in-progress banner %q", v.Banner)
	}
	lk.Release()

	if _, _, err := ensure(t, state); err != nil {
		t.Fatal(err)
	}
	v, err = Inspect(state)
	if err != nil || !v.Migrated || v.Root != lane.LanesDir(state) || v.Banner != "" || v.FenceMissing || v.Registry == nil {
		t.Fatalf("migrated view %+v %v", v, err)
	}
}

func TestSuggest(t *testing.T) {
	for _, tc := range []struct {
		key   string
		pools string
		quiet bool
		ok    bool
	}{
		{"cap-gate", "tests", false, true},
		{"x-test", "tests", false, true},
		{"x-tests", "tests", false, true},
		{"kungfoo-build", "builds", false, true},
		{"compiles", "builds", false, true},
		{"kungfoo-ui", "computer-use", false, true},
		{"wintty-desktop", "computer-use", false, true},
		{"cap-e2e", "computer-use,tests", false, true},
		{"cap-measure", "tests", true, true},
		{"polymatto", "", false, false},
		{"test", "", false, false},
		{"-gate", "", false, false},
		{"wintty-publish", "", false, false},
	} {
		s, ok := Suggest(tc.key)
		if ok != tc.ok || strings.Join(s.Pools, ",") != tc.pools || s.QuietMachine != tc.quiet {
			t.Fatalf("Suggest(%q) = %+v %v", tc.key, s, ok)
		}
	}
}

// TestCommitRefencesWhenTheFenceVanishedDuringM5: the fence is removed
// during M5 and an older binary recreates queues/<K> with a live run. M8
// must not commit unfenced: it re-places the fence (queues/ goes to
// strays/), goes back to M5, waits for the run and merges its lane, and
// only then writes machine.json.
func TestCommitRefencesWhenTheFenceVanishedDuringM5(t *testing.T) {
	state := t.TempDir()
	seedOld(t, state)
	var released atomic.Bool
	checks := 0
	beforeCommitCheck = func() {
		checks++
		if _, err := ReadRegistry(state); !errors.Is(err, ErrNoRegistry) {
			t.Errorf("check %d: machine.json already written: %v", checks, err)
		}
		switch checks {
		case 1:
			if err := os.Remove(lane.QueuesDir(state)); err != nil {
				t.Fatal(err)
			}
			release := holdTicket(t, lane.QueuesDir(state), "slipped", 999999, "zig", "build")
			go func() {
				time.Sleep(300 * time.Millisecond)
				released.Store(true)
				release()
			}()
		case 2:
			if !released.Load() {
				t.Error("M8 reached before the slipped-in run ended")
			}
			if !FencePlaced(state) {
				t.Error("the fence is not back")
			}
			if !lane.Exists(state, "slipped") {
				t.Error("the slipped-in lane was not merged into lanes/")
			}
		}
	}
	defer func() { beforeCommitCheck = func() {} }()
	_, out, err := ensure(t, state)
	if err != nil {
		t.Fatal(err)
	}
	if checks != 2 {
		t.Fatalf("fence checks before the commit: %d, want 2", checks)
	}
	if !strings.Contains(out, "incoda: upgrade-wait: state upgrade waits for 1 run(s) by an older incoda:\n") ||
		!strings.Contains(out, "slipped pid 999999: zig build") ||
		!strings.Contains(out, "incoda kill --queue slipped --pid 999999 --reason 'incoda upgrade'\n") {
		t.Fatalf("output:\n%s", out)
	}
	assertMigrated(t, state, true)
	if b, _ := os.ReadFile(MachineLogPath(state)); !strings.Contains(string(b), "event=refence") {
		t.Fatalf("machine.log:\n%s", b)
	}
}

// TestInspectRootFollowsTheLanesThroughAMigration: a read-only command
// must read the lanes wherever the migration has put them, never the
// fence file. In row 3 (swapped, not yet renamed) they are in queues.new/;
// in row 6 (a fence without lanes/) there are none yet; in every other
// row with the fence placed they are in lanes/.
func TestInspectRootFollowsTheLanesThroughAMigration(t *testing.T) {
	fence := func(t *testing.T, s string) {
		if err := os.WriteFile(lane.QueuesDir(s), []byte(FenceText), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, s string)
		row   Row
		root  func(s string) string
	}{
		{"row 1, not started", func(t *testing.T, s string) {}, RowNotStarted, lane.QueuesDir},
		{"row 3, swapped", func(t *testing.T, s string) {
			if err := writePlan(s); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(lane.QueuesDir(s), fenceNewPath(s)); err != nil {
				t.Fatal(err)
			}
			fence(t, s)
		}, RowSwapped, fenceNewPath},
		{"row 6, a fence without lanes/", func(t *testing.T, s string) {
			if err := os.RemoveAll(lane.QueuesDir(s)); err != nil {
				t.Fatal(err)
			}
			fence(t, s)
		}, RowFenceNoLanes, lane.LanesDir},
		{"row 7, resume at M5", func(t *testing.T, s string) {
			if err := writePlan(s); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(lane.QueuesDir(s), lane.LanesDir(s)); err != nil {
				t.Fatal(err)
			}
			fence(t, s)
		}, RowResume, lane.LanesDir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			seedOld(t, state)
			tc.setup(t, state)
			if got := scanLayout(state).row(); got != tc.row {
				t.Fatalf("classified as %v, want %v", got, tc.row)
			}
			before := machineSnapshot(t, state)
			v, err := Inspect(state)
			if err != nil || v.Migrated || v.Root != tc.root(state) {
				t.Fatalf("Inspect = %+v %v, want root %s", v, err, tc.root(state))
			}
			if fmt.Sprint(before) != fmt.Sprint(machineSnapshot(t, state)) {
				t.Fatal("Inspect wrote to the state directory")
			}
			if tc.row == RowSwapped && !lane.ExistsIn(v.Root, "alpha") {
				t.Fatal("the lanes in queues.new/ must be visible")
			}
		})
	}
}
