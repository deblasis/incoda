# System pools, plan 2a: Layout, registry and migration

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move lane state to `<state>/lanes/`, fence every released incoda out with a `<state>/queues` file, register the bootstrap pools in `machine.json`, and do it as one crash-safe transaction under `<state>/machine.lock` that waits for live old-binary tickets; fail closed on a broken registry, and give `doctor` the layout checks and `--rebuild-registry`.

**Architecture:** `internal/lane/statedir.go` becomes the one place that knows paths (`LanesDir`, `LaneDir`, legacy `QueuesDir`), and `lane.OpenIn` takes an explicit root plus an open mode so read-only views can read the old `queues/` without creating or reaping anything. A new `internal/machine` package owns `machine.lock` (TryLock plus poll, a note in the lock file), `machine.json` (unknown fields kept, temp plus rename through a new `internal/atomicfile`), the fence with per-OS atomic exchange, the M0 to M8 migration with every recovery row, `Inspect` for read-only commands and `Diagnose`/`Rebuild` for doctor. Mutating commands (`run`, `config`) call `machine.Ensure`; read-only commands call `machine.Inspect`. Crash injection lives behind the `incoda_crashpoints` build tag.

**Tech Stack:** Go 1.27, `golang.org/x/sys` (unix, windows), standard library. Tests: `go test`, integration tests that build and run the real binary, a crashpoint build of it, and binaries of the v0.2.0 and v0.6.0 tags.

**Spec:** `docs/superpowers/specs/2026-10-01-system-pools-design.md` (sections 2.1, 2.3, 3.1, 3.2 first paragraph, 3.3, 3.5 bootstrap content, 3.6, 5.5 in part). Plan index: `docs/superpowers/plans/2026-10-01-system-pools-00-index.md`. Plan 1 (`...-01-foundations.md`) has landed; this plan builds on its `held`, `procinfo`, `textsafe` and config schema 2.

## Global Constraints

- Go 1.27.0 or newer; no new module dependencies (standard library, `golang.org/x/sys`, the existing charm libraries only).
- `just ci` must pass at the end of every task: `gofmt` no-op, `go mod tidy` no-op, `go vet ./...` (from Task 4 also `go vet -tags incoda_crashpoints ./...`), `go test -race ./...` (plain `go test` on Windows).
- The Windows build must keep compiling: run `GOOS=windows go vet ./...` before each commit (from Task 4 also `GOOS=windows go vet -tags incoda_crashpoints ./...`).
- Plain prose in comments, docs and commit messages: no em dashes or en dashes, no emoji.
- Commit messages carry no `Co-Authored-By` or other AI attribution lines.
- Never run the real `incoda` on PATH or touch the real state directory. Tests build their own binaries and set `INCODA_DIR` to a temp dir (the existing `laneEnv` helper does this). M0 only stats PATH entries and never executes one; its tests put a fake `incoda` on a temp PATH and prove it never ran.
- Exit codes unchanged: 120 usage and refusals, 121 timeout, 122 state, 123 spawn, 124 killed, 125 kill pending, 130 interrupt.
- Work on branch `feat/system-pools`.
- Carried forward from plan 1: no build from this branch is released, installed on PATH or handed to anyone until Task 8 has landed the fence (a v0.6 binary and a pre-fence build of this branch queue behind each other's nested runs until `--wait`).
- Layout knowledge lives in `internal/lane/statedir.go` only: `LanesDir(state)` is `<state>/lanes`, `LaneDir(state, key)` is `<state>/lanes/<key>`, `QueuesDir(state)` is `<state>/queues`. `QueuesDir` is used only by `internal/machine` and by tests. This binary never creates `<state>/queues` as a directory.
- Lock order (spec 3.1): `machine.lock`, then lane registry locks (several only in key order), then ticket locks (non-blocking only). No path takes `machine.lock` while holding a registry lock or a ticket. `machine.lock` is never acquired with the blocking `Lock()`.
- `machine.json`, `migration.json` and `config.json` are written by temp file plus rename through `internal/atomicfile` (Windows: a refused rename is retried 10 times at 50ms). `machine.json` is written only while holding `machine.lock` (the writer takes a `*machine.Lock`).
- Atomic exchange, verified in the module cache at `~/go/pkg/mod/golang.org/x/sys@v0.47.0/unix`: darwin `unix.RenamexNp(from string, to string, flag uint32) error` (syscall_darwin.go:407) with `unix.RENAME_SWAP` (0x2, zerrors_darwin_arm64.go and zerrors_darwin_amd64.go:1179); linux `unix.Renameat2(olddirfd int, oldpath string, newdirfd int, newpath string, flags uint) error` (zsyscall_linux.go:1441) with `unix.AT_FDCWD` and `unix.RENAME_EXCHANGE` (0x2, zerrors_linux.go:3141). Errors that mean "no exchange here": `unix.EINVAL`, `unix.ENOTSUP`, and on linux also `unix.ENOSYS`. Windows has no exchange. A darwin swap of a file and a non-empty directory on APFS was run on this machine and works.
- Windows process liveness uses `windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, ...)` and `windows.GetExitCodeProcess`; x/sys has no `STILL_ACTIVE` constant, so the code uses the literal 259 with a comment.
- Crash injection: only binaries built with `-tags incoda_crashpoints` contain it (`internal/machine/crash_on.go`); every other build compiles the no-op `crash_off.go`, and `just dist` never passes the tag. Environment variables, read only by the tagged build: `INCODA_TEST_CRASH_AT=<step>` exits with status 97 right after that step; `INCODA_TEST_PAUSE_AT=<step>` with `INCODA_TEST_PAUSE_FILE=<path>` writes `<path>.reached` and waits for `<path>`; `INCODA_TEST_NO_EXCHANGE=1` forces the M4 rename fallback. Step names: `locked`, `M2`, `M3`, `M4-new`, `M4-swapped`, `M4-moved`, `M4-lanes`, `M4`, `M5`, `M6`, `M7`, `M8-registry`.
- Old-binary tests build tags `v0.2.0` and `v0.6.0` by `git archive <tag> | tar -x -C <tempdir>` and `go build` there, cached once per test run; never `git worktree`, never anything that writes to the repository's `.git`. A failed build skips the test with a message naming the tag.
- Every string that came from state, a file, argv or the environment is printed through `textsafe.Escape` (keys are validated and print bare).
- Informational lines (`migrated:`, `upgrade-wait:`, `upgrade-warning:`, `waiting for machine.lock:`) are printed even with `run --quiet`: they are one-time machine events an agent has to surface.
- Pre-existing timing-sensitive tests fail on a loaded machine on the base commit 9562bfd too (measured: `TestDisagreeingSlotsRefusedAtEnrollAfterConfigChange` 7 of 10 runs, its holder and contender start together; `TestFIFOOrder` under a loaded `-race` suite, its holder holds only 3s; `internal/sysinfo` `TestReadCPUDarwin`). If one of these alone fails `just ci`, rerun it in isolation before debugging this plan's changes.

---

## File structure

| File | Responsibility |
|---|---|
| `internal/lane/statedir.go` | `LanesDir`, `LaneDir`, legacy `QueuesDir` (the only place that knows the layout) |
| `internal/lane/queue.go` | `Mode` (`Create`, `Existing`, `ReadOnly`), `OpenIn`, `ListIn`, `ExistsIn`, `AppendLog`, `LogPath`, `LockAll`, `LiveLocked` |
| `internal/lane/probe.go` | the create-free ticket probe of spec 2.6 step 2: `Probed`, `ProbeTicket`, `ProbeLane` |
| `internal/lane/config.go` | `ReadConfig(dir)`; `UpdateConfig` writes through `atomicfile` |
| `internal/lockfile/lockfile.go` | `IsFreeExisting` |
| `internal/atomicfile/atomicfile.go` | temp file plus rename with the Windows retry |
| `internal/procinfo/alive_unix.go`, `alive_windows.go` | `Alive(pid)` for the status banner |
| `internal/machine/errors.go` | `StateError` (122), `Refusal` (120), `Timeout` (121), message joining |
| `internal/machine/note.go` | the `machine.lock` note: `Blocker`, `Note`, `ParseNote`, `ReadNote` |
| `internal/machine/lock.go` | `Lock`, `LockOptions`, `AcquireLock` |
| `internal/machine/registry.go` | `Registry`, `ReadRegistry`, `UpdateRegistry`, `writeRegistry`, bootstrap pool list |
| `internal/machine/fence.go` | `FenceText`, `FencePlaced`, `placeFence` with the race rule, strays |
| `internal/machine/exchange_darwin.go`, `exchange_linux.go`, `exchange_other.go` | the atomic exchange per OS |
| `internal/machine/crash_on.go`, `crash_off.go` | crash and pause points behind the `incoda_crashpoints` tag |
| `internal/machine/options.go` | `Options` a mutating command passes in, budget helpers |
| `internal/machine/idle.go` | M2 and M5: blockers, the wait loop and its texts |
| `internal/machine/layout.go` | layout scan, recovery rows, `View`, `Inspect`, the read-only banner |
| `internal/machine/plan.go` | `migration.json` (M3) |
| `internal/machine/bootstrap.go` | M6 merge, M7 apply, M8 summary text, `Suggest` |
| `internal/machine/migrate.go` | `Ensure`, the transaction and recovery dispatch, re-fence |
| `internal/machine/pathcheck.go` | M0 |
| `internal/machine/doctor.go`, `rebuild.go` | `Diagnose`, `Rebuild` |
| `internal/cli/state.go` | `readState`, `mutatingState`, `machineExit` |
| `internal/report/report.go`, `internal/tui/*` | read through `Inspect`; banner |
| `migrate_test.go`, `migrate_crash_test.go`, `doctor_test.go`, `oldbin_test.go` | root integration tests |

Test helpers do not migrate implicitly. Most integration tests start with a `run` or `config`, which migrates the fresh temp state directory on its first call; `countTickets`, `laneDir` and every log path read `lanes/<key>`. Tests that start with `status` on a fresh directory keep working because a read-only command on an unmigrated layout reads the (absent) old root and reports every queue as never used.

---

### Task 0: Make the enroll-time slots test independent of machine load

`TestDisagreeingSlotsRefusedAtEnrollAfterConfigChange` (integration_test.go) fails in most runs on a loaded machine, on the base commit too. The holder keeps `racea` for only 6000 ms; when status polling plus the reconfig take longer than that, the contender acquires `racea`, enrolls `raceb` before the config change lands, and runs. The fix keeps the holder until the reconfig is done, then releases it through the lane.

**Files:**
- Modify: `integration_test.go` (`TestDisagreeingSlotsRefusedAtEnrollAfterConfigChange` only)

**Interfaces:** none.

- [ ] **Step 1: Hold until released**

In the holder command, change the stamp hold from `"6000"` to `"60000"`.

- [ ] **Step 2: Release the holder after the reconfig**

Directly after the `reconfig` block succeeds and before `err := contender.Wait()`, add:

```go
	// Only now let the contender reach raceb: the config change has landed,
	// so the enroll-time check must see it whatever the machine's load.
	if out, code := runIncoda(t, incoda, state, "kill", "--queue", "racea",
		"--pid", strconv.Itoa(holder.Process.Pid), "--reason", "release the holder"); code != 0 {
		t.Fatalf("kill holder: exit %d\n%s", code, out)
	}
	_ = holder.Wait()
```

Add `"strconv"` to the file's imports if it is missing. Remove any later `holder.Wait()` or holder kill in this test that would now double-wait (keep at most one `Wait` on `holder`).

- [ ] **Step 3: Run it repeatedly**

Run: `go test . -run TestDisagreeingSlotsRefusedAtEnrollAfterConfigChange -count=5`
Expected: PASS 5 of 5.

- [ ] **Step 4: Commit**

```bash
git add integration_test.go
git commit -m "test: hold racea until the reconfig lands, then release it through the lane

The holder held for a fixed 6 seconds, so on a loaded machine the
contender could reach raceb before the config change and the test failed
in most runs."
```

---

### Task 1: One place for the layout; open modes and the create-free probe

Every caller moves from `queues/<key>` to `lanes/<key>` in one step. `lane.OpenIn` takes the root explicitly, so Task 8 can point read-only commands at the old `queues/` root without creating or reaping anything there. The probe that plan 1 wrote inside `held` moves to `lane` so the migration can reuse it.

**Files:**
- Modify: `internal/lane/statedir.go:42-46`
- Modify: `internal/lane/queue.go` (the `Queue` struct, `Open`, `Logf`, `scanLocked`, `ListQueues`, `Exists`)
- Modify: `internal/lane/config.go` (`LoadConfig`)
- Create: `internal/lane/probe.go`
- Create: `internal/lane/layout_test.go`
- Modify: `internal/lockfile/lockfile.go`, `internal/lockfile/lockfile_test.go`
- Modify: `internal/held/verify.go` (imports and `probe`), `internal/held/verify_test.go:157`
- Modify: `internal/report/report.go:85`
- Modify: `internal/cli/cli.go:175-184`, `internal/cli/misc.go:207`
- Modify: `integration_test.go`, `config_test.go`, `held_test.go`, `kill_test.go`, `reentry_test.go` (paths)

**Interfaces:**
- Produces: `lane.LanesDir(stateDir string) string`; `lane.LaneDir(stateDir, key string) string`; `lane.QueuesDir(stateDir string) string` (legacy); `type lane.Mode int` with `lane.Create`, `lane.Existing`, `lane.ReadOnly`; `lane.OpenIn(root, key string, mode Mode) (*Queue, error)`; `lane.Open(stateDir, key string) (*Queue, error)` (now `OpenIn(LanesDir(stateDir), key, Create)`); `lane.ListIn(root string) ([]string, error)`; `lane.ListQueues(stateDir string) ([]string, error)`; `lane.ExistsIn(root, key string) bool`; `lane.Exists(stateDir, key string) bool`; `lane.ReadConfig(dir string) (Config, error)`; `lane.AppendLog(dir, format string, args ...any)`; `lane.LogPath(dir string) string`; `type lane.Probed struct{ Name string; Live bool; Ticket Ticket; PayloadErr, ProbeErr error }` with `(Probed) PID() int`; `lane.ProbeTicket(laneDir, name string) Probed`; `lane.ProbeLane(laneDir string) ([]Probed, error)`; `lockfile.IsFreeExisting(path string) (bool, error)`.
- Removed: `lane.QueueDir` (every caller uses `LaneDir`).

- [ ] **Step 1: Write the failing lane tests**

`internal/lane/layout_test.go`:

```go
package lane

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// treeSnapshot maps every path under root to its mode, size and
// modification time, so a test can prove that nothing was written.
func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTree(t *testing.T, what string, before, after map[string]string) {
	t.Helper()
	for p, v := range before {
		if after[p] != v {
			t.Fatalf("%s changed %s: %q -> %q", what, p, v, after[p])
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			t.Fatalf("%s created %s", what, p)
		}
	}
}

func TestLayoutPaths(t *testing.T) {
	s := filepath.Join("x", "state")
	if got := LanesDir(s); got != filepath.Join(s, "lanes") {
		t.Fatalf("LanesDir = %q", got)
	}
	if got := LaneDir(s, "k"); got != filepath.Join(s, "lanes", "k") {
		t.Fatalf("LaneDir = %q", got)
	}
	if got := QueuesDir(s); got != filepath.Join(s, "queues") {
		t.Fatalf("QueuesDir = %q", got)
	}
}

func TestOpenCreatesUnderLanesOnly(t *testing.T) {
	state := t.TempDir()
	q, err := Open(state, "k")
	if err != nil {
		t.Fatal(err)
	}
	q.Close()
	if !Exists(state, "k") {
		t.Fatal("Open did not create lanes/k")
	}
	if _, err := os.Lstat(QueuesDir(state)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open created queues/: %v", err)
	}
	keys, err := ListQueues(state)
	if err != nil || len(keys) != 1 || keys[0] != "k" {
		t.Fatalf("ListQueues = %v, %v", keys, err)
	}
}

func TestExistingModeNeverCreatesADirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "queues")
	if _, err := OpenIn(root, "k", Existing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Existing created the root")
	}
}

func TestReadOnlyModeChangesNothing(t *testing.T) {
	root := t.TempDir()
	q, err := OpenIn(root, "k", Create)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	en, err := q.Enroll(Ticket{Command: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	defer en.Release(0) // runs before q.Close: Release needs the registry handle
	// A dead ticket and a kill request whose ticket is gone: a normal scan
	// would remove both and log the reap.
	if err := os.WriteFile(filepath.Join(q.Dir, ticketName(1, 1)), []byte(`{"pid":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(q.Dir, ticketName(2, 2)+killExt), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	before := treeSnapshot(t, root)
	ro, err := OpenIn(root, "k", ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := ro.Observe(5)
	if err != nil {
		t.Fatal(err)
	}
	ro.Logf("queue=k event=nothing")
	ro.Close()
	if len(snap.Holders) != 1 || snap.Holders[0].File != en.Name() {
		t.Fatalf("read-only observe must still see the live ticket: %+v", snap.Holders)
	}
	sameTree(t, "a read-only observe", before, treeSnapshot(t, root))

	if _, err := OpenIn(root, "nope", ReadOnly); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only open of a missing lane: want ErrNotExist, got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "nope")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-only open created a lane")
	}
}

