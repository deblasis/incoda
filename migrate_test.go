package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/lockfile"
	"github.com/deblasis/incoda/internal/machine"
)

// seedOldLayout writes the queues/ layout an older incoda leaves behind:
// a project lane with slots, a lane named like a pool with fields to keep,
// a malformed config, a lane with a suggestion and a closed lane.
func seedOldLayout(t *testing.T, state string) {
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

// assertLayout2 checks the invariants of a finished migration of
// seedOldLayout (seeded) or of an empty state directory.
func assertLayout2(t *testing.T, state string, seeded bool) {
	t.Helper()
	if b, err := os.ReadFile(filepath.Join(state, "queues")); err != nil || string(b) != machine.FenceText {
		t.Fatalf("queues is not the fence: %q %v", b, err)
	}
	for _, name := range []string{"queues.new", "migration.json", "strays"} {
		if _, err := os.Lstat(filepath.Join(state, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s left behind", name)
		}
	}
	reg, err := machine.ReadRegistry(state)
	if err != nil || reg.Layout != 2 || reg.Generation != 1 || strings.Join(reg.Pools, ",") != "builds,computer-use,tests,vm" {
		t.Fatalf("machine.json %+v %v", reg, err)
	}
	for _, p := range reg.Pools {
		cfg, err := lane.ReadConfig(laneDir(state, p))
		if err != nil || cfg.Schema != 2 || cfg.Slots != 1 {
			t.Fatalf("pool %s: %+v %v", p, cfg, err)
		}
	}
	if !seeded {
		return
	}
	if cfg, _ := lane.ReadConfig(laneDir(state, "builds")); !cfg.RequireReason || cfg.Description != "heavy builds" {
		t.Fatalf("the builds lane lost its fields: %+v", cfg)
	}
	if b, _ := os.ReadFile(filepath.Join(laneDir(state, "alpha"), "config.json")); string(b) != `{"slots":2}` {
		t.Fatalf("a project lane's config must not be rewritten: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(laneDir(state, "bad"), "config.json")); string(b) != "nope" {
		t.Fatal("a malformed config is never rewritten")
	}
	if b, _ := os.ReadFile(filepath.Join(laneDir(state, "alpha"), "lane.log")); !strings.Contains(string(b), "event=release pid=1") || !strings.Contains(string(b), "event=migrate") {
		t.Fatalf("alpha lane.log:\n%s", b)
	}
}

// treeState maps every path under root to its mode, size and modification
// time, so a test can prove a command wrote nothing.
// treeState renders the files under root: path, mode, size and mtime.
// Directory mtimes are deliberately not compared. On Windows, opening a file
// by its long name materialises its 8.3 short-name entry, which modifies the
// parent directory entry and so bumps the parent directory's mtime with no
// write of any kind: a read-only command then looks like it changed the state
// directory. Files are the invariant; a directory's timestamp is not.
func treeState(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s %v %d %d\n", path, fi.Mode(), fi.Size(), fi.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// holdOldTicket makes root/key look like a lane of an older incoda with one
// live ticket, the way its Enroll leaves it: registry.lock first, then a
// ticket whose lock this test process holds, pid in its name and payload.
// The returned func releases it and leaves a dead ticket, as a killed holder
// would.
func holdOldTicket(t *testing.T, root, key string, pid int, cmd ...string) func() {
	t.Helper()
	dir := filepath.Join(root, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	reg, err := lockfile.Open(lane.RegistryLockPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	reg.Close()
	name := fmt.Sprintf("%020d-%d.ticket", time.Now().UnixNano(), pid)
	lf, err := lockfile.Open(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := lf.TryLock(); !ok || err != nil {
		t.Fatalf("lock ticket: %v %v", ok, err)
	}
	b, _ := json.Marshal(lane.Ticket{PID: pid, Queue: key, Slots: 1, Command: cmd})
	if err := lf.Truncate(b); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(func() { lf.Close() }) }
	t.Cleanup(release)
	return release
}

// syncBuffer is a buffer a test can read while a child process writes it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func waitForText(t *testing.T, b *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(b.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q in:\n%s", want, b.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestFirstMutatingCommandMigrates(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	seedOldLayout(t, state)
	out, code := runIncoda(t, incoda, state, "config", "alpha")
	if code != 0 {
		t.Fatalf("config: exit %d\n%s", code, out)
	}
	for _, want := range []string{
		"incoda: migrated: pools builds, computer-use, tests, vm; 2 queues need a link before they run again\n",
		"incoda: ask the user to run incoda init: it shows each queue's suggested pools and asks (1 have one)\n",
		"incoda: 1 queue(s) have an unreadable config.json: see incoda doctor\n",
		"slots: 2",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	assertLayout2(t, state, true)
	if out, code := runIncoda(t, incoda, state, "config", "alpha"); code != 0 || strings.Contains(out, "migrated:") {
		t.Fatalf("the second command must not migrate again: %d\n%s", code, out)
	}
}

// TestFreshDirMigratesOnFirstRun: a run on a never-used state directory
// takes the empty-dir path, keeps its lane under lanes/ and never creates
// queues/ as a directory. The migrated line shows even with --quiet.
func TestFreshDirMigratesOnFirstRun(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	// A pool runs from the moment the migration commits (spec 2.1).
	out, code := runIncoda(t, incoda, state, "run", "--queue", "builds", "--quiet", "--", stamp, filepath.Join(t.TempDir(), "s"), "s", "1")
	if code != 0 {
		t.Fatalf("run: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "incoda: migrated: pools builds, computer-use, tests, vm; 0 queues need a link before they run again\n") {
		t.Fatalf("missing the migrated line:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(laneDir(state, "builds"), "lane.log")); err != nil {
		t.Fatalf("lane.log not under lanes/: %v", err)
	}
	assertLayout2(t, state, false)
}

func TestReadOnlyCommandsNeverMigrate(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	seedOldLayout(t, state)
	before := treeState(t, state)
	const banner = "incoda: state not upgraded yet: the next mutating incoda command upgrades it\n"
	for _, tc := range []struct {
		args   []string
		banner bool
	}{
		{[]string{"status", "--all"}, true},
		{[]string{"status", "--json", "--all"}, false},
		{[]string{"queues"}, true},
		{[]string{"watch", "--once", "--all"}, true},
	} {
		out, code := runIncoda(t, incoda, state, tc.args...)
		if code != 0 {
			t.Fatalf("%v: exit %d\n%s", tc.args, code, out)
		}
		if strings.Contains(out, banner) != tc.banner {
			t.Fatalf("%v: banner shown=%v, want %v:\n%s", tc.args, !tc.banner, tc.banner, out)
		}
		if !tc.banner && !json.Valid([]byte(out)) {
			t.Fatalf("status --json must stay pure JSON:\n%s", out)
		}
		if !strings.Contains(out, "alpha") {
			t.Fatalf("%v must read the old queues/ layout:\n%s", tc.args, out)
		}
	}
	if after := treeState(t, state); after != before {
		t.Fatalf("a read-only command wrote to an unmigrated state directory:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	// kill and force-release address the layout they find and never migrate.
	if out, code := runIncoda(t, incoda, state, "kill", "--queue", "alpha", "--pid", "999999", "--reason", "x"); code != 120 {
		t.Fatalf("kill of a missing pid: exit %d\n%s", code, out)
	}
	if out, code := runIncoda(t, incoda, state, "force-release", "--queue", "alpha"); code != 0 {
		t.Fatalf("force-release: exit %d\n%s", code, out)
	}
	for _, name := range []string{"lanes", "machine.json", "machine.lock", "migration.json"} {
		if _, err := os.Lstat(filepath.Join(state, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s exists: a command that never migrates created it", name)
		}
	}
	if fi, err := os.Lstat(filepath.Join(state, "queues")); err != nil || !fi.IsDir() {
		t.Fatal("queues/ must stay a directory until a mutating command migrates it")
	}
}

func TestStatusOnTheOldLayoutDoesNotReap(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	dir := filepath.Join(state, "queues", "alpha")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lane.RegistryLockPath(dir), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	dead := filepath.Join(dir, "00000000000000000001-1.ticket")
	if err := os.WriteFile(dead, []byte(`{"pid":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runIncoda(t, incoda, state, "status", "--queue", "alpha")
	if code != 0 || !strings.Contains(out, "FREE") {
		t.Fatalf("status: exit %d\n%s", code, out)
	}
	if _, err := os.Stat(dead); err != nil {
		t.Fatal("status reaped a ticket on a layout it may only read")
	}
}

func TestRunReplacesAMissingFence(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "x"); code != 0 {
		t.Fatalf("config: %d\n%s", code, out)
	}
	linkTestKeys(t, incoda, state, "y")
	q := filepath.Join(state, "queues")
	if err := os.Remove(q); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(q, "stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, code := runIncoda(t, incoda, state, "status", "--all"); code != 0 {
		t.Fatalf("status: %d\n%s", code, out)
	}
	if fi, err := os.Lstat(q); err != nil || !fi.IsDir() {
		t.Fatal("status must never re-fence")
	}
	if out, code := runIncoda(t, incoda, state, "run", "--queue", "y", "--quiet", "--", stamp, filepath.Join(t.TempDir(), "s"), "s", "1"); code != 0 {
		t.Fatalf("run: %d\n%s", code, out)
	}
	if !machine.FencePlaced(state) {
		t.Fatal("run must re-place the fence")
	}
	// The stale queues/ went to strays/ and, holding no live ticket, was
	// deleted by the same re-fence (spec 2.3).
	if batches, _ := os.ReadDir(machine.StraysDir(state)); len(batches) != 0 {
		t.Fatalf("a dead stray lane must not linger, got %v", batches)
	}
	if b, _ := os.ReadFile(machine.MachineLogPath(state)); !strings.Contains(string(b), "event=refence") {
		t.Fatalf("machine.log:\n%s", b)
	}
}

func TestBrokenRegistryFailsClosed(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "x"); code != 0 {
		t.Fatalf("config: %d\n%s", code, out)
	}
	if err := os.WriteFile(machine.RegistryPath(state), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	const want = "incoda: machine-state: machine.json: unexpected end of JSON input; run incoda doctor"
	for _, args := range [][]string{
		{"run", "--queue", "x", "--", stamp, filepath.Join(t.TempDir(), "s"), "s", "1"},
		{"config", "x"},
		{"status", "--all"},
		{"status", "--json", "--queue", "x"},
		{"watch", "--once", "--all"},
		{"queues"},
		{"kill", "--queue", "x", "--pid", "1", "--reason", "r"},
		{"force-release", "--queue", "x"},
	} {
		if out, code := runIncoda(t, incoda, state, args...); code != 122 || !strings.Contains(out, want) {
			t.Fatalf("%v: want exit 122 and %q, got %d:\n%s", args, want, code, out)
		}
	}
	for _, args := range [][]string{{"version"}, {"help"}} {
		if out, code := runIncoda(t, incoda, state, args...); code != 0 {
			t.Fatalf("%v must work on a broken registry: %d\n%s", args, code, out)
		}
	}
}

func TestNewerRegistryFailsClosed(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "x"); code != 0 {
		t.Fatalf("config: %d\n%s", code, out)
	}
	if err := os.WriteFile(machine.RegistryPath(state), []byte(`{"schema":2,"layout":2,"generation":1,"pools":["builds"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runIncoda(t, incoda, state, "run", "--queue", "x", "--", stamp, filepath.Join(t.TempDir(), "s"), "s", "1")
	if code != 122 || !strings.Contains(out, "incoda: machine-state: machine.json was written by a newer incoda; upgrade this one (") {
		t.Fatalf("want the newer refusal, got %d:\n%s", code, out)
	}
}

func TestLostRegistryFailsClosedAndKeepsTheFence(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "x"); code != 0 {
		t.Fatalf("config: %d\n%s", code, out)
	}
	if err := os.Remove(machine.RegistryPath(state)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(state, "queues")); err != nil {
		t.Fatal(err)
	}
	const want = "incoda: machine-state: machine.json: missing while lanes/ exists; run incoda doctor"
	if out, code := runIncoda(t, incoda, state, "status", "--all"); code != 122 || !strings.Contains(out, want) {
		t.Fatalf("status: %d\n%s", code, out)
	}
	if machine.FencePlaced(state) {
		t.Fatal("status must never place the fence")
	}
	if out, code := runIncoda(t, incoda, state, "run", "--queue", "x", "--", stamp, filepath.Join(t.TempDir(), "s"), "s", "1"); code != 122 || !strings.Contains(out, want) {
		t.Fatalf("run: %d\n%s", code, out)
	}
	if !machine.FencePlaced(state) {
		t.Fatal("a mutating command places the fence before failing closed")
	}
	if _, err := os.Lstat(machine.RegistryPath(state)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("nothing re-bootstraps a lost registry")
	}
}

// TestPoolUsableDirectly: --queue builds works from the moment the
// migration commits, before any project lane is linked (spec 2.1).
func TestPoolUsableDirectly(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	h, _ := startHolder(t, incoda, stamp, state, "builds", "pool", 1500, "50ms")
	waitFor(t, incoda, state, "builds", func(q queueReport) bool { return len(q.Holders) == 1 })
	if n := countTickets(t, state, "builds"); n != 1 {
		t.Fatalf("the pool ticket lives in lanes/builds, found %d", n)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("holder: %v", err)
	}
}

func TestUpgradeBlockedByAnAncestorHolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no ancestry walk on Windows")
	}
	incoda, stamp := binaries(t)
	state := t.TempDir()
	// This test process is the incoda's parent, so a ticket it holds is an
	// older incoda that is an ancestor of the run.
	holdOldTicket(t, filepath.Join(state, "queues"), "anc", os.Getpid(), "outer", "job")
	out, code := runIncoda(t, incoda, state, "run", "--queue", "inner", "--wait", "30s", "--", stamp, filepath.Join(t.TempDir(), "s"), "s", "1")
	want := fmt.Sprintf(`incoda: upgrade-blocked: an older incoda (pid %d, an ancestor of this process) holds "anc"; rerun the outer command after it exits`, os.Getpid())
	if code != 120 || !strings.Contains(out, want) {
		t.Fatalf("want exit 120 and %q, got %d:\n%s", want, code, out)
	}
	if _, err := os.Lstat(machine.RegistryPath(state)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an upgrade-blocked run must not migrate")
	}
}

func TestMigrationWaitsForALiveOldTicket(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	release := holdOldTicket(t, filepath.Join(state, "queues"), "held", 999999, "zig", "build")
	// A pool, so the run needs no link: it is the command that migrates.
	cmd := exec.Command(incoda, "run", "--queue", "builds", "--wait", "60s", "--poll", "50ms", "--", stamp, filepath.Join(t.TempDir(), "s"), "s", "1")
	cmd.Env = laneEnv(state)
	var errBuf syncBuffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	waitForText(t, &errBuf, "incoda: upgrade-wait: state upgrade waits for 1 run(s) by an older incoda:\nincoda:   held pid 999999: zig build\n")
	pid := cmd.Process.Pid
	if n, ok := machine.ReadNote(state); !ok || n.Op != "migrate" || n.PID != pid || len(n.Blockers) != 1 || n.Blockers[0].Key != "held" {
		t.Fatalf("machine.lock note %+v %v", n, ok)
	}
	out, code := runIncoda(t, incoda, state, "status", "--all")
	if code != 0 || !strings.Contains(out, fmt.Sprintf("incoda: state upgrade in progress by pid %d since ", pid)) || !strings.Contains(out, "; waiting for older runs: held pid 999999") {
		t.Fatalf("status banner during the wait: %d\n%s", code, out)
	}
	out, code = runIncoda(t, incoda, state, "config", "other", "--wait", "2s")
	if code != 121 || !strings.Contains(out, fmt.Sprintf("incoda: waiting for machine.lock: pid %d migrate since ", pid)) ||
		!strings.Contains(out, fmt.Sprintf("incoda: machine-lock-timeout: held by pid %d (migrate)", pid)) {
		t.Fatalf("a second mutating command waits for machine.lock, then times out: %d\n%s", code, out)
	}
	release()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the migrating run must finish once the old ticket dies: %v\n%s", err, errBuf.String())
	}
	if !lane.Exists(state, "held") || !machine.FencePlaced(state) {
		t.Fatal("the migration did not complete")
	}
}

func TestMigrationTimesOutWhileAnOldTicketIsHeld(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	holdOldTicket(t, filepath.Join(state, "queues"), "held", 999999, "zig", "build")
	out, code := runIncoda(t, incoda, state, "run", "--queue", "newq", "--wait", "1s", "--poll", "50ms", "--", stamp, filepath.Join(t.TempDir(), "s"), "s", "1")
	if code != 121 {
		t.Fatalf("want exit 121, got %d:\n%s", code, out)
	}
	for _, want := range []string{
		"incoda: upgrade-timeout: state upgrade still waits for 1 run(s) by an older incoda after 1s:\n",
		"incoda:   held pid 999999: zig build\n",
		"incoda:   " + machine.KillLine("held", 999999, machine.UpgradeReason, false) + "\n",
		"incoda: upgrade the older incoda on PATH; see incoda doctor\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	for _, name := range []string{"lanes", "machine.json", "migration.json"} {
		if _, err := os.Lstat(filepath.Join(state, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s exists after a migration that never started", name)
		}
	}
}