func TestProbeLaneFindsLiveTicketsAndCreatesNothing(t *testing.T) {
	root := t.TempDir()
	q, err := OpenIn(root, "k", Create)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	en, err := q.Enroll(Ticket{Command: []string{"zig", "build"}})
	if err != nil {
		t.Fatal(err)
	}
	defer en.Release(0)
	dead := ticketName(1, 4242)
	if err := os.WriteFile(filepath.Join(q.Dir, dead), []byte(`{"pid":4242}`), 0o644); err != nil {
		t.Fatal(err)
	}

	before := treeSnapshot(t, root)
	live, err := ProbeLane(q.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].Name != en.Name() || live[0].PID() != os.Getpid() || live[0].Ticket.CommandString() != "zig build" {
		t.Fatalf("ProbeLane = %+v", live)
	}
	if p := ProbeTicket(q.Dir, en.Name()); !p.Live {
		t.Fatal("ProbeTicket: the enrolled ticket is live")
	}
	if p := ProbeTicket(q.Dir, dead); p.Live {
		t.Fatal("ProbeTicket: an unlocked ticket is dead")
	}
	sameTree(t, "a probe", before, treeSnapshot(t, root))

	ghost := filepath.Join(root, "ghost")
	if live, err := ProbeLane(ghost); err != nil || len(live) != 0 {
		t.Fatalf("a missing lane holds nothing: %v %v", live, err)
	}
	if _, err := os.Lstat(ghost); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ProbeLane created a lane")
	}
}
```

Add to `internal/lockfile/lockfile_test.go`:

```go
func TestIsFreeExistingNeverCreates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent")
	if free, err := IsFreeExisting(path); !free || err != nil {
		t.Fatalf("a missing file has no owner: free=%v err=%v", free, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("IsFreeExisting created the file")
	}
	held, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if ok, _ := held.TryLock(); !ok {
		t.Fatal("lock")
	}
	if free, err := IsFreeExisting(path); free || err != nil {
		t.Fatalf("a held file is not free: free=%v err=%v", free, err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/lane/ ./internal/lockfile/`
Expected: compile errors: `undefined: LanesDir`, `undefined: LaneDir`, `undefined: OpenIn`, `undefined: Existing`, `undefined: ProbeLane`, `undefined: IsFreeExisting`.

- [ ] **Step 3: Add the layout paths**

In `internal/lane/statedir.go`, replace lines 42 to 46 (`QueuesDir` and `QueueDir`) with:

```go
// LanesDir is the parent of every lane directory (layout 2).
func LanesDir(stateDir string) string { return filepath.Join(stateDir, "lanes") }

// LaneDir is one lane's state directory. key must already be validated.
func LaneDir(stateDir, key string) string { return filepath.Join(LanesDir(stateDir), key) }

// QueuesDir is <state>/queues: the lanes root of every release before
// layout 2, and the fence file once a state directory is on layout 2. Only
// the migration (internal/machine) and tests use it; this binary never
// creates it as a directory.
func QueuesDir(stateDir string) string { return filepath.Join(stateDir, "queues") }
```

- [ ] **Step 4: Add `IsFreeExisting`**

In `internal/lockfile/lockfile.go`, replace `import "os"` with:

```go
import (
	"errors"
	"os"
)
```

and add after `IsFree`:

```go
// IsFreeExisting is IsFree for a path that must not be created: a missing
// file reads as free, because no process can hold a lock on a file that is
// gone. Read-only scans use it so that looking never writes.
func IsFreeExisting(path string) (bool, error) {
	l, err := OpenExisting(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	defer l.Close()
	return l.TryLock()
}
```

- [ ] **Step 5: Add open modes, roots and the log helpers to the queue**

In `internal/lane/queue.go`, replace the `Queue` struct and `Open` (from `type Queue struct {` through the end of `func Open`) with:

```go
type Queue struct {
	Key      string
	Dir      string
	registry *lockfile.File
	readOnly bool
}

// Mode says what a Queue handle may create or change.
type Mode int

const (
	// Create makes the lane directory and its registry lock when missing.
	// Only commands that have migrated the state directory use it, so it
	// never runs against the old queues/ root.
	Create Mode = iota
	// Existing opens a lane that already exists and never creates a
	// directory, so it cannot recreate a root the migration has just moved
	// away. It may create the registry lock inside an existing lane
	// directory, and scans reap dead tickets as usual.
	Existing
	// ReadOnly opens only files that already exist and changes nothing:
	// scans skip dead tickets without removing them and Logf writes
	// nothing. Read-only commands use it on a layout not upgraded yet.
	ReadOnly
)

// Open prepares key's lane under <stateDir>/lanes and opens its registry
// lock file. It does not take any lock.
func Open(stateDir, key string) (*Queue, error) {
	return OpenIn(LanesDir(stateDir), key, Create)
}

// OpenIn opens key's lane directory inside root (lanes/ on layout 2, the old
// queues/ for a read-only view of a layout not upgraded yet) in mode.
func OpenIn(root, key string, mode Mode) (*Queue, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	dir := filepath.Join(root, key)
	var reg *lockfile.File
	var err error
	switch mode {
	case Create:
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create queue directory: %w", err)
		}
		reg, err = lockfile.Open(RegistryLockPath(dir))
	case Existing:
		if fi, serr := os.Stat(dir); serr != nil || !fi.IsDir() {
			return nil, fmt.Errorf("queue %q has no state in %s: %w", key, root, os.ErrNotExist)
		}
		reg, err = lockfile.Open(RegistryLockPath(dir))
	case ReadOnly:
		reg, err = lockfile.OpenExisting(RegistryLockPath(dir))
	default:
		return nil, fmt.Errorf("unknown open mode %d", mode)
	}
	if err != nil {
		return nil, fmt.Errorf("open registry lock: %w", err)
	}
	return &Queue{Key: key, Dir: dir, registry: reg, readOnly: mode == ReadOnly}, nil
}
```

Replace `Logf` with:

```go
// Logf appends one line to the queue's handoff log. Log failures are never
// fatal: the log is history for humans, not state the algorithm reads. A
// read-only handle writes nothing.
func (q *Queue) Logf(format string, args ...any) {
	if q.readOnly {
		return
	}
	AppendLog(q.Dir, format, args...)
}

// AppendLog appends one timestamped line to the lane.log of a lane
// directory. The migration uses it for lanes it touches without opening
// them.
func AppendLog(dir, format string, args ...any) {
	f, err := os.OpenFile(LogPath(dir), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

// LogPath is the lane.log of a lane directory.
func LogPath(dir string) string { return filepath.Join(dir, logName) }
```

In `scanLocked`, replace:

```go
		path := ticketPath(q.Dir, de.Name())
		free, err := lockfile.IsFree(path)
		if err != nil {
```

with:

```go
		path := ticketPath(q.Dir, de.Name())
		var free bool
		var err error
		if q.readOnly {
			free, err = lockfile.IsFreeExisting(path)
		} else {
			free, err = lockfile.IsFree(path)
		}
		if err != nil {
```

and make the first line inside `if free {` read:

```go
		if free {
			if q.readOnly {
				continue
			}
```

(the existing comment, removals and `q.Logf` follow unchanged). Replace `q.reapKillFiles(names)` near the end of `scanLocked` with:

```go
	if !q.readOnly {
		q.reapKillFiles(names)
	}
```

Replace `ListQueues` and `Exists` at the end of the file with:

```go
// ListQueues returns the keys that have state under <stateDir>/lanes.
func ListQueues(stateDir string) ([]string, error) { return ListIn(LanesDir(stateDir)) }

// ListIn returns the keys that have a lane directory inside root. A missing
// root has none.
func ListIn(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, de := range entries {
		if de.IsDir() && ValidateKey(de.Name()) == nil {
			keys = append(keys, de.Name())
		}
	}
	return keys, nil
}

// Exists reports whether a queue key has state under <stateDir>/lanes.
func Exists(stateDir, key string) bool { return ExistsIn(LanesDir(stateDir), key) }

// ExistsIn reports whether root holds a lane directory for key.
func ExistsIn(root, key string) bool {
	fi, err := os.Stat(filepath.Join(root, key))
	return err == nil && fi.IsDir()
}
```

- [ ] **Step 6: Read config by directory**

In `internal/lane/config.go`, replace `LoadConfig` with:

```go
// LoadConfig reads the queue's config. A missing file is the zero Config
// and no error; a file that cannot be parsed is an error, because a queue
// that silently forgot it was closed would let the old key back in.
func (q *Queue) LoadConfig() (Config, error) { return ReadConfig(q.Dir) }

// ReadConfig is LoadConfig for a lane directory that is not open. It takes
// no lock; the migration and doctor use it on lanes nobody can enroll on at
// that moment, and its reads are atomic because every write is a rename.
func ReadConfig(dir string) (Config, error) {
	path := filepath.Join(dir, configName)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("config %s is not valid JSON: %w", path, err)
	}
	if c.Schema > ConfigSchema {
		return Config{}, &NewerSchemaError{Path: path, Schema: c.Schema}
	}
	return c, nil
}
```

- [ ] **Step 7: Add the probe**

`internal/lane/probe.go`:

```go
package lane

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/deblasis/incoda/internal/lockfile"
)

// Probed is one ticket as the create-free probe saw it.
type Probed struct {
	// Name is the ticket file name.
	Name string
	// Live is set when another process holds the ticket's lock.
	Live bool
	// Ticket is a live ticket's payload; PayloadErr says why it could not
	// be read.
	Ticket     Ticket
	PayloadErr error
	// ProbeErr is set when the probe itself failed: the ticket could not be
	// opened for a reason other than being absent, or a lock call errored.
	// ProbeTicket reports such a ticket as not live (the INCODA_HELD rule of
	// plan 1); ProbeLane reports it as live, because the migration must
	// never read "cannot tell" as idle.
	ProbeErr error
}

// PID is the holder's pid: the payload's, else the one in the ticket name.
func (p Probed) PID() int {
	if p.Ticket.PID != 0 {
		return p.Ticket.PID
	}
	if ord, ok := parseTicketName(p.Name); ok {
		return ord.pid
	}
	return 0
}

// ProbeTicket is the liveness probe of spec 2.6 step 2 for one ticket: it
// opens the lane's registry lock without O_CREATE and takes it (the lock
// Enroll and Release hold, in this binary and in every older one), opens
// the ticket without O_CREATE and tries its lock. A missing lane directory,
// registry lock or ticket is dead. It never creates or removes a file and
// never keeps a lock.
//
// The caller must not hold this lane's registry lock through another
// handle: the blocking Lock here would wait for itself.
func ProbeTicket(laneDir, name string) Probed {
	reg, err := lockfile.OpenExisting(RegistryLockPath(laneDir))
	if err != nil {
		return Probed{Name: name}
	}
	defer reg.Close()
	if err := reg.Lock(); err != nil {
		return Probed{Name: name, ProbeErr: err}
	}
	return probeLocked(laneDir, name)
}

// ProbeLane probes every ticket file in laneDir under one hold of its
// registry lock and returns the live ones in file name order. A missing
// directory or registry lock holds no live ticket (an older incoda creates
// the registry lock before any ticket). The migration uses it for the idle
// checks of spec 3.3 M2 and M5; like ProbeTicket it creates, removes and
// keeps nothing.
func ProbeLane(laneDir string) ([]Probed, error) {
	reg, err := lockfile.OpenExisting(RegistryLockPath(laneDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer reg.Close()
	if err := reg.Lock(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(laneDir)
	if err != nil {
		return nil, err
	}
	var live []Probed
	for _, de := range entries {
		if de.IsDir() {
			continue
		}
		if _, ok := parseTicketName(de.Name()); !ok {
			continue
		}
		p := probeLocked(laneDir, de.Name())
		if p.ProbeErr != nil {
			p.Live = true
		}
		if p.Live {
			live = append(live, p)
		}
	}
	return live, nil
}

// probeLocked probes one ticket. The caller holds the lane's registry lock.
func probeLocked(laneDir, name string) Probed {
	p := Probed{Name: name}
	path := ticketPath(laneDir, name)
	tf, err := lockfile.OpenExisting(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			p.ProbeErr = err
		}
		return p
	}
	free, err := tf.TryLock()
	tf.Close()
	if err != nil {
		p.ProbeErr = err
		return p
	}
	if free {
		return p
	}
	p.Live = true
	b, err := os.ReadFile(path)
	if err != nil {
		p.PayloadErr = err
		return p
	}
	if err := json.Unmarshal(b, &p.Ticket); err != nil {
		p.PayloadErr = err
	}
	return p
}
```

- [ ] **Step 8: Use the shared probe in `held`**

In `internal/held/verify.go`, change the import block to:

```go
import (
	"errors"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/procinfo"
)
```

and replace `probe` with:

```go
// probe reports whether e's ticket is held by a live process, and the pid
// recorded in its payload. perr reports a payload that could not be read.
// The probe itself is lane.ProbeTicket, shared with the migration's idle
// checks; a probe that failed counts as dead here, as it did in plan 1.
func probe(stateDir string, e Entry) (live bool, payloadPID int, perr error) {
	p := lane.ProbeTicket(lane.LaneDir(stateDir, e.Key), e.Ticket)
	if !p.Live {
		return false, 0, nil
	}
	if p.PayloadErr != nil {
		return true, 0, p.PayloadErr
	}
	if p.Ticket.PID == 0 {
		return true, 0, errors.New("ticket payload has no pid")
	}
	return true, p.Ticket.PID, nil
}
```

In `internal/held/verify_test.go:157`, replace `lane.QueueDir(state, "dead")` with `lane.LaneDir(state, "dead")`.

- [ ] **Step 9: Move the remaining callers**

- `internal/report/report.go:85`: `Dir: lane.QueueDir(dir, key),` becomes `Dir: lane.LaneDir(dir, key),`.
- `internal/cli/cli.go`, replace `stateDir` with:

```go
// stateDir resolves the state directory and creates it. It never creates
// <state>/queues: that path belongs to older releases, and on layout 2 it is
// the fence file (internal/machine).
func stateDir() (string, error) {
	d, err := lane.StateDir()
	if err != nil {
		return "", exitWith(ExitState, "cannot resolve state directory: %v", err)
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", exitWith(ExitState, "cannot create state directory %s: %v", d, err)
	}
	return d, nil
}
```

- `internal/cli/misc.go:207`: `os.MkdirAll(lane.QueuesDir(dir), 0o755)` becomes `os.MkdirAll(dir, 0o755)`.

- [ ] **Step 10: Point the integration tests at `lanes/`**

In `integration_test.go`, add above `countTickets`:

```go
// laneDir is where a key's state lives on layout 2.
func laneDir(state, key string) string { return filepath.Join(state, "lanes", key) }
```

and in `countTickets` replace `filepath.Join(state, "queues", key)` with `laneDir(state, key)`. Replace every other hard-coded path:

| file:line | old | new |
|---|---|---|
| `config_test.go:245` | `filepath.Join(state, "queues", "newer", "config.json")` | `filepath.Join(laneDir(state, "newer"), "config.json")` |
| `config_test.go:264` | `filepath.Join(state, "queues", "oneline", "lane.log")` | `filepath.Join(laneDir(state, "oneline"), "lane.log")` |
| `held_test.go:47` | `filepath.Join(state, "queues", "hd", "lane.log")` | `filepath.Join(laneDir(state, "hd"), "lane.log")` |
| `held_test.go:78` | `filepath.Join(state, "queues", "na")` | `laneDir(state, "na")` |
| `kill_test.go:66` | `filepath.Join(state, "queues", "kill", "lane.log")` | `filepath.Join(laneDir(state, "kill"), "lane.log")` |
| `kill_test.go:179` | `filepath.Join(state, "queues", "kf", "lane.log")` | `filepath.Join(laneDir(state, "kf"), "lane.log")` |
| `kill_test.go:216` | `filepath.Join(state, "queues", "killesc", "lane.log")` | `filepath.Join(laneDir(state, "killesc"), "lane.log")` |
| `reentry_test.go:37` | `filepath.Join(state, "queues", "re", "lane.log")` | `filepath.Join(laneDir(state, "re"), "lane.log")` |
| `reentry_test.go:73` | `filepath.Join(state, "queues", "acct", "lane.log")` | `filepath.Join(laneDir(state, "acct"), "lane.log")` |

Add `"errors"` to the imports of `integration_test.go` (it does not import it yet) and add:

```go
// TestStateLivesUnderLanes: a run keeps its lane under lanes/ and never
// creates queues/, the path older releases use.
func TestStateLivesUnderLanes(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "run", "--queue", "lay", "--quiet", "--", stamp, filepath.Join(t.TempDir(), "s"), "s", "1"); code != 0 {
		t.Fatalf("run: exit %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(laneDir(state, "lay"), "lane.log")); err != nil {
		t.Fatalf("lane.log not under lanes/: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(state, "queues")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("this binary must never create queues/: %v", err)
	}
}
```

Verify nothing else still names the old path: `grep -rn '"queues", ' --include='*.go' .` prints nothing, and `grep -rnw QueueDir --include='*.go' .` prints nothing.

- [ ] **Step 11: Run the tests**

Run: `go test ./internal/lane/ ./internal/lockfile/ ./internal/held/ && go test . -run 'TestStateLivesUnderLanes|TestDeadHeldEntry|TestLiveNonAncestor|TestKill|TestReentrant|TestNewerConfigSchema|TestLogLineStaysOneLine|TestDisagreeing' -v`
Expected: PASS.

- [ ] **Step 12: Run the full gates**

Run: `GOOS=windows go vet ./... && just ci`
Expected: PASS.

- [ ] **Step 13: Commit**

```bash
git add internal/lane internal/lockfile internal/held internal/report/report.go internal/cli/cli.go internal/cli/misc.go integration_test.go config_test.go held_test.go kill_test.go reentry_test.go
git commit -m "feat: lanes live under lanes/, one place knows the layout

statedir.go names LanesDir, LaneDir and the legacy QueuesDir. Queues open
in a mode (create, existing, read only) against an explicit root, so a
read-only view of the old layout creates and reaps nothing. The
create-free ticket probe moves from held to lane for reuse by the
migration. This binary no longer creates queues/."
```

---

### Task 2: `machine.lock` with its note, waiting lines and upgrade-blocked waiters

`machine.lock` serialises migration, re-fencing and registry writes (spec 3.1). It is taken with `TryLock` plus a poll inside the caller's `--wait` budget, never less than 2s, and never with the blocking `Lock()`. The holder writes a one-line note into the lock file; waiters read it to say who holds it, and refuse at once when a listed blocker is their own ancestor.

**Files:**
- Create: `internal/machine/errors.go`
- Create: `internal/machine/note.go`
- Create: `internal/machine/lock.go`
- Create: `internal/machine/lock_test.go`
- Create: `internal/procinfo/alive_unix.go`, `internal/procinfo/alive_windows.go`, `internal/procinfo/alive_test.go`

**Interfaces:**
- Consumes: `lockfile.Open`, `(*lockfile.File).TryLock`, `Truncate`, `Close`; `procinfo.Chain` (`Contains`, `Skip`); `lane.ValidateKey`.
- Produces: `type machine.StateError struct{ Msg string }`, `type machine.Refusal struct{ Msg string }`, `type machine.Timeout struct{ Msg string }` (each `Error() string` returns `Msg`, the text after `incoda: `; lines after the first are joined with `"\nincoda: "`); `machine.stateErrorf(format string, args ...any) *StateError` (prefixes `machine-state: `); `machine.joinLines(lines []string) string`; `machine.upgradeBlocked(pid int, key string) *Refusal`; `type machine.Blocker struct{ Key string; PID int; Command string }`; `type machine.Note struct{ PID int; Op string; Since time.Time; Blockers []Blocker }` with `String() string`; `machine.ParseNote(s string) (Note, bool)`; `machine.ReadNote(stateDir string) (Note, bool)`; `machine.blockerList(bs []Blocker) string`; `machine.LockPath(stateDir string) string`; `type machine.Lock`; `type machine.LockOptions struct{ Op string; Start time.Time; Wait, Poll time.Duration; Chain procinfo.Chain; Stderr io.Writer }`; `machine.AcquireLock(stateDir string, o LockOptions) (*Lock, error)`; `(*Lock) SetBlockers(bs []Blocker) error`; `(*Lock) Release()`; `machine.lockDeadline(start time.Time, wait time.Duration, now time.Time) time.Time`; `procinfo.Alive(pid int) bool`.

- [ ] **Step 1: Write the failing tests**

`internal/procinfo/alive_test.go`:

```go
package procinfo

import (
	"os"
	"os/exec"
	"testing"
)

func TestAlive(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Fatal("this process is alive")
	}
	if Alive(0) || Alive(-1) {
		t.Fatal("pid 0 and negative pids are never alive")
	}
	// A child that has exited and been waited for is gone.
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if Alive(cmd.Process.Pid) {
		t.Fatalf("pid %d exited and was reaped", cmd.Process.Pid)
	}
}
```

`internal/machine/lock_test.go`:

```go
package machine

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/procinfo"
)

func TestNoteRoundTrip(t *testing.T) {
	n := Note{PID: 4711, Op: "migrate", Since: time.Date(2026, 10, 2, 9, 14, 3, 0, time.UTC),
		Blockers: []Blocker{{Key: "builds", PID: 4711}, {Key: "kungfoo-ui", PID: 5120}}}
	s := n.String()
	if s != "pid=4711 op=migrate since=2026-10-02T09:14:03Z blockers=4711:builds,5120:kungfoo-ui" {
		t.Fatalf("note = %q", s)
	}
	got, ok := ParseNote(s)
	if !ok || got.PID != 4711 || got.Op != "migrate" || !got.Since.Equal(n.Since) || len(got.Blockers) != 2 ||
		got.Blockers[1] != (Blocker{Key: "kungfoo-ui", PID: 5120}) {
		t.Fatalf("ParseNote = %+v %v", got, ok)
	}
	if got, ok := ParseNote("pid=7 op=refence since=2026-10-02T09:14:03Z future=1"); !ok || got.Op != "refence" {
		t.Fatalf("an unknown field from a newer incoda is ignored: %+v %v", got, ok)
	}
	for _, bad := range []string{
		"",
		"pid=x op=migrate since=2026-10-02T09:14:03Z",
		"pid=1 op=Migrate since=2026-10-02T09:14:03Z",
		"pid=1 op=migrate",
		"pid=1 op=migrate since=yesterday",
		"pid=1 op=migrate since=2026-10-02T09:14:03Z blockers=1:a/b",
		"pid=1 op=migrate since=2026-10-02T09:14:03Z blockers=x:builds",
	} {
		if _, ok := ParseNote(bad); ok {
			t.Fatalf("ParseNote(%q) must fail", bad)
		}
	}
}

func TestLockDeadline(t *testing.T) {
	now := time.Now()
	if got := lockDeadline(now, 0, now); got.Sub(now) != 2*time.Second {
		t.Fatalf("--wait 0 still gets 2s: %v", got.Sub(now))
	}
	if got := lockDeadline(now.Add(-10*time.Second), 30*time.Minute, now); got.Sub(now) != 30*time.Minute-10*time.Second {
		t.Fatalf("the budget runs from the start: %v", got.Sub(now))
	}
	if got := lockDeadline(now.Add(-time.Hour), 30*time.Minute, now); got.Sub(now) != 2*time.Second {
		t.Fatalf("a spent budget still gets 2s: %v", got.Sub(now))
	}
	if !lockDeadline(now, -1, now).IsZero() {
		t.Fatal("a negative --wait waits forever")
	}
}

func TestAcquireLockWritesAndClearsTheNote(t *testing.T) {
	state := t.TempDir()
	lk, err := AcquireLock(state, LockOptions{Op: "migrate", Start: time.Now(), Wait: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	n, ok := ReadNote(state)
	if !ok || n.PID != os.Getpid() || n.Op != "migrate" || len(n.Blockers) != 0 {
		t.Fatalf("note = %+v %v", n, ok)
	}
	if err := lk.SetBlockers([]Blocker{{Key: "builds", PID: 4711}}); err != nil {
		t.Fatal(err)
	}
	if n, _ := ReadNote(state); len(n.Blockers) != 1 || n.Blockers[0].PID != 4711 {
		t.Fatalf("blockers not in the note: %+v", n)
	}
	lk.Release()
	lk.Release() // idempotent
	if b, _ := os.ReadFile(LockPath(state)); len(b) != 0 {
		t.Fatalf("Release must clear the note, left %q", b)
	}
}

func TestAcquireLockWaitsAtLeastTwoSecondsThenTimesOut(t *testing.T) {
	state := t.TempDir()
	holder, err := AcquireLock(state, LockOptions{Op: "migrate", Start: time.Now(), Wait: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	if err := holder.SetBlockers([]Blocker{{Key: "builds", PID: 4711}}); err != nil {
		t.Fatal(err)
	}
	var errBuf bytes.Buffer
	start := time.Now()
	_, err = AcquireLock(state, LockOptions{Op: "migrate", Start: start, Wait: 0, Poll: 100 * time.Millisecond, Stderr: &errBuf})
	var to *Timeout
	if !errors.As(err, &to) || to.Msg != fmt.Sprintf("machine-lock-timeout: held by pid %d (migrate)", os.Getpid()) {
		t.Fatalf("want the machine-lock-timeout, got %v", err)
	}
	if el := time.Since(start); el < 2*time.Second {
		t.Fatalf("a --wait 0 caller still gets 2s for machine.lock, waited %v", el)
	}
	out := errBuf.String()
	want := fmt.Sprintf("incoda: waiting for machine.lock: pid %d migrate since ", os.Getpid())
	if !strings.HasPrefix(out, want) || !strings.HasSuffix(out, "; waiting for older runs: builds pid 4711\n") || strings.Count(out, "\n") != 1 {
		t.Fatalf("want one waiting line naming the holder and its blockers, got:\n%s", out)
	}
}

func TestAcquireLockRefusesWhenABlockerIsAnAncestor(t *testing.T) {
	state := t.TempDir()
	holder, err := AcquireLock(state, LockOptions{Op: "migrate", Start: time.Now(), Wait: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	if err := holder.SetBlockers([]Blocker{{Key: "builds", PID: 4711}}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = AcquireLock(state, LockOptions{Op: "migrate", Start: start, Wait: time.Minute, Poll: 50 * time.Millisecond,
		Chain: procinfo.Chain{PIDs: []int{999, 4711, 1}}})
	var rf *Refusal
	if !errors.As(err, &rf) || rf.Msg != `upgrade-blocked: an older incoda (pid 4711, an ancestor of this process) holds "builds"; rerun the outer command after it exits` {
		t.Fatalf("want upgrade-blocked, got %v", err)
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("upgrade-blocked must not wait, waited %v", el)
	}
	// Windows has no ancestry walk (Skip): the same waiter times out instead.
	_, err = AcquireLock(state, LockOptions{Op: "migrate", Start: time.Now(), Wait: 0, Poll: 50 * time.Millisecond,
		Chain: procinfo.Chain{Skip: true, PIDs: []int{4711}}})
	var to *Timeout
	if !errors.As(err, &to) {
		t.Fatalf("a Skip chain is never upgrade-blocked, got %v", err)
	}
}

func TestAcquireLockGetsTheLockOnceFreed(t *testing.T) {
	state := t.TempDir()
	holder, err := AcquireLock(state, LockOptions{Op: "refence", Start: time.Now(), Wait: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		holder.Release()
	}()
	lk, err := AcquireLock(state, LockOptions{Op: "migrate", Start: time.Now(), Wait: time.Minute, Poll: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	lk.Release()
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/machine/ ./internal/procinfo/`
Expected: `internal/machine` does not exist yet (no Go files), and `undefined: Alive` in procinfo.

- [ ] **Step 3: Add `Alive`**

`internal/procinfo/alive_unix.go`:

```go
//go:build !windows

package procinfo

import (
	"errors"

	"golang.org/x/sys/unix"
)

// Alive reports whether a process with pid exists. EPERM means it exists
// but belongs to someone else, which still counts.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := unix.Kill(pid, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}
```

`internal/procinfo/alive_windows.go`:

```go
//go:build windows

package procinfo

import (
	"errors"

	"golang.org/x/sys/windows"
)

// stillActive is STILL_ACTIVE, the exit code GetExitCodeProcess reports for
// a running process; golang.org/x/sys/windows has no name for it.
const stillActive = 259

// Alive reports whether a process with pid is running. Access denied means
// it exists but belongs to someone else, which still counts.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return true
	}
	return code == stillActive
}
```

- [ ] **Step 4: Add the error types**

`internal/machine/errors.go`:

```go
// Package machine owns the machine-level state of layout 2: machine.lock,
// machine.json, the queues fence, and the migration from the queues/
// layout of older releases (spec 2.1, 2.3, 3.1 to 3.3, 3.6).
package machine

import (
	"fmt"
	"strings"
)

// StateError fails closed with exit 122. Msg is the whole message after
// "incoda: "; a message of several lines carries "\nincoda: " between them.
type StateError struct {
	Msg string
	// newer marks the "written by a newer incoda" refusal, which a registry
	// rebuild must not overwrite.
	newer bool
}

func (e *StateError) Error() string { return e.Msg }

// Refusal is an exit-120 refusal (upgrade-blocked:, kind-busy:).
type Refusal struct{ Msg string }

func (e *Refusal) Error() string { return e.Msg }

// Timeout is an exit-121 refusal: the --wait budget ran out before any lane
// was taken (machine-lock-timeout:, upgrade-timeout:).
type Timeout struct{ Msg string }

func (e *Timeout) Error() string { return e.Msg }

func stateErrorf(format string, args ...any) *StateError {
	return &StateError{Msg: "machine-state: " + fmt.Sprintf(format, args...)}
}

// joinLines joins the lines of one message so that every line after the
// first also starts with "incoda: " once the CLI prints the message.
func joinLines(lines []string) string { return strings.Join(lines, "\nincoda: ") }

// upgradeBlocked is the refusal of spec 3.3 M2 and 3.1: an older incoda that
// the migration waits for is an ancestor of this process, so waiting would
// never end.
func upgradeBlocked(pid int, key string) *Refusal {
	return &Refusal{Msg: fmt.Sprintf("upgrade-blocked: an older incoda (pid %d, an ancestor of this process) holds %q; rerun the outer command after it exits", pid, key)}
}
```

- [ ] **Step 5: Add the note**

`internal/machine/note.go`:

```go
package machine

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/lane"
)

// Blocker is one live ticket of an older incoda that the migration waits
// for. Command is already escaped for display; it is empty in a note read
// back from machine.lock, which carries only pid and key.
type Blocker struct {
	Key     string
	PID     int
	Command string
}

// Note is the line the machine.lock holder writes into the lock file, so
// waiters and status can say who holds it, for what, and which older runs
// a migration waits for (spec 3.1).
type Note struct {
	PID      int
	Op       string
	Since    time.Time
	Blockers []Blocker
}

// String renders the note: pid=N op=<op> since=<RFC 3339 UTC> and, when
// there are any, blockers=<pid>:<key>,...
func (n Note) String() string {
	s := fmt.Sprintf("pid=%d op=%s since=%s", n.PID, n.Op, n.Since.UTC().Format(time.RFC3339))
	if len(n.Blockers) > 0 {
		parts := make([]string, len(n.Blockers))
		for i, b := range n.Blockers {
			parts[i] = fmt.Sprintf("%d:%s", b.PID, b.Key)
		}
		s += " blockers=" + strings.Join(parts, ",")
	}
	return s
}

// ParseNote reads a note back. Anything that does not parse, including a
// note caught half rewritten, reports false: callers then say less, never
// something wrong. Fields a newer incoda adds are ignored.
func ParseNote(s string) (Note, bool) {
	var n Note
	for _, f := range strings.Fields(s) {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return Note{}, false
		}
		switch k {
		case "pid":
			p, err := strconv.Atoi(v)
			if err != nil || p <= 0 {
				return Note{}, false
			}
			n.PID = p
		case "op":
			if !validOp(v) {
				return Note{}, false
			}
			n.Op = v
		case "since":
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return Note{}, false
			}
			n.Since = t
		case "blockers":
			for _, b := range strings.Split(v, ",") {
				ps, key, ok := strings.Cut(b, ":")
				p, err := strconv.Atoi(ps)
				if !ok || err != nil || p <= 0 || lane.ValidateKey(key) != nil {
					return Note{}, false
				}
				n.Blockers = append(n.Blockers, Blocker{Key: key, PID: p})
			}
		}
	}
	if n.PID == 0 || n.Op == "" || n.Since.IsZero() {
		return Note{}, false
	}
	return n, true
}

// validOp keeps the op printable as is: lower-case letters and '-'.
func validOp(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && r != '-' {
			return false
		}
	}
	return true
}

// ReadNote reads the note in <state>/machine.lock without locking it (the
// lock is a byte range far past the note on Windows and an flock on Unix,
// so the content stays readable).
func ReadNote(stateDir string) (Note, bool) {
	b, err := os.ReadFile(LockPath(stateDir))
	if err != nil {
		return Note{}, false
	}
	return ParseNote(string(b))
}

// blockerList names blockers the way the waiting line and the status banner
// do: "builds pid 4711, kungfoo-ui pid 5120".
func blockerList(bs []Blocker) string {
	parts := make([]string, len(bs))
	for i, b := range bs {
		parts[i] = fmt.Sprintf("%s pid %d", b.Key, b.PID)
	}
	return strings.Join(parts, ", ")
}
```

- [ ] **Step 6: Add the lock**

`internal/machine/lock.go`:

```go
package machine

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/deblasis/incoda/internal/lockfile"
	"github.com/deblasis/incoda/internal/procinfo"
)

const (
	lockName = "machine.lock"
	// minLockWait is the floor of a machine.lock wait: its normal holds
	// last milliseconds, so even --wait 0 gets this long (spec 3.1).
	minLockWait = 2 * time.Second
	// lockNotify is how often a waiter repeats its waiting line.
	lockNotify = 60 * time.Second
)

// LockPath is <state>/machine.lock, a file no released incoda knows about.
func LockPath(stateDir string) string { return filepath.Join(stateDir, lockName) }

// Lock is a held machine.lock. It is taken for migration and its recovery,
// re-fencing and registry writes, and never while waiting for user input.
type Lock struct {
	f     *lockfile.File
	op    string
	since time.Time
}

// LockOptions says how long to wait and where to say so.
type LockOptions struct {
	// Op names the holder's operation in the note: migrate, refence,
	// recover, rebuild-registry.
	Op string
	// Start and Wait are the caller's --wait budget, which runs from the
	// start of the command; a negative Wait waits forever.
	Start time.Time
	Wait  time.Duration
	// Poll is the retry interval; zero means 500ms.
	Poll time.Duration
	// Chain is this process's parent chain, computed once by the caller. A
	// waiter whose chain contains a blocker named in the holder's note is
	// upgrade-blocked at once (Unix; Windows chains are Skip).
	Chain  procinfo.Chain
	Stderr io.Writer
}

// lockDeadline is when a machine.lock wait gives up: the end of the --wait
// budget, but never less than minLockWait from now. The zero time means
// never.
func lockDeadline(start time.Time, wait time.Duration, now time.Time) time.Time {
	if wait < 0 {
		return time.Time{}
	}
	if start.IsZero() {
		start = now
	}
	d := start.Add(wait).Sub(now)
	if d < minLockWait {
		d = minLockWait
	}
	return now.Add(d)
}

// AcquireLock takes machine.lock with TryLock and a poll, never the
// blocking Lock (spec 3.1). Waiters are not FIFO. While it waits it reads
// the holder's note on every poll, prints "waiting for machine.lock: ..."
// after its first failed poll and then every 60s, and refuses with
// upgrade-blocked when a blocker in the note is its own ancestor. On budget
// expiry it returns a Timeout: machine-lock-timeout.
func AcquireLock(stateDir string, o LockOptions) (*Lock, error) {
	stderr := o.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	poll := o.Poll
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	f, err := lockfile.Open(LockPath(stateDir))
	if err != nil {
		return nil, stateErrorf("cannot open %s: %v", LockPath(stateDir), err)
	}
	deadline := lockDeadline(o.Start, o.Wait, time.Now())
	var printedAt time.Time
	for attempt := 0; ; attempt++ {
		ok, err := f.TryLock()
		if err != nil {
			f.Close()
			return nil, stateErrorf("machine.lock: %v", err)
		}
		if ok {
			l := &Lock{f: f, op: o.Op, since: time.Now()}
			if err := l.SetBlockers(nil); err != nil {
				l.Release()
				return nil, stateErrorf("cannot write the machine.lock note: %v", err)
			}
			return l, nil
		}
		note, noted := ReadNote(stateDir)
		if noted && !o.Chain.Skip {
			for _, b := range note.Blockers {
				if o.Chain.Contains(b.PID) {
					f.Close()
					return nil, upgradeBlocked(b.PID, b.Key)
				}
			}
		}
		now := time.Now()
		if !deadline.IsZero() && !now.Before(deadline) {
			f.Close()
			return nil, lockTimeout(note, noted)
		}
		if attempt > 0 && (printedAt.IsZero() || now.Sub(printedAt) >= lockNotify) {
			fmt.Fprintf(stderr, "incoda: %s\n", waitingLine(note, noted))
			printedAt = now
		}
		time.Sleep(poll)
	}
}

func waitingLine(n Note, ok bool) string {
	if !ok {
		return "waiting for machine.lock (its holder has not written its note yet)"
	}
	s := fmt.Sprintf("waiting for machine.lock: pid %d %s since %s", n.PID, n.Op, n.Since.UTC().Format(time.RFC3339))
	if len(n.Blockers) > 0 {
		s += "; waiting for older runs: " + blockerList(n.Blockers)
	}
	return s
}

func lockTimeout(n Note, ok bool) *Timeout {
	if !ok {
		return &Timeout{Msg: "machine-lock-timeout: held by a process that left no note"}
	}
	return &Timeout{Msg: fmt.Sprintf("machine-lock-timeout: held by pid %d (%s)", n.PID, n.Op)}
}

// SetBlockers rewrites the note (Truncate) with this holder's pid, op and
// start time, plus the older runs it waits for.
func (l *Lock) SetBlockers(bs []Blocker) error {
	return l.f.Truncate([]byte(Note{PID: os.Getpid(), Op: l.op, Since: l.since, Blockers: bs}.String()))
}

// Release clears the note and drops the lock. It is safe to call more than
// once. A holder that dies instead leaves its note behind; the kernel still
// frees the lock, and readers check the pid (status) or the lock itself
// (waiters) before trusting it.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = l.f.Truncate(nil)
	_ = l.f.Close()
	l.f = nil
}
```

- [ ] **Step 7: Run the tests**

Run: `go test ./internal/machine/ ./internal/procinfo/ -v -run 'TestNote|TestLock|TestAcquire|TestAlive'`
Expected: PASS (the timeout test takes about 2s).

- [ ] **Step 8: Run the full gates**

Run: `GOOS=windows go vet ./... && just ci`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/machine internal/procinfo
git commit -m "feat: machine.lock with a holder note and upgrade-blocked waiters

machine.lock is taken by TryLock and a poll inside the caller's --wait
budget, never under 2s. The holder notes its pid, op, start and the older
runs it waits for; waiters print who holds it, time out with
machine-lock-timeout, and refuse at once with upgrade-blocked when a
listed blocker is their own ancestor."
```

---

### Task 3: `machine.json`, written atomically under `machine.lock`

`machine.json` is the sole registry of which keys are pools and of the layout version (spec 2.1). Readers read it without a lock; the rename makes the read atomic. Writers hold `machine.lock`, write a temp file and rename it (with the Windows retry of spec 4.4, now shared with `config.json` through `internal/atomicfile`), bump `generation`, and keep fields this binary does not know. Missing, unreadable, malformed or newer files fail closed.

**Files:**
- Create: `internal/atomicfile/atomicfile.go`, `internal/atomicfile/atomicfile_test.go`
- Modify: `internal/lane/config.go` (`UpdateConfig` uses `atomicfile`; delete `renameRetry`; drop the `runtime` and `time` imports)
- Create: `internal/machine/registry.go`, `internal/machine/registry_test.go`

**Interfaces:**
- Consumes: `machine.Lock` (Task 2), `machine.stateErrorf`, `lane.ValidateKey`, `textsafe.Escape`.
- Produces: `atomicfile.Write(path string, data []byte, perm os.FileMode) error`; `atomicfile.Rename(from, to string) error`; `machine.RegistrySchema = 1`; `machine.Layout = 2`; `machine.BootstrapPools() []string` (`builds`, `computer-use`, `tests`, `vm`, sorted); `type machine.Registry struct{ Schema, Layout int; Generation int64; Pools []string; MigratedBy, MigratedAt string }` (unknown fields kept) with `(*Registry) IsPool(key string) bool`; `machine.ErrNoRegistry`; `machine.RegistryPath(stateDir string) string`; `machine.ReadRegistry(stateDir string) (*Registry, error)` (missing: `ErrNoRegistry`; anything else wrong: `*StateError`); `machine.writeRegistry(stateDir string, lk *Lock, r *Registry) error`; `machine.UpdateRegistry(stateDir string, lk *Lock, fn func(*Registry) error) (*Registry, error)` (plan 3 uses it for kind changes); `machine.newerError() *StateError`.

- [ ] **Step 1: Write the failing tests**

`internal/atomicfile/atomicfile_test.go`:

```go
package atomicfile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteReplacesAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.json")
	for _, body := range []string{"one\n", "two\n"} {
		if err := Write(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(path); string(b) != body {
			t.Fatalf("content %q, want %q", b, body)
		}
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("the temp file was left behind")
	}
}

func TestRenameFailsFastOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows retries")
	}
	err := Rename(filepath.Join(t.TempDir(), "absent"), filepath.Join(t.TempDir(), "x"))
	if err == nil {
		t.Fatal("renaming a missing file must fail")
	}
}
```

`internal/machine/registry_test.go`:

```go
package machine

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReadRegistryMissing(t *testing.T) {
	if _, err := ReadRegistry(t.TempDir()); !errors.Is(err, ErrNoRegistry) {
		t.Fatalf("want ErrNoRegistry, got %v", err)
	}
}

func TestUpdateRegistryBumpsGenerationAndKeepsUnknownFields(t *testing.T) {
	state := t.TempDir()
	body := `{"schema":1,"layout":2,"generation":7,"pools":["vm","builds"],"migrated_by":"incoda 0.7.0","future":{"x":1}}`
	if err := os.WriteFile(RegistryPath(state), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := ReadRegistry(state)
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsPool("vm") || r.IsPool("cap-gate") {
		t.Fatalf("IsPool: %+v", r)
	}
	lk, err := AcquireLock(state, LockOptions{Op: "test", Start: time.Now(), Wait: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Release()
	r, err = UpdateRegistry(state, lk, func(r *Registry) error { r.Pools = append(r.Pools, "tests"); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if r.Generation != 8 {
		t.Fatalf("generation %d, want 8", r.Generation)
	}
	b, _ := os.ReadFile(RegistryPath(state))
	if !strings.Contains(string(b), `"future"`) || !strings.Contains(string(b), `"pools": [`+"\n"+`    "builds",`) {
		t.Fatalf("unknown field lost or pools not sorted:\n%s", b)
	}
	if err := writeRegistry(state, nil, r); err == nil {
		t.Fatal("writing machine.json without machine.lock must fail")
	}
}

func TestReadRegistryFailsClosed(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"{", "machine-state: machine.json: unexpected end of JSON input; run incoda doctor"},
		{`{"schema":1,"layout":1,"generation":1,"pools":[]}`, "machine-state: machine.json: schema 1, layout 1 is not a registry this incoda reads; run incoda doctor"},
		{`{"schema":1,"layout":2,"generation":1,"pools":["a/b"]}`, `machine-state: machine.json: pool "a/b" is not a valid key; run incoda doctor`},
		{`{"schema":2,"layout":2,"generation":1,"pools":[]}`, "machine-state: machine.json was written by a newer incoda; upgrade this one ("},
		{`{"schema":1,"layout":3,"generation":1,"pools":[]}`, "machine-state: machine.json was written by a newer incoda; upgrade this one ("},
	} {
		state := t.TempDir()
		if err := os.WriteFile(RegistryPath(state), []byte(tc.body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := ReadRegistry(state)
		var se *StateError
		if !errors.As(err, &se) || !strings.HasPrefix(se.Msg, tc.want) {
			t.Fatalf("%s: got %v, want prefix %q", tc.body, err, tc.want)
		}
	}
	state := t.TempDir()
	if err := os.Mkdir(RegistryPath(state), 0o755); err != nil {
		t.Fatal(err)
	}
	var se *StateError
	if _, err := ReadRegistry(state); !errors.As(err, &se) || !strings.HasPrefix(se.Msg, "machine-state: machine.json: ") || !strings.HasSuffix(se.Msg, "; run incoda doctor") {
		t.Fatalf("an unreadable machine.json fails closed, got %v", err)
	}
}

func TestBootstrapPools(t *testing.T) {
	got := strings.Join(BootstrapPools(), ",")
	if got != "builds,computer-use,tests,vm" {
		t.Fatalf("bootstrap pools %s", got)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/atomicfile/ ./internal/machine/ -run 'TestWrite|TestRename|TestReadRegistry|TestUpdateRegistry|TestBootstrap'`
Expected: `internal/atomicfile` has no non-test Go files; `undefined: ReadRegistry`, `RegistryPath`, `ErrNoRegistry`, `UpdateRegistry`, `writeRegistry`, `BootstrapPools`.

- [ ] **Step 3: Add `atomicfile`**

`internal/atomicfile/atomicfile.go`:

```go
// Package atomicfile writes a file so that a reader sees either the old
// content or the new one, never half of either: the data goes to a temp
// file beside the target, which is then renamed over it. config.json,
// machine.json and migration.json are all written this way (spec 4.4).
package atomicfile

import (
	"os"
	"runtime"
	"time"
)

const (
	retries  = 10
	retryGap = 50 * time.Millisecond
)

// Write replaces path with data. The temp file is path+".tmp"; callers
// serialise writers of one path with a lock (the lane's registry lock, or
// machine.lock), so the fixed name never races.
func Write(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Rename renames, retrying on Windows, where a reader holding the target
// open makes the rename fail for a moment: 10 tries, 50ms apart.
func Rename(from, to string) error {
	var err error
	for i := 0; i < retries; i++ {
		if err = os.Rename(from, to); err == nil || runtime.GOOS != "windows" {
			return err
		}
		if i < retries-1 {
			time.Sleep(retryGap)
		}
	}
	return err
}
```

- [ ] **Step 4: Use it for `config.json`**

In `internal/lane/config.go`, inside `UpdateConfig` replace:

```go
		path := filepath.Join(q.Dir, configName)
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
			return err
		}
		if err := renameRetry(tmp, path); err != nil {
			return err
		}
```

with:

```go
		if err := atomicfile.Write(filepath.Join(q.Dir, configName), append(b, '\n'), 0o644); err != nil {
			return err
		}
```

Delete `renameRetry` and its comment at the end of the file. Change the import block to:

```go
import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/deblasis/incoda/internal/atomicfile"
)
```

- [ ] **Step 5: Add the registry**

`internal/machine/registry.go`:

```go
package machine

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/deblasis/incoda/internal/atomicfile"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/textsafe"
)

const (
	// RegistrySchema is the machine.json format this binary reads and writes.
	RegistrySchema = 1
	// Layout is the state layout this binary runs on: lanes/ plus the
	// queues fence.
	Layout = 2

	registryName = "machine.json"
)

// bootstrapPools are the pools a migration registers (spec 3.5).
var bootstrapPools = []string{"builds", "computer-use", "tests", "vm"}

// BootstrapPools returns the pools a migration registers, sorted.
func BootstrapPools() []string { return append([]string(nil), bootstrapPools...) }

// Registry is machine.json: the sole record of which keys are pools and of
// the layout version (spec 2.1). Pool existence and kind are never derived
// from scanning lane configs.
type Registry struct {
	Schema     int
	Layout     int
	Generation int64
	Pools      []string
	MigratedBy string
	MigratedAt string

	// extra keeps fields this binary does not know, so a rewrite never
	// drops what a newer incoda wrote.
	extra map[string]json.RawMessage
}

type registryJSON struct {
	Schema     int      `json:"schema"`
	Layout     int      `json:"layout"`
	Generation int64    `json:"generation"`
	Pools      []string `json:"pools"`
	MigratedBy string   `json:"migrated_by,omitempty"`
	MigratedAt string   `json:"migrated_at,omitempty"`
}

var registryKnown = map[string]bool{
	"schema": true, "layout": true, "generation": true, "pools": true,
	"migrated_by": true, "migrated_at": true,
}

// UnmarshalJSON reads the known fields and keeps the rest.
func (r *Registry) UnmarshalJSON(b []byte) error {
	var k registryJSON
	if err := json.Unmarshal(b, &k); err != nil {
		return err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return err
	}
	*r = Registry{Schema: k.Schema, Layout: k.Layout, Generation: k.Generation, Pools: k.Pools,
		MigratedBy: k.MigratedBy, MigratedAt: k.MigratedAt}
	for key, v := range all {
		if !registryKnown[key] {
			if r.extra == nil {
				r.extra = map[string]json.RawMessage{}
			}
			r.extra[key] = v
		}
	}
	return nil
}

// MarshalJSON writes the known fields plus every preserved unknown one.
func (r Registry) MarshalJSON() ([]byte, error) {
	pools := r.Pools
	if pools == nil {
		pools = []string{}
	}
	b, err := json.Marshal(registryJSON{Schema: r.Schema, Layout: r.Layout, Generation: r.Generation,
		Pools: pools, MigratedBy: r.MigratedBy, MigratedAt: r.MigratedAt})
	if err != nil || len(r.extra) == 0 {
		return b, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range r.extra {
		if _, ok := m[k]; !ok {
			m[k] = v
		}
	}
	return json.Marshal(m)
}

// IsPool reports whether key is a registered pool.
func (r *Registry) IsPool(key string) bool {
	for _, p := range r.Pools {
		if p == key {
			return true
		}
	}
	return false
}

// ErrNoRegistry means machine.json does not exist. Whether that is "not
// migrated yet" or "registry lost" depends on the rest of the layout
// (Inspect, Ensure).
var ErrNoRegistry = errors.New("machine.json does not exist")

// RegistryPath is <state>/machine.json.
func RegistryPath(stateDir string) string { return filepath.Join(stateDir, registryName) }

// ReadRegistry reads machine.json without a lock. A missing file is
// ErrNoRegistry; an unreadable or malformed one, or one written by a newer
// incoda, is a *StateError that fails closed (spec 2.1, 3.6).
func ReadRegistry(stateDir string) (*Registry, error) {
	b, err := os.ReadFile(RegistryPath(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoRegistry
	}
	if err != nil {
		return nil, stateErrorf("machine.json: %s; run incoda doctor", textsafe.Escape(err.Error()))
	}
	var r Registry
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, stateErrorf("machine.json: %s; run incoda doctor", textsafe.Escape(err.Error()))
	}
	if r.Schema > RegistrySchema || r.Layout > Layout {
		return nil, newerError()
	}
	if r.Schema < 1 || r.Layout != Layout {
		return nil, stateErrorf("machine.json: schema %d, layout %d is not a registry this incoda reads; run incoda doctor", r.Schema, r.Layout)
	}
	for _, p := range r.Pools {
		if lane.ValidateKey(p) != nil {
			return nil, stateErrorf("machine.json: pool %q is not a valid key; run incoda doctor", textsafe.Escape(p))
		}
	}
	return &r, nil
}

// newerError is the refusal for a machine.json whose schema or layout is
// newer than this binary knows. It names this binary, the one to upgrade.
func newerError() *StateError {
	self, err := os.Executable()
	if err != nil {
		self = "this incoda"
	}
	e := stateErrorf("machine.json was written by a newer incoda; upgrade this one (%s)", textsafe.Escape(self))
	e.newer = true
	return e
}

// writeRegistry writes machine.json by temp file plus rename. It takes the
// held machine.lock as proof that the caller is the only writer.
func writeRegistry(stateDir string, lk *Lock, r *Registry) error {
	if lk == nil || lk.f == nil {
		return stateErrorf("machine.json is written only under machine.lock")
	}
	pools := map[string]bool{}
	var sorted []string
	for _, p := range r.Pools {
		if !pools[p] {
			pools[p] = true
			sorted = append(sorted, p)
		}
	}
	sort.Strings(sorted)
	r.Pools = sorted
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return stateErrorf("cannot encode machine.json: %v", err)
	}
	if err := atomicfile.Write(RegistryPath(stateDir), append(b, '\n'), 0o644); err != nil {
		return stateErrorf("cannot write machine.json: %s", textsafe.Escape(err.Error()))
	}
	return nil
}

// UpdateRegistry rewrites machine.json under machine.lock: read it, apply
// fn, increment generation, write it by temp file plus rename. Fields this
// binary does not know are kept (spec 2.1).
func UpdateRegistry(stateDir string, lk *Lock, fn func(*Registry) error) (*Registry, error) {
	r, err := ReadRegistry(stateDir)
	if err != nil {
		return nil, err
	}
	if err := fn(r); err != nil {
		return nil, err
	}
	r.Generation++
	if err := writeRegistry(stateDir, lk, r); err != nil {
		return nil, err
	}
	return r, nil
}
```

`MarshalIndent` on a type with a custom `MarshalJSON` re-indents its output, so the file stays pretty printed like `config.json`.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/atomicfile/ ./internal/lane/ ./internal/machine/ -v -run 'TestWrite|TestRename|TestReadRegistry|TestUpdateRegistry|TestBootstrap|Config'`
Expected: PASS.

- [ ] **Step 7: Run the full gates**

Run: `GOOS=windows go vet ./... && just ci`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/atomicfile internal/lane/config.go internal/machine/registry.go internal/machine/registry_test.go
git commit -m "feat: machine.json registry, written atomically under machine.lock

machine.json records the pools and the layout. It is read without a lock,
written only by a machine.lock holder through temp file plus rename (the
Windows retry now lives in one package shared with config.json), bumps
its generation on every rewrite and keeps unknown fields. Unreadable,
malformed and newer files fail closed with a machine-state message."
```

---

### Task 4: The fence, the atomic exchange and the crash points

`<state>/queues` as a regular file is the fence (spec 2.3): every released incoda calls `MkdirAll(<state>/queues)` first and stops with `not a directory`. This task adds the constant, the one-`lstat` check, the placement with the race rule of M4 (whatever sits at `queues` and is not a regular file goes to `strays/<unix-nanos>` first, decided by `lstat`), the per-OS atomic exchange, and the test-only crash points the migration tasks use.

**Files:**
- Create: `internal/machine/fence.go`, `internal/machine/fence_test.go`
- Create: `internal/machine/exchange_darwin.go`, `internal/machine/exchange_linux.go`, `internal/machine/exchange_other.go`
- Create: `internal/machine/crash_on.go`, `internal/machine/crash_off.go`
- Modify: `justfile` (`vet` recipe)

**Interfaces:**
- Consumes: `lane.QueuesDir`, `machine.stateErrorf`, `textsafe.Escape`.
- Produces: `machine.FenceText` (const); `machine.FencePlaced(stateDir string) bool`; `machine.StraysDir(stateDir string) string`; `machine.fenceNewPath(stateDir string) string`; `machine.writeFenceNew(stateDir string) error`; `machine.placeFence(stateDir string) (moved []string, err error)` (may return `errNotIdle` on Windows); `machine.moveToStrays(stateDir, path string) (string, error)`; `machine.errNoExchange`; `machine.errNotIdle`; test seams `machine.exchangeFn`, `machine.beforePlace`, `machine.renameDir`, `machine.notIdleOnRenameFailure`; `machine.exchange(a, b string) error` per OS; `machine.crashpoint(step string)`; `machine.noExchange() bool`.

- [ ] **Step 1: Write the failing tests**

`internal/machine/fence_test.go`:

```go
package machine

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/lane"
)

const specFence = "This state directory is managed by incoda >= 0.7 (system pools).\n" +
	"Lanes now live in lanes/. An older incoda stops here with \"not a directory\"\n" +
	"(exit 122) on purpose: it does not know about the machine-wide pools.\n" +
	"Upgrade it: brew upgrade incoda, or the install script at\n" +
	"https://github.com/deblasis/incoda#install (SHA256SUMS-verified).\n" +
	"Do not delete this file: that lets old binaries run jobs outside the pools.\n"

func TestFenceTextIsTheSpecConstant(t *testing.T) {
	if FenceText != specFence {
		t.Fatalf("FenceText drifted from spec 2.3:\n%s", FenceText)
	}
}

func TestFencePlaced(t *testing.T) {
	state := t.TempDir()
	if FencePlaced(state) {
		t.Fatal("missing is not placed")
	}
	if err := os.Mkdir(lane.QueuesDir(state), 0o755); err != nil {
		t.Fatal(err)
	}
	if FencePlaced(state) {
		t.Fatal("a directory is not the fence")
	}
	if err := os.Remove(lane.QueuesDir(state)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lane.QueuesDir(state), []byte("anything"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !FencePlaced(state) {
		t.Fatal("any regular file is the fence, whatever its content")
	}
}

func readFence(t *testing.T, state string) {
	t.Helper()
	b, err := os.ReadFile(lane.QueuesDir(state))
	if err != nil || string(b) != FenceText {
		t.Fatalf("fence not placed: %q %v", b, err)
	}
	if _, err := os.Lstat(fenceNewPath(state)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("queues.new left behind")
	}
}

func TestPlaceFenceOnAnEmptyStateDir(t *testing.T) {
	state := t.TempDir()
	moved, err := placeFence(state)
	if err != nil || len(moved) != 0 {
		t.Fatalf("placeFence = %v, %v", moved, err)
	}
	readFence(t, state)
}

func TestPlaceFenceMovesADirectoryToStrays(t *testing.T) {
	state := t.TempDir()
	if err := os.MkdirAll(filepath.Join(lane.QueuesDir(state), "k"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lane.QueuesDir(state), "k", "lane.log"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	moved, err := placeFence(state)
	if err != nil || len(moved) != 1 {
		t.Fatalf("placeFence = %v, %v", moved, err)
	}
	if !strings.HasPrefix(moved[0], StraysDir(state)+string(filepath.Separator)) {
		t.Fatalf("moved to %s, want under strays/", moved[0])
	}
	if b, _ := os.ReadFile(filepath.Join(moved[0], "k", "lane.log")); string(b) != "x\n" {
		t.Fatal("the stray lost its content")
	}
	readFence(t, state)
}

// TestPlaceFenceRaceRule: an old binary's MkdirAll recreates queues/
// between the move and the rename; each time it goes to strays/ and the
// placement retries.
func TestPlaceFenceRaceRule(t *testing.T) {
	state := t.TempDir()
	n := 0
	beforePlace = func() {
		if n < 3 {
			n++
			if err := os.MkdirAll(filepath.Join(lane.QueuesDir(state), "late"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	defer func() { beforePlace = func() {} }()
	moved, err := placeFence(state)
	if err != nil || len(moved) != 3 {
		t.Fatalf("placeFence = %v, %v", moved, err)
	}
	readFence(t, state)
}

func TestPlaceFenceGivesUpAfterAHundredTries(t *testing.T) {
	state := t.TempDir()
	beforePlace = func() { _ = os.MkdirAll(lane.QueuesDir(state), 0o755) }
	defer func() { beforePlace = func() {} }()
	_, err := placeFence(state)
	var se *StateError
	if !errors.As(err, &se) || se.Msg != "machine-state: cannot place the queues fence" {
		t.Fatalf("want the placement refusal, got %v", err)
	}
	// The first try finds nothing to move; every later one moves the
	// directory the previous try's hook recreated.
	entries, _ := os.ReadDir(StraysDir(state))
	if len(entries) != maxPlaceTries-1 {
		t.Fatalf("%d strays, want %d", len(entries), maxPlaceTries-1)
	}
}

// TestPlaceFenceNotIdleWhereADirectoryCannotMove: on Windows a directory
// with an open file inside cannot be renamed; that means "an older run is
// still in there", not an error.
func TestPlaceFenceNotIdleWhereADirectoryCannotMove(t *testing.T) {
	state := t.TempDir()
	if err := os.Mkdir(lane.QueuesDir(state), 0o755); err != nil {
		t.Fatal(err)
	}
	renameDir = func(string, string) error { return errors.New("sharing violation") }
	notIdleOnRenameFailure = true
	defer func() { renameDir = os.Rename; notIdleOnRenameFailure = runtime.GOOS == "windows" }()
	if _, err := placeFence(state); !errors.Is(err, errNotIdle) {
		t.Fatalf("want errNotIdle, got %v", err)
	}
}

func TestExchangeSwapsAFileAndADirectory(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no atomic exchange on " + runtime.GOOS)
	}
	d := t.TempDir()
	dir := filepath.Join(d, "queues")
	file := filepath.Join(d, "queues.new")
	if err := os.MkdirAll(filepath.Join(dir, "k"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(FenceText), 0o644); err != nil {
		t.Fatal(err)
	}
	err := exchange(file, dir)
	if errors.Is(err, errNoExchange) {
		t.Skip("this filesystem has no atomic exchange; M4 takes the rename fallback")
	}
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(dir); err != nil || string(b) != FenceText {
		t.Fatalf("queues is not the fence after the swap: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(file, "k")); err != nil || !fi.IsDir() {
		t.Fatalf("queues.new is not the old directory after the swap: %v", err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/machine/ -run 'TestFence|TestPlaceFence|TestExchange'`
Expected: compile errors: `undefined: FenceText`, `FencePlaced`, `placeFence`, `StraysDir`, `beforePlace`, `renameDir`, `exchange`.

- [ ] **Step 3: Add the crash points**

`internal/machine/crash_on.go`:

```go
//go:build incoda_crashpoints

package machine

import (
	"os"
	"time"
)

// crashExit is the exit status of a process stopped at a crash point.
const crashExit = 97

// crashpoint is compiled only into test binaries built with
// -tags incoda_crashpoints; just dist never passes that tag, so release
// builds get the no-op in crash_off.go. With INCODA_TEST_CRASH_AT=<step> the
// process exits at once after that step, running no deferred code, which
// leaves the state directory exactly as a kill at that instant would (the
// kernel frees machine.lock with the process). With
// INCODA_TEST_PAUSE_AT=<step> it instead creates
// $INCODA_TEST_PAUSE_FILE.reached and waits until $INCODA_TEST_PAUSE_FILE
// exists, so a test can act inside the window after that step.
func crashpoint(step string) {
	if os.Getenv("INCODA_TEST_CRASH_AT") == step {
		os.Exit(crashExit)
	}
	if os.Getenv("INCODA_TEST_PAUSE_AT") == step {
		f := os.Getenv("INCODA_TEST_PAUSE_FILE")
		_ = os.WriteFile(f+".reached", nil, 0o644)
		for {
			if _, err := os.Stat(f); err == nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// noExchange forces the rename fallback of M4 (INCODA_TEST_NO_EXCHANGE=1),
// so the fallback and its race rule are tested where an exchange exists.
func noExchange() bool { return os.Getenv("INCODA_TEST_NO_EXCHANGE") == "1" }
```

`internal/machine/crash_off.go`:

```go
//go:build !incoda_crashpoints

package machine

// crashpoint is a no-op in every build without the incoda_crashpoints tag
// (see crash_on.go).
func crashpoint(string) {}

// noExchange is always false outside test builds.
func noExchange() bool { return false }
```

- [ ] **Step 4: Add the exchange per OS**

`internal/machine/exchange_darwin.go`:

```go
//go:build darwin

package machine

import (
	"errors"

	"golang.org/x/sys/unix"
)

// exchange swaps a and b atomically with renamex_np(RENAME_SWAP)
// (golang.org/x/sys/unix.RenamexNp). APFS supports it for a file and a
// directory; a filesystem that does not answers EINVAL or ENOTSUP, reported
// as errNoExchange so M4 takes the rename fallback.
func exchange(a, b string) error {
	if noExchange() {
		return errNoExchange
	}
	err := unix.RenamexNp(a, b, unix.RENAME_SWAP)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) {
		return errNoExchange
	}
	return err
}
```

`internal/machine/exchange_linux.go`:

```go
//go:build linux

package machine

import (
	"errors"

	"golang.org/x/sys/unix"
)

// exchange swaps a and b atomically with renameat2(RENAME_EXCHANGE)
// (golang.org/x/sys/unix.Renameat2). A filesystem without it answers
// EINVAL or ENOTSUP, and a kernel older than 3.15 ENOSYS; all three are
// errNoExchange, so M4 takes the rename fallback.
func exchange(a, b string) error {
	if noExchange() {
		return errNoExchange
	}
	err := unix.Renameat2(unix.AT_FDCWD, a, unix.AT_FDCWD, b, unix.RENAME_EXCHANGE)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.ENOSYS) {
		return errNoExchange
	}
	return err
}
```

`internal/machine/exchange_other.go`:

```go
//go:build !darwin && !linux

package machine

// exchange is not available here (Windows, the BSDs): M4 always takes the
// rename fallback.
func exchange(string, string) error { return errNoExchange }
```

- [ ] **Step 5: Add the fence**

`internal/machine/fence.go`:

```go
package machine

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/textsafe"
)

// FenceText is the whole content of <state>/queues on layout 2 (spec 2.3).
// It is a constant of this binary, never read from config or the
// environment. Every released incoda before 0.7 calls
// MkdirAll(<state>/queues) first and stops with "not a directory" (exit
// 122) when it finds this file.
const FenceText = `This state directory is managed by incoda >= 0.7 (system pools).
Lanes now live in lanes/. An older incoda stops here with "not a directory"
(exit 122) on purpose: it does not know about the machine-wide pools.
Upgrade it: brew upgrade incoda, or the install script at
https://github.com/deblasis/incoda#install (SHA256SUMS-verified).
Do not delete this file: that lets old binaries run jobs outside the pools.
`

// maxPlaceTries bounds the race-rule loop of spec 3.3 M4.
const maxPlaceTries = 100

var (
	// errNoExchange means the filesystem or OS has no atomic exchange; M4
	// takes the rename fallback.
	errNoExchange = errors.New("no atomic exchange on this filesystem")
	// errNotIdle means a directory could not be renamed because something
	// still has a file open inside it (Windows): an older run is still
	// there, so the caller waits and tries again within its budget.
	errNotIdle = errors.New("a directory to move still has an open file inside")
)

// Seams for tests; production never changes them.
var (
	exchangeFn             = exchange
	beforePlace            = func() {}
	renameDir              = os.Rename
	notIdleOnRenameFailure = runtime.GOOS == "windows"
)

// FencePlaced reports whether the fence is in place: <state>/queues is a
// regular file, whatever its content (one lstat, spec 2.3).
func FencePlaced(stateDir string) bool {
	fi, err := os.Lstat(lane.QueuesDir(stateDir))
	return err == nil && fi.Mode().IsRegular()
}

// StraysDir is <state>/strays: where a queues/ directory goes when it is
// found in the fence's place (an old binary's run, or the old layout during
// the rename fallback). Plan 2b counts the live tickets left there.
func StraysDir(stateDir string) string { return filepath.Join(stateDir, "strays") }

func fenceNewPath(stateDir string) string { return filepath.Join(stateDir, "queues.new") }

// writeFenceNew writes the constant to <state>/queues.new.
func writeFenceNew(stateDir string) error {
	if err := os.WriteFile(fenceNewPath(stateDir), []byte(FenceText), 0o644); err != nil {
		return stateErrorf("cannot write %s: %s", textsafe.Escape(fenceNewPath(stateDir)), textsafe.Escape(err.Error()))
	}
	return nil
}

// placeFence writes the constant to queues.new and renames it to queues
// with the race rule of spec 3.3 M4: whatever sits at queues and is not a
// regular file (an old binary's MkdirAll can recreate queues/ at any moment)
// is moved to strays/<unix-nanos> first. The decision is made by lstat,
// never by the rename's errno (APFS answers EEXIST where Linux answers
// EISDIR). It gives up after maxPlaceTries. It returns the strays it made.
func placeFence(stateDir string) ([]string, error) {
	if err := writeFenceNew(stateDir); err != nil {
		return nil, err
	}
	q := lane.QueuesDir(stateDir)
	var moved []string
	for i := 0; i < maxPlaceTries; i++ {
		if fi, err := os.Lstat(q); err == nil && !fi.Mode().IsRegular() {
			dst, err := moveToStrays(stateDir, q)
			if err != nil {
				if notIdleOnRenameFailure {
					return moved, errNotIdle
				}
				return moved, stateErrorf("cannot move %s to strays/: %s", textsafe.Escape(q), textsafe.Escape(err.Error()))
			}
			moved = append(moved, dst)
		}
		beforePlace()
		if err := os.Rename(fenceNewPath(stateDir), q); err == nil {
			return moved, nil
		}
	}
	return moved, stateErrorf("cannot place the queues fence")
}

// moveToStrays renames path to strays/<unix-nanos>, picking the next free
// name. Strays are only written under machine.lock, so the check before
// the rename does not race.
func moveToStrays(stateDir, path string) (string, error) {
	if err := os.MkdirAll(StraysDir(stateDir), 0o755); err != nil {
		return "", err
	}
	for {
		dst := filepath.Join(StraysDir(stateDir), strconv.FormatInt(time.Now().UnixNano(), 10))
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if err := renameDir(path, dst); err != nil {
			return "", err
		}
		return dst, nil
	}
}
```

`exchangeFn` is unused until Task 6; Go does not reject unused package-level variables.

- [ ] **Step 6: Vet the crash build too**

In `justfile`, replace the `vet` recipe with:

```
# vet the release build and the crash-injection test build (internal/machine/crash_on.go)
vet:
	go vet ./...
	go vet -tags incoda_crashpoints ./...
```

- [ ] **Step 7: Run the tests**

Run: `go test ./internal/machine/ -v -run 'TestFence|TestPlaceFence|TestExchange' && go vet -tags incoda_crashpoints ./internal/machine/`
Expected: PASS (on macOS `TestExchangeSwapsAFileAndADirectory` passes; on a filesystem without the exchange it skips).

- [ ] **Step 8: Run the full gates**

Run: `GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./internal/machine/ && just ci`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/machine/fence.go internal/machine/fence_test.go internal/machine/exchange_darwin.go internal/machine/exchange_linux.go internal/machine/exchange_other.go internal/machine/crash_on.go internal/machine/crash_off.go justfile
git commit -m "feat: the queues fence, atomic exchange and test crash points

FenceText is the constant that stops every released incoda with not a
directory. placeFence applies the M4 race rule, moving anything at queues
that is not a regular file to strays/ first. The exchange uses
renamex_np(RENAME_SWAP) on darwin and renameat2(RENAME_EXCHANGE) on linux.
Crash and pause points exist only in builds with the incoda_crashpoints
tag, which just vet now checks too."
```

---

### Task 5: The idle checks M2 and M5

Before the fence (M2) the migration probes every ticket under `queues/<K>/`; after it (M5) every ticket under `lanes/<K>/` and `strays/<n>/<K>/`. Each probe is the create-free `lane.ProbeLane` of Task 1, under that lane's `registry.lock` (old binaries lock the same inode, so the probe serialises with their Enroll). While any ticket is live the migrator keeps `machine.lock`, writes the blockers into its note, prints the `upgrade-wait:` block once, and re-probes every `--poll`; an ancestor holder refuses at once with `upgrade-blocked:`; budget expiry is `upgrade-timeout:`. The `incoda kill ...` lines are printed as text; what `kill` does to an old holder is plan 2b. Orphan records (spec 3.2) are also plan 2b; they will add to `findBlockers`.

**Files:**
- Create: `internal/machine/options.go`
- Create: `internal/machine/idle.go`, `internal/machine/idle_test.go`

**Interfaces:**
- Consumes: `lane.ProbeLane`, `lane.ListIn`, `lane.QueuesDir`, `lane.LanesDir`, `(lane.Probed).PID`, `machine.StraysDir`, `machine.Lock`, `(*Lock).SetBlockers`, `machine.upgradeBlocked`, `machine.joinLines`, `machine.Timeout`.
- Produces: `type machine.Options struct{ Start time.Time; Wait, Poll time.Duration; Chain procinfo.Chain; By string; Stderr io.Writer; Path, Exe string }` with unexported `poll()`, `stderr()`, `lockOptions(op string) LockOptions`; `machine.budgetDeadline(start time.Time, wait time.Duration) time.Time`; `type machine.phase` with `phaseM2`, `phaseM5`; `machine.findBlockers(stateDir string, ph phase) ([]Blocker, error)`; `machine.waitIdle(stateDir string, lk *Lock, o Options, ph phase) error`; `machine.upgradeWaitLines`, `machine.upgradeTimeoutLines`, `machine.killLine`.

- [ ] **Step 1: Write the failing tests**

`internal/machine/idle_test.go`:

```go
package machine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/lockfile"
	"github.com/deblasis/incoda/internal/procinfo"
)

// holdTicket makes root/key look like a lane of an older incoda with one
// live ticket, the way its Enroll leaves it: registry.lock first, then a
// ticket whose lock this test process holds, with pid and cmd in the
// payload and the name. The returned func releases the lock and leaves a
// dead ticket behind, as a killed holder would.
func holdTicket(t *testing.T, root, key string, pid int, cmd ...string) func() {
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

func takeLock(t *testing.T, state string) *Lock {
	t.Helper()
	lk, err := AcquireLock(state, LockOptions{Op: "migrate", Start: time.Now(), Wait: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lk.Release)
	return lk
}

const specWait = `incoda: upgrade-wait: state upgrade waits for 2 run(s) by an older incoda:
incoda:   builds pid 4711: zig build -Denable-llvm
incoda:   kungfoo-ui pid 5120: just ui
incoda: ask the user before stopping another session's job; they can run:
incoda:   incoda kill --queue builds --pid 4711 --reason 'incoda upgrade'
incoda:   incoda kill --queue kungfoo-ui --pid 5120 --reason 'incoda upgrade'
incoda: do not force-release them: the job keeps running and the upgrade would overlap it.
`

func TestFindBlockersM2(t *testing.T) {
	state := t.TempDir()
	q := lane.QueuesDir(state)
	holdTicket(t, q, "kungfoo-ui", 5120, "just", "ui")
	holdTicket(t, q, "builds", 4711, "zig", "build", "-Denable-llvm")
	holdTicket(t, q, "idle", 6000, "x")() // released at once: a dead ticket
	bs, err := findBlockers(state, phaseM2)
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 2 || bs[0] != (Blocker{Key: "builds", PID: 4711, Command: "zig build -Denable-llvm"}) || bs[1].Key != "kungfoo-ui" {
		t.Fatalf("blockers %+v", bs)
	}
	entries, _ := os.ReadDir(filepath.Join(q, "idle"))
	if len(entries) != 2 {
		t.Fatal("the probe must not reap the dead ticket")
	}
}

func TestWaitIdlePrintsTheSpecTextAndNotesBlockers(t *testing.T) {
	state := t.TempDir()
	q := lane.QueuesDir(state)
	rb := holdTicket(t, q, "builds", 4711, "zig", "build", "-Denable-llvm")
	ru := holdTicket(t, q, "kungfoo-ui", 5120, "just", "ui")
	lk := takeLock(t, state)
	noted := make(chan Note, 1)
	go func() {
		for {
			if n, ok := ReadNote(state); ok && len(n.Blockers) == 2 {
				noted <- n
				rb()
				ru()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	var errBuf bytes.Buffer
	if err := waitIdle(state, lk, Options{Start: time.Now(), Wait: time.Minute, Poll: 50 * time.Millisecond, Stderr: &errBuf}, phaseM2); err != nil {
		t.Fatal(err)
	}
	n := <-noted
	if n.Op != "migrate" || n.Blockers[0] != (Blocker{Key: "builds", PID: 4711}) {
		t.Fatalf("note while waiting: %+v", n)
	}
	if errBuf.String() != specWait {
		t.Fatalf("upgrade-wait text:\n%s\nwant:\n%s", errBuf.String(), specWait)
	}
	if n, _ := ReadNote(state); len(n.Blockers) != 0 {
		t.Fatalf("blockers must leave the note once idle: %+v", n)
	}
}

func TestWaitIdleTimesOut(t *testing.T) {
	state := t.TempDir()
	holdTicket(t, lane.QueuesDir(state), "builds", 4711, "zig", "build")
	lk := takeLock(t, state)
	err := waitIdle(state, lk, Options{Start: time.Now(), Wait: 200 * time.Millisecond, Poll: 50 * time.Millisecond}, phaseM2)
	var to *Timeout
	if !errors.As(err, &to) {
		t.Fatalf("want a Timeout, got %v", err)
	}
	lines := strings.Split(to.Msg, "\nincoda: ")
	if lines[0] != "upgrade-timeout: state upgrade still waits for 1 run(s) by an older incoda after 200ms:" ||
		lines[1] != "  builds pid 4711: zig build" ||
		lines[len(lines)-1] != "upgrade the older incoda on PATH; see incoda doctor" {
		t.Fatalf("upgrade-timeout text:\n%s", to.Msg)
	}
}

func TestWaitIdleRefusesAnAncestorHolder(t *testing.T) {
	state := t.TempDir()
	holdTicket(t, lane.QueuesDir(state), "builds", 4711, "zig", "build")
	lk := takeLock(t, state)
	var errBuf bytes.Buffer
	err := waitIdle(state, lk, Options{Start: time.Now(), Wait: time.Minute, Stderr: &errBuf,
		Chain: procinfo.Chain{PIDs: []int{4711, 1}}}, phaseM2)
	var rf *Refusal
	if !errors.As(err, &rf) || rf.Msg != `upgrade-blocked: an older incoda (pid 4711, an ancestor of this process) holds "builds"; rerun the outer command after it exits` {
		t.Fatalf("want upgrade-blocked, got %v", err)
	}
	if errBuf.Len() != 0 {
		t.Fatalf("an upgrade-blocked run prints no wait block:\n%s", errBuf.String())
	}
	// No ancestry walk on Windows: the same holder is waited for instead.
	err = waitIdle(state, lk, Options{Start: time.Now(), Wait: 0, Chain: procinfo.Chain{Skip: true, PIDs: []int{4711}}}, phaseM2)
	var to *Timeout
	if !errors.As(err, &to) {
		t.Fatalf("a Skip chain waits, got %v", err)
	}
}

func TestWaitIdleM5ProbesLanesAndStraysWithForce(t *testing.T) {
	state := t.TempDir()
	holdTicket(t, lane.LanesDir(state), "slip", 6001, "just", "gate")
	holdTicket(t, filepath.Join(StraysDir(state), "1727853243000000000"), "late", 6002, "make")
	lk := takeLock(t, state)
	var errBuf bytes.Buffer
	err := waitIdle(state, lk, Options{Start: time.Now(), Wait: 0, Stderr: &errBuf}, phaseM5)
	var to *Timeout
	if !errors.As(err, &to) {
		t.Fatalf("want a Timeout, got %v", err)
	}
	out := errBuf.String()
	for _, want := range []string{
		"incoda: upgrade-wait: state upgrade waits for 2 run(s) by an older incoda:\n",
		"incoda:   late pid 6002: make\n",
		"incoda:   slip pid 6001: just gate\n",
		"incoda:   incoda kill --queue late --pid 6002 --reason 'incoda upgrade' --force\n",
		"incoda:   incoda kill --queue slip --pid 6001 --reason 'incoda upgrade' --force\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/machine/ -run 'TestFindBlockers|TestWaitIdle'`
Expected: compile errors: `undefined: findBlockers`, `phaseM2`, `phaseM5`, `waitIdle`, `Options`.

- [ ] **Step 3: Add the options**

`internal/machine/options.go`:

```go
package machine

import (
	"io"
	"time"

	"github.com/deblasis/incoda/internal/procinfo"
)

// Options is what a mutating command gives the migration.
type Options struct {
	// Start and Wait are the command's --wait budget: one budget, measured
	// from the start of the command, covers machine.lock waits, migration
	// waits and every lane (spec 2.4). A negative Wait waits forever.
	Start time.Time
	Wait  time.Duration
	// Poll is how often waits re-check; zero means 500ms.
	Poll time.Duration
	// Chain is this process's parent chain, computed once.
	Chain procinfo.Chain
	// By is recorded as migrated_by, for example "incoda 0.7.0".
	By string
	// Stderr receives the informational lines (migrated:, upgrade-wait:,
	// upgrade-warning:, waiting for machine.lock:).
	Stderr io.Writer
	// Path is the PATH incoda was started with, for M0; Exe is this
	// binary, "" for os.Executable.
	Path string
	Exe  string
}

func (o Options) poll() time.Duration {
	if o.Poll <= 0 {
		return 500 * time.Millisecond
	}
	return o.Poll
}

func (o Options) stderr() io.Writer {
	if o.Stderr == nil {
		return io.Discard
	}
	return o.Stderr
}

func (o Options) lockOptions(op string) LockOptions {
	return LockOptions{Op: op, Start: o.Start, Wait: o.Wait, Poll: o.Poll, Chain: o.Chain, Stderr: o.Stderr}
}

// budgetDeadline is when the --wait budget ends; the zero time means never.
// Waits for older runs get no floor: --wait 0 means "do not wait for them".
func budgetDeadline(start time.Time, wait time.Duration) time.Time {
	if wait < 0 {
		return time.Time{}
	}
	if start.IsZero() {
		start = time.Now()
	}
	return start.Add(wait)
}
```

- [ ] **Step 4: Add the idle checks**

`internal/machine/idle.go`:

```go
package machine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/textsafe"
)

// phase is which idle check runs: M2 before the fence, M5 after it.
type phase int

const (
	// phaseM2 probes queues/<K>/: older runs on the old layout.
	phaseM2 phase = iota
	// phaseM5 probes lanes/<K>/ and strays/<n>/<K>/: an older run that
	// slipped in between M2 and the fence.
	phaseM5
)

// laneDirsFor lists the lane directories whose tickets the phase probes.
func laneDirsFor(stateDir string, ph phase) ([]string, error) {
	var roots []string
	switch ph {
	case phaseM2:
		roots = []string{lane.QueuesDir(stateDir)}
	case phaseM5:
		roots = []string{lane.LanesDir(stateDir)}
		batches, err := os.ReadDir(StraysDir(stateDir))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, b := range batches {
			if b.IsDir() {
				roots = append(roots, filepath.Join(StraysDir(stateDir), b.Name()))
			}
		}
	}
	var dirs []string
	for _, root := range roots {
		keys, err := lane.ListIn(root)
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			dirs = append(dirs, filepath.Join(root, k))
		}
	}
	return dirs, nil
}

// findBlockers probes every ticket the phase covers and returns the live
// ones, sorted by key then pid. Plan 2b adds orphan records here (a record
// is live until its recorded tree is empty).
func findBlockers(stateDir string, ph phase) ([]Blocker, error) {
	dirs, err := laneDirsFor(stateDir, ph)
	if err != nil {
		return nil, err
	}
	var bs []Blocker
	for _, d := range dirs {
		live, err := lane.ProbeLane(d)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", d, err)
		}
		for _, p := range live {
			cmd := "(unreadable ticket)"
			if p.PayloadErr == nil && p.ProbeErr == nil {
				cmd = p.Ticket.CommandString()
			}
			bs = append(bs, Blocker{Key: filepath.Base(d), PID: p.PID(), Command: textsafe.Escape(cmd)})
		}
	}
	sort.Slice(bs, func(i, j int) bool {
		if bs[i].Key != bs[j].Key {
			return bs[i].Key < bs[j].Key
		}
		return bs[i].PID < bs[j].PID
	})
	return bs, nil
}

// waitIdle is the wait of spec 3.3 M2 and M5. The caller holds machine.lock
// and keeps it while waiting: while machine.json is absent no new-binary
// command can do useful work anyway, and waiters read the blockers from the
// note. It returns nil once no ticket the phase covers is live.
func waitIdle(stateDir string, lk *Lock, o Options, ph phase) error {
	deadline := budgetDeadline(o.Start, o.Wait)
	printed, noted := false, false
	for {
		bs, err := findBlockers(stateDir, ph)
		if err != nil {
			return stateErrorf("cannot probe the tickets of older runs: %s", textsafe.Escape(err.Error()))
		}
		if len(bs) == 0 {
			if noted {
				_ = lk.SetBlockers(nil)
			}
			return nil
		}
		if !o.Chain.Skip {
			for _, b := range bs {
				if o.Chain.Contains(b.PID) {
					return upgradeBlocked(b.PID, b.Key)
				}
			}
		}
		if err := lk.SetBlockers(bs); err != nil {
			return stateErrorf("cannot write the machine.lock note: %s", textsafe.Escape(err.Error()))
		}
		noted = true
		if !printed {
			fmt.Fprintf(o.stderr(), "incoda: %s\n", joinLines(upgradeWaitLines(bs, ph)))
			printed = true
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return &Timeout{Msg: joinLines(upgradeTimeoutLines(bs, ph, o.Wait))}
		}
		time.Sleep(o.poll())
	}
}

func upgradeWaitLines(bs []Blocker, ph phase) []string {
	lines := []string{fmt.Sprintf("upgrade-wait: state upgrade waits for %d run(s) by an older incoda:", len(bs))}
	return append(lines, blockerLines(bs, ph)...)
}

func upgradeTimeoutLines(bs []Blocker, ph phase, waited time.Duration) []string {
	lines := []string{fmt.Sprintf("upgrade-timeout: state upgrade still waits for %d run(s) by an older incoda after %s:", len(bs), waited)}
	lines = append(lines, blockerLines(bs, ph)...)
	return append(lines, "upgrade the older incoda on PATH; see incoda doctor")
}

// blockerLines lists the older runs, then the ask-the-user text and one
// stop line per run. Stopping another session's job is the user's call, so
// the lines are printed for them, never run.
func blockerLines(bs []Blocker, ph phase) []string {
	var lines []string
	for _, b := range bs {
		lines = append(lines, fmt.Sprintf("  %s pid %d: %s", b.Key, b.PID, b.Command))
	}
	lines = append(lines, "ask the user before stopping another session's job; they can run:")
	for _, b := range bs {
		lines = append(lines, "  "+killLine(b, ph))
	}
	return append(lines, "do not force-release them: the job keeps running and the upgrade would overlap it.")
}

// killLine is the stop line for one older run. After the fence (M5) the old
// holder can no longer see kill requests, so the line carries --force
// (spec 3.2; what kill does with it is plan 2b).
func killLine(b Blocker, ph phase) string {
	s := fmt.Sprintf("incoda kill --queue %s --pid %d --reason 'incoda upgrade'", b.Key, b.PID)
	if ph == phaseM5 {
		s += " --force"
	}
	return s
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/machine/ -v -run 'TestFindBlockers|TestWaitIdle'`
Expected: PASS.

- [ ] **Step 6: Run the full gates**

Run: `GOOS=windows go vet ./... && just ci`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/machine/options.go internal/machine/idle.go internal/machine/idle_test.go
git commit -m "feat: migration idle checks for live tickets of older runs

M2 probes queues/, M5 probes lanes/ and strays/, both with the create-free
probe under each lane's registry lock. While a ticket is live the
migrator keeps machine.lock, notes the blockers, prints the upgrade-wait
block once with the stop lines for the user, refuses an ancestor holder
with upgrade-blocked and gives up with upgrade-timeout."
```

---

### Task 6: The migration transaction and every recovery row

This task assembles M1 to M8 (spec 3.3) into `machine.Ensure`, the one entry point for commands that take a ticket or write config, and `machine.Inspect`, the read-only view. Each step is idempotent and keyed on what exists; on entry the layout is classified into the rows of the recovery table, and each row resumes where the table says. M0 (the PATH check) is Task 7; the CLI calls these in Task 8.

The steps, as code:

- **Fast path** (`Ensure`): `machine.json` valid, the fence a regular file and no `migration.json`: return the registry; no lock taken.
- **Repair**: `machine.json` valid but the fence missing (re-fence, spec 2.3) or a `migration.json` left after the commit (row 8): take `machine.lock`, re-check under it, delete `migration.json`, re-place the fence with the race rule, log `event=refence` to `<state>/machine.log`.
- **M1**: under `machine.lock`, a valid `machine.json` means another process won: continue with its registry. A leftover `queues.new` file next to a `queues/` directory is deleted.
- **M2**: when `queues/` is a directory, `waitIdle(phaseM2)` (Task 5).
- **M3**: `migration.json` recomputed: target layout 2, the bootstrap pools, the lanes whose config is unreadable.
- **M4**: write `queues.new`; with `queues/` a directory, swap it in (`exchangeFn`) then rename `queues.new` (now the directory) to `lanes`; on `errNoExchange` rename `queues` to `lanes` then place the fence with the race rule; on Windows a refused directory rename means "not idle yet": back to M2 within the same budget. With no `queues` at all (empty or fresh state directory) create `lanes/`, then place the fence with the race rule.
- **M5**: `waitIdle(phaseM5)`.
- **M6**: merge `strays/<n>/<K>`: renamed into `lanes/` when `lanes/<K>` does not exist, else its `lane.log` is appended to `lanes/<K>/lane.log` and the directory deleted; `strays/` ends removed.
- **M7**: register the bootstrap pools: a pool key with no lane gets a new `config.json` (schema 2, `slots: 1`); an existing pool lane with a readable config is upgraded to schema 2 keeping its fields, with `slots: 1` only when it set none; a malformed, unreadable or newer config is never rewritten. Project lanes are not touched (their configs upgrade on their next write, spec 2.2).
- **M8**: log `event=migrate` in every lane's `lane.log`, write `machine.json` (generation 1), delete `migration.json`, print the `migrated:` lines with computed counts. The log lines go first so that a crash right after the commit (row 8) still leaves them; a crash between the log and the commit logs them twice, which is harmless history.

Recovery rows and their action (spec 3.3 table):

| row | state found | action |
|---|---|---|
| `RowNotStarted` | `queues/` dir (or nothing), no `migration.json`, no `lanes/` | M1 onwards |
| `RowPlanned` | `queues/` dir, `migration.json`, no `lanes/` | M1 (deletes a leftover `queues.new`), M2, M3, M4 |
| `RowSwapped` | `queues` file, `queues.new` dir | rename `queues.new` to `lanes`, then M5 |
| `RowFenceMissing` | `lanes/`, `migration.json`, no `queues` or a `queues/` dir | place the fence (race rule), then M5 |
| `RowEmptyPlanned` | `migration.json` only | M1 onwards (empty-dir path) |
| `RowFenceNoLanes` | `queues` file, no `lanes/`, no `machine.json` | create `lanes/`, then M5 |
| `RowResume` | `queues` file, `lanes/`, `migration.json` | M5 |
| `RowCommitted` | valid `machine.json` and `migration.json` | delete `migration.json` |
| `RowRegistryLost` | `lanes/`, no `machine.json`, no `migration.json` | place the fence if missing, then exit 122 |

**Files:**
- Create: `internal/machine/layout.go`
- Create: `internal/machine/plan.go`
- Create: `internal/machine/bootstrap.go`
- Create: `internal/machine/migrate.go`
- Create: `internal/machine/migrate_test.go`

**Interfaces:**
- Consumes: everything from Tasks 2 to 5; `lane.Open`, `lane.ListIn`, `lane.ListQueues`, `lane.Exists`, `lane.LaneDir`, `lane.LanesDir`, `lane.QueuesDir`, `lane.ReadConfig`, `lane.AppendLog`, `lane.LogPath`, `(*lane.Queue).LoadConfig`, `UpdateConfig`; `procinfo.Alive`; `atomicfile.Write`.
- Produces: `machine.Ensure(stateDir string, o Options) (*Registry, error)`; `machine.Inspect(stateDir string) (View, error)`; `type machine.View struct{ Root string; Migrated bool; Registry *Registry; Banner string; FenceMissing bool }`; `type machine.Row int` with `RowNotStarted`, `RowPlanned`, `RowSwapped`, `RowFenceMissing`, `RowEmptyPlanned`, `RowFenceNoLanes`, `RowResume`, `RowCommitted`, `RowRegistryLost` and `String()`; `machine.scanLayout(stateDir string) layoutState` with `row() Row`; `machine.banner(stateDir string) string`; `machine.migrationNote(stateDir string) (Note, bool)`; `machine.registryLostError() *StateError`; `machine.MachineLogPath(stateDir string) string`; `type machine.Suggestion struct{ Pools []string; QuietMachine bool; Pattern string }`; `machine.Suggest(key string) (Suggestion, bool)` (plan 3 prints suggestions; here only M8 counts them); `machine.planPath(stateDir string) string`.

- [ ] **Step 1: Write the failing tests**

`internal/machine/migrate_test.go`:

```go
package machine

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	if err := os.MkdirAll(filepath.Join(lane.QueuesDir(state), "stale"), 0o755); err != nil {
		t.Fatal(err)
	}
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
	if _, err := os.Stat(filepath.Join(StraysDir(state), batches[0].Name(), "stale")); err != nil {
		t.Fatal(err)
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
	notIdleOnRenameFailure = true
	defer func() {
		exchangeFn, renameDir, notIdleOnRenameFailure = exchange, os.Rename, runtime.GOOS == "windows"
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
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/machine/ -run 'TestEnsure|TestRecovery|TestM4|TestFallbackRace|TestInspect|TestSuggest'`
Expected: compile errors: `undefined: Ensure`, `Inspect`, `writePlan`, `planPath`, `scanLayout`, `RowNotStarted`, `MachineLogPath`, `Suggest`.

- [ ] **Step 3: Add the layout scan, recovery rows and `Inspect`**

`internal/machine/layout.go`:

```go
package machine

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/procinfo"
)

type entryKind int

const (
	absent entryKind = iota
	aDir
	aFile
	other
)

func kindOf(path string) entryKind {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return absent
	case err != nil:
		return other
	case fi.IsDir():
		return aDir
	case fi.Mode().IsRegular():
		return aFile
	default:
		return other
	}
}

// layoutState is what lstat finds at the five names the migration uses.
type layoutState struct {
	Queues, QueuesNew     entryKind
	Lanes, Plan, Registry bool
}

func scanLayout(stateDir string) layoutState {
	return layoutState{
		Queues:    kindOf(lane.QueuesDir(stateDir)),
		QueuesNew: kindOf(fenceNewPath(stateDir)),
		Lanes:     kindOf(lane.LanesDir(stateDir)) == aDir,
		Plan:      kindOf(planPath(stateDir)) == aFile,
		Registry:  kindOf(RegistryPath(stateDir)) != absent,
	}
}

// Row is a row of the crash-recovery table of spec 3.3.
type Row int

const (
	RowNotStarted Row = iota + 1
	RowPlanned
	RowSwapped
	RowFenceMissing
	RowEmptyPlanned
	RowFenceNoLanes
	RowResume
	RowCommitted
	RowRegistryLost
)

var rowMeaning = map[Row]string{
	RowNotStarted:   "not started",
	RowPlanned:      "stopped in M3 or M4 before the fence",
	RowSwapped:      "stopped between the swap and the rename",
	RowFenceMissing: "the fallback or empty-dir path stopped before the fence was placed",
	RowEmptyPlanned: "the empty-dir path stopped after M3",
	RowFenceNoLanes: "a fence without lanes/, which no step order produces",
	RowResume:       "stopped in M5 to M7",
	RowCommitted:    "stopped after the commit",
	RowRegistryLost: "registry lost",
}

// String is the "meaning" column of the recovery table; doctor prints it.
func (r Row) String() string {
	if s, ok := rowMeaning[r]; ok {
		return s
	}
	return fmt.Sprintf("row %d", int(r))
}

// row classifies a state directory whose machine.json is absent.
// RowCommitted needs a valid machine.json and is handled by the caller.
func (s layoutState) row() Row {
	switch {
	case s.Queues == aFile && s.QueuesNew == aDir:
		return RowSwapped
	case s.Queues == aFile && !s.Lanes:
		return RowFenceNoLanes
	case s.Queues == aFile && s.Lanes && s.Plan:
		return RowResume
	case s.Lanes && !s.Plan:
		return RowRegistryLost
	case s.Lanes && s.Plan:
		return RowFenceMissing
	case s.Plan && s.Queues == absent:
		return RowEmptyPlanned
	case s.Plan:
		return RowPlanned
	default:
		return RowNotStarted
	}
}

// View is what a read-only command needs to know about the layout.
type View struct {
	// Root holds one directory per lane: lanes/ once migrated; the old
	// queues/ before (lanes/ when a migration has already moved it).
	Root     string
	Migrated bool
	Registry *Registry
	// Banner is set on a layout not migrated yet (spec 3.2).
	Banner string
	// FenceMissing is set on a migrated layout whose <state>/queues is not
	// a regular file; only the next mutating command re-places it.
	FenceMissing bool
}

// Inspect reads the layout for status, watch, queues, kill and
// force-release. It never writes, never re-fences and never takes
// machine.lock. A migrated layout with a broken or lost registry fails
// closed with a *StateError (spec 2.1, 3.6).
func Inspect(stateDir string) (View, error) {
	for attempt := 0; ; attempt++ {
		reg, err := ReadRegistry(stateDir)
		if err == nil {
			return View{Root: lane.LanesDir(stateDir), Migrated: true, Registry: reg, FenceMissing: !FencePlaced(stateDir)}, nil
		}
		if !errors.Is(err, ErrNoRegistry) {
			return View{}, err
		}
		st := scanLayout(stateDir)
		if st.row() == RowRegistryLost {
			// M8 writes machine.json and then deletes migration.json; a
			// read between our two looks can see neither. Look once more
			// before failing closed.
			if attempt == 0 {
				continue
			}
			return View{}, registryLostError()
		}
		v := View{Root: lane.QueuesDir(stateDir), Banner: banner(stateDir)}
		if st.Queues != aDir && st.Lanes {
			v.Root = lane.LanesDir(stateDir)
		}
		return v, nil
	}
}

func registryLostError() *StateError {
	return stateErrorf("machine.json: missing while lanes/ exists; run incoda doctor")
}

// migrationNote returns the note of a live migration holder, read from
// machine.lock without taking it. A note left by a holder that died does
// not count.
func migrationNote(stateDir string) (Note, bool) {
	n, ok := ReadNote(stateDir)
	if !ok || n.Op != "migrate" || !procinfo.Alive(n.PID) {
		return Note{}, false
	}
	return n, true
}

// banner is the read-only banner of spec 3.2.
func banner(stateDir string) string {
	if n, ok := migrationNote(stateDir); ok {
		s := fmt.Sprintf("state upgrade in progress by pid %d since %s", n.PID, n.Since.UTC().Format(time.RFC3339))
		if len(n.Blockers) > 0 {
			s += "; waiting for older runs: " + blockerList(n.Blockers)
		}
		return s
	}
	return "state not upgraded yet: the next mutating incoda command upgrades it"
}
```

- [ ] **Step 4: Add the plan file (M3)**

`internal/machine/plan.go`:

```go
package machine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/deblasis/incoda/internal/atomicfile"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/textsafe"
)

const planName = "migration.json"

func planPath(stateDir string) string { return filepath.Join(stateDir, planName) }

// migrationPlan is migration.json: proof that a migration is under way, and
// what it found. It never carries links.
type migrationPlan struct {
	TargetLayout int      `json:"target_layout"`
	Pools        []string `json:"pools"`
	Unreadable   []string `json:"unreadable_configs"`
	PID          int      `json:"pid"`
	PlannedAt    string   `json:"planned_at"`
}

// writePlan is M3. It is recomputed every time it is reached, from every
// lane's config as it is now; a plan from before a crash is never reused.
func writePlan(stateDir string) error {
	p := migrationPlan{TargetLayout: Layout, Pools: BootstrapPools(), Unreadable: []string{},
		PID: os.Getpid(), PlannedAt: time.Now().UTC().Format(time.RFC3339)}
	root := lane.QueuesDir(stateDir)
	if kindOf(root) == aDir {
		keys, err := lane.ListIn(root)
		if err != nil {
			return stateErrorf("cannot list queues/: %s", textsafe.Escape(err.Error()))
		}
		for _, k := range keys {
			if _, err := lane.ReadConfig(filepath.Join(root, k)); err != nil {
				p.Unreadable = append(p.Unreadable, k)
			}
		}
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return stateErrorf("cannot encode migration.json: %v", err)
	}
	if err := atomicfile.Write(planPath(stateDir), append(b, '\n'), 0o644); err != nil {
		return stateErrorf("cannot write migration.json: %s", textsafe.Escape(err.Error()))
	}
	return nil
}
```

- [ ] **Step 5: Add M6, M7, the M8 text and the suggestion table**

`internal/machine/bootstrap.go`:

```go
package machine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/textsafe"
)

func esc(err error) string { return textsafe.Escape(err.Error()) }

// mergeStrays is M6. Every ticket under strays/ is dead by now (M5 waited
// for the live ones). A strays/<n>/<K> whose lanes/<K> does not exist is
// renamed into lanes/; the rest hold only dead tickets and a log fragment,
// which is appended to lanes/<K>/lane.log before the directory is deleted.
// strays/ ends removed.
func mergeStrays(stateDir string) error {
	batches, err := os.ReadDir(StraysDir(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return stateErrorf("cannot read strays/: %s", esc(err))
	}
	for _, b := range batches {
		batch := filepath.Join(StraysDir(stateDir), b.Name())
		if !b.IsDir() {
			if err := os.Remove(batch); err != nil {
				return stateErrorf("cannot remove %s: %s", textsafe.Escape(batch), esc(err))
			}
			continue
		}
		entries, err := os.ReadDir(batch)
		if err != nil {
			return stateErrorf("cannot read %s: %s", textsafe.Escape(batch), esc(err))
		}
		for _, e := range entries {
			src := filepath.Join(batch, e.Name())
			isLane := e.IsDir() && lane.ValidateKey(e.Name()) == nil
			if isLane && !lane.Exists(stateDir, e.Name()) {
				if err := renameDir(src, lane.LaneDir(stateDir, e.Name())); err != nil {
					return stateErrorf("cannot move %s into lanes/: %s", textsafe.Escape(src), esc(err))
				}
				continue
			}
			if isLane {
				appendFragment(lane.LogPath(src), lane.LogPath(lane.LaneDir(stateDir, e.Name())))
			}
			if err := os.RemoveAll(src); err != nil {
				return stateErrorf("cannot remove %s: %s", textsafe.Escape(src), esc(err))
			}
		}
		if err := os.Remove(batch); err != nil {
			return stateErrorf("cannot remove %s: %s", textsafe.Escape(batch), esc(err))
		}
	}
	if err := os.Remove(StraysDir(stateDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return stateErrorf("cannot remove strays/: %s", esc(err))
	}
	return nil
}

// appendFragment appends the log of a stray to its lane's log. Log failures
// are never fatal: the log is history, not state.
func appendFragment(src, dst string) {
	b, err := os.ReadFile(src)
	if err != nil || len(b) == 0 {
		return
	}
	f, err := os.OpenFile(dst, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if b[len(b)-1] != '\n' {
		b = append(b, '\n')
	}
	_, _ = f.Write(b)
}

// applyBootstrap is M7: every bootstrap pool gets a lane with a schema 2
// config, slots 1 unless an existing lane of that name sets slots (spec
// 3.5). An existing pool lane keeps description, slots, closed and
// require_reason as read now (UpdateConfig loads under the registry lock).
// A malformed, unreadable or newer config is never rewritten: the key is
// registered as a pool with the file left in place, so runs through it exit
// 122 naming the file until a human fixes it. The migration never fails on
// a lane config.
func applyBootstrap(stateDir string) error {
	for _, key := range BootstrapPools() {
		q, err := lane.Open(stateDir, key)
		if err != nil {
			return stateErrorf("cannot create pool %q: %s", key, esc(err))
		}
		if _, err := q.LoadConfig(); err != nil {
			q.Close()
			continue
		}
		_, err = q.UpdateConfig(func(c *lane.Config) error {
			if c.Slots < 1 {
				c.Slots = 1
			}
			return nil
		})
		q.Close()
		if err != nil {
			return stateErrorf("cannot write the config of pool %q: %s", key, esc(err))
		}
	}
	return nil
}

// summary is what the M8 lines count, computed from lanes/ at commit time.
type summary struct {
	needLink       int // open, unlinked project lanes with a readable config
	withSuggestion int // of those, the ones the name table suggests pools for
	unreadable     int // lanes (pools included) whose config.json cannot be read
}

func summarize(stateDir string, reg *Registry) summary {
	var s summary
	keys, _ := lane.ListQueues(stateDir)
	for _, k := range keys {
		cfg, err := lane.ReadConfig(lane.LaneDir(stateDir, k))
		if err != nil {
			s.unreadable++
			continue
		}
		if reg.IsPool(k) || cfg.Closed != "" || len(cfg.Pools) > 0 {
			continue
		}
		s.needLink++
		if _, ok := Suggest(k); ok {
			s.withSuggestion++
		}
	}
	return s
}

// migratedLines is the M8 text, printed once to stderr.
func migratedLines(reg *Registry, s summary) []string {
	lines := []string{fmt.Sprintf("incoda: migrated: pools %s; %d queues need a link before they run again", strings.Join(reg.Pools, ", "), s.needLink)}
	if s.needLink > 0 {
		lines = append(lines, fmt.Sprintf("incoda: ask the user to run incoda init: it shows each queue's suggested pools and asks (%d have one)", s.withSuggestion))
	}
	if s.unreadable > 0 {
		lines = append(lines, fmt.Sprintf("incoda: %d queue(s) have an unreadable config.json: see incoda doctor", s.unreadable))
	}
	return lines
}

// logMigrate writes event=migrate into every lane's lane.log.
func logMigrate(stateDir string, reg *Registry) {
	keys, _ := lane.ListQueues(stateDir)
	for _, k := range keys {
		kind := "project"
		if reg.IsPool(k) {
			kind = "pool"
		}
		lane.AppendLog(lane.LaneDir(stateDir, k), "queue=%s event=migrate pid=%d layout=%d kind=%s", k, os.Getpid(), Layout, kind)
	}
}

// Suggestion is the link the name table of spec 3.5 suggests for a key.
type Suggestion struct {
	Pools        []string
	QuietMachine bool
	Pattern      string
}

var suffixTable = []struct {
	suffix string
	pools  []string
	quiet  bool
}{
	{"-gate", []string{"tests"}, false},
	{"-test", []string{"tests"}, false},
	{"-tests", []string{"tests"}, false},
	{"-build", []string{"builds"}, false},
	{"-ui", []string{"computer-use"}, false},
	{"-desktop", []string{"computer-use"}, false},
	{"-e2e", []string{"computer-use", "tests"}, false},
	{"-measure", []string{"tests"}, true},
}

// Suggest returns the suggestion of spec 3.5 for key, or false when no
// pattern matches. A suffix pattern needs a non-empty name before it.
// Plan 3 prints suggestions; the migration only counts them (M8).
func Suggest(key string) (Suggestion, bool) {
	if key == "compiles" {
		return Suggestion{Pools: []string{"builds"}, Pattern: "compiles"}, true
	}
	for _, r := range suffixTable {
		if len(key) > len(r.suffix) && strings.HasSuffix(key, r.suffix) {
			return Suggestion{Pools: append([]string(nil), r.pools...), QuietMachine: r.quiet, Pattern: "*" + r.suffix}, true
		}
	}
	return Suggestion{}, false
}
```

- [ ] **Step 6: Add the transaction**

`internal/machine/migrate.go`:

```go
package machine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/textsafe"
)

const machineLogName = "machine.log"

// MachineLogPath is <state>/machine.log, the log of machine-level events
// that belong to no lane (event=refence).
func MachineLogPath(stateDir string) string { return filepath.Join(stateDir, machineLogName) }

func appendMachineLog(stateDir, format string, args ...any) {
	f, err := os.OpenFile(MachineLogPath(stateDir), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

// Ensure makes stateDir ready for a command that takes a ticket or writes
// config (run, config; later link, init, pools). On a migrated layout with
// the fence in place and no leftover migration.json it reads machine.json
// and returns, taking no lock. Otherwise it repairs (re-fence, row 8) or
// migrates under machine.lock, before the caller holds any ticket.
func Ensure(stateDir string, o Options) (*Registry, error) {
	reg, err := ReadRegistry(stateDir)
	if err == nil {
		if FencePlaced(stateDir) && kindOf(planPath(stateDir)) == absent {
			return reg, nil
		}
		return repair(stateDir, o)
	}
	if !errors.Is(err, ErrNoRegistry) {
		return nil, err
	}
	return migrate(stateDir, o)
}

// repair takes machine.lock to re-place a missing fence (spec 2.3, only on
// a migrated layout) or to delete a migration.json left after the commit
// (row 8). runMigration re-checks both under the lock: another process may
// have done it already.
func repair(stateDir string, o Options) (*Registry, error) {
	op := "recover"
	if !FencePlaced(stateDir) {
		op = "refence"
	}
	lk, err := AcquireLock(stateDir, o.lockOptions(op))
	if err != nil {
		return nil, err
	}
	defer lk.Release()
	return runMigration(stateDir, lk, o)
}

// migrate runs the transaction of spec 3.3 under machine.lock.
func migrate(stateDir string, o Options) (*Registry, error) {
	lk, err := AcquireLock(stateDir, o.lockOptions("migrate"))
	if err != nil {
		return nil, err
	}
	defer lk.Release()
	crashpoint("locked")
	return runMigration(stateDir, lk, o)
}

// runMigration classifies the layout and resumes at the step the recovery
// table names. The caller holds machine.lock.
func runMigration(stateDir string, lk *Lock, o Options) (*Registry, error) {
	for {
		reg, err := ReadRegistry(stateDir)
		if err == nil {
			// M1: another process won, or this is row 8, or a re-fence.
			if err := os.Remove(planPath(stateDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, stateErrorf("cannot remove migration.json: %s", esc(err))
			}
			if !FencePlaced(stateDir) {
				if err := refence(stateDir); errors.Is(err, errNotIdle) {
					if err := waitNotIdle(o); err != nil {
						return nil, err
					}
					continue
				} else if err != nil {
					return nil, err
				}
			}
			return reg, nil
		}
		if !errors.Is(err, ErrNoRegistry) {
			return nil, err
		}
		st := scanLayout(stateDir)
		var stepErr error
		switch st.row() {
		case RowRegistryLost:
			if !FencePlaced(stateDir) {
				stepErr = refence(stateDir)
			}
			if stepErr == nil {
				return nil, registryLostError()
			}
		case RowSwapped:
			if err := renameDir(fenceNewPath(stateDir), lane.LanesDir(stateDir)); err != nil {
				return nil, stateErrorf("cannot rename queues.new to lanes: %s", esc(err))
			}
		case RowFenceMissing:
			_, stepErr = placeFence(stateDir)
		case RowFenceNoLanes:
			if err := os.Mkdir(lane.LanesDir(stateDir), 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return nil, stateErrorf("cannot create lanes/: %s", esc(err))
			}
		case RowResume:
		default:
			stepErr = beginMigration(stateDir, lk, o, st)
		}
		if errors.Is(stepErr, errNotIdle) {
			if err := waitNotIdle(o); err != nil {
				return nil, err
			}
			continue
		}
		if stepErr != nil {
			return nil, stepErr
		}
		return finishMigration(stateDir, lk, o)
	}
}

// waitNotIdle waits one poll after a directory rename was refused because
// something still has a file open inside (Windows), or gives up when the
// --wait budget is spent.
func waitNotIdle(o Options) error {
	if dl := budgetDeadline(o.Start, o.Wait); !dl.IsZero() && !time.Now().Before(dl) {
		return &Timeout{Msg: joinLines([]string{
			"upgrade-timeout: a directory the upgrade must move still has a file open inside it (an older incoda or another program)",
			"upgrade the older incoda on PATH; see incoda doctor",
		})}
	}
	time.Sleep(o.poll())
	return nil
}

// beginMigration is M1 (leftover queues.new), M2, M3 and M4.
func beginMigration(stateDir string, lk *Lock, o Options, st layoutState) error {
	if st.Queues == aDir && st.QueuesNew == aFile {
		if err := os.Remove(fenceNewPath(stateDir)); err != nil {
			return stateErrorf("cannot remove a leftover queues.new: %s", esc(err))
		}
	}
	if st.Queues == aDir {
		if err := waitIdle(stateDir, lk, o, phaseM2); err != nil {
			return err
		}
	}
	crashpoint("M2")
	if err := writePlan(stateDir); err != nil {
		return err
	}
	crashpoint("M3")
	return fenceMigration(stateDir)
}

// fenceMigration is M4. With the atomic exchange no instant is unfenced;
// with the fallback, or on an empty state directory, the fence is placed
// with the race rule. On Windows a refused rename of queues/ means an older
// run still has a file open in it: errNotIdle sends the caller back to M2.
func fenceMigration(stateDir string) error {
	if err := writeFenceNew(stateDir); err != nil {
		return err
	}
	crashpoint("M4-new")
	q, lanes := lane.QueuesDir(stateDir), lane.LanesDir(stateDir)
	switch kindOf(q) {
	case aDir:
		err := exchangeFn(fenceNewPath(stateDir), q)
		if err == nil {
			crashpoint("M4-swapped")
			if err := renameDir(fenceNewPath(stateDir), lanes); err != nil {
				return stateErrorf("cannot rename queues.new to lanes: %s", esc(err))
			}
			crashpoint("M4")
			return nil
		}
		if !errors.Is(err, errNoExchange) {
			return stateErrorf("cannot swap the queues fence in: %s", esc(err))
		}
		if err := renameDir(q, lanes); err != nil {
			if notIdleOnRenameFailure {
				return errNotIdle
			}
			return stateErrorf("cannot rename queues to lanes: %s", esc(err))
		}
		crashpoint("M4-moved")
	case absent:
		if err := os.Mkdir(lanes, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return stateErrorf("cannot create lanes/: %s", esc(err))
		}
		crashpoint("M4-lanes")
	default:
		return stateErrorf("%s is neither a directory nor absent; move it away and rerun", textsafe.Escape(q))
	}
	if _, err := placeFence(stateDir); err != nil {
		return err
	}
	crashpoint("M4")
	return nil
}

// finishMigration is M5 to M8.
func finishMigration(stateDir string, lk *Lock, o Options) (*Registry, error) {
	if err := waitIdle(stateDir, lk, o, phaseM5); err != nil {
		return nil, err
	}
	crashpoint("M5")
	if err := mergeStrays(stateDir); err != nil {
		return nil, err
	}
	crashpoint("M6")
	if err := applyBootstrap(stateDir); err != nil {
		return nil, err
	}
	crashpoint("M7")
	reg := &Registry{Schema: RegistrySchema, Layout: Layout, Generation: 1, Pools: BootstrapPools(),
		MigratedBy: o.By, MigratedAt: time.Now().UTC().Format(time.RFC3339)}
	// Logged before the commit, so a crash right after it (row 8) still
	// leaves the history; a crash in between logs twice, which is harmless.
	logMigrate(stateDir, reg)
	if err := writeRegistry(stateDir, lk, reg); err != nil {
		return nil, err
	}
	crashpoint("M8-registry")
	if err := os.Remove(planPath(stateDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, stateErrorf("cannot remove migration.json: %s", esc(err))
	}
	for _, line := range migratedLines(reg, summarize(stateDir, reg)) {
		fmt.Fprintln(o.stderr(), line)
	}
	return reg, nil
}

// refence re-places the fence with the race rule and logs event=refence to
// machine.log, naming the strays it made. Counting and cleaning strays is
// plan 2b.
func refence(stateDir string) error {
	moved, err := placeFence(stateDir)
	if err != nil {
		return err
	}
	names := make([]string, len(moved))
	for i, m := range moved {
		names[i] = filepath.Base(m)
	}
	appendMachineLog(stateDir, "event=refence pid=%d strays=%s", os.Getpid(), textsafe.LogValue(strings.Join(names, ",")))
	return nil
}
```

Note on `RowSwapped`: the rename of `queues.new` to `lanes` uses `renameDir` like every other directory move, so the Windows not-idle seam covers it too (Windows never reaches this row: it has no exchange).

- [ ] **Step 7: Run the tests**

Run: `go test ./internal/machine/ -v -run 'TestEnsure|TestRecovery|TestM4|TestFallbackRace|TestInspect|TestSuggest'`
Expected: PASS.

- [ ] **Step 8: Run the full gates**

Run: `GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && just ci`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/machine/layout.go internal/machine/plan.go internal/machine/bootstrap.go internal/machine/migrate.go internal/machine/migrate_test.go
git commit -m "feat: the layout migration with every crash-recovery row

Ensure runs M1 to M8 under machine.lock: idle check, plan, fence by atomic
exchange or the rename fallback with the race rule, sweep, stray merge,
bootstrap pools, commit. Each step is keyed on what exists, so any later
mutating command resumes where a crash stopped. A migrated layout
re-places a missing fence; a lost registry fails closed. Inspect gives
read-only commands the layout and banner without writing anything."
```

---

### Task 7: M0, the PATH check

Before taking `machine.lock`, once per process, the migration looks at every directory on PATH for an executable `incoda` that is not this binary (`os.SameFile` after resolving symlinks) and prints `upgrade-warning:` for each. Nothing is executed and it is not a refusal (spec 3.3 M0). `OtherIncodas` is exported because plan 2b's doctor probes the versions of the same list.

**Files:**
- Create: `internal/machine/pathcheck.go`, `internal/machine/pathcheck_test.go`
- Modify: `internal/machine/migrate.go` (`migrate` calls `checkPath`)

**Interfaces:**
- Consumes: `machine.Options` (`Path`, `Exe`, `stderr()`), `textsafe.Escape`.
- Produces: `machine.OtherIncodas(path, self string) []string`; `machine.checkPath(o Options)` (once per process, guarded by `machine.pathChecked`, an `atomic.Bool` tests reset).

- [ ] **Step 1: Write the failing tests**

`internal/machine/pathcheck_test.go`:

```go
package machine

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeIncoda writes an executable script named incoda in dir that leaves a
// marker if anything ever runs it.
func fakeIncoda(t *testing.T, dir, marker string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "incoda")
	if err := os.WriteFile(p, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOtherIncodasFindsOthersAndSkipsSelf(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script and a symlink")
	}
	root := t.TempDir()
	marker := filepath.Join(root, "ran")
	self := fakeIncoda(t, filepath.Join(root, "self"), marker)
	other := fakeIncoda(t, filepath.Join(root, "old"), marker)
	linkDir := filepath.Join(root, "link")
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self, filepath.Join(linkDir, "incoda")); err != nil {
		t.Fatal(err)
	}
	notExec := filepath.Join(root, "noexec")
	if err := os.MkdirAll(notExec, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notExec, "incoda"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := strings.Join([]string{filepath.Join(root, "self"), linkDir, filepath.Join(root, "old"), notExec, filepath.Join(root, "missing"), filepath.Join(root, "old")}, string(os.PathListSeparator))
	got := OtherIncodas(path, self)
	if len(got) != 1 || got[0] != other {
		t.Fatalf("OtherIncodas = %v, want [%s]", got, other)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("M0 executed an incoda from PATH")
	}
}

func TestMigrationWarnsAboutAnOtherIncodaOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script")
	}
	root := t.TempDir()
	marker := filepath.Join(root, "ran")
	self := fakeIncoda(t, filepath.Join(root, "self"), marker)
	other := fakeIncoda(t, filepath.Join(root, "old"), marker)
	pathChecked.Store(false)
	defer pathChecked.Store(false)
	opts := func(errBuf *bytes.Buffer) Options {
		return Options{Start: time.Now(), Wait: 10 * time.Second, Stderr: errBuf, Path: filepath.Dir(other), Exe: self}
	}
	var errBuf bytes.Buffer
	if _, err := Ensure(t.TempDir(), opts(&errBuf)); err != nil {
		t.Fatal(err)
	}
	want := "incoda: upgrade-warning: another incoda at " + other + `; if it is older than 0.7 it will stop with "not a directory" after this upgrade (incoda doctor shows its version)` + "\n"
	if !strings.HasPrefix(errBuf.String(), want) {
		t.Fatalf("output:\n%s\nwant prefix:\n%s", errBuf.String(), want)
	}
	errBuf.Reset()
	if _, err := Ensure(t.TempDir(), opts(&errBuf)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errBuf.String(), "upgrade-warning") {
		t.Fatal("M0 runs once per process")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("M0 executed an incoda from PATH")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/machine/ -run 'TestOtherIncodas|TestMigrationWarns'`
Expected: compile errors: `undefined: OtherIncodas`, `undefined: pathChecked`.

- [ ] **Step 3: Add the PATH check**

`internal/machine/pathcheck.go`:

```go
package machine

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"

	"github.com/deblasis/incoda/internal/textsafe"
)

// pathChecked makes M0 run once per process.
var pathChecked atomic.Bool

// checkPath is M0 (spec 3.3): before the migration takes machine.lock, warn
// about every other incoda on PATH, which stops with "not a directory" once
// the fence is placed if it is older than 0.7. It never executes anything
// and never refuses.
func checkPath(o Options) {
	if !pathChecked.CompareAndSwap(false, true) {
		return
	}
	for _, p := range OtherIncodas(o.Path, o.Exe) {
		fmt.Fprintf(o.stderr(), "incoda: upgrade-warning: another incoda at %s; if it is older than 0.7 it will stop with \"not a directory\" after this upgrade (incoda doctor shows its version)\n", textsafe.Escape(p))
	}
}

// OtherIncodas lists, in PATH order, every executable regular file named
// incoda (incoda.exe on Windows) in a PATH directory that is not the same
// file as self after resolving symlinks. self "" means os.Executable. An
// empty PATH entry is the current directory, as exec.LookPath reads it.
// Nothing is executed.
func OtherIncodas(path, self string) []string {
	if self == "" {
		self, _ = os.Executable()
	}
	var selfInfo os.FileInfo
	if real, err := filepath.EvalSymlinks(self); err == nil {
		selfInfo, _ = os.Stat(real)
	}
	name := "incoda"
	if runtime.GOOS == "windows" {
		name = "incoda.exe"
	}
	seen := map[string]bool{}
	var out []string
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
		}
		p := filepath.Join(dir, name)
		if seen[p] {
			continue
		}
		seen[p] = true
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			continue
		}
		fi, err := os.Stat(real)
		if err != nil || !fi.Mode().IsRegular() || (runtime.GOOS != "windows" && fi.Mode().Perm()&0o111 == 0) {
			continue
		}
		if selfInfo != nil && os.SameFile(fi, selfInfo) {
			continue
		}
		out = append(out, p)
	}
	return out
}
```

- [ ] **Step 4: Call it before the lock**

In `internal/machine/migrate.go`, make `migrate` start with the check:

```go
// migrate runs the transaction of spec 3.3: M0 before machine.lock, then
// everything else under it.
func migrate(stateDir string, o Options) (*Registry, error) {
	checkPath(o)
	lk, err := AcquireLock(stateDir, o.lockOptions("migrate"))
	if err != nil {
		return nil, err
	}
	defer lk.Release()
	crashpoint("locked")
	return runMigration(stateDir, lk, o)
}
```

The `ensure` helper of Task 6's tests passes no `Path`, so `OtherIncodas("", ...)` sees no directory and those tests print no warning.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/machine/ -v -run 'TestOtherIncodas|TestMigrationWarns|TestEnsure'`
Expected: PASS.

- [ ] **Step 6: Run the full gates**

Run: `GOOS=windows go vet ./... && just ci`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/machine/pathcheck.go internal/machine/pathcheck_test.go internal/machine/migrate.go
git commit -m "feat: warn about other incoda binaries on PATH before migrating

M0 lists every executable incoda on PATH that is not this binary,
resolving symlinks and comparing with os.SameFile, and prints an
upgrade-warning for each, once per process. It never executes them."
```

---

### Task 8: Wire the CLI: mutating commands migrate, read-only commands never do

`run` and `config` call `machine.Ensure` before any ticket (spec 3.2: mutating commands migrate; `link`, `init` and `pools` join them in plan 3). `status`, `watch` and `queues` call `machine.Inspect`: on a layout not upgraded yet they read the old `queues/` read only and print the banner; they never migrate, never re-fence and never take `machine.lock`. `kill` and `force-release` address the layout they find without creating a directory. Every command except `doctor`, `version` and `help` fails closed with exit 122 on a broken or lost registry. From this task on the fence is in place, which lifts the plan-1 hold on cutting builds.

**Files:**
- Create: `internal/cli/state.go`
- Modify: `internal/cli/run.go`, `internal/cli/config.go`, `internal/cli/status.go`, `internal/cli/misc.go` (`cmdWatch`, `cmdQueues`, `cmdForceRelease`), `internal/cli/kill.go`, `internal/cli/cli.go` (`rootUsage`)
- Modify: `internal/report/report.go`
- Modify: `internal/tui/model.go`, `internal/tui/killer.go`, `internal/tui/view.go`, `internal/tui/layout.go`, `internal/tui/view_test.go`
- Create: `migrate_test.go` (root)
- Modify: `integration_test.go` (move `TestStateLivesUnderLanes` out)

**Interfaces:**
- Consumes: `machine.Ensure`, `machine.Inspect`, `machine.View`, `machine.Options`, `machine.StateError`, `machine.Refusal`, `machine.Timeout`, `machine.ReadNote`, `machine.FencePlaced`, `machine.FenceText`, `machine.ReadRegistry`, `machine.RegistryPath`, `machine.MachineLogPath`, `machine.StraysDir`; `lane.OpenIn`, `lane.ListIn`, `lane.ExistsIn`, `lane.Existing`, `lane.ReadOnly`, `lane.ReadConfig`, `lane.RegistryLockPath`; `procinfo.ParentChain`; `startGetenv` (plan 1).
- Produces: in package `cli`: `readState() (string, machine.View, error)`, `mutatingState(start time.Time, wait, poll time.Duration, chain procinfo.Chain, stderr io.Writer) (string, error)`, `machineExit(err error) error`, `printBanner(w io.Writer, banner string)`; `config --wait` (default 1m, spec 4.3); `report.Build(stateDir, version string, keys []string, all bool, events int) (*report.Report, error)` and `report.Report.Banner` (`json:"-"`); `report.Keys` is removed; `(tui.Model).renderBanner(w int) string`. Root test helpers: `seedOldLayout`, `assertLayout2`, `treeState`, `holdOldTicket`, `syncBuffer`, `waitForText`, `waitForFile`.

- [ ] **Step 1: Write the failing integration tests**

In `integration_test.go`, delete `TestStateLivesUnderLanes` and the `"errors"` import that Task 1 added for it (no other test there uses `errors`). Create `migrate_test.go`:

```go
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
func treeState(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
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
	out, code := runIncoda(t, incoda, state, "run", "--queue", "lay", "--quiet", "--", stamp, filepath.Join(t.TempDir(), "s"), "s", "1")
	if code != 0 {
		t.Fatalf("run: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "incoda: migrated: pools builds, computer-use, tests, vm; 0 queues need a link before they run again\n") {
		t.Fatalf("missing the migrated line:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(laneDir(state, "lay"), "lane.log")); err != nil {
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
	batches, _ := os.ReadDir(machine.StraysDir(state))
	if len(batches) != 1 {
		t.Fatalf("want one strays batch, got %v", batches)
	}
	if _, err := os.Stat(filepath.Join(machine.StraysDir(state), batches[0].Name(), "stale")); err != nil {
		t.Fatal(err)
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
	cmd := exec.Command(incoda, "run", "--queue", "newq", "--wait", "60s", "--poll", "50ms", "--", stamp, filepath.Join(t.TempDir(), "s"), "s", "1")
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
		"incoda:   incoda kill --queue held --pid 999999 --reason 'incoda upgrade'\n",
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
```

Add to `internal/tui/view_test.go`:

```go
func TestBannerShowsUnderTheHeader(t *testing.T) {
	m := newTestModel(nil)
	base := m.bodyStartRow()
	m.rep.Banner = "state not upgraded yet: the next mutating incoda command upgrades it"
	if out := m.render(); !strings.Contains(out, "incoda: state not upgraded yet") {
		t.Fatalf("banner missing:\n%s", out)
	}
	if got := m.bodyStartRow(); got != base+1 {
		t.Fatalf("click rows must move down with the banner: %d, want %d", got, base+1)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/tui/ -run TestBannerShowsUnderTheHeader; go test . -run 'TestFirstMutatingCommandMigrates|TestReadOnlyCommandsNeverMigrate|TestBrokenRegistryFailsClosed|TestMigrationWaitsForALiveOldTicket' -v`
Expected: the tui test fails to compile (`m.rep.Banner undefined`); the root tests fail: no `migrated:` line, no `queues` fence, `status` exits 0 on a broken registry.

- [ ] **Step 3: Add the CLI state helpers**

`internal/cli/state.go`:

```go
package cli

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/procinfo"
)

// readState resolves the state directory for a command that only reads
// lanes or addresses one (status, watch, queues, kill, force-release). It
// creates nothing, never migrates, never re-fences and never takes
// machine.lock; a migrated layout with a broken or lost registry fails
// closed with exit 122 (spec 2.1, 3.6).
func readState() (string, machine.View, error) {
	d, err := lane.StateDir()
	if err != nil {
		return "", machine.View{}, exitWith(ExitState, "cannot resolve state directory: %v", err)
	}
	v, err := machine.Inspect(d)
	if err != nil {
		return "", machine.View{}, machineExit(err)
	}
	return d, v, nil
}

// mutatingState resolves and creates the state directory for a command
// that takes a ticket or writes config, then migrates it or re-places a
// missing fence (machine.Ensure), before the caller holds any ticket.
// start and wait are the command's --wait budget, which this spends first.
func mutatingState(start time.Time, wait, poll time.Duration, chain procinfo.Chain, stderr io.Writer) (string, error) {
	d, err := stateDir()
	if err != nil {
		return "", err
	}
	v, _, _ := versionInfo()
	_, err = machine.Ensure(d, machine.Options{
		Start: start, Wait: wait, Poll: poll, Chain: chain,
		By: "incoda " + v, Stderr: stderr, Path: startGetenv("PATH"),
	})
	if err != nil {
		return "", machineExit(err)
	}
	return d, nil
}

// machineExit maps the machine package's errors to exit codes: 122 for
// machine-state, 120 for refusals, 121 for timeouts.
func machineExit(err error) error {
	var se *machine.StateError
	var rf *machine.Refusal
	var to *machine.Timeout
	var ec *exitCode
	switch {
	case errors.As(err, &se):
		return exitWith(ExitState, "%s", se.Msg)
	case errors.As(err, &rf):
		return exitWith(ExitUsage, "%s", rf.Msg)
	case errors.As(err, &to):
		return exitWith(ExitTimeout, "%s", to.Msg)
	case errors.As(err, &ec):
		return err
	}
	return exitWith(ExitState, "%v", err)
}

// printBanner prints the read-only banner of spec 3.2 on w.
func printBanner(w io.Writer, banner string) {
	if banner != "" {
		fmt.Fprintf(w, "incoda: %s\n", banner)
	}
}
```

- [ ] **Step 4: `run` migrates first and shares one budget**

In `internal/cli/run.go`:

1. Make the first line of `cmdRun` (before `fs := newFlagSet("run", stderr)`):

```go
	// One --wait budget, measured from the start of the command, covers
	// the machine.lock and migration waits and every lane (spec 2.4).
	start := time.Now()
```

2. Replace:

```go
	dir, err := stateDir()
	if err != nil {
		return err
	}
```

with:

```go
	chain := procinfo.ParentChain()
	dir, err := mutatingState(start, wait.d, *poll, chain, stderr)
	if err != nil {
		return err
	}
```

3. Replace `inherited := held.Verify(dir, startGetenv("INCODA_HELD"), procinfo.ParentChain())` with `inherited := held.Verify(dir, startGetenv("INCODA_HELD"), chain)`.

4. Replace:

```go
	// argument, and the whole reason a list is allowed at all.
	start := time.Now()
	for _, pt := range toTake {
```

with:

```go
	// argument, and the whole reason a list is allowed at all. The budget
	// started with the command, so machine.lock and migration waits above
	// have already spent part of it.
	for _, pt := range toTake {
```

- [ ] **Step 5: `config` migrates first and gains `--wait`**

In `internal/cli/config.go`:

1. First line of `cmdConfig`: `start := time.Now()`.
2. After the `noColor` flag, add:

```go
	wait := &waitValue{d: time.Minute}
	fs.Var(wait, "wait", "how long to wait for machine.lock and a state upgrade: a Go duration (1m) or bare seconds; negative waits forever")
```

3. Change the usage line to `"usage: incoda config KEY [--slots N] [--description TEXT] [--require-reason[=false]] [--close MSG | --open] [--wait DUR]\n\n"`.
4. Replace:

```go
	dir, err := stateDir()
	if err != nil {
		return err
	}
```

with:

```go
	dir, err := mutatingState(start, wait.d, 200*time.Millisecond, procinfo.ParentChain(), stderr)
	if err != nil {
		return err
	}
```

5. Add `"time"` and `"github.com/deblasis/incoda/internal/procinfo"` to the imports.

- [ ] **Step 6: Build reports from the layout `Inspect` finds**

Replace `Keys` and `Build` in `internal/report/report.go` with:

```go
// Build observes the named queues, or every queue with state when all is
// set, in the layout machine.Inspect finds: lanes/ once migrated, the old
// queues/ read only before (spec 3.2), so a status on a layout not upgraded
// yet creates and reaps nothing. A key with no state is reported as free,
// because a never-used queue is simply free. A broken or lost registry is
// returned as its *machine.StateError so the caller fails closed.
func Build(stateDir, version string, keys []string, all bool, events int) (*Report, error) {
	v, err := machine.Inspect(stateDir)
	if err != nil {
		return nil, err
	}
	if all {
		keys, err = lane.ListIn(v.Root)
		if err != nil {
			return nil, fmt.Errorf("cannot list queues: %w", err)
		}
		sort.Strings(keys)
	}
	mode := lane.Existing
	if !v.Migrated {
		mode = lane.ReadOnly
	}
	host, _ := os.Hostname()
	rep := &Report{
		Schema:         1,
		Version:        version,
		StateDir:       stateDir,
		StateDirSource: StateDirSource(),
		Host:           host,
		Time:           time.Now().Format(time.RFC3339),
		Memory:         sysinfo.ReadMemory(),
		CPU:            sysinfo.ReadCPU(),
		Queues:         []Queue{},
		Banner:         v.Banner,
	}
	for _, key := range keys {
		qr := Queue{
			Key:     key,
			Dir:     filepath.Join(v.Root, key),
			Exists:  lane.ExistsIn(v.Root, key),
			Holders: []lane.Entry{},
			Waiting: []lane.Entry{},
		}
		var q *lane.Queue
		if qr.Exists {
			q, err = lane.OpenIn(v.Root, key, mode)
			if errors.Is(err, os.ErrNotExist) {
				qr.Exists = false
			} else if err != nil {
				return nil, err
			}
		}
		if !qr.Exists {
			qr.EffectiveSlots = 1
			qr.Free = true
			rep.Queues = append(rep.Queues, qr)
			continue
		}
		snap, err := q.Observe(events)
		q.Close()
		if err != nil {
			return nil, fmt.Errorf("cannot read queue %q: %w", key, err)
		}
		qr.EffectiveSlots = snap.EffectiveSlots
		qr.Config = snap.Config
		qr.ConfigError = snap.ConfigError
		qr.Holders = snap.Holders
		qr.Waiting = snap.Waiting
		qr.RecentEvents = snap.RecentEvents
		qr.Free = len(snap.Holders) == 0
		rep.Queues = append(rep.Queues, qr)
	}
	return rep, nil
}
```

Add to the `Report` struct, after `Queues`:

```go
	// Banner is the read-only banner of a layout not upgraded yet (spec
	// 3.2). It is display text, not part of the JSON report; plan 5 adds
	// the layout fields to status --json.
	Banner string `json:"-"`
```

Change the imports to:

```go
import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/sysinfo"
)
```

- [ ] **Step 7: `status`, `watch` and `queues` read; `kill` and `force-release` address what they find**

In `internal/cli/status.go`, replace `buildReport` with:

```go
func buildReport(queueFlag string, all bool, events int) (*Report, error) {
	dir, err := lane.StateDir()
	if err != nil {
		return nil, exitWith(ExitState, "cannot resolve state directory: %v", err)
	}
	var keys []string
	if !all {
		key, err := resolveKey(queueFlag)
		if err != nil {
			return nil, err
		}
		keys = []string{key}
	}
	rep, err := report.Build(dir, Version, keys, all, events)
	if err != nil {
		return nil, machineExit(err)
	}
	return rep, nil
}
```

and in `cmdStatus`, after `rep, err := buildReport(...)` and its error check, add:

```go
	if !*asJSON {
		printBanner(stderr, rep.Banner)
	}
```

In `internal/cli/misc.go`:

1. In `cmdWatch`, replace the interactive branch's `dir, err := stateDir()` with `dir, _, err := readState()`.
2. In the plain loop of `cmdWatch`, right after the `fmt.Fprintf(stdout, "%s  %s\n\n", p.Bold("incoda watch"), ...)` line, add:

```go
		if rep.Banner != "" {
			fmt.Fprintf(stdout, "%s\n\n", p.Yellow("incoda: "+rep.Banner))
		}
```

3. In `cmdQueues`, replace from `dir, err := stateDir()` through the `q, err := lane.Open(dir, k)` line of the loop with:

```go
	dir, v, err := readState()
	if err != nil {
		return err
	}
	printBanner(stderr, v.Banner)
	p := paletteFor(stdout, *noColor)
	keys, err := lane.ListIn(v.Root)
	if err != nil {
		return exitWith(ExitState, "cannot list queues: %v", err)
	}
	sort.Strings(keys)
	fmt.Fprintf(stdout, "%s %s  %s\n", p.Dim("state dir:"), dir, p.Dim("("+stateDirSource()+")"))
	if len(keys) == 0 {
		fmt.Fprintln(stdout, p.Dim("no queues have state on this machine yet"))
		return nil
	}
	mode := lane.Existing
	if !v.Migrated {
		mode = lane.ReadOnly
	}
	for _, k := range keys {
		q, err := lane.OpenIn(v.Root, k, mode)
```

(the rest of the loop is unchanged).

4. In `cmdForceRelease`, replace from `dir, err := stateDir()` through `q, err := lane.Open(dir, key)` with:

```go
	_, v, err := readState()
	if err != nil {
		return err
	}
	if !lane.ExistsIn(v.Root, key) {
		fmt.Fprintf(stdout, "queue %q has no state on this machine; nothing to release\n", key)
		return nil
	}
	q, err := lane.OpenIn(v.Root, key, lane.Existing)
```

In `internal/cli/kill.go`, replace:

```go
	dir, err := stateDir()
	if err != nil {
		return err
	}
	q, err := lane.Open(dir, key)
	if err != nil {
		return exitWith(ExitState, "%v", err)
	}
```

with:

```go
	// kill addresses the layout it finds and never creates a lane: before
	// the upgrade the request goes under queues/<key>, the path older
	// binaries poll (spec 3.2). It never migrates or takes machine.lock.
	_, v, err := readState()
	if err != nil {
		return err
	}
	q, err := lane.OpenIn(v.Root, key, lane.Existing)
	if errors.Is(err, os.ErrNotExist) {
		return usagef("queue %q has no live participant with pid %d: %v", key, *pid, lane.ErrNoParticipant)
	}
	if err != nil {
		return exitWith(ExitState, "%v", err)
	}
```

- [ ] **Step 8: The TUI reads through `Inspect` and shows the banner**

In `internal/tui/model.go`, replace the default `Load` closure in `New` with:

```go
		opt.Load = func() (*report.Report, error) {
			var keys []string
			if key != "" {
				keys = []string{key}
			}
			return report.Build(dir, version, keys, key == "", events)
		}
```

In `internal/tui/killer.go`, add:

```go
// open finds key's lane in the layout the state directory has now (lanes/
// once migrated, queues/ before) without creating anything: a kill
// addresses the layout it finds (spec 3.2).
func (k LaneKiller) open(key string) (*lane.Queue, error) {
	v, err := machine.Inspect(k.Dir)
	if err != nil {
		return nil, err
	}
	return lane.OpenIn(v.Root, key, lane.Existing)
}
```

replace each of the three `lane.Open(k.Dir, key)` calls with `k.open(key)`, and add `"github.com/deblasis/incoda/internal/machine"` to its imports.

In `internal/tui/view.go`, replace in `render`:

```go
	top := lipgloss.JoinVertical(lipgloss.Left, m.renderHeader(w), m.renderGauge(w), "", body)
```

with:

```go
	rows := []string{m.renderHeader(w)}
	if b := m.renderBanner(w); b != "" {
		rows = append(rows, b)
	}
	rows = append(rows, m.renderGauge(w), "", body)
	top := lipgloss.JoinVertical(lipgloss.Left, rows...)
```

and add after `renderHeader`:

```go
// renderBanner is the banner of a state directory not upgraded yet (spec
// 3.2): watch shows the old layout read only and says so.
func (m Model) renderBanner(w int) string {
	if m.rep == nil || m.rep.Banner == "" {
		return ""
	}
	return m.st.accent.Render(trunc("incoda: "+m.rep.Banner, w))
}
```

In `internal/tui/layout.go`, replace `bodyStartRow` with:

```go
// bodyStartRow is the terminal row where the overview/queue body begins,
// derived from the same header, banner and gauge strings render() uses so
// hit testing stays aligned if any of them wraps.
func (m Model) bodyStartRow() int {
	w := m.layoutWidth()
	rows := lipgloss.Height(m.renderHeader(w)) + lipgloss.Height(m.renderGauge(w)) + 1
	if b := m.renderBanner(w); b != "" {
		rows += lipgloss.Height(b)
	}
	return rows
}
```

- [ ] **Step 9: Update the help text**

In `internal/cli/cli.go` `rootUsage`, change the config line to `  incoda config KEY [--slots N] [--description TEXT] [--require-reason] [--close MSG | --open] [--wait DUR]`, and add after the paragraph that ends `$XDG_STATE_HOME/incoda or ~/.local/state/incoda (Linux).`:

```

Lanes live in <state>/lanes/. The first run or config on a state directory
used by an older incoda upgrades it once: it waits for that incoda's runs
to finish, then makes <state>/queues a file that stops older binaries
(exit 122) and registers the machine-wide pools builds, computer-use, tests
and vm in <state>/machine.json. status, watch and queues never upgrade.
```

and replace the exit-code lines for 120, 121 and 122 with:

```
  120      usage error (bad flags, missing/invalid queue key, refused force-release),
           or a refusal such as upgrade-blocked
  121      --wait elapsed while still queued, waiting for machine.lock, or
           waiting for older incoda runs before the state upgrade
  122      state directory, machine.json or OS file locking unusable
```

- [ ] **Step 10: Run the tests**

Run: `go test ./internal/tui/ ./internal/report/ ./internal/cli/ && go test . -v -run 'TestFirstMutating|TestFreshDir|TestReadOnly|TestStatusOnTheOld|TestRunReplaces|TestBrokenRegistry|TestNewerRegistry|TestLostRegistry|TestPoolUsable|TestUpgradeBlocked|TestMigrationWaits|TestMigrationTimesOut'`
Expected: PASS.

- [ ] **Step 11: Run the full gates**

Run: `GOOS=windows go vet ./... && just ci`
Expected: PASS, including every existing integration test (they migrate their fresh state directory on their first `run` or `config`).

- [ ] **Step 12: Commit**

```bash
git add internal/cli internal/report internal/tui migrate_test.go integration_test.go
git commit -m "feat: run and config migrate the state directory, readers never do

run and config call machine.Ensure before any ticket, inside their one
--wait budget; config gains --wait. status, watch and queues read the
layout machine.Inspect finds, the old queues/ read only with a banner
until the upgrade, and kill and force-release address that layout
without creating directories. A broken or lost registry fails closed
with exit 122 for every command but doctor, version and help."
```

---

### Task 9: Crash injection against the real binary, one test per recovery row

Task 6 proves each row by building its state by hand. This task proves that the real binary leaves exactly those states when it dies after each step, and that the next ordinary command recovers. A second binary is built with `-tags incoda_crashpoints`; `INCODA_TEST_CRASH_AT=<step>` makes it exit with status 97 right after that step, running no deferred code, as a kill would. Rows 6 and 9 are not produced by any step order (spec 3.3), so they are built by hand. The same build's pause point drives the fallback race and the "old and new first run on an empty directory" race deterministically, and the release binary is shown to ignore the crash variables.

**Files:**
- Create: `migrate_crash_test.go` (root)

**Interfaces:**
- Consumes: `binaries`, `laneEnv`, `runIncoda`, `exitCodeOf` (integration_test.go, config_test.go); `seedOldLayout`, `assertLayout2`, `holdOldTicket`, `syncBuffer`, `waitForText`, `waitForFile` (migrate_test.go); `machine.FencePlaced`, `machine.StraysDir`, `machine.RegistryPath`.
- Produces: `crashBinary(t) string`; `describeLayout(state string) string`.

- [ ] **Step 1: Write the tests**

`migrate_crash_test.go`:

```go
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
// live stray ticket with the --force stop line, and M6 merges it into lanes/.
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
			waitForText(t, &errBuf, "incoda:   incoda kill --queue late --pid 999998 --reason 'incoda upgrade' --force\n")
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
```

- [ ] **Step 2: Run them**

Run: `go test . -v -run 'TestCrashAtEveryStepRecovers|TestRecoveryRow|TestReleaseBuildIgnoresCrashpoints|TestFenceRaces'`
Expected: PASS (on macOS every subtest runs; the swap row skips only where no exchange exists).

If a row fails, the message names the step, the layout found and the layout expected. Fix the migration (Task 6), not the expectation: the expectations are the recovery table of spec 3.3.

- [ ] **Step 3: Run the full gates**

Run: `GOOS=windows go vet ./... && just ci`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add migrate_crash_test.go
git commit -m "test: crash the migration after every step and recover

A build with the incoda_crashpoints tag dies after each named step; each
death leaves the recovery-table row the spec names, and the next ordinary
command finishes the migration. Rows 6 and 9 are built by hand. Pause
points drive the fallback race and the empty-directory race, and the
release build ignores the crash variables."
```

---

### Task 10: `doctor` reads the layout; `--rebuild-registry`

`doctor` reports and never migrates (spec 3.2). This task gives it the parts of spec 5.5 that exist once plan 2a is in: the layout and `machine.json` state (newer schema or layout included), an unfinished migration, the fence, lanes with an unreadable `config.json` (the M8 text points here), and `INCODA_DIR`. Exit 0 when healthy or when only `attention:` items exist; exit 122 when anything makes runs fail closed. It also adds `incoda doctor --rebuild-registry builds,tests,...`, the human act of spec 3.6. Doctor's PATH version probing, strays, orphan records and stopped holders are plan 2b.

**Files:**
- Modify: `internal/lane/queue.go` (add `LockAll`, `LiveLocked`), `internal/lane/layout_test.go`
- Create: `internal/machine/doctor.go`, `internal/machine/rebuild.go`, `internal/machine/rebuild_test.go`
- Modify: `internal/cli/misc.go` (`cmdDoctor`), `internal/cli/cli.go` (`rootUsage` doctor line)
- Create: `doctor_test.go` (root)

**Interfaces:**
- Consumes: `machine.ReadRegistry`, `scanLayout`, `row`, `Row.String`, `banner`, `migrationNote`, `FencePlaced`, `refence`, `writeRegistry`, `AcquireLock`, `Options.lockOptions`, `esc`; `lane.ListQueues`, `lane.ReadConfig`, `lane.OpenIn`, `lane.Existing`.
- Produces: `lane.LockAll(qs []*Queue) (func(), error)`; `(*lane.Queue).LiveLocked() ([]Entry, error)`; `type machine.Health struct{ Layout, Fence string; Problems, Attention []string }`; `machine.Diagnose(stateDir string) Health`; `machine.Rebuild(stateDir string, pools []string, o Options, report func(key, kind string)) (*Registry, error)`; `doctor --rebuild-registry LIST` and `doctor --wait DUR` (default 1m, used only with `--rebuild-registry`).

- [ ] **Step 1: Write the failing tests**

Add to `internal/lane/layout_test.go`:

```go
func TestLockAllAndLiveLocked(t *testing.T) {
	root := t.TempDir()
	a, err := OpenIn(root, "a", Create)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := OpenIn(root, "b", Create)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	en, err := a.Enroll(Ticket{Command: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	defer en.Release(0)
	unlock, err := LockAll([]*Queue{a, b})
	if err != nil {
		t.Fatal(err)
	}
	la, err := a.LiveLocked()
	if err != nil || len(la) != 1 {
		t.Fatalf("a: %v %v", la, err)
	}
	lb, err := b.LiveLocked()
	if err != nil || len(lb) != 0 {
		t.Fatalf("b: %v %v", lb, err)
	}
	unlock()
	// Released: an enrollment can take b's registry lock again.
	enb, err := b.Enroll(Ticket{Command: []string{"y"}})
	if err != nil {
		t.Fatal(err)
	}
	enb.Release(0)
}
```

`internal/machine/rebuild_test.go`:

```go
package machine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
)

func rebuild(state string, pools ...string) (*Registry, []string, error) {
	var kinds []string
	reg, err := Rebuild(state, pools, Options{Start: time.Now(), Wait: 5 * time.Second, By: "incoda test"},
		func(k, kind string) { kinds = append(kinds, k+"="+kind) })
	return reg, kinds, err
}

func TestRebuildRegistry(t *testing.T) {
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
	before := time.Now().UnixNano()
	reg, kinds, err := rebuild(state, "tests", "builds", "tests", "printer")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(reg.Pools, ",") != "builds,printer,tests" || reg.Generation < before {
		t.Fatalf("registry %+v", reg)
	}
	if got := strings.Join(kinds, ","); got != "builds=pool,computer-use=project,tests=pool,vm=project,printer=pool (no lane yet)" {
		t.Fatalf("kinds %s", got)
	}
	if !FencePlaced(state) {
		t.Fatal("the rebuild places a missing fence")
	}
	if r, err := ReadRegistry(state); err != nil || r.Generation != reg.Generation {
		t.Fatalf("written registry %+v %v", r, err)
	}
}

func TestRebuildRefusals(t *testing.T) {
	var rf *Refusal
	var se *StateError

	fresh := t.TempDir()
	if _, _, err := rebuild(fresh, "builds"); !errors.As(err, &se) || !strings.HasPrefix(se.Msg, "machine-state: nothing to rebuild: ") {
		t.Fatalf("no lanes/: %v", err)
	}

	state := t.TempDir()
	if _, _, err := ensure(t, state); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rebuild(state, "a/b"); !errors.As(err, &rf) || !strings.HasPrefix(rf.Msg, `rebuild-registry: queue key "a/b" contains`) {
		t.Fatalf("invalid key: %v", err)
	}
	if _, _, err := rebuild(state); !errors.As(err, &rf) || !strings.HasPrefix(rf.Msg, "rebuild-registry: name at least one pool") {
		t.Fatalf("empty list: %v", err)
	}

	cfg := filepath.Join(lane.LaneDir(state, "tests"), "config.json")
	if err := os.WriteFile(cfg, []byte(`{"schema":2,"pools":["builds"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rebuild(state, "tests"); !errors.As(err, &rf) || rf.Msg != `kind-busy: "tests" links pools` {
		t.Fatalf("a named key with a link: %v", err)
	}
	if err := os.WriteFile(cfg, []byte(`{"schema":2,"slots":1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	release := holdTicket(t, lane.LanesDir(state), "vm", 4711, "qemu")
	if _, _, err := rebuild(state, "builds"); !errors.As(err, &rf) || rf.Msg != `kind-busy: "vm" has live tickets` {
		t.Fatalf("a live ticket: %v", err)
	}
	release()

	if err := writePlan(state); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rebuild(state, "builds"); !errors.As(err, &se) || !strings.Contains(se.Msg, "a migration is unfinished") {
		t.Fatalf("unfinished migration: %v", err)
	}
	if err := os.Remove(planPath(state)); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(RegistryPath(state), []byte(`{"schema":2,"layout":2,"generation":1,"pools":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rebuild(state, "builds"); !errors.As(err, &se) || !strings.Contains(se.Msg, "written by a newer incoda") {
		t.Fatalf("a newer registry is never overwritten: %v", err)
	}
}
```

`doctor_test.go`:

```go
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
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/lane/ ./internal/machine/ -run 'TestLockAll|TestRebuild'; go test . -run 'TestDoctor' -v`
Expected: compile errors (`undefined: LockAll`, `LiveLocked`, `Rebuild`); the doctor tests fail (no `layout:` line, `--rebuild-registry` is an unknown flag).

- [ ] **Step 3: Add `LockAll` and `LiveLocked`**

Add to `internal/lane/queue.go`:

```go
// LockAll takes the registry lock of every queue in the order given;
// callers pass them sorted by key, the lock order of spec 3.1. It returns
// the function that releases them in reverse order. A registry rebuild
// uses it to hold every lane still while it checks for tickets and
// rewrites machine.json.
func LockAll(qs []*Queue) (func(), error) {
	var held []*Queue
	unlock := func() {
		for i := len(held) - 1; i >= 0; i-- {
			_ = held[i].registry.Unlock()
		}
	}
	for _, q := range qs {
		if err := q.registry.Lock(); err != nil {
			unlock()
			return nil, fmt.Errorf("registry lock of %q: %w", q.Key, err)
		}
		held = append(held, q)
	}
	return unlock, nil
}

// LiveLocked lists the live tickets, reaping dead ones as any scan does.
// The caller holds this queue's registry lock (LockAll).
func (q *Queue) LiveLocked() ([]Entry, error) {
	live, _, err := q.scanLocked(time.Now())
	return live, err
}
```

- [ ] **Step 4: Add `Diagnose`**

`internal/machine/doctor.go`:

```go
package machine

import (
	"errors"
	"fmt"
	"strings"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/textsafe"
)

// Health is doctor's reading of the layout: the parts of spec 5.5 that
// exist with layout 2. PATH versions, strays, orphan records and stopped
// holders are added by plan 2b.
type Health struct {
	// Layout is one line describing the layout.
	Layout string
	// Fence is "present" or "missing" on a migrated layout, else empty.
	Fence string
	// Problems make runs fail closed: doctor exits 122.
	Problems []string
	// Attention items are printed; doctor still exits 0.
	Attention []string
}

// Diagnose reads the layout like Inspect: no lock, no writes.
func Diagnose(stateDir string) Health {
	var h Health
	reg, err := ReadRegistry(stateDir)
	if errors.Is(err, ErrNoRegistry) && scanLayout(stateDir).row() == RowRegistryLost {
		// A migration may have committed between the two looks.
		reg, err = ReadRegistry(stateDir)
	}
	st := scanLayout(stateDir)
	switch {
	case err == nil:
		h.Layout = fmt.Sprintf("2 (machine.json schema %d, generation %d; pools %s)", reg.Schema, reg.Generation, strings.Join(reg.Pools, ", "))
		if st.Plan {
			h.Problems = append(h.Problems, fmt.Sprintf("migration unfinished (%s); the next mutating incoda command deletes migration.json", RowCommitted))
		}
		if FencePlaced(stateDir) {
			h.Fence = "present"
		} else {
			h.Fence = "missing"
			h.Problems = append(h.Problems, fmt.Sprintf("fence missing: %s is not a regular file, so an older incoda can run outside the pools; the next run or config re-places it", textsafe.Escape(lane.QueuesDir(stateDir))))
		}
		h.Attention = append(h.Attention, unreadableConfigs(stateDir)...)
	case !errors.Is(err, ErrNoRegistry):
		h.Layout = "2 (machine.json unusable)"
		h.Problems = append(h.Problems, strings.TrimPrefix(err.Error(), "machine-state: "))
	default:
		switch row := st.row(); row {
		case RowRegistryLost:
			h.Layout = "2 (machine.json missing)"
			h.Problems = append(h.Problems, "machine.json: missing while lanes/ exists; nothing re-creates it on its own. A human decides which lanes are pools and runs: incoda doctor --rebuild-registry builds,computer-use,tests,vm")
		case RowNotStarted:
			if st.Queues == aDir {
				h.Layout = "1 (queues/)"
				h.Attention = append(h.Attention, banner(stateDir))
			} else {
				h.Layout = "none yet (the next mutating incoda command creates layout 2)"
			}
		default:
			h.Layout = "upgrade unfinished"
			if _, ok := migrationNote(stateDir); ok {
				h.Problems = append(h.Problems, fmt.Sprintf("migration in progress (%s): %s", row, banner(stateDir)))
			} else {
				h.Problems = append(h.Problems, fmt.Sprintf("migration unfinished (%s); the next mutating incoda command resumes it", row))
			}
		}
	}
	return h
}

// unreadableConfigs names every lane whose config.json cannot be read; the
// M8 text sends the user here.
func unreadableConfigs(stateDir string) []string {
	keys, _ := lane.ListQueues(stateDir)
	var out []string
	for _, k := range keys {
		if _, err := lane.ReadConfig(lane.LaneDir(stateDir, k)); err != nil {
			out = append(out, fmt.Sprintf("queue %q has an unreadable config.json: %s", k, textsafe.Escape(err.Error())))
		}
	}
	return out
}
```

- [ ] **Step 5: Add `Rebuild`**

`internal/machine/rebuild.go`:

```go
package machine

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/textsafe"
)

// Rebuild is incoda doctor --rebuild-registry (spec 3.6), a human act. It
// writes a new machine.json naming exactly pools, under machine.lock, in
// this order: every name must pass ValidateKey; a named key whose
// config.json carries pools is refused; then every lane's registry lock is
// taken in key order and any live ticket refuses the rebuild (it may change
// any lane's kind, and runs that acquired before the loss may still be
// running); each lane's resulting kind is reported; the fence is placed if
// it is missing; machine.json is written with generation set to the
// current Unix time in nanoseconds, so it differs from any earlier value.
// It refuses a newer machine.json, a state directory without lanes/, and an
// unfinished migration.
func Rebuild(stateDir string, pools []string, o Options, report func(key, kind string)) (*Registry, error) {
	set := map[string]bool{}
	for _, p := range pools {
		if err := lane.ValidateKey(p); err != nil {
			return nil, &Refusal{Msg: "rebuild-registry: " + err.Error()}
		}
		set[p] = true
	}
	if len(set) == 0 {
		return nil, &Refusal{Msg: "rebuild-registry: name at least one pool, for example builds,computer-use,tests,vm"}
	}
	names := make([]string, 0, len(set))
	for p := range set {
		names = append(names, p)
	}
	sort.Strings(names)

	lk, err := AcquireLock(stateDir, o.lockOptions("rebuild-registry"))
	if err != nil {
		return nil, err
	}
	defer lk.Release()

	old, err := ReadRegistry(stateDir)
	var se *StateError
	if errors.As(err, &se) && se.newer {
		return nil, err
	}
	if err != nil {
		old = nil
	}
	st := scanLayout(stateDir)
	if !st.Lanes {
		return nil, stateErrorf("nothing to rebuild: %s has no lanes/; the next mutating incoda command upgrades this state directory", textsafe.Escape(stateDir))
	}
	if st.Plan {
		return nil, stateErrorf("a migration is unfinished; run any mutating incoda command to resume it before rebuilding the registry")
	}
	for _, p := range names {
		if cfg, err := lane.ReadConfig(lane.LaneDir(stateDir, p)); err == nil && len(cfg.Pools) > 0 {
			return nil, &Refusal{Msg: fmt.Sprintf("kind-busy: %q links pools", p)}
		}
	}

	keys, err := lane.ListQueues(stateDir)
	if err != nil {
		return nil, stateErrorf("cannot list lanes/: %s", esc(err))
	}
	sort.Strings(keys)
	var qs []*lane.Queue
	defer func() {
		for _, q := range qs {
			q.Close()
		}
	}()
	for _, k := range keys {
		q, err := lane.OpenIn(lane.LanesDir(stateDir), k, lane.Existing)
		if err != nil {
			return nil, stateErrorf("cannot open lane %q: %s", k, esc(err))
		}
		qs = append(qs, q)
	}
	unlock, err := lane.LockAll(qs)
	if err != nil {
		return nil, stateErrorf("%s", esc(err))
	}
	defer unlock()
	for _, q := range qs {
		live, err := q.LiveLocked()
		if err != nil {
			return nil, stateErrorf("cannot scan lane %q: %s", q.Key, esc(err))
		}
		if len(live) > 0 {
			return nil, &Refusal{Msg: fmt.Sprintf("kind-busy: %q has live tickets", q.Key)}
		}
	}

	inLanes := map[string]bool{}
	for _, k := range keys {
		inLanes[k] = true
		kind := "project"
		if set[k] {
			kind = "pool"
		}
		report(k, kind)
	}
	for _, p := range names {
		if !inLanes[p] {
			report(p, "pool (no lane yet)")
		}
	}
	if !FencePlaced(stateDir) {
		if err := refence(stateDir); errors.Is(err, errNotIdle) {
			return nil, stateErrorf("cannot place the queues fence: the directory at queues still has a file open inside")
		} else if err != nil {
			return nil, err
		}
	}
	now := time.Now()
	reg := &Registry{Schema: RegistrySchema, Layout: Layout, Generation: now.UnixNano(), Pools: names,
		MigratedBy: o.By, MigratedAt: now.UTC().Format(time.RFC3339)}
	if old != nil {
		reg.extra, reg.MigratedBy, reg.MigratedAt = old.extra, old.MigratedBy, old.MigratedAt
	}
	if err := writeRegistry(stateDir, lk, reg); err != nil {
		return nil, err
	}
	return reg, nil
}
```

- [ ] **Step 6: Rewrite `cmdDoctor`**

Replace `cmdDoctor` in `internal/cli/misc.go` with:

```go
func cmdDoctor(args []string, stdout, stderr io.Writer) error {
	start := time.Now()
	fs := newFlagSet("doctor", stderr)
	noColor := fs.Bool("no-color", false, "never emit ANSI color, even on a terminal (the NO_COLOR environment variable does the same)")
	rebuild := fs.String("rebuild-registry", "", "a human decision after machine.json was lost or broken: write a new one naming exactly these pools (comma-separated)")
	wait := &waitValue{d: time.Minute}
	fs.Var(wait, "wait", "with --rebuild-registry: how long to wait for machine.lock")
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for doctor"}
	}
	p := paletteFor(stdout, *noColor)

	v, c, d := versionInfo()
	fmt.Fprintf(stdout, "incoda %s (commit %s, built %s)\n", p.Bold(v), c, d)
	fmt.Fprintf(stdout, "%s %s  %s/%s\n", p.Dim("go:       "), runtime.Version(), runtime.GOOS, runtime.GOARCH)
	host, _ := os.Hostname()
	fmt.Fprintf(stdout, "%s %s\n", p.Dim("host:     "), host)

	dir, err := lane.StateDir()
	if err != nil {
		fmt.Fprintf(stdout, "%s %s\n", p.Dim("state dir:"), p.BoldRed("UNRESOLVED: "+err.Error()))
		return exitWith(ExitState, "cannot resolve the state directory")
	}
	fmt.Fprintf(stdout, "%s %s\n", p.Dim("state dir:"), dir)
	fmt.Fprintf(stdout, "  %s %s\n", p.Dim("source: "), stateDirSource())
	if src := stateDirSource(); src == "INCODA_DIR" {
		// A per-project override is the one configuration mistake that breaks
		// the whole model quietly: every fragment looks like a healthy, empty
		// lane while the jobs it was meant to serialise run side by side.
		fmt.Fprintf(stdout, "  %s INCODA_DIR is set. It is a MACHINE-level override, not a per-project one.\n", p.BoldYellow("WARNING:"))
		fmt.Fprintln(stdout, "           If some callers have it set and others do not, they will use different")
		fmt.Fprintln(stdout, "           state directories, form separate lanes, and stop serialising each other.")
	}
	fmt.Fprintf(stdout, "  %s %s (state is never derived from the working directory)\n", p.Dim("cwd-independent:"), p.Green("yes"))
	if cwd, err := os.Getwd(); err == nil {
		fmt.Fprintf(stdout, "  %s %s\n", p.Dim("current cwd (not used for resolution):"), cwd)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(stdout, "  %s %s\n", p.Dim("writable:"), p.BoldRed(fmt.Sprintf("NO (%v)", err)))
		return exitWith(ExitState, "state directory is not usable")
	}
	fmt.Fprintf(stdout, "  %s %s\n", p.Dim("writable:"), p.Green("yes"))

	if err := probeLocking(dir, stdout, p); err != nil {
		return exitWith(ExitState, "OS file locking is not usable: %v", err)
	}

	if *rebuild != "" {
		var pools []string
		for _, k := range strings.Split(*rebuild, ",") {
			if k = strings.TrimSpace(k); k != "" {
				pools = append(pools, k)
			}
		}
		reg, err := machine.Rebuild(dir, pools, machine.Options{
			Start: start, Wait: wait.d, Poll: 200 * time.Millisecond, Chain: procinfo.ParentChain(),
			By: "incoda " + v, Stderr: stderr,
		}, func(key, kind string) { fmt.Fprintf(stdout, "rebuild-registry: %s: %s\n", key, kind) })
		if err != nil {
			return machineExit(err)
		}
		fmt.Fprintf(stdout, "rebuild-registry: wrote machine.json (generation %d)\n", reg.Generation)
	}

	if view, err := machine.Inspect(dir); err == nil {
		keys, err := lane.ListIn(view.Root)
		if err == nil {
			sort.Strings(keys)
			if len(keys) == 0 {
				fmt.Fprintln(stdout, p.Dim("queues:    none yet"))
			} else {
				fmt.Fprintf(stdout, "%s %s\n", p.Dim("queues:   "), strings.Join(keys, ", "))
			}
		}
	}
	if k := strings.TrimSpace(os.Getenv("INCODA_QUEUE")); k != "" {
		note := p.Green("ok")
		if err := lane.ValidateKey(k); err != nil {
			note = p.BoldRed("INVALID: " + err.Error())
		}
		fmt.Fprintf(stdout, "%s %s (%s)\n", p.Dim("INCODA_QUEUE:"), k, note)
	} else {
		fmt.Fprintf(stdout, "%s %s\n", p.Dim("INCODA_QUEUE:"), p.Dim("unset (run needs --queue)"))
	}

	h := machine.Diagnose(dir)
	fmt.Fprintf(stdout, "%s %s\n", p.Dim("layout:   "), textsafe.Escape(h.Layout))
	if h.Fence != "" {
		fmt.Fprintf(stdout, "%s %s\n", p.Dim("fence:    "), h.Fence)
	}
	attention := h.Attention
	if stateDirSource() == "INCODA_DIR" {
		attention = append(attention, "INCODA_DIR is set: pools are per state directory, so a caller without it uses other pools")
	}
	for _, a := range attention {
		fmt.Fprintf(stdout, "%s %s\n", p.BoldYellow("attention:"), textsafe.Escape(a))
	}
	for _, pr := range h.Problems {
		fmt.Fprintf(stdout, "%s %s\n", p.BoldRed("problem:  "), textsafe.Escape(pr))
	}
	fmt.Fprintf(stdout, "%s\n", p.Dim(sysinfo.MachineLine(sysinfo.ReadMemory(), sysinfo.ReadCPU())))
	if len(h.Problems) > 0 {
		return exitWith(ExitState, "machine-state: %d problem(s) make runs fail closed; see the problem: lines above", len(h.Problems))
	}
	return nil
}
```

Add `"github.com/deblasis/incoda/internal/machine"`, `"github.com/deblasis/incoda/internal/procinfo"` and `"github.com/deblasis/incoda/internal/textsafe"` to the imports of `misc.go`.

In `rootUsage` (`internal/cli/cli.go`), change the doctor line to `  incoda doctor [--rebuild-registry POOL,POOL... [--wait DUR]]`.

- [ ] **Step 7: Run the tests**

Run: `go test ./internal/lane/ ./internal/machine/ -run 'TestLockAll|TestRebuild' -v && go test . -run 'TestDoctor' -v`
Expected: PASS (including the existing `TestDoctorAndVersionAndQueues`, which only has the `INCODA_DIR` attention item and exits 0).

- [ ] **Step 8: Run the full gates**

Run: `GOOS=windows go vet ./... && just ci`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/lane/queue.go internal/lane/layout_test.go internal/machine/doctor.go internal/machine/rebuild.go internal/machine/rebuild_test.go internal/cli/misc.go internal/cli/cli.go doctor_test.go
git commit -m "feat: doctor reports the layout and can rebuild a lost registry

doctor names the layout, the machine.json state, an unfinished migration,
the fence, unreadable lane configs and INCODA_DIR, and exits 122 only
when runs fail closed. doctor --rebuild-registry writes a new
machine.json naming exactly the given pools, under machine.lock and every
lane's registry lock, refusing while any lane has a live ticket."
```

---

### Task 11: Released binaries against the new layout

The fence is only worth something if real old binaries stop at it. This task builds incoda v0.2.0 and v0.6.0 from their tags (`git archive <tag> | tar -x` into a temp dir, then `go build` there; never a worktree, never a write to the repository's `.git`), once per test run, and proves: against a migrated directory every old command that touches state exits 122 and writes nothing; the migration waits for a live old run (M2) and for one that slips in after M2 (M5); and a new-binary run started by an old run while the migrator waits for that old run refuses at once with `upgrade-blocked:` (spec 3.1). If an old tag does not build (offline, no module cache, not a git checkout), the test skips and says which tag.

**Files:**
- Create: `oldbin_test.go` (root)

**Interfaces:**
- Consumes: `binaries`, `laneEnv`, `runIncoda`, `exitCodeOf`, `readInterval`, `laneDir` (integration_test.go, config_test.go); `crashBinary` (migrate_crash_test.go); `syncBuffer`, `waitForText`, `waitForFile`, `treeState` (migrate_test.go); `machine.FencePlaced`.
- Produces: `oldBinary(t *testing.T, tag string) string`; `countTicketsIn(dir string) int`.

- [ ] **Step 1: Write the tests**

`oldbin_test.go`:

```go
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
		var errs strings.Builder
		archive.Stderr, untar.Stderr = &errs, &errs
		if err := untar.Start(); err != nil {
			ob.err = err
			return
		}
		if err := archive.Run(); err != nil {
			_ = untar.Wait()
			ob.err = fmt.Errorf("git archive %s: %v\n%s", tag, err, errs.String())
			return
		}
		if err := untar.Wait(); err != nil {
			ob.err = fmt.Errorf("tar: %v\n%s", err, errs.String())
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
					if args[0] != "doctor" && !strings.Contains(string(out), "not a directory") {
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

			out, code := runIncoda(t, incoda, state, "run", "--queue", "newq", "--wait", "60s", "--poll", "50ms", "--", stamp, filepath.Join(stamps, "new.txt"), "new", "10")
			if code != 0 {
				t.Fatalf("new run: exit %d\n%s", code, out)
			}
			for _, want := range []string{
				"incoda: upgrade-wait: state upgrade waits for 1 run(s) by an older incoda:\n",
				fmt.Sprintf("incoda:   oldq pid %d: ", o.Process.Pid),
				fmt.Sprintf("incoda:   incoda kill --queue oldq --pid %d --reason 'incoda upgrade'\n", o.Process.Pid),
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
// ticket into lanes/, and the migration waits for it with the --force
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
	m := exec.Command(bin, "run", "--queue", "newq", "--wait", "60s", "--poll", "50ms", "--", stamp, filepath.Join(stamps, "new.txt"), "new", "10")
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

	waitForText(t, &mErr, fmt.Sprintf("incoda:   incoda kill --queue slip --pid %d --reason 'incoda upgrade' --force\n", o.Process.Pid))
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

	out, code := runIncoda(t, incoda, state, "run", "--queue", "m", "--wait", "60s", "--poll", "50ms", "--", stamp, filepath.Join(t.TempDir(), "m.txt"), "m", "10")
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
```

- [ ] **Step 2: Run them**

Run: `go test . -v -run 'TestOldBinaries|TestMigrationWaitsForAnOldRun|TestBlockedWaiter'`
Expected: PASS (the first run builds both tags, a few seconds each). Offline without a module cache, or from a copy of the tree that is not a git checkout: SKIP with `cannot build incoda v0.6.0 (offline, no module cache, or not run from a git checkout?)`.

- [ ] **Step 3: Run the full gates**

Run: `GOOS=windows go vet ./... && just ci`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add oldbin_test.go
git commit -m "test: released binaries stop at the fence and are waited for

v0.2.0 and v0.6.0 are built from their tags with git archive into a temp
dir. On a migrated directory every command of theirs that touches state
exits 122 and writes nothing. The migration waits for a live old run and
for one that slips in after the idle check, and a new run started by an
old run the migrator waits for refuses with upgrade-blocked."
```

---

## Self-review against the spec

Every spec clause in scope, and the task that implements and tests it:

- **2.1 machine.json**: shape, `schema`/`layout`/`generation`, unknown fields kept: Task 3 (`Registry`, `TestUpdateRegistryBumpsGenerationAndKeepsUnknownFields`). Written only under `machine.lock` (`writeRegistry` takes `*Lock`) by temp plus rename with the Windows retry (`atomicfile`): Task 3. Every write increments `generation`: `UpdateRegistry` (Task 3); M8 writes 1, a rebuild writes Unix nanoseconds (Tasks 6, 10). Readers unlocked: `ReadRegistry`, `Inspect`. Missing (on a migrated layout), unreadable or malformed fails closed for every command except `doctor`, `version`, `help` with `machine-state: ...; run incoda doctor`: Tasks 3, 6, 8 (`TestBrokenRegistryFailsClosed`, `TestLostRegistryFailsClosedAndKeepsTheFence`). Newer `schema` or `layout`: `newerError` naming this binary (Task 3, `TestNewerRegistryFailsClosed`). Nothing re-bootstraps (Task 6 row 9). Pools usable directly from the commit: `TestPoolUsableDirectly` (Task 8).
- **2.3 fence**: lanes under `lanes/` (Task 1); `queues` a regular file with the exact constant (Task 4, `TestFenceTextIsTheSpecConstant`); this binary never creates `queues/` (Task 1 `stateDir`, `OpenIn` modes; Task 8 `TestReadOnlyCommandsNeverMigrate`); old binaries stop with exit 122 on every key, with and without `INCODA_HELD` (Task 11). Re-fence: `lstat`, only with a valid `machine.json`, only from `run` and `config` (the commands of 2.3 that exist in 2a), under `machine.lock` with a re-check, `queues/` to `strays/<unix-nanos>/`, M4 race rule, `event=refence` (Tasks 4, 6, 8: `TestEnsureRefencesAMigratedLayout`, `TestRunReplacesAMissingFence`). `status`, `watch`, `queues`, `kill`, `doctor` never re-fence and never take `machine.lock` (Task 8 `readState`, Task 10 doctor test "fence missing").
- **3.1 machine.lock**: TryLock plus poll, budget floor 2s, never `Lock()`, note with `pid op since` and `blockers`, re-read on each poll, waiting line once then every 60s, `machine-lock-timeout: held by pid N (<op>)`, upgrade-blocked waiters via the parent chain: Task 2 (unit) and Task 8 (`TestMigrationWaitsForALiveOldTicket`), Task 11 (`TestBlockedWaiterExitsUpgradeBlocked`). Lock order machine.lock, registry, ticket: Global Constraints; `ProbeLane` takes registry locks only under `machine.lock`; `Rebuild` takes registry locks in key order (Task 10). The migrator keeps `machine.lock` across its idle wait (Task 5).
- **3.2 first paragraph**: `run` and `config` migrate when `machine.json` is absent; `status`, `watch`, `queues` show the old layout read only with both banner texts; `doctor` reports and never migrates: Tasks 6 (`banner`), 8, 10.
- **3.3 M0**: Task 7. **M1**: Task 6 (`runMigration`, `beginMigration`). **M2**: Task 5, wired in Task 6, real old binaries in Task 11. **M3**: Task 6 `writePlan`, recomputed each time. **M4**: both swap paths (Task 4 `exchange` per OS, Task 6 `fenceMigration`), fallback on `EINVAL`/`ENOTSUP` (and `ENOSYS` on linux) and on Windows, Windows rename failure back to M2 (`TestM4NotIdleGoesBackToM2`), the race loop decided by `lstat` with the 100-try limit and its exact message (Task 4), the empty-dir path (Task 6). **M5**: Task 5, wired in Task 6, slip-in with a real v0.6.0 in Task 11. **M6**: Task 6 `mergeStrays`. **M7**: Task 6 `applyBootstrap`, malformed configs never rewritten (`TestEnsureKeepsAPoolLaneSlotsAndRegistersAMalformedPool`). **M8**: Task 6, counts computed by `summarize`. The exact `upgrade-wait`, `upgrade-timeout` and `upgrade-blocked` texts: Task 5. Every recovery row: hand-built in Task 6 (`TestRecoveryRows`, `TestRecoveryCommittedDeletesThePlan`, `TestRecoveryRegistryLostFailsClosedAndKeepsTheFence`), crash-injected against the real binary in Task 9 (rows 1, 2, 3, 4, 5, 7, 8; rows 6 and 9 by hand, as no step order produces them).
- **3.5 bootstrap content**: `builds`, `computer-use`, `tests`, `vm`, `slots: 1` unless an existing lane sets `slots`, nothing linked: Task 6.
- **3.6**: fail-closed registry (Tasks 3, 6, 8); `doctor --rebuild-registry` in the specified order with `kind-busy:` refusals and `generation` set to Unix nanoseconds: Task 10.
- **5.5 (2a part)**: layout and `machine.json` state, unfinished migration, fence present or missing, unreadable lane configs, `INCODA_DIR`, exit 0 with `attention:` lines or 122: Task 10.
- **Probes reuse plan 1's primitive**: `lane.ProbeTicket` and `lane.ProbeLane` (Task 1); `held` calls `ProbeTicket`.
- **Carried forward from plan 1**: no build cut before the fence (Global Constraints; lifted when Task 8 lands).

Left to plan 2b: counting live stray holders for admission and the `unpooled run by an older incoda` busy lines; stray cleanup on re-fence, on doctor and on acquisition polls; orphan records (written by old-holder kill, counted in M2, M5 and by acquisitions; `findBlockers` is where they join); the old-holder kill of 3.2 (SIGSTOP walk, `--force` on strays and on `lanes/` before `machine.json`, the `kill: pid N is an older incoda...` refusal); `force-release --live` refused while `machine.json` is absent (`upgrade-pending:`); the `stopped holder:` lines; doctor's PATH version probe, strays, orphans and stopped holders; the status warning lines for a missing fence and live strays (5.3).

Left to plan 3: kind enforcement and linking (`unlinked:` refusals, `--pool`, `link`, `init`, `pools add|remove`, which also become mutating commands that call `Ensure` and re-fence); printing suggestions (the `Suggest` table exists so M8 can count); `Enroll` failing closed on `NewerSchemaError`.
