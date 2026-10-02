# System pools, plan 3a: Pools at top level for run

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A top-level `run` takes its project lanes and then the pools they link, one at a time in one total order; it refuses an unlinked project lane with its suggestion, makes a first link only to that suggestion (`--pool`), verifies its plan after each enroll and before its child and replans on a change; and the user-side link writes of `config` exist so the printed fixes can be run. It also lands every plan 1 and plan 2b item the index routes to plan 3.

**Architecture:** A new package `internal/runplan` computes a run's plan from `machine.json` and the lanes' `config.json` files without opening any lane: the lane set (named keys plus each named project key's linked pools, deduplicated), the total order (project lanes by key, then pools by key), the rules of spec 4.5 with the pool path in their texts, the unlinked, `pool-mismatch:` and `link-needs-user:` refusals with their fix lines, and the verify check (`Plan.Changed`). A new package `internal/fixline` renders every printed command (POSIX sh or PowerShell, carried flags, no placeholders). `machine.WriteLink` writes a link as a compare-and-set under `machine.lock` and the lane's registry lock; `config` and `run`'s first link use it. `cli.cmdRun` becomes a plan, enroll, verify loop around a `takeLane` closure. `lane.Queue` handles carry their caller's `--wait` budget so no registry lock wait outlives it.

**Tech Stack:** Go 1.27, `golang.org/x/sys`, standard library. Tests: `go test`, integration tests that build and run the real binary (and the `incoda_crashpoints` build and the v0.2.0 and v0.6.0 tags where plan 2a and 2b tests already do), in-process `cli.Main` tests with test seams, and a new stub program `internal/testprog/argv`.

**Spec:** `docs/superpowers/specs/2026-10-01-system-pools-design.md` (sections 2.2, 2.4, 2.5 except trigger 3, 2.7 `via` and `wait`, 3.5 suggestions in refusals, 4.1, 4.2, 4.3 `config` link flags, 4.4, 4.5, 4.6 for config text, 5.1 for run's texts, 2.6 "Fix lines" as a shared builder). Plan index: `docs/superpowers/plans/2026-10-01-system-pools-00-index.md`. Plan 2b (`...-02b-strays-kill-doctor.md`) has landed (`6462533..5326208`, follow-ups carried in `21ed98f`). Plan 3b (`...-03b-pools-setup.md`) follows this plan: kind changes, `pools`, `link`, `init`, quiet-machine and `incoda help`.

## Global Constraints

- Go 1.27.0 or newer; no new module dependencies (standard library, `golang.org/x/sys`, the existing charm libraries only).
- `just ci` must pass at the end of every task: `gofmt` no-op, `go mod tidy` no-op, `go vet ./...` and `go vet -tags incoda_crashpoints ./...`, `go test -race ./...` (plain `go test` on Windows).
- The Windows and Linux builds must keep compiling: before each commit run `GOOS=windows go vet ./...`, `GOOS=windows go vet -tags incoda_crashpoints ./...`, `GOOS=linux go vet ./...` and `GOOS=linux go vet -tags incoda_crashpoints ./...`.
- Plain prose in comments, docs, printed texts and commit messages: no em dashes or en dashes, no emoji. Beware `gofmt`'s doc-comment rewriting: two single quotes in a doc comment become a typographic quote, so never write them there (write "a single quote" instead).
- Commit messages carry no `Co-Authored-By` or other AI attribution lines.
- Exit codes unchanged: 120 usage and refusals, 121 timeout, 122 state, 123 spawn, 124 killed, 125 kill pending, 130 interrupt.
- Work on branch `feat/system-pools`. No build from this branch is released, installed on PATH or handed to anyone before plan 5 (plan index): after plan 3a every project lane refuses runs until it is linked.
- Layout knowledge stays in `internal/lane/statedir.go` (`LanesDir`, `LaneDir`, `QueuesDir`) plus `machine.StraysDir` and `machine.OrphansDir`. This binary never creates `<state>/queues` as a directory. `machine.json` is written only by `machine.UpdateRegistry` (or `writeRegistry` in the migration and rebuild) under a held `machine.lock`.
- Kind comes from `machine.json` alone (`Registry.IsPool`), never from a lane's config. Pools carry no `pools` and no `quiet_machine` (spec 2.2).
- Lock order (spec 3.1): `machine.lock`, then lane registry locks (several only in key order), then ticket locks (non-blocking only). Links and kind changes take `machine.lock` first, then the one lane's registry lock. A run takes `machine.lock` only before it holds any ticket: for migration, a re-fence, or a first link; a replan releases every ticket before the next plan. Nothing holds a lock while a person answers a question.
- No registry lock wait of this binary is unbounded except under a negative `--wait`: every lane operation waits through `LockBy` within its handle's budget (`Queue.SetBudget`, plan 3a Task 1) or a fixed bound; probes keep plan 2b's deadlines.
- Planning (`internal/runplan`) reads `machine.json` and `config.json` files only: it never opens, creates or locks a lane, so a refused run leaves no directory, ticket or log behind (spec 4.1).
- `run` never reads a terminal (spec 4.1). `link` and `init` read one only through the `openTerminal` seam (plan 3b Task 3).
- Every string that came from state, a file, argv, another process or the environment is printed through `textsafe.Escape` (keys are validated and print bare); in `lane.log` such values go through `textsafe.LogValue`. Descriptions and closed texts are checked with `textsafe.CheckWrite` before any write (spec 4.6).
- Every printed run, config or kill command is built by `internal/fixline` (plan 3a Task 2): POSIX sh on Unix, PowerShell on Windows, no placeholders.
- Test safety rules, binding on every test this plan adds or changes:
  - Never invoke an incoda found on PATH, not even `incoda version`. Tests build their own binaries (`binaries`, `oldBinary`, `crashBinary`) and run them by absolute path. The one test that pastes a printed line into a shell (`TestPrintedUnlinkedLineRunsAsPrinted`) sets PATH to the test build's directory followed by `/usr/bin:/bin` only.
  - Every invocation of any incoda, old or new, gets `INCODA_DIR` set to a `t.TempDir()` through `laneEnv` (or `doctorEnv`); in-process tests (`internal/cli`) set it with `t.Setenv`.
  - Never read or write `~/Library/Application Support/incoda` (or the platform default state directory). Before the first task and after the last, record that directory's modification time and its number of lanes (`stat -f %m`, `ls .../queues | wc -l` or `.../lanes`) and check they are unchanged.
  - Run every command with bash, not zsh. Never use `git stash` or `git worktree`.
  - Interactive commands (`link`, `init`) are tested only through the injected terminal (`fakeTerminal`), never by reading the real terminal; the end of the injected input cancels, so no test can wait for input. Through the binary they are tested only for their `needs-terminal:` refusal (the test's stdin is not a terminal).
  - A test that runs a new binary on a project key links that key first (`linkTestKeys`, plan 3a Task 3); a test whose run is the command that migrates a state directory runs on a pool (`builds`), since no link can exist before the migration.
  - Old-binary tests need the tags `v0.2.0` and `v0.6.0` in the checkout (`oldBinary` skips with a message otherwise). Kill tests keep plan 2b's process-group rules (`startInGroup`, `runnerSentinel`).
- Pre-existing timing-sensitive tests fail on a loaded machine without any change of this plan: `TestDisagreeingSlotsRefusedAtEnrollAfterConfigChange`, `TestFIFOOrder` under a loaded `-race` suite, and `internal/sysinfo` `TestReadCPUDarwin` (it failed once during this plan's validation on an unchanged tree). If one of these alone fails `just ci`, rerun it in isolation before debugging this plan's changes. On a heavily loaded machine (load average several times the core count) these were also seen failing by timing during this plan's validation, each on one run only and passing alone: `TestMultiKeyAcquiresEveryQueue` and `TestClosedWhileWaiting` (a short holder ends before the next process starts), `TestMigrationWaitsForAnOldRun` (the 1.5s old run ends before the migration looks), `TestDoctorProbesEveryIncodaOnPath` (its 15s wall-clock bound), and `TestMigrationWaitsForALiveOldTicket` (a pre-existing race: `status` can read the `machine.lock` note while the migrator rewrites it by truncate and write, and then prints the not-upgraded banner).

## Decisions this plan makes where the spec leaves room

- **Bounded registry waits (carried from plan 2b).** A `lane.Queue` handle carries its caller's budget (`SetBudget(start, wait)`). Every registry lock wait polls `TryLock` until the budget ends, with a 2s floor (as `machine.lock`, spec 3.1: normal holds last microseconds); `Position` waits at most `PollProbeWait` per poll, and a poll it cannot read admits nobody; `Release` waits 2s and then frees the ticket without the registry lock (closing the ticket lock frees the slot; a scanner that listed the name finds it gone or unlocked); a handle with no budget waits 5s; a negative `--wait` keeps the blocking `Lock()` except per poll. A run whose Enroll cannot get the lock within its budget exits 121 `cannot enter queue "K" within --wait: registry lock: cannot tell: ...`; `config` and `force-release` exit 122.
- **Strays on a project key (carried from plan 2b).** Decided: an unpooled holder (stray or orphan record) on project key K counts as one held slot on lane K itself as well as on the pools `ChargedPools` names, since the older incoda that held K used K's own width (the safe direction; spec 2.3 names only the pools). Every acquisition poll rescans, on project lanes too.
- **Kinds.** A pool is a lane in `machine.json` `pools`; `config` on a pool may set slots, description, closed and require_reason, and refuses link flags (`pool-mismatch: "builds" is a pool; a pool never links other pools and carries no quiet_machine`).
- **Role texts (spec 5.1).** Project-lane lines are byte-identical. A pool's role is `pool, via K1,K2` when links bring it, `pool` when it is only named directly (and, with plan 3b, `pool, quiet-machine` when only quiet-machine brings it). The busy line puts it before the slot count (`busy (pool, via cap-gate; 1 slot(s), ...)`), the 121 line after the key (`queue "tests" (pool, via cap-gate) still busy after ...`) and sends the caller to the first via key's status, the acquired line reads `acquired queue "tests" (pool, via cap-gate; pid N)`, and holder lines of pool tickets end ` via <keys>`. Closed and require_reason refusals append ` (<role>)` for pools, as spec 4.5 shows for linked pools; a pool named directly says `(pool)`.
- **`--slots` on a named pool** is checked against the pool's configured count, 1 when unset (spec 3.3 M7: a pool without a config reads as one slot); pool tickets carry `slots` 0 and Enroll stamps the configured count.
- **`wait` on tickets** is the `--wait` text as given and is omitted when the caller gave none (plan 4's rebuilt line then carries no `--wait`, so the default applies).
- **Fix lines.** On PowerShell keys are quoted too (a bare comma there builds an array, which reaches the program as several words) and `--` prints as `'--'`. A top-level line repeats every flag the caller gave except `--queue`, `--pool` and `--pools`, in flag-name order, `--wait` as given. When no runnable line can be printed the lead is `no runnable command (<why>); run it with these fields:` (plan 4 passes `rerun the outer job`), and the fields are `queue:`, `pool:` (when the line sets one), `flags:` (`(none)` when empty), `reason:` (when given), `cwd:` and `command:`. A lane in the line that requires a reason the run lacks rules the line out.
- **Unlinked refusal details.** Descriptions in the pool rows are cut to 60 characters as 57 characters plus `...` (the spec's example description is 64 characters, so its row is cut). A suggestion is usable only when every pool it names is registered; otherwise it prints `suggested: none (the suggested pools ... are not all pools on this machine)` and asks the user. When the unlinked key is one of several named project keys, the fix is `incoda config KEY --pool <suggestion>` and then the run line without `--pool` (a `--pool` would apply to every named project key). A quiet suggestion's line adds `--wait '5m'` when the caller gave no `--wait` and says why on the next line: `(--wait 5m added: quiet-machine holds every pool it has drained while it waits for the rest)`. Several unlinked keys: `unlinked: a, b`, `suggested: a -> tests (name matches *-gate); b -> builds (...)`, one config line each, the run line, and `if a suggestion does not fit, ask the user; they run: incoda link a, incoda link b`.
- **`--pool` refusals.** `link-needs-user:` prints `run it with the suggestion instead (stored; every later run on this queue takes these pools):` and the line before `ask the user for anything else; they run: incoda link K`; a suggestion naming a pool this machine lacks reads `has no usable suggestion (...)`. `pool-mismatch:` names only the pools outside the link; its line names them next to the queue and keeps the part of `--pool` the link allows. A `--pool` naming an unregistered pool is `pool-mismatch: "x" is not a pool on this machine (pools: ...)`. With several project keys, the first refused key in key order is reported.
- **link-conflict** names the winner by the pid of the last `event=link` line in the lane's `lane.log`, else `by another process`.
- **`linked:` and `replan:`** are printed even with `--quiet`: the first records a stored change, the second a lost FIFO place. `--pool ignored for pool key` is ordinary chatter (`--quiet` hides it).
- **Verify (spec 2.5).** Trigger 1 is looked for only when the generation moved; trigger 2 does not fire while the run's `--pool` subset is still part of the new link. A `machine.json` that disappears mid-run fails closed (122). Every pool the plan reaches through a link must still resolve at each verify point (122 otherwise). The replan line is `replan: <what>` with `<what>` one of `pool "K" left the registry`, `queue "K" became a pool`, `the link of "K" changed: a -> b` (and, with plan 3b, `the pools on this machine changed: a -> b`); `event=replan why=<what>` goes into every lane the run had enrolled.
- **`config` details.** `config KEY` with no setting flag reads the config without creating the lane. Its listing adds `kind:`, and for a project lane `pools:` and `quiet machine:`, after every existing line. `--remove-pool` on an unlinked key is the `already unlinked:` no-op; removing the last pool is refused (exit 120) with `--remove-pool would leave "K" linked to no pool; use --unlink to remove the link`. `--pool` with `--add-pool`, `--remove-pool` or `--unlink`, `--unlink` with `--add-pool` or `--remove-pool`, and `--replace` without `--pool` are usage errors. The `link:` echo goes to stdout; `already linked:` and `already unlinked:` to stderr, exit 0. `--quiet-machine` also takes `machine.lock` (it is part of a project lane's link to the pools, and only a project lane may carry it).
- **Test harness.** `linkTestKeys` widens the `vm` pool to 64 slots and links each test key to it, so a test about one lane is never serialised by a pool it did not mean to test.

## File structure

| File | Responsibility |
|---|---|
| `internal/lane/queue.go`, `acquire.go` | `SetBudget`, bounded registry waits, `ErrRegistryBusy` read as a busy poll; Enroll's closed, reason and newer checks; `AcquireOptions.Check`; `via=` in the enqueue log |
| `internal/lane/config.go` | `ErrNoChange`, `ClosedError`, `ReasonRequiredError` |
| `internal/lane/ticket.go` | `Ticket.Via`, `Ticket.Wait` |
| `internal/fixline/fixline.go` | the fix-line builder (spec 2.6) |
| `internal/testprog/argv/main.go` | stub that records argv and directory for the reproduction test |
| `internal/machine/link.go` | set helpers, `NotAPool`, `WriteLink`, `LastLinker`, `LinkedLine` |
| `internal/machine/killtarget.go` | `KillLine` quotes with `fixline.Quote` |
| `internal/machine/unpooled.go` | `ChargedTo` answers for project keys too |
| `internal/runplan/runplan.go` | `Request`, `Lane`, `Plan`, `Less`, `Make`, `Changed` |
| `internal/runplan/refusals.go` | `Suggest`, `PoolRows`, the unlinked, link-needs-user and pool-mismatch refusals |
| `internal/cli/linkflags.go` | `poolsValue`, `linkEdit` |
| `internal/cli/config.go` | link flags, text check table, listing |
| `internal/cli/run.go` | the plan, enroll, verify loop; first links; role texts; seams |
| `internal/cli/kill.go`, `misc.go` | bounded kill request and force-release |
| root `pools_test.go` (new), `config_test.go`, `busyregistry_test.go`, `strays_test.go` and the adapted tests | integration tests |


---

### Task 1: Registry lock waits stay inside the caller's budget

Plan 2b's first carried item. Every lane operation of this binary took the registry lock with the blocking `Lock()`: Enroll, Release, Position, MarkAcquired, `RequestKill`, `WaitGone`, `ForceRelease`, `UpdateConfig` and `SaveConfig` (through `withRegistry`) and the rebuild's `LockAll`. A run of this binary stopped with Ctrl-Z inside a registry hold keeps that lock, and every other run, kill, config or force-release on the lane then hung past its `--wait`. Each `Queue` handle now carries the caller's budget (`SetBudget`), and every wait polls `TryLock` (`LockBy`) until the budget's end, never less than a 2s floor; `Position` waits at most `PollProbeWait` per poll so a waiting run keeps checking its interrupt and kill file; `Release` frees the ticket without the registry lock when it cannot get it within 2s. A handle without a budget (force-release, the watch screen's killer) waits at most 5s.

**Files:**
- Modify: `internal/cli/config.go`
- Modify: `internal/cli/kill.go`
- Modify: `internal/cli/misc.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/lane/acquire.go`
- Modify: `internal/lane/queue.go`
- Modify: `internal/machine/rebuild.go`
- Test: `busyregistry_test.go`
- Test: `internal/lane/busy_test.go`
- Test: `internal/lane/layout_test.go`

**Interfaces:**
- Consumes: `lane.LockBy`, `lane.PollProbeWait`, `lane.ErrRegistryBusy` (plan 2b); `machine.lockDeadline` (plan 2a).
- Produces: `func (q *Queue) SetBudget(start time.Time, wait time.Duration)`; unexported `registryDeadline(limit time.Duration) time.Time`, `lockRegistry(limit)`, `withRegistryLimit(limit, fn)` and the variables `defaultRegistryWait` (5s), `releaseRegistryWait` (2s), `registryFloor` (2s, tests shorten it); `func LockAll(qs []*Queue, deadline time.Time) (func(), error)` (zero deadline: as long as it takes). `Acquire` reads `ErrRegistryBusy` from `Position` as a poll that admits nobody. `run` maps a busy registry at Enroll to exit 121 `cannot enter queue "K" within --wait: ...`; `force-release` maps it to 122.

- [ ] **Step 1: Write the failing tests**

`TestRegistryWaitsAreBounded` holds the registry lock in a helper process (the `holdLockElsewhere` helper already in `busy_test.go`) and times every operation; `TestOwnLaneOperationsAreBoundedByTheirWait` does the same through the binary on the `builds` pool (a pool needs no link, so this test keeps working after Task 7).

In `internal/lane/busy_test.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 2) Replace:

```go

import (
	"bufio"
	"errors"
	"fmt"
	"io"
```

with:

```go

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
```

(2 of 2) Replace:

```go
	}
}

func TestProbeDeadline(t *testing.T) {
	now := time.Now()
	if d := ProbeDeadline(time.Time{}, time.Second); d.Sub(now) < 900*time.Millisecond || d.Sub(now) > 2*time.Second {
```

with:

```go
	}
}

// TestRegistryWaitsAreBounded: every lane operation of this binary waits
// for a registry lock another process keeps (a stopped incoda) only within
// the handle's budget, never for ever. Enroll, Position, RequestKill,
// WaitGone, ForceRelease, UpdateConfig, SaveConfig and LockAll fail with
// ErrRegistryBusy; MarkAcquired gives up silently; Release frees the
// ticket without the lock; Acquire reads a busy poll as "not admitted" and
// times out within its wait.
func TestRegistryWaitsAreBounded(t *testing.T) {
	saved := registryFloor
	registryFloor = 100 * time.Millisecond
	defer func() { registryFloor = saved }()
	root := t.TempDir()
	q, err := OpenIn(root, "bounded", Create)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	en, err := q.Enroll(Ticket{Slots: 1, Command: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	holdLockElsewhere(t, RegistryLockPath(q.Dir))
	q.SetBudget(time.Now(), 200*time.Millisecond)
	const limit = 1500 * time.Millisecond
	busy := func(what string, fn func() error) {
		t.Helper()
		within(t, what, limit, func() {
			if err := fn(); !errors.Is(err, ErrRegistryBusy) {
				t.Fatalf("%s: want ErrRegistryBusy, got %v", what, err)
			}
		})
	}
	busy("Enroll", func() error { _, err := q.Enroll(Ticket{Slots: 1}); return err })
	busy("Position", func() error { _, _, _, err := en.Position(); return err })
	busy("RequestKill", func() error { _, err := q.RequestKill(os.Getpid(), KillRequest{By: "t", Reason: "r"}); return err })
	busy("WaitGone", func() error { _, err := q.WaitGone(os.Getpid(), 0, 10*time.Millisecond); return err })
	busy("ForceRelease", func() error { _, err := q.ForceRelease(true); return err })
	busy("UpdateConfig", func() error { _, err := q.UpdateConfig(func(*Config) error { return nil }); return err })
	busy("SaveConfig", func() error { return q.SaveConfig(Config{Slots: 2}) })
	busy("LockAll", func() error { _, err := LockAll([]*Queue{q}, time.Now().Add(100*time.Millisecond)); return err })
	within(t, "MarkAcquired", limit, func() { en.MarkAcquired() })
	within(t, "Acquire", limit+time.Second, func() {
		if err := en.Acquire(context.Background(), AcquireOptions{Wait: 300 * time.Millisecond, Poll: 20 * time.Millisecond}); !errors.Is(err, ErrTimeout) {
			t.Fatalf("Acquire must time out within its wait: %v", err)
		}
	})
	within(t, "Release", releaseRegistryWait+time.Second, func() { en.Release(0) })
	if _, err := os.Stat(TicketFilePath(q.Dir, en.Name())); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Release must remove the ticket without the lock: %v", err)
	}
}

// TestRegistryDeadline: the budget end, capped by limit, floored by
// registryFloor; no budget gives defaultRegistryWait; a negative wait has
// no deadline unless a limit caps it.
func TestRegistryDeadline(t *testing.T) {
	q := &Queue{}
	near := func(got time.Time, want time.Duration) bool {
		d := time.Until(got)
		return d > want-200*time.Millisecond && d <= want+50*time.Millisecond
	}
	if dl := q.registryDeadline(0); !near(dl, defaultRegistryWait) {
		t.Fatalf("no budget: %s", time.Until(dl))
	}
	q.SetBudget(time.Now(), 10*time.Second)
	if dl := q.registryDeadline(0); !near(dl, 10*time.Second) {
		t.Fatalf("budget end: %s", time.Until(dl))
	}
	if dl := q.registryDeadline(PollProbeWait); !near(dl, registryFloor) {
		t.Fatalf("a limit below the floor gets the floor: %s", time.Until(dl))
	}
	q.SetBudget(time.Now().Add(-time.Hour), time.Minute)
	if dl := q.registryDeadline(0); !near(dl, registryFloor) {
		t.Fatalf("a spent budget gets the floor: %s", time.Until(dl))
	}
	q.SetBudget(time.Now(), -1)
	if dl := q.registryDeadline(0); !dl.IsZero() {
		t.Fatalf("a negative wait has no deadline: %s", time.Until(dl))
	}
	if dl := q.registryDeadline(3 * time.Second); !near(dl, 3*time.Second) {
		t.Fatalf("a limit caps a negative wait: %s", time.Until(dl))
	}
}

func TestProbeDeadline(t *testing.T) {
	now := time.Now()
	if d := ProbeDeadline(time.Time{}, time.Second); d.Sub(now) < 900*time.Millisecond || d.Sub(now) > 2*time.Second {
```

In `internal/lane/layout_test.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 2) Replace:

```go
	"os"
	"path/filepath"
	"testing"
)

// treeSnapshot maps every path under root to its mode, size and
```

with:

```go
	"os"
	"path/filepath"
	"testing"
	"time"
)

// treeSnapshot maps every path under root to its mode, size and
```

(2 of 2) Replace:

```go
		t.Fatal(err)
	}
	defer en.Release(0)
	unlock, err := LockAll([]*Queue{a, b})
	if err != nil {
		t.Fatal(err)
	}
```

with:

```go
		t.Fatal(err)
	}
	defer en.Release(0)
	unlock, err := LockAll([]*Queue{a, b}, time.Now().Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
```

In `busyregistry_test.go`, replace:

```go
	}
}

// TestRegistryLockHeldElsewhereNeverHangs: a registry lock another process
// keeps for ever (a stray's, and a lane's of this layout) costs status,
// doctor, kill and a pool run at most their bounds. status and doctor name
```

with:

```go
	}
}

// TestOwnLaneOperationsAreBoundedByTheirWait: a registry lock another
// process keeps on a lane of this layout (a stopped incoda of this binary)
// costs a run, a config and a force-release on that lane at most their
// --wait plus the 2s floor: the run times out with 121, config and
// force-release fail closed with 122, and none of them hangs.
func TestOwnLaneOperationsAreBoundedByTheirWait(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "seed"); code != 0 {
		t.Fatalf("migrate: %d\n%s", code, out)
	}
	holdRegistryElsewhere(t, laneDir(state, "builds"))
	const cannot = "cannot tell: registry lock held by another process"
	timed := func(limit time.Duration, args ...string) (string, int) {
		t.Helper()
		start := time.Now()
		out, code := runIncoda(t, incoda, state, args...)
		if d := time.Since(start); d > limit {
			t.Fatalf("incoda %s took %s:\n%s", strings.Join(args, " "), d, out)
		}
		return out, code
	}
	out, code := timed(8*time.Second, "run", "--queue", "builds", "--wait", "1s", "--poll", "50ms", "--",
		stamp, filepath.Join(t.TempDir(), "s.txt"), "x", "1")
	if code != 121 || !strings.Contains(out, `cannot enter queue "builds" within --wait: registry lock: `+cannot) {
		t.Fatalf("run must time out within its wait: %d\n%s", code, out)
	}
	out, code = timed(8*time.Second, "config", "builds", "--slots", "2", "--wait", "1s")
	if code != 122 || !strings.Contains(out, cannot) {
		t.Fatalf("config must fail closed within its wait: %d\n%s", code, out)
	}
	out, code = timed(10*time.Second, "force-release", "--queue", "builds")
	if code != 122 || !strings.Contains(out, cannot) {
		t.Fatalf("force-release must fail closed within its bound: %d\n%s", code, out)
	}
}

// TestRegistryLockHeldElsewhereNeverHangs: a registry lock another process
// keeps for ever (a stray's, and a lane's of this layout) costs status,
// doctor, kill and a pool run at most their bounds. status and doctor name
```

- [ ] **Step 2: Run the tests to see them fail**

Run (bash), one at a time:

- `go test ./internal/lane/ -run 'TestRegistryWaitsAreBounded|TestRegistryDeadline|TestLockAllAndLiveLocked' -count=1 -timeout 120s`
- `go test . -run 'TestOwnLaneOperationsAreBoundedByTheirWait' -count=1 -timeout 120s`

Expected: the `internal/lane` test build fails (`undefined: registryFloor`, `q.SetBudget undefined (type *Queue has no field or method SetBudget)`, `too many arguments in call to LockAll`); the root test never finishes, because the run waits for the held registry lock for ever, so `-timeout` ends it with `panic: test timed out after 2m0s` naming `TestOwnLaneOperationsAreBoundedByTheirWait`.

- [ ] **Step 3: Implement**

The budget lives on the handle, so no caller signature changes except `LockAll`. A negative `--wait` keeps the blocking `Lock()` for operations without a per-poll limit: the caller asked to wait for ever.

In `internal/cli/config.go`, replace:

```go
		return exitWith(ExitState, "%v", err)
	}
	defer q.Close()

	apply := func(cfg *lane.Config) bool {
		changed := false
```

with:

```go
		return exitWith(ExitState, "%v", err)
	}
	defer q.Close()
	q.SetBudget(start, wait.d)

	apply := func(cfg *lane.Config) bool {
		changed := false
```

In `internal/cli/kill.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 2) Replace:

```go
// why, takes its job tree down and exits 124. --force is for the participant
// that never answers.
func cmdKill(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("kill", stderr)
	queue := fs.String("queue", "", "queue key (defaults to $INCODA_QUEUE)")
	pid := fs.Int("pid", 0, "pid of the holder or waiter, as status shows it")
```

with:

```go
// why, takes its job tree down and exits 124. --force is for the participant
// that never answers.
func cmdKill(args []string, stdout, stderr io.Writer) error {
	start := time.Now()
	fs := newFlagSet("kill", stderr)
	queue := fs.String("queue", "", "queue key (defaults to $INCODA_QUEUE)")
	pid := fs.Int("pid", 0, "pid of the holder or waiter, as status shows it")
```

(2 of 2) Replace:

```go
		return exitWith(ExitState, "%v", err)
	}
	defer q.Close()

	entry, err := q.RequestKill(*pid, req)
	if errors.Is(err, lane.ErrNoParticipant) {
```

with:

```go
		return exitWith(ExitState, "%v", err)
	}
	defer q.Close()
	// The request and the checks after it wait for the registry lock only
	// within --wait (plus the floor), so a stopped incoda keeping that lock
	// cannot hang kill.
	q.SetBudget(start, *wait)

	entry, err := q.RequestKill(*pid, req)
	if errors.Is(err, lane.ErrNoParticipant) {
```

In `internal/cli/misc.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 2) Replace:

```go
package cli

import (
	"flag"
	"fmt"
	"io"
```

with:

```go
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
```

(2 of 2) Replace:

```go
	}
	defer q.Close()
	removed, err := q.ForceRelease(*live)
	if err != nil {
		return exitWith(ExitUsage, "%v", err)
	}
```

with:

```go
	}
	defer q.Close()
	removed, err := q.ForceRelease(*live)
	if errors.Is(err, lane.ErrRegistryBusy) {
		return exitWith(ExitState, "queue %q: %v", key, err)
	}
	if err != nil {
		return exitWith(ExitUsage, "%v", err)
	}
```

In `internal/cli/run.go`, make these 3 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 3) Replace:

```go
		if err != nil {
			return exitWith(ExitState, "%v", err)
		}
		pt := &lanePart{key: key, q: q}
		parts = append(parts, pt)
		cfg, err := q.LoadConfig()
```

with:

```go
		if err != nil {
			return exitWith(ExitState, "%v", err)
		}
		// Every registry lock wait of this run stays inside its --wait
		// budget: a stopped incoda keeping a registry lock costs this run
		// its budget, never more.
		q.SetBudget(start, wait.d)
		pt := &lanePart{key: key, q: q}
		parts = append(parts, pt)
		cfg, err := q.LoadConfig()
```

(2 of 3) Replace:

```go
				rc = ExitUsage
				return usagef("%v", err)
			}
			rc = ExitState
			return exitWith(ExitState, "cannot enter queue %q: %v", pt.key, err)
		}
```

with:

```go
				rc = ExitUsage
				return usagef("%v", err)
			}
			if errors.Is(err, lane.ErrRegistryBusy) {
				rc = ExitTimeout
				return exitWith(ExitTimeout, "cannot enter queue %q within --wait: %v. Check `incoda status --queue %s`. Do NOT bypass the lane; surface the wait and coordinate instead", pt.key, err, pt.key)
			}
			rc = ExitState
			return exitWith(ExitState, "cannot enter queue %q: %v", pt.key, err)
		}
```

(3 of 3) Replace:

```go
			Unpooled: countUnpooled,
			OnWait: func(pos, effSlots int, live []lane.Entry, waited time.Duration) {
				if *quiet {
					return
				}
				ahead := pos
```

with:

```go
			Unpooled: countUnpooled,
			OnWait: func(pos, effSlots int, live []lane.Entry, waited time.Duration) {
				if *quiet {
					return
				}
				if live == nil && effSlots == 0 {
					// The poll could not read the lane: another process
					// kept its registry lock (a stopped incoda).
					fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
						p.Yellow(fmt.Sprintf("queue %q busy (%s), waited %s%s",
							key, lane.ErrRegistryBusy, waited.Round(time.Second), waitBudget(wait.d))))
					return
				}
				ahead := pos
```

In `internal/lane/acquire.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 2) Replace:

```go

import (
	"context"
	"time"
)

```

with:

```go

import (
	"context"
	"errors"
	"time"
)

```

(2 of 2) Replace:

```go
			}
		}
		idx, slots, live, err := e.Position()
		if err != nil {
			return err
		}
```

with:

```go
			}
		}
		idx, slots, live, err := e.Position()
		if errors.Is(err, ErrRegistryBusy) {
			// Another process kept the registry lock past this poll's
			// bound (a stopped incoda): nobody can tell who holds the
			// lane, so this poll admits nobody and the wait goes on
			// within its budget. OnWait sees no live set.
			idx, slots, live, err = -1, 0, nil, nil
		}
		if err != nil {
			return err
		}
```

In `internal/lane/queue.go`, make these 6 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 6) Replace:

```go
	Dir      string
	registry *lockfile.File
	readOnly bool
}

// Mode says what a Queue handle may create or change.
```

with:

```go
	Dir      string
	registry *lockfile.File
	readOnly bool
	// budget bounds every wait for the registry lock (SetBudget).
	budget budget
}

// budget is a caller's --wait budget: it runs from start for wait, and a
// negative wait never ends. The zero budget is unset.
type budget struct {
	start time.Time
	wait  time.Duration
	set   bool
}

// Registry lock bounds for this binary's own lane operations. A registry
// hold lasts microseconds; only a holder stopped inside one (Ctrl-Z, a
// debugger, a kill window never resumed) keeps it longer, and such a holder
// must cost a run, a kill or a config at most its own --wait, never hang it.
// Every wait polls TryLock (LockBy); none blocks in the kernel except a
// handle whose budget never ends (a negative --wait).
var (
	// defaultRegistryWait bounds a registry lock wait of a handle that has
	// no budget: force-release, the watch screen's killer, tests.
	defaultRegistryWait = 5 * time.Second
	// releaseRegistryWait bounds Release's wait. Past it Release goes on
	// without the lock (see Release).
	releaseRegistryWait = 2 * time.Second
	// registryFloor is the least any bounded wait gets, so a run whose
	// budget is spent still takes a lock an ordinary Enroll or Release holds
	// for an instant. Tests shorten it.
	registryFloor = 2 * time.Second
)

// SetBudget bounds every registry lock wait of this handle by the caller's
// --wait budget, which runs from start; a negative wait has no end. Each
// wait still gets registryFloor, and Position waits at most PollProbeWait
// per poll, so a waiting run keeps checking its interrupt and kill file. A
// wait that runs out fails with ErrRegistryBusy.
func (q *Queue) SetBudget(start time.Time, wait time.Duration) {
	q.budget = budget{start: start, wait: wait, set: true}
}

// registryDeadline is when one registry lock wait gives up: the end of the
// budget (defaultRegistryWait from now without one), at most limit from now
// when limit is positive, and never less than registryFloor from now. The
// zero time means no deadline.
func (q *Queue) registryDeadline(limit time.Duration) time.Time {
	now := time.Now()
	var d time.Time
	switch {
	case !q.budget.set:
		d = now.Add(defaultRegistryWait)
	case q.budget.wait >= 0:
		d = q.budget.start.Add(q.budget.wait)
	}
	if limit > 0 && (d.IsZero() || now.Add(limit).Before(d)) {
		d = now.Add(limit)
	}
	if d.IsZero() {
		return d
	}
	if floor := now.Add(registryFloor); d.Before(floor) {
		d = floor
	}
	return d
}

// lockRegistry takes the registry lock within registryDeadline(limit).
func (q *Queue) lockRegistry(limit time.Duration) error {
	var err error
	if dl := q.registryDeadline(limit); dl.IsZero() {
		err = q.registry.Lock()
	} else {
		err = LockBy(q.registry, dl)
	}
	if err != nil {
		return fmt.Errorf("registry lock: %w", err)
	}
	return nil
}

// Mode says what a Queue handle may create or change.
```

(2 of 6) Replace:

```go
// RegistryLockPath is the path of a queue directory's registry lock.
func RegistryLockPath(queueDir string) string { return filepath.Join(queueDir, registryLockName) }

func (q *Queue) withRegistry(fn func() error) error {
	if err := q.registry.Lock(); err != nil {
		return fmt.Errorf("registry lock: %w", err)
	}
	defer q.registry.Unlock()
	return fn()
```

with:

```go
// RegistryLockPath is the path of a queue directory's registry lock.
func RegistryLockPath(queueDir string) string { return filepath.Join(queueDir, registryLockName) }

// withRegistry runs fn inside one hold of the registry lock, waiting for it
// only as long as the handle's budget allows (registryDeadline).
func (q *Queue) withRegistry(fn func() error) error { return q.withRegistryLimit(0, fn) }

// withRegistryLimit is withRegistry with each wait also capped at limit.
func (q *Queue) withRegistryLimit(limit time.Duration, fn func() error) error {
	if err := q.lockRegistry(limit); err != nil {
		return err
	}
	defer q.registry.Unlock()
	return fn()
```

(3 of 6) Replace:

```go
	if e == nil || e.lock == nil {
		return
	}
	_ = e.q.withRegistry(func() error {
		// Close before unlink: on Windows the handle must go away for the
		// delete to take effect promptly even with share-delete.
		_ = e.lock.Close()
		_ = os.Remove(e.path)
		_ = os.Remove(e.path + killExt)
		return nil
	})
	e.lock = nil
	// dur is the wall time from enqueue to release (the arrival nano is the
	// enqueue instant): time in the lane, wait included; cpu= already covers
```

with:

```go
	if e == nil || e.lock == nil {
		return
	}
	// Release never waits for ever: past releaseRegistryWait it goes on
	// without the registry lock. That is safe for a release: the slot is
	// freed by closing the ticket lock, and a scanner that listed the name
	// meanwhile finds the file gone or unlocked and reaps it.
	locked := LockBy(e.q.registry, time.Now().Add(releaseRegistryWait)) == nil
	// Close before unlink: on Windows the handle must go away for the
	// delete to take effect promptly even with share-delete.
	_ = e.lock.Close()
	_ = os.Remove(e.path)
	_ = os.Remove(e.path + killExt)
	if locked {
		_ = e.q.registry.Unlock()
	}
	e.lock = nil
	// dur is the wall time from enqueue to release (the arrival nano is the
	// enqueue instant): time in the lane, wait included; cpu= already covers
```

(4 of 6) Replace:

```go

// Position reports this enrollment's 0-based place in the live queue plus the
// current effective slot count. Both come from one scan under the registry
// lock, so the place and the count describe the same instant.
func (e *Enrollment) Position() (idx, slots int, live []Entry, err error) {
	err = e.q.withRegistry(func() error {
		var scanErr error
		live, slots, scanErr = e.q.scanLocked(time.Now())
		return scanErr
```

with:

```go

// Position reports this enrollment's 0-based place in the live queue plus the
// current effective slot count. Both come from one scan under the registry
// lock, so the place and the count describe the same instant. It waits for
// the registry lock at most PollProbeWait (and never past the budget); past
// that it fails with ErrRegistryBusy, which Acquire reads as "not admitted
// on this poll".
func (e *Enrollment) Position() (idx, slots int, live []Entry, err error) {
	err = e.q.withRegistryLimit(PollProbeWait, func() error {
		var scanErr error
		live, slots, scanErr = e.q.scanLocked(time.Now())
		return scanErr
```

(5 of 6) Replace:

```go
// callers pass them sorted by key, the lock order of spec 3.1. It returns
// the function that releases them in reverse order. A registry rebuild
// uses it to hold every lane still while it checks for tickets and
// rewrites machine.json.
func LockAll(qs []*Queue) (func(), error) {
	var held []*Queue
	unlock := func() {
		for i := len(held) - 1; i >= 0; i-- {
```

with:

```go
// callers pass them sorted by key, the lock order of spec 3.1. It returns
// the function that releases them in reverse order. A registry rebuild
// uses it to hold every lane still while it checks for tickets and
// rewrites machine.json. Each lock is waited for only until deadline (the
// zero time: as long as it takes); past it LockAll releases what it took
// and fails with ErrRegistryBusy.
func LockAll(qs []*Queue, deadline time.Time) (func(), error) {
	var held []*Queue
	unlock := func() {
		for i := len(held) - 1; i >= 0; i-- {
```

(6 of 6) Replace:

```go
		}
	}
	for _, q := range qs {
		if err := q.registry.Lock(); err != nil {
			unlock()
			return nil, fmt.Errorf("registry lock of %q: %w", q.Key, err)
		}
```

with:

```go
		}
	}
	for _, q := range qs {
		var err error
		if deadline.IsZero() {
			err = q.registry.Lock()
		} else {
			err = LockBy(q.registry, deadline)
		}
		if err != nil {
			unlock()
			return nil, fmt.Errorf("registry lock of %q: %w", q.Key, err)
		}
```

In `internal/machine/rebuild.go`, replace:

```go
		}
		qs = append(qs, q)
	}
	unlock, err := lane.LockAll(qs)
	if err != nil {
		return nil, stateErrorf("%s", esc(err))
	}
```

with:

```go
		}
		qs = append(qs, q)
	}
	unlock, err := lane.LockAll(qs, lockDeadline(o.Start, o.Wait, time.Now()))
	if err != nil {
		return nil, stateErrorf("%s", esc(err))
	}
```

- [ ] **Step 4: Run the tests to see them pass**

Run (bash), one at a time:

- `go test ./internal/lane/ -run 'TestRegistryWaitsAreBounded|TestRegistryDeadline|TestLockAllAndLiveLocked' -count=1`
- `go test . -run 'TestOwnLaneOperationsAreBoundedByTheirWait' -count=1`

Expected: `ok` for each package.

- [ ] **Step 5: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 6: Commit**

```bash
git add busyregistry_test.go internal/cli/config.go internal/cli/kill.go internal/cli/misc.go internal/cli/run.go internal/lane/acquire.go internal/lane/busy_test.go internal/lane/layout_test.go internal/lane/queue.go internal/machine/rebuild.go
git commit -F - <<'MSG'
fix: this binary's own lane operations wait for a registry lock only within their budget

Enroll, Release, Position, MarkAcquired, the kill request and its
wait, force-release, config writes and the rebuild's LockAll took the
registry lock with a blocking Lock, so a run of this binary stopped
inside a registry hold could keep every other command on that lane
waiting past its --wait. Each queue handle now carries its caller's
budget and every wait polls TryLock until the budget ends, with a 2s
floor; Position waits at most one poll interval and Release frees the
ticket without the lock when it cannot get it.
MSG
```


---

### Task 2: One fix-line builder for POSIX sh and PowerShell

Every printed command this plan adds (the unlinked refusal's run line, the `incoda config` lines, the pool-mismatch and link-needs-user lines) follows the fix-line rules of spec 2.6: POSIX single quotes with `'\''` on Unix, PowerShell single quotes with doubled quotes and `Set-Location -LiteralPath ...; if ($?) { ... }` on Windows, `--` as `'--'` on PowerShell, carried flags, and never a placeholder: a value no quoting can carry gives no runnable line, only the escaped fields. No shared builder exists yet (plan 2b's `machine.fixWord` quotes one reason), so this task adds `internal/fixline` with the whole rule set, including the directory part plan 4 needs for rebuilt outer commands, and points `machine.KillLine` at it. The reproduction test pastes rendered lines into the real shell against a stub `incoda` (`internal/testprog/argv`) that records its argv and directory.

**Files:**
- Create: `internal/fixline/fixline.go`
- Modify: `internal/machine/killtarget.go`
- Create: `internal/testprog/argv/main.go`
- Test: `internal/fixline/fixline_test.go` (new)

**Interfaces:**
- Consumes: `textsafe.Unsafe`, `textsafe.Escape` (plan 1).
- Produces: package `fixline`: `type Shell int` with `POSIX`, `PowerShell`; `func Native() Shell`; `type Word`, `func Lit(s string) Word` (bare: incoda, subcommands, flag names), `func Key(s string) Word` (bare on POSIX, quoted on PowerShell), `func Val(s string) Word` (always quoted), `func Sep() Word` (`--`); `func Quote(sh Shell, s string) string`; `func Problem(sh Shell, s string) string`; `type Line struct { Words []Word; Dir, Here string }` with `func (l Line) Render(sh Shell) Rendered`; `type Rendered struct { Text, Why string; DirUnknown bool }`; `const DirUnknownText`; `func NoRunnable(why, lead string) string`; `type Field struct{ Name, Value string }`, `func FieldLines(fs []Field) []string`; `type Flag struct { Name, Value string; Bool bool }`; `type Run struct { Queue, Pool []string; Flags []Flag; Argv []string; Dir, Here string }` with `Line() Line` and `Fields() []Field`; `func RunLines(sh Shell, r Run, why, lead string) []string` (the lines after `incoda: `: one indented runnable line, or `no runnable command (...)` and the indented fields). Test program `internal/testprog/argv`.

- [ ] **Step 1: Write the failing tests**

Also create the stub the reproduction test builds. It is a test fixture, so it goes in with the tests:

Create `internal/fixline/fixline_test.go`:

```go
package fixline

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestQuote(t *testing.T) {
	for _, c := range []struct {
		sh   Shell
		in   string
		want string
	}{
		{POSIX, "it's", `'it'\''s'`},
		{POSIX, `a\b`, `'a\b'`},
		{PowerShell, "it's", `'it''s'`},
		{PowerShell, `C:\x y\`, `'C:\x y\'`},
	} {
		if got := Quote(c.sh, c.in); got != c.want {
			t.Errorf("Quote(%v, %q) = %s, want %s", c.sh, c.in, got, c.want)
		}
	}
}

func TestRenderRunLine(t *testing.T) {
	r := Run{
		Queue: []string{"builds", "kungfoo-gate"},
		Pool:  []string{"tests"},
		Flags: []Flag{{Name: "exclusive", Value: "true", Bool: true}, {Name: "quiet", Value: "false", Bool: true},
			{Name: "reason", Value: "kungfoo gate"}, {Name: "wait", Value: "30m"}},
		Argv: []string{"just", "gate"},
		Dir:  "/src/kungfoo", Here: "/src/kungfoo",
	}
	if got, want := r.Line().Render(POSIX).Text, `incoda run --queue builds,kungfoo-gate --pool tests --exclusive --quiet=false --reason 'kungfoo gate' --wait '30m' -- 'just' 'gate'`; got != want {
		t.Fatalf("POSIX:\n got %s\nwant %s", got, want)
	}
	if got, want := r.Line().Render(PowerShell).Text, `incoda run --queue 'builds,kungfoo-gate' --pool 'tests' --exclusive --quiet=false --reason 'kungfoo gate' --wait '30m' '--' 'just' 'gate'`; got != want {
		t.Fatalf("PowerShell:\n got %s\nwant %s", got, want)
	}
	r.Here = "/elsewhere"
	if got := r.Line().Render(POSIX).Text; !strings.HasPrefix(got, `cd '/src/kungfoo' && incoda run `) {
		t.Fatalf("a different directory changes into it first: %s", got)
	}
	r.Dir = `C:\src\kung foo`
	if got := r.Line().Render(PowerShell).Text; !strings.HasPrefix(got, `Set-Location -LiteralPath 'C:\src\kung foo'; if ($?) { incoda run `) || !strings.HasSuffix(got, " }") {
		t.Fatalf("PowerShell changes directory with Set-Location and if ($?): %s", got)
	}
	r.Dir = ""
	if rd := r.Line().Render(POSIX); rd.Why != "" || !rd.DirUnknown || strings.HasPrefix(rd.Text, "cd ") {
		t.Fatalf("an unknown directory prints the line without a directory part: %+v", rd)
	}
	r.Dir = "relative/dir"
	if rd := r.Line().Render(POSIX); rd.Text != "" || rd.Why == "" {
		t.Fatalf("a relative directory gives no runnable line: %+v", rd)
	}
}

// TestNoRunnableLine: a value no quoting can carry gives no runnable line
// and the fields instead, never a placeholder.
func TestNoRunnableLine(t *testing.T) {
	base := Run{Queue: []string{"q"}, Argv: []string{"x"}, Dir: "/d", Here: "/d"}
	for _, c := range []struct {
		name string
		sh   Shell
		argv []string
		ok   bool
	}{
		{"tab", POSIX, []string{"a\tb"}, false},
		{"escape", PowerShell, []string{"a\x1bb"}, false},
		{"bidi", POSIX, []string{"a\u202eb"}, false},
		{"bad utf-8", POSIX, []string{"a\xffb"}, false},
		{"double quote on PowerShell", PowerShell, []string{`say "hi"`}, false},
		{"empty on PowerShell", PowerShell, []string{""}, false},
		{"space and trailing backslash on PowerShell", PowerShell, []string{`C:\a b\`}, false},
		{"double quote on POSIX", POSIX, []string{`say "hi"`}, true},
		{"empty on POSIX", POSIX, []string{""}, true},
		{"space and trailing backslash on POSIX", POSIX, []string{`C:\a b\`}, true},
		{"backslash on PowerShell", PowerShell, []string{`a\b`}, true},
	} {
		r := base
		r.Argv = c.argv
		lines := RunLines(c.sh, r, "", "run it")
		if c.ok != (len(lines) == 1) {
			t.Errorf("%s: %q", c.name, lines)
			continue
		}
		if !c.ok && (!strings.HasPrefix(lines[0], "no runnable command (") || !strings.HasSuffix(lines[0], "); run it with these fields:")) {
			t.Errorf("%s: %q", c.name, lines)
		}
	}
	lines := RunLines(POSIX, Run{Queue: []string{"q"}, Pool: []string{"tests"}, Flags: []Flag{{Name: "reason", Value: "r\x1b"}, {Name: "exclusive", Value: "true", Bool: true}},
		Argv: []string{"a", "b"}, Here: "/w"}, "", "run it")
	want := []string{
		"no runnable command (a value contains a control, bidi or invalid UTF-8 character); run it with these fields:",
		"  queue: q", "  pool: tests", "  flags: --exclusive", `  reason: r\x1b`, "  cwd: /w", "  command: a b",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("fields:\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	if got := RunLines(POSIX, base, `queue "q" requires --reason and this run has none`, "run it"); !strings.HasPrefix(got[0], `no runnable command (queue "q" requires --reason`) {
		t.Fatalf("a reason the caller knows rules the line out: %q", got)
	}
}

// argvStub builds internal/testprog/argv as "incoda" in a directory of its
// own and returns that directory.
func argvStub(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	name := "incoda"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, name), "github.com/deblasis/incoda/internal/testprog/argv")
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=auto")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build argv: %v\n%s", err, out)
	}
	return dir
}

// paste runs line in the native shell with the stub first on PATH and
// returns the arguments and directory the stub saw (ok false when it never
// ran).
func paste(t *testing.T, stub, line string) (args []string, cwd string, ok bool) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "argv.json")
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		ps, err := exec.LookPath("powershell")
		if err != nil {
			t.Skip("no powershell on this machine")
		}
		cmd = exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", line)
	} else {
		cmd = exec.Command("/bin/sh", "-c", line)
	}
	env := []string{"ARGV_OUT=" + out}
	for _, kv := range os.Environ() {
		if k, v, _ := strings.Cut(kv, "="); strings.EqualFold(k, "PATH") {
			env = append(env, k+"="+stub+string(os.PathListSeparator)+v)
		} else {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	_ = cmd.Run()
	b, err := os.ReadFile(out)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Args []string `json:"args"`
		Cwd  string   `json:"cwd"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	return got.Args, got.Cwd, true
}

// TestPastedLineReproducesArgvAndDirectory: a printed run line, pasted into
// the native shell, reaches incoda with exactly the arguments and the
// directory it names, for every value class that survives quoting; and a
// directory that does not exist never runs the command (spec 9, fix-line
// reproduction).
func TestPastedLineReproducesArgvAndDirectory(t *testing.T) {
	stub := argvStub(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	argv := []string{"it's", `say "hi"`, "a;b", "$(echo pwned)", "two words", "back`tick", `a\b`, `grep 'a\.b'`}
	if Native() == POSIX {
		argv = append(argv, `C:\Program Files\x\`, "")
	}
	r := Run{Queue: []string{"cap-gate"}, Pool: []string{"tests"},
		Flags: []Flag{{Name: "reason", Value: "it's a 'gate'"}, {Name: "exclusive", Value: "true", Bool: true}},
		Argv:  argv, Dir: dir, Here: "/"}
	rd := r.Line().Render(Native())
	if rd.Why != "" {
		t.Fatalf("no runnable line: %s", rd.Why)
	}
	args, cwd, ok := paste(t, stub, rd.Text)
	if !ok {
		t.Fatalf("the stub never ran: %s", rd.Text)
	}
	want := append([]string{"run", "--queue", "cap-gate", "--pool", "tests", "--reason", "it's a 'gate'", "--exclusive", "--"}, argv...)
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv:\n got %q\nwant %q\nline: %s", args, want, rd.Text)
	}
	if cwd != dir {
		t.Fatalf("cwd %s, want %s", cwd, dir)
	}

	r.Argv = []string{"x"}
	r.Dir = filepath.Join(dir, "does-not-exist")
	rd = r.Line().Render(Native())
	if _, _, ok := paste(t, stub, rd.Text); ok {
		t.Fatalf("a missing directory must never run the command: %s", rd.Text)
	}

	r.Dir = ""
	rd = r.Line().Render(Native())
	if !rd.DirUnknown {
		t.Fatalf("an empty directory is unknown: %+v", rd)
	}
	if args, _, ok := paste(t, stub, rd.Text); !ok || args[len(args)-1] != "x" {
		t.Fatalf("an unknown directory still runs the command where it is pasted: %v %q", ok, args)
	}
}
```

Create `internal/testprog/argv/main.go`:

```go
// Command argv is a test fixture standing in for incoda when a test pastes
// a printed fix line into a shell: it writes the arguments it received and
// its working directory, as JSON, to the file named by ARGV_OUT, so the
// test can check the shell reproduced them exactly.
package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	out := os.Getenv("ARGV_OUT")
	if out == "" {
		fmt.Fprintln(os.Stderr, "argv: ARGV_OUT is not set")
		os.Exit(2)
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "argv:", err)
		os.Exit(2)
	}
	b, _ := json.Marshal(map[string]any{"args": os.Args[1:], "cwd": cwd})
	if err := os.WriteFile(out, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "argv:", err)
		os.Exit(2)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run (bash): `go test ./internal/fixline/ -count=1 -timeout 120s`

Expected: the test build fails: `undefined: Shell`, `undefined: POSIX`, `undefined: Quote`, `undefined: Run` (the package has no code yet).

- [ ] **Step 3: Implement**

`isAbs` judges a PowerShell directory by Windows rules even on Unix, so `TestRenderRunLine` covers the PowerShell form on every platform; `TestPastedLineReproducesArgvAndDirectory` pastes into PowerShell only on Windows (skipped where `powershell` is missing).

Create `internal/fixline/fixline.go`:

```go
// Package fixline builds the commands incoda prints for a person or an
// agent to paste after a refusal (spec 2.6, "Fix lines"): POSIX sh on Unix,
// PowerShell on Windows, never cmd.exe.
//
// Every value is one single-quoted word, emitted raw inside its quotes and
// never escaped for display: a backslash is literal inside single quotes in
// both shells. A value that no quoting can carry safely (a control, bidi or
// invalid UTF-8 character anywhere; on PowerShell also a double quote, an
// empty value, or a space or tab before a trailing backslash) gives no
// runnable line at all, never a line with a placeholder: the caller prints
// the fields instead (NoRunnable, Fields).
package fixline

import (
	"path/filepath"
	"runtime"
	"strings"

	"github.com/deblasis/incoda/internal/textsafe"
)

// Shell is the syntax a line is written in.
type Shell int

const (
	// POSIX is sh: Unix.
	POSIX Shell = iota
	// PowerShell is Windows PowerShell 5.1 and later.
	PowerShell
)

// Native is the shell this platform's lines are written for.
func Native() Shell {
	if runtime.GOOS == "windows" {
		return PowerShell
	}
	return POSIX
}

type kind int

const (
	lit kind = iota
	key
	val
	sep
)

// Word is one word of a printed command.
type Word struct {
	text string
	kind kind
}

// Lit is printed bare in both shells: "incoda", a subcommand, a flag name.
func Lit(s string) Word { return Word{s, lit} }

// Key is a queue key or a comma-separated list of keys. Keys pass
// lane.ValidateKey (letters, digits, '-', '_', '.'), so POSIX prints them
// bare. PowerShell quotes them: a bare comma there builds an array, which
// reaches the program as several arguments.
func Key(s string) Word { return Word{s, key} }

// Val is every other value: a reason, an owner, a wait, a word of the
// command. It is always one single-quoted word.
func Val(s string) Word { return Word{s, val} }

// Sep is the "--" before the command: bare on POSIX, '--' on PowerShell,
// where a bare -- is the end-of-parameters token and is not passed on.
func Sep() Word { return Word{"--", sep} }

// Quote renders s as one single-quoted word: on POSIX each single quote
// inside is closed, escaped with a backslash and reopened; on PowerShell it
// is doubled.
func Quote(sh Shell, s string) string {
	if sh == PowerShell {
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Problem says why s cannot be printed as a word of a runnable line in sh,
// or "" when it can.
func Problem(sh Shell, s string) string {
	if textsafe.Unsafe(s) {
		return "a value contains a control, bidi or invalid UTF-8 character"
	}
	if sh != PowerShell {
		return ""
	}
	// Windows PowerShell 5.1 calling a native program strips embedded
	// double quotes, drops empty arguments, and re-quotes an argument with a
	// space that ends in a backslash as "...\", which the program reads as
	// an escaped quote.
	switch {
	case strings.Contains(s, `"`):
		return "a value contains a double quote, which PowerShell does not pass on intact"
	case s == "":
		return "a value is empty, which PowerShell does not pass on"
	case strings.ContainsAny(s, " \t") && strings.HasSuffix(s, `\`):
		return "a value with a space ends in a backslash, which PowerShell does not pass on intact"
	}
	return ""
}

// Line is a command to print and the directory it must run from.
type Line struct {
	Words []Word
	// Dir is the directory the command must run from and Here this
	// process's directory. When they differ the line changes into Dir
	// first, in a form that never runs the command anywhere else. An empty
	// Dir with a non-empty Here means the directory is unknown: the line
	// has no directory part and Rendered.DirUnknown is set.
	Dir, Here string
}

// Rendered is a line ready to print, or why there is none.
type Rendered struct {
	// Text is the runnable line; empty when Why is set.
	Text string
	// Why says why no runnable line can be printed.
	Why string
	// DirUnknown is set when the line has no directory part because the
	// directory it must run from is not known (DirUnknownText).
	DirUnknown bool
}

// DirUnknownText goes on the line before a line whose directory is
// unknown.
const DirUnknownText = "the outer job's directory is unknown; run this from it"

// Render builds the line for sh.
func (l Line) Render(sh Shell) Rendered {
	parts := make([]string, 0, len(l.Words))
	for _, w := range l.Words {
		switch w.kind {
		case lit:
			parts = append(parts, w.text)
		case key:
			if sh == PowerShell {
				parts = append(parts, Quote(sh, w.text))
			} else {
				parts = append(parts, w.text)
			}
		case sep:
			if sh == PowerShell {
				parts = append(parts, Quote(sh, w.text))
			} else {
				parts = append(parts, w.text)
			}
		default:
			if why := Problem(sh, w.text); why != "" {
				return Rendered{Why: why}
			}
			parts = append(parts, Quote(sh, w.text))
		}
	}
	cmd := strings.Join(parts, " ")
	switch {
	case l.Dir == l.Here:
		return Rendered{Text: cmd}
	case l.Dir == "":
		return Rendered{Text: cmd, DirUnknown: true}
	case !isAbs(sh, l.Dir):
		return Rendered{Why: "the directory it must run from is not an absolute path"}
	}
	if why := Problem(sh, l.Dir); why != "" {
		return Rendered{Why: why}
	}
	if sh == PowerShell {
		return Rendered{Text: "Set-Location -LiteralPath " + Quote(sh, l.Dir) + "; if ($?) { " + cmd + " }"}
	}
	return Rendered{Text: "cd " + Quote(sh, l.Dir) + " && " + cmd}
}

// isAbs is filepath.IsAbs for the shell's platform, so the PowerShell form
// can be tested on Unix too.
func isAbs(sh Shell, p string) bool {
	if sh == Native() {
		return filepath.IsAbs(p)
	}
	if sh == PowerShell {
		return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') || strings.HasPrefix(p, `\\`)
	}
	return strings.HasPrefix(p, "/")
}

// NoRunnable is the line printed instead of a runnable one: lead says what
// to do with the fields that follow ("run it", "rerun the outer job").
func NoRunnable(why, lead string) string {
	return "no runnable command (" + why + "); " + lead + " with these fields:"
}

// Field is one line of the fields printed instead of a runnable command.
type Field struct{ Name, Value string }

// FieldLines renders fields as "name: value" lines, each value escaped
// for display (spec 4.6).
func FieldLines(fs []Field) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Name + ": " + textsafe.Escape(f.Value)
	}
	return out
}

// Flag is one flag of a printed run line. A switch carries Bool: it prints
// as --name when Value is "true" and as --name=false otherwise.
type Flag struct {
	Name, Value string
	Bool        bool
}

// Run is an incoda run command line.
type Run struct {
	// Queue is every key the line names, in the order to print.
	Queue []string
	// Pool is the --pool set; nil prints no --pool.
	Pool []string
	// Flags are the carried flags, in the order to print.
	Flags []Flag
	// Argv is the command after --.
	Argv []string
	// Dir and Here are Line's.
	Dir, Here string
}

// Line is the run line as words.
func (r Run) Line() Line {
	w := []Word{Lit("incoda"), Lit("run"), Lit("--queue"), Key(strings.Join(r.Queue, ","))}
	if r.Pool != nil {
		w = append(w, Lit("--pool"), Key(strings.Join(r.Pool, ",")))
	}
	for _, f := range r.Flags {
		switch {
		case f.Bool && f.Value == "true":
			w = append(w, Lit("--"+f.Name))
		case f.Bool:
			w = append(w, Lit("--"+f.Name+"=false"))
		default:
			w = append(w, Lit("--"+f.Name), Val(f.Value))
		}
	}
	w = append(w, Sep())
	for _, a := range r.Argv {
		w = append(w, Val(a))
	}
	return Line{Words: w, Dir: r.Dir, Here: r.Here}
}

// Fields are the run line's parts for NoRunnable: queue, pool (when set),
// flags (every carried flag but the reason), reason (when set), cwd and the
// command, one escaped line each.
func (r Run) Fields() []Field {
	fs := []Field{{"queue", strings.Join(r.Queue, ",")}}
	if r.Pool != nil {
		fs = append(fs, Field{"pool", strings.Join(r.Pool, ",")})
	}
	var flags []string
	reason, hasReason := "", false
	for _, f := range r.Flags {
		switch {
		case f.Name == "reason":
			reason, hasReason = f.Value, true
		case f.Bool && f.Value == "true":
			flags = append(flags, "--"+f.Name)
		case f.Bool:
			flags = append(flags, "--"+f.Name+"=false")
		default:
			flags = append(flags, "--"+f.Name+" "+f.Value)
		}
	}
	fs = append(fs, Field{"flags", strings.Join(flags, " ")})
	if hasReason {
		fs = append(fs, Field{"reason", reason})
	}
	dir := r.Dir
	if dir == "" {
		dir = r.Here
	}
	fs = append(fs, Field{"cwd", dir}, Field{"command", strings.Join(r.Argv, " ")})
	return fs
}

// RunLines is what a refusal prints for r: the runnable line indented
// under a lead, or NoRunnable and the fields when there is none. why, when
// not empty, is a reason the caller already knows that rules a runnable
// line out (a lane in the line requires a reason and none is known).
// Every returned line is the text after "incoda: ".
func RunLines(sh Shell, r Run, why, lead string) []string {
	var rd Rendered
	if why == "" {
		rd = r.Line().Render(sh)
		why = rd.Why
	}
	if why != "" {
		out := []string{NoRunnable(why, lead)}
		for _, f := range FieldLines(r.Fields()) {
			out = append(out, "  "+f)
		}
		return out
	}
	if rd.DirUnknown {
		return []string{DirUnknownText, "  " + rd.Text}
	}
	return []string{"  " + rd.Text}
}
```

In `internal/machine/killtarget.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 2) Replace:

```go
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/lane"
)

```

with:

```go
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/deblasis/incoda/internal/fixline"
	"github.com/deblasis/incoda/internal/lane"
)

```

(2 of 2) Replace:

```go
	return KillTarget{Kind: TargetNone, Key: key, PID: pid}, nil
}

// fixWord quotes one value of a printed command (spec 2.6, fix lines):
// one single-quoted word, an embedded quote closed, escaped with a
// backslash and reopened on Unix (POSIX sh), doubled on Windows
// (PowerShell).
func fixWord(s string) string {
	if runtime.GOOS == "windows" {
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// KillLine is a printed stop line: incoda kill --queue K --pid N --reason
// '<reason>', plus --force when force is set (the stopped-holder rerun line
// of spec 3.2 carries it; kill needs no --force for an older incoda). The key is
// validated and prints bare; the reason is always a constant of this
// binary, so the line never needs a placeholder.
func KillLine(key string, pid int, reason string, force bool) string {
	s := fmt.Sprintf("incoda kill --queue %s --pid %d --reason %s", key, pid, fixWord(reason))
	if force {
		s += " --force"
	}
```

with:

```go
	return KillTarget{Kind: TargetNone, Key: key, PID: pid}, nil
}

// KillLine is a printed stop line: incoda kill --queue K --pid N --reason
// '<reason>' (quoted by fixline.Quote), plus --force when force is set (the stopped-holder rerun line
// of spec 3.2 carries it; kill needs no --force for an older incoda). The key is
// validated and prints bare; the reason is always a constant of this
// binary, so the line never needs a placeholder.
func KillLine(key string, pid int, reason string, force bool) string {
	s := fmt.Sprintf("incoda kill --queue %s --pid %d --reason %s", key, pid, fixline.Quote(fixline.Native(), reason))
	if force {
		s += " --force"
	}
```

- [ ] **Step 4: Run the tests to see them pass**

Run (bash): `go test ./internal/fixline/ -count=1`

Expected: `ok` for each package.

- [ ] **Step 5: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 6: Commit**

```bash
git add internal/fixline/fixline.go internal/fixline/fixline_test.go internal/machine/killtarget.go internal/testprog/argv/main.go
git commit -F - <<'MSG'
feat: one fix-line builder prints runnable commands for POSIX sh and PowerShell

internal/fixline quotes every value as one single-quoted word, keys
bare on POSIX and quoted on PowerShell, changes directory with cd ... &&
or Set-Location ...; if ($?) so a failed change never runs the command
elsewhere, and prints the escaped fields instead of a runnable line
whenever a value cannot be carried. A stub incoda reproduces argv and
directory from pasted lines. kill's stop lines use it.
MSG
```


---

### Task 3: config links a queue to pools under machine.lock

Spec 4.3 and 4.4: `incoda config KEY --pool a,b` sets the link of an unlinked key, `--add-pool` on an unlinked key is the same, a `--pool` on a linked key needs `--replace` (else `link-exists:`), the same set is an `already linked:` no-op (so concurrent first links to the same set never escalate), `--add-pool` and `--remove-pool` edit the set (never down to nothing), `--unlink` removes it, `--quiet-machine[=false]` sets the field. Every link change echoes `link: old -> new` and logs `event=link by=config old=... new=...`. Links are validated against machine.json under machine.lock, then written inside one hold of the lane's registry lock (`machine.WriteLink`, a compare-and-set callback). Setting a link on a pool is refused. The task lands here, before the unlinked refusal, because that refusal prints `incoda config KEY --pool ...` lines and the test suite links its project keys with it (`linkTestKeys`). The bad-text checks become one table (`textFields`, `checkTexts`), with a table-driven test, as plan 1 asked once link flags arrived. `incoda config KEY` without a setting flag now reads the config without creating the lane, and its listing gains `kind:`, `pools:` and `quiet machine:` lines after the existing ones.

**Files:**
- Modify: `internal/cli/config.go`
- Create: `internal/cli/linkflags.go`
- Modify: `internal/lane/config.go`
- Create: `internal/machine/link.go`
- Test: `config_test.go`
- Test: `internal/cli/linkflags_test.go` (new)
- Test: `internal/machine/link_test.go` (new)

**Interfaces:**
- Consumes: `machine.AcquireLock`, `machine.ReadRegistry`, `machine.Options` (plan 2a), `lane.Queue.UpdateConfig` (plan 1), `Queue.SetBudget` (Task 1), `textsafe.CheckWrite` (plan 1).
- Produces: `lane.ErrNoChange` (an `UpdateConfig` callback returns it to write nothing); package `machine`: `func SortedSet(names []string) []string`, `func SameSet(a, b []string) bool`, `func Subset(a, b []string) bool`, `func SetText(names []string) string` (`"(none)"` or `a,b`), `func (r *Registry) NotPools(names []string) []string`, `func NotAPool(r *Registry, names []string) *Refusal`, `type LinkResult struct { Old, New lane.Config; Changed bool; Registry *Registry }`, `func WriteLink(stateDir, key, by string, o Options, fn func(reg *Registry, c *lane.Config) error) (LinkResult, error)`, `func LastLinker(stateDir, key string) (int, bool)`, `func LinkedLine(key string, pools []string, quiet bool) string`. Package `cli`: `type poolsValue` (a comma-separated key set flag), `type linkEdit` with `check()`, `apply(key, cur)`, `configError(key, err) error`, `checkTexts(given, values) error`, `textFields`. Root test helper `linkTestKeys(t, incoda, state string, keys ...string)` and `const testPool = "vm"`.

- [ ] **Step 1: Write the failing tests**

`linkTestKeys` goes into `config_test.go` with the tests: it widens the `vm` pool to 64 slots and links each key to it, so a test about one lane is never serialised by a pool it did not mean to test. Nothing calls it yet; Task 7 does.

Create `internal/machine/link_test.go`:

```go
package machine

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
)

func TestSets(t *testing.T) {
	if got := SortedSet([]string{"tests", "builds", "tests"}); strings.Join(got, ",") != "builds,tests" {
		t.Fatalf("SortedSet: %v", got)
	}
	if !SameSet([]string{"b", "a"}, []string{"a", "b", "a"}) || SameSet([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("SameSet")
	}
	if !Subset([]string{"a"}, []string{"a", "b"}) || Subset([]string{"c"}, []string{"a"}) || !Subset(nil, nil) {
		t.Fatal("Subset")
	}
	if SetText(nil) != "(none)" || SetText([]string{"vm", "builds"}) != "builds,vm" {
		t.Fatal("SetText")
	}
}

// TestWriteLink: a link is written under machine.lock and the lane's
// registry lock, logged as event=link, refused on a pool, and a callback
// that finds nothing to do writes nothing.
func TestWriteLink(t *testing.T) {
	state := t.TempDir()
	if _, _, err := ensure(t, state); err != nil {
		t.Fatal(err)
	}
	o := Options{Start: time.Now(), Wait: 10 * time.Second, Poll: 20 * time.Millisecond}
	set := func(pools ...string) func(*Registry, *lane.Config) error {
		return func(_ *Registry, c *lane.Config) error {
			if SameSet(c.Pools, pools) {
				return lane.ErrNoChange
			}
			c.Pools = pools
			return nil
		}
	}
	res, err := WriteLink(state, "cap-gate", "test", o, set("tests"))
	if err != nil || !res.Changed || len(res.Old.Pools) != 0 || SetText(res.New.Pools) != "tests" || !res.Registry.IsPool("tests") {
		t.Fatalf("first link: %+v %v", res, err)
	}
	cfg, err := lane.ReadConfig(lane.LaneDir(state, "cap-gate"))
	if err != nil || SetText(cfg.Pools) != "tests" || cfg.Schema != lane.ConfigSchema {
		t.Fatalf("stored config: %+v %v", cfg, err)
	}
	res, err = WriteLink(state, "cap-gate", "test", o, set("tests"))
	if err != nil || res.Changed {
		t.Fatalf("the same link again writes nothing: %+v %v", res, err)
	}
	b, _ := os.ReadFile(lane.LogPath(lane.LaneDir(state, "cap-gate")))
	if n := strings.Count(string(b), " event=link "); n != 1 || !strings.Contains(string(b), " by=test old= new=tests") {
		t.Fatalf("one event=link line, got %d:\n%s", n, b)
	}
	if pid, ok := LastLinker(state, "cap-gate"); !ok || pid != os.Getpid() {
		t.Fatalf("LastLinker = %d %v", pid, ok)
	}
	var rf *Refusal
	if _, err := WriteLink(state, "builds", "test", o, set("tests")); !errors.As(err, &rf) || !strings.HasPrefix(rf.Msg, `pool-mismatch: "builds" is a pool`) {
		t.Fatalf("a pool never links: %v", err)
	}
	if err := os.WriteFile(lane.LaneDir(state, "cap-gate")+"/config.json", []byte(`{"schema":9}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var se *StateError
	if _, err := WriteLink(state, "cap-gate", "test", o, set("vm")); !errors.As(err, &se) || !strings.Contains(se.Msg, "newer incoda") {
		t.Fatalf("a newer config fails closed: %v", err)
	}
}

// TestConcurrentFirstLinksToOneSetNeverEscalate: several writers making
// the same first link at once: one writes, the others find it in place.
func TestConcurrentFirstLinksToOneSetNeverEscalate(t *testing.T) {
	state := t.TempDir()
	if _, _, err := ensure(t, state); err != nil {
		t.Fatal(err)
	}
	o := Options{Start: time.Now(), Wait: 10 * time.Second, Poll: 5 * time.Millisecond}
	var wg sync.WaitGroup
	var mu sync.Mutex
	changed := 0
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := WriteLink(state, "kf-gate", "test", o, func(_ *Registry, c *lane.Config) error {
				if len(c.Pools) > 0 {
					return lane.ErrNoChange
				}
				c.Pools = []string{"tests"}
				return nil
			})
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			if res.Changed {
				changed++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if changed != 1 {
		t.Fatalf("%d writers changed the link, want exactly 1", changed)
	}
}
```

Create `internal/cli/linkflags_test.go`:

```go
package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
)

func TestPoolsValue(t *testing.T) {
	var v poolsValue
	if err := v.Set("tests, builds,tests"); err != nil || v.String() != "builds,tests" || !v.set {
		t.Fatalf("got %q %v", v.String(), err)
	}
	for _, bad := range []string{"", "a,,b", "bad key", "a/b"} {
		if err := (&poolsValue{}).Set(bad); err == nil {
			t.Errorf("Set(%q) must fail", bad)
		}
	}
}

func TestLinkEdit(t *testing.T) {
	yes := true
	unlinked := lane.Config{}
	linked := lane.Config{Pools: []string{"tests", "builds"}}
	for _, c := range []struct {
		name  string
		e     linkEdit
		cur   lane.Config
		want  string
		quiet bool
		err   string
	}{
		{"pool on unlinked", linkEdit{set: []string{"tests"}}, unlinked, "tests", false, ""},
		{"same pool on linked", linkEdit{set: []string{"builds", "tests"}}, linked, "builds,tests", false, ""},
		{"other pool on linked", linkEdit{set: []string{"vm"}}, linked, "", false, `link-exists: "k" is linked to builds,tests; changing a link is the user's call: ask them`},
		{"replace", linkEdit{set: []string{"vm"}, replace: true}, linked, "vm", false, ""},
		{"add on unlinked", linkEdit{add: []string{"vm"}}, unlinked, "vm", false, ""},
		{"add", linkEdit{add: []string{"vm"}}, linked, "builds,tests,vm", false, ""},
		{"add contained", linkEdit{add: []string{"tests"}}, linked, "builds,tests", false, ""},
		{"remove", linkEdit{remove: []string{"builds"}}, linked, "tests", false, ""},
		{"remove last", linkEdit{remove: []string{"builds", "tests"}}, linked, "", false, `--remove-pool would leave "k" linked to no pool; use --unlink to remove the link`},
		{"remove on unlinked", linkEdit{remove: []string{"vm"}}, unlinked, "(none)", false, ""},
		{"unlink", linkEdit{unlink: true}, linked, "(none)", false, ""},
		{"quiet", linkEdit{quiet: &yes}, linked, "builds,tests", true, ""},
	} {
		pools, quiet, err := c.e.apply("k", c.cur)
		if c.err != "" {
			var rf *machine.Refusal
			if !errors.As(err, &rf) || rf.Msg != c.err {
				t.Errorf("%s: err %v, want %q", c.name, err, c.err)
			}
			continue
		}
		if err != nil || machine.SetText(pools) != c.want || quiet != c.quiet {
			t.Errorf("%s: %v %v %v", c.name, pools, quiet, err)
		}
	}
	for _, e := range []linkEdit{
		{set: []string{"a"}, add: []string{"b"}},
		{set: []string{"a"}, unlink: true},
		{unlink: true, remove: []string{"a"}},
		{replace: true},
	} {
		if err := e.check(); err == nil || !strings.Contains(err.Error(), "--") {
			t.Errorf("%+v must be refused: %v", e, err)
		}
	}
}
```

In `config_test.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 2) Replace:

```go
		code = exitCodeOf(err)
	}
	return string(out), code
}

// TestQueueConfigSuppliesSlots: callers should not have to agree on --slots
```

with:

```go
		code = exitCodeOf(err)
	}
	return string(out), code
}

// testPool is the pool linkTestKeys links test lanes to, widened so that it
// never serialises lanes a test did not mean to put behind one cap.
const testPool = "vm"

// linkTestKeys links each project key to testPool, widened to 64 slots,
// with the user's own command (config --pool). Since spec 4.1 a project
// lane refuses runs until it is linked, so every test that runs on a
// project key links it first; a test about pools picks its pools itself.
func linkTestKeys(t *testing.T, incoda, state string, keys ...string) {
	t.Helper()
	if out, code := runIncoda(t, incoda, state, "config", testPool, "--slots", "64"); code != 0 {
		t.Fatalf("widen %s: %d\n%s", testPool, code, out)
	}
	for _, k := range keys {
		if out, code := runIncoda(t, incoda, state, "config", k, "--pool", testPool); code != 0 {
			t.Fatalf("link %s: %d\n%s", k, code, out)
		}
	}
}

// TestConfigLinkFlags walks config's link flags (spec 4.3): a first link,
// the already-linked no-op, link-exists without --replace, --add-pool,
// --remove-pool (never the last pool), --unlink, --quiet-machine, the
// pool-mismatch refusals, and the echo and event=link log of each change.
func TestConfigLinkFlags(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	cfgOf := func() (pools []string, quiet bool) {
		b, err := os.ReadFile(filepath.Join(laneDir(state, "cap-gate"), "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		var c struct {
			Pools []string `json:"pools"`
			Quiet bool     `json:"quiet_machine"`
		}
		if err := json.Unmarshal(b, &c); err != nil {
			t.Fatal(err)
		}
		return c.Pools, c.Quiet
	}
	step := func(wantCode int, want string, args ...string) string {
		t.Helper()
		out, code := runIncoda(t, incoda, state, append([]string{"config", "cap-gate"}, args...)...)
		if code != wantCode || !strings.Contains(out, want) {
			t.Fatalf("config cap-gate %s: want exit %d and %q, got %d:\n%s", strings.Join(args, " "), wantCode, want, code, out)
		}
		return out
	}
	out := step(0, "link: (none) -> tests\n", "--pool", "tests")
	if !strings.Contains(out, "  kind: project\n  pools: tests\n  quiet machine: no\n") {
		t.Fatalf("config shows the link:\n%s", out)
	}
	step(0, "incoda: already linked: cap-gate -> tests\n", "--pool", "tests")
	step(120, `incoda: link-exists: "cap-gate" is linked to tests; changing a link is the user's call: ask them`, "--pool", "builds")
	step(0, "link: tests -> builds\n", "--pool", "builds", "--replace")
	step(0, "link: builds -> builds,tests\n", "--add-pool", "tests")
	step(0, "incoda: already linked: cap-gate -> builds,tests\n", "--add-pool", "tests,builds")
	step(0, "link: builds,tests -> tests\n", "--remove-pool", "builds")
	step(120, `incoda: --remove-pool would leave "cap-gate" linked to no pool; use --unlink to remove the link`, "--remove-pool", "tests")
	step(120, `incoda: pool-mismatch: "nosuch" is not a pool on this machine (pools: builds, computer-use, tests, vm)`, "--add-pool", "nosuch")
	step(0, "  quiet machine: yes\n", "--quiet-machine")
	if pools, quiet := cfgOf(); strings.Join(pools, ",") != "tests" || !quiet {
		t.Fatalf("stored: %v %v", pools, quiet)
	}
	step(0, "  quiet machine: no\n", "--quiet-machine=false")
	step(0, "link: tests -> (none)\n", "--unlink")
	step(0, "incoda: already unlinked: cap-gate\n", "--unlink")
	step(0, "  pools: (none: unlinked, runs are refused until it is linked)\n")
	step(120, "--pool sets the whole link", "--pool", "tests", "--unlink")
	step(120, "--replace goes with --pool", "--replace")
	out, code := runIncoda(t, incoda, state, "config", "builds", "--pool", "tests")
	if code != 120 || !strings.Contains(out, `incoda: pool-mismatch: "builds" is a pool; a pool never links other pools and carries no quiet_machine`) {
		t.Fatalf("a pool never links: %d\n%s", code, out)
	}
	out, code = runIncoda(t, incoda, state, "config", "builds")
	if code != 0 || !strings.Contains(out, "  kind: pool\n") || strings.Contains(out, "pools:") {
		t.Fatalf("a pool shows its kind and no link: %d\n%s", code, out)
	}
	b, _ := os.ReadFile(filepath.Join(laneDir(state, "cap-gate"), "lane.log"))
	for _, want := range []string{" by=config old= new=tests", " by=config old=tests new=builds", " by=config old=builds new=builds,tests",
		" by=config old=builds,tests new=tests", " by=config old=tests new=\n"} {
		if !strings.Contains(string(b)+"\n", want) {
			t.Fatalf("lane.log lacks %q:\n%s", want, b)
		}
	}
	if n := strings.Count(string(b), " event=link "); n != 5 {
		t.Fatalf("%d event=link lines, want one per change (5):\n%s", n, b)
	}
}

// TestConcurrentConfigFirstLinksNeverEscalate: agents running the same
// printed first link at once all succeed; exactly one writes it.
func TestConcurrentConfigFirstLinksNeverEscalate(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "seed"); code != 0 {
		t.Fatalf("migrate: %d\n%s", code, out)
	}
	var wg sync.WaitGroup
	outs := make([]string, 4)
	codes := make([]int, 4)
	for i := range outs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outs[i], codes[i] = runIncoda(t, incoda, state, "config", "kf-gate", "--pool", "tests")
		}(i)
	}
	wg.Wait()
	linked := 0
	for i := range outs {
		if codes[i] != 0 {
			t.Fatalf("writer %d: exit %d\n%s", i, codes[i], outs[i])
		}
		if strings.Contains(outs[i], "link: (none) -> tests") {
			linked++
		}
	}
	if linked != 1 {
		t.Fatalf("%d writers echoed the link, want 1:\n%s", linked, strings.Join(outs, "\n---\n"))
	}
}

// TestQueueConfigSuppliesSlots: callers should not have to agree on --slots
```

(2 of 2) Replace:

```go
	_ = strconv.Itoa
}

// TestConfigRefusesControlCharacters: a description is shown in status and
// in refusals, so it must not carry anything that repaints a terminal.
func TestConfigRefusesControlCharacters(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	out, code := runIncoda(t, incoda, state, "config", "badtext", "--description", "red\x1b[31m")
	if code != 120 || !strings.Contains(out, "incoda: bad-text: description contains control characters") {
		t.Fatalf("want exit 120 and a bad-text refusal, got %d:\n%s", code, out)
	}
	out, code = runIncoda(t, incoda, state, "config", "badtext", "--close", "two\nlines")
	if code != 120 || !strings.Contains(out, "bad-text: closed contains control characters") {
		t.Fatalf("want exit 120 for --close, got %d:\n%s", code, out)
	}
}

```

with:

```go
	_ = strconv.Itoa
}

// TestConfigRefusesControlCharacters: descriptions and closed texts are
// shown in status and in refusals, so a write path must refuse anything
// that repaints a terminal or reorders text, and anything over 200
// characters, before it writes (spec 4.6). One row per write path.
func TestConfigRefusesControlCharacters(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"config", "badtext", "--description", "red\x1b[31m"}, "incoda: bad-text: description contains control characters"},
		{[]string{"config", "badtext", "--description", "a\u202eb"}, "incoda: bad-text: description contains control characters"},
		{[]string{"config", "badtext", "--close", "two\nlines"}, "incoda: bad-text: closed contains control characters"},
		{[]string{"config", "badtext", "--close", "tab\there"}, "incoda: bad-text: closed contains control characters"},
		{[]string{"config", "badtext", "--description", strings.Repeat("x", 201)}, "incoda: bad-text: description is longer than 200 characters"},
	} {
		out, code := runIncoda(t, incoda, state, c.args...)
		if code != 120 || !strings.Contains(out, c.want) {
			t.Errorf("%q: want exit 120 and %q, got %d:\n%s", c.args, c.want, code, out)
		}
	}
	if _, err := os.Stat(laneDir(state, "badtext")); !os.IsNotExist(err) {
		t.Fatal("a refused write must leave nothing behind")
	}
}

```

- [ ] **Step 2: Run the tests to see them fail**

Run (bash), one at a time:

- `go test ./internal/machine/ -run 'TestSets|TestWriteLink|TestConcurrentFirstLinksToOneSetNeverEscalate' -count=1 -timeout 120s`
- `go test ./internal/cli/ -run 'TestPoolsValue|TestLinkEdit' -count=1 -timeout 120s`
- `go test . -run 'TestConfigLinkFlags|TestConcurrentConfigFirstLinksNeverEscalate|TestConfigRefusesControlCharacters' -count=1 -timeout 120s`

Expected: the `internal/machine` test build fails (`undefined: SortedSet`, `undefined: WriteLink`, `undefined: lane.ErrNoChange`), the `internal/cli` one too (`undefined: poolsValue`, `undefined: linkEdit`), and the root `TestConfigLinkFlags` fails with `flag provided but not defined: -pool`.

- [ ] **Step 3: Implement**

`WriteLink` refuses a pool before it opens the lane, and creates the lane of a fresh project key (a first link is that key's first config). A newer config fails closed with `machine-state:`; any other unreadable config is the existing `queue "K": ...` exit 122.

In `internal/cli/config.go`, make these 4 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 4) Replace:

```go
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/textsafe"
)
```

with:

```go
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/textsafe"
)
```

(2 of 4) Replace:

```go
	requireReason := fs.Bool("require-reason", false, "refuse a run that has no --reason")
	closeMsg := fs.String("close", "", "refuse every run with this message, for a retired key that should name its replacements")
	open := fs.Bool("open", false, "clear a --close")
	noColor := fs.Bool("no-color", false, "never emit ANSI color, even on a terminal (the NO_COLOR environment variable does the same)")
	wait := &waitValue{d: time.Minute}
	fs.Var(wait, "wait", "how long to wait for machine.lock and a state upgrade: a Go duration (1m) or bare seconds; negative waits forever")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: incoda config KEY [--slots N] [--description TEXT] [--require-reason[=false]] [--close MSG | --open] [--wait DUR]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for config"}
	}
	if *slots < 0 {
		return usagef("--slots must be at least 1, got %d", *slots)
	}
	if *closeMsg != "" && *open {
		return usagef("--close and --open contradict each other")
	}
	for _, c := range []struct{ name, flag string }{{"description", "description"}, {"closed", "close"}} {
		set := false
		fs.Visit(func(f *flag.Flag) { set = set || f.Name == c.flag })
		if !set {
			continue
		}
		v := *desc
		if c.flag == "close" {
			v = *closeMsg
		}
		if err := textsafe.CheckWrite(c.name, v); err != nil {
			return usagef("%v", err)
		}
	}
	key, err := resolveKey(*queue)
	if err != nil {
		return err
	}
	dir, _, err := mutatingState(start, wait.d, 200*time.Millisecond, procinfo.ParentChain(), stderr)
	if err != nil {
		return err
	}
	q, err := lane.Open(dir, key)
	if err != nil {
		return exitWith(ExitState, "%v", err)
	}
	defer q.Close()
	q.SetBudget(start, wait.d)

	apply := func(cfg *lane.Config) bool {
		changed := false
```

with:

```go
	requireReason := fs.Bool("require-reason", false, "refuse a run that has no --reason")
	closeMsg := fs.String("close", "", "refuse every run with this message, for a retired key that should name its replacements")
	open := fs.Bool("open", false, "clear a --close")
	pool := &poolsValue{}
	fs.Var(pool, "pool", "link the queue to these pools (comma-separated); on a linked queue only with --replace, which is the user's call")
	fs.Var(pool, "pools", "alias of --pool")
	replace := fs.Bool("replace", false, "with --pool: replace an existing link (the user's call)")
	addPool := &poolsValue{}
	fs.Var(addPool, "add-pool", "add these pools to the link (on an unlinked queue: the same as --pool)")
	removePool := &poolsValue{}
	fs.Var(removePool, "remove-pool", "remove these pools from the link (never the last one: use --unlink)")
	unlink := fs.Bool("unlink", false, "remove the link; runs on the queue are then refused until it is linked again")
	quietMachine := fs.Bool("quiet-machine", false, "every run on this queue takes quiet-machine: an exclusive ticket on every pool (--quiet-machine=false clears it)")
	noColor := fs.Bool("no-color", false, "never emit ANSI color, even on a terminal (the NO_COLOR environment variable does the same)")
	wait := &waitValue{d: time.Minute}
	fs.Var(wait, "wait", "how long to wait for machine.lock and a state upgrade: a Go duration (1m) or bare seconds; negative waits forever")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: incoda config KEY [--slots N] [--description TEXT] [--require-reason[=false]] [--close MSG | --open]\n"+
			"                   [--pool P,P [--replace] | --add-pool P,P | --remove-pool P,P | --unlink] [--quiet-machine[=false]] [--wait DUR]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for config"}
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if *slots < 0 {
		return usagef("--slots must be at least 1, got %d", *slots)
	}
	if *closeMsg != "" && *open {
		return usagef("--close and --open contradict each other")
	}
	edit := linkEdit{replace: *replace, unlink: *unlink}
	if pool.set {
		edit.set = pool.keys
	}
	if addPool.set {
		edit.add = addPool.keys
	}
	if removePool.set {
		edit.remove = removePool.keys
	}
	if given["quiet-machine"] {
		edit.quiet = quietMachine
	}
	if err := edit.check(); err != nil {
		return err
	}
	if err := checkTexts(given, map[string]string{"description": *desc, "close": *closeMsg}); err != nil {
		return err
	}
	key, err := resolveKey(*queue)
	if err != nil {
		return err
	}
	chain := procinfo.ParentChain()
	dir, reg, err := mutatingState(start, wait.d, 200*time.Millisecond, chain, stderr)
	if err != nil {
		return err
	}

	apply := func(cfg *lane.Config) bool {
		changed := false
```

(3 of 4) Replace:

```go
		})
		return changed
	}
	var cfg lane.Config
	if apply(&lane.Config{}) {
		cfg, err = q.UpdateConfig(func(c *lane.Config) error { apply(c); return nil })
		if err == nil {
			q.Logf("queue=%s event=config pid=%d slots=%d require_reason=%v closed=%s", key, os.Getpid(), cfg.Slots, cfg.RequireReason, textsafe.LogValue(cfg.Closed))
		}
	} else {
		cfg, err = q.LoadConfig()
	}
	var ns *lane.NewerSchemaError
	if errors.As(err, &ns) {
		return exitWith(ExitState, "machine-state: %v", err)
	}
	if err != nil {
		return exitWith(ExitState, "queue %q: %v", key, err)
	}

	p := paletteFor(stdout, *noColor)
```

with:

```go
		})
		return changed
	}
	logConfig := func(cfg lane.Config) {
		lane.AppendLog(lane.LaneDir(dir, key), "queue=%s event=config pid=%d slots=%d require_reason=%v closed=%s", key, os.Getpid(), cfg.Slots, cfg.RequireReason, textsafe.LogValue(cfg.Closed))
	}
	var cfg lane.Config
	switch {
	case edit.any():
		// A link write (spec 4.3, 4.4): machine.lock, then the lane's
		// registry lock, one load-modify-store, the pools checked against
		// machine.json under the lock. Other fields given in the same
		// command are written in the same step.
		res, err := machine.WriteLink(dir, key, "config", machine.Options{
			Start: start, Wait: wait.d, Poll: 200 * time.Millisecond, Chain: chain, Stderr: stderr,
		}, func(reg *machine.Registry, c *lane.Config) error {
			if bad := reg.NotPools(edit.named()); len(bad) > 0 {
				return machine.NotAPool(reg, bad)
			}
			pools, quiet, err := edit.apply(key, *c)
			if err != nil {
				return err
			}
			other := apply(c)
			if machine.SameSet(pools, c.Pools) && quiet == c.QuietMachine && !other {
				return lane.ErrNoChange
			}
			c.Pools, c.QuietMachine = pools, quiet
			return nil
		})
		if err != nil {
			return machineExit(err)
		}
		reg, cfg = res.Registry, res.New
		if res.Changed && apply(&lane.Config{}) {
			logConfig(cfg)
		}
		switch {
		case !machine.SameSet(res.Old.Pools, res.New.Pools):
			fmt.Fprintf(stdout, "link: %s -> %s\n", machine.SetText(res.Old.Pools), machine.SetText(res.New.Pools))
		case edit.touchesPools() && len(res.New.Pools) == 0:
			fmt.Fprintf(stderr, "incoda: already unlinked: %s\n", key)
		case edit.touchesPools():
			fmt.Fprintf(stderr, "incoda: already linked: %s -> %s\n", key, machine.SetText(res.New.Pools))
		}
	case apply(&lane.Config{}):
		q, err := lane.Open(dir, key)
		if err != nil {
			return exitWith(ExitState, "%v", err)
		}
		q.SetBudget(start, wait.d)
		cfg, err = q.UpdateConfig(func(c *lane.Config) error { apply(c); return nil })
		q.Close()
		if err == nil {
			logConfig(cfg)
		}
		if err := configError(key, err); err != nil {
			return err
		}
	default:
		cfg, err = lane.ReadConfig(lane.LaneDir(dir, key))
		if err := configError(key, err); err != nil {
			return err
		}
	}

	p := paletteFor(stdout, *noColor)
```

(4 of 4) Replace:

```go
	} else {
		fmt.Fprintf(stdout, "  %s %s\n", p.Dim("closed:"), p.BoldRed(textsafe.Escape(cfg.Closed)))
	}
	return nil
}

```

with:

```go
	} else {
		fmt.Fprintf(stdout, "  %s %s\n", p.Dim("closed:"), p.BoldRed(textsafe.Escape(cfg.Closed)))
	}
	if reg.IsPool(key) {
		fmt.Fprintf(stdout, "  %s pool\n", p.Dim("kind:"))
		return nil
	}
	fmt.Fprintf(stdout, "  %s project\n", p.Dim("kind:"))
	if len(cfg.Pools) == 0 {
		fmt.Fprintf(stdout, "  %s %s\n", p.Dim("pools:"), p.Yellow("(none: unlinked, runs are refused until it is linked)"))
	} else {
		fmt.Fprintf(stdout, "  %s %s\n", p.Dim("pools:"), textsafe.Escape(machine.SetText(cfg.Pools)))
	}
	fmt.Fprintf(stdout, "  %s %s\n", p.Dim("quiet machine:"), yesNo(cfg.QuietMachine))
	return nil
}

// configError maps a config read or write error to its exit: 122, with the
// machine-state prefix for a config written by a newer incoda.
func configError(key string, err error) error {
	var ns *lane.NewerSchemaError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ns):
		return exitWith(ExitState, "machine-state: %v", err)
	}
	return exitWith(ExitState, "queue %q: %v", key, err)
}

// textFields are the free-text flags a setup command writes into a config,
// with the field name a bad-text refusal names (spec 4.6).
var textFields = []struct{ flag, field string }{
	{"description", "description"},
	{"close", "closed"},
}

// checkTexts refuses, before anything is written, a text flag the caller
// gave whose value carries control, bidi or invalid UTF-8 characters or is
// longer than 200 characters.
func checkTexts(given map[string]bool, values map[string]string) error {
	for _, f := range textFields {
		if !given[f.flag] {
			continue
		}
		if err := textsafe.CheckWrite(f.field, values[f.flag]); err != nil {
			return usagef("%v", err)
		}
	}
	return nil
}

```

Create `internal/cli/linkflags.go`:

```go
package cli

import (
	"fmt"
	"strings"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
)

// poolsValue parses a comma-separated set of keys (--pool and its alias
// --pools, --add-pool, --remove-pool): order does not matter and
// duplicates are dropped, so the result is sorted.
type poolsValue struct {
	keys []string
	set  bool
}

func (v *poolsValue) String() string {
	if v == nil {
		return ""
	}
	return strings.Join(v.keys, ",")
}

func (v *poolsValue) Set(s string) error {
	var keys []string
	for _, k := range strings.Split(s, ",") {
		k = strings.TrimSpace(k)
		if k == "" {
			return fmt.Errorf("empty key in the list %q", s)
		}
		if err := lane.ValidateKey(k); err != nil {
			return err
		}
		keys = append(keys, k)
	}
	v.keys, v.set = machine.SortedSet(keys), true
	return nil
}

// linkEdit is what config's link flags ask for (spec 4.3).
type linkEdit struct {
	set, add, remove []string // nil when the flag is absent
	replace, unlink  bool
	quiet            *bool // nil when --quiet-machine is absent
}

// touchesPools reports whether the edit changes the pools of the link.
func (e linkEdit) touchesPools() bool {
	return e.set != nil || e.add != nil || e.remove != nil || e.unlink
}

// any reports whether the edit is a link write at all.
func (e linkEdit) any() bool { return e.touchesPools() || e.quiet != nil }

// check refuses flag combinations that contradict each other.
func (e linkEdit) check() error {
	switch {
	case e.set != nil && (e.add != nil || e.remove != nil || e.unlink):
		return usagef("--pool sets the whole link; it does not combine with --add-pool, --remove-pool or --unlink")
	case e.unlink && (e.add != nil || e.remove != nil):
		return usagef("--unlink removes the link; it does not combine with --add-pool or --remove-pool")
	case e.replace && e.set == nil:
		return usagef("--replace goes with --pool")
	}
	return nil
}

// named is every pool the edit adds to a link; each must be registered.
func (e linkEdit) named() []string { return append(append([]string(nil), e.set...), e.add...) }

// apply computes the link the edit leaves on a lane whose current config
// is cur: --pool on an unlinked lane sets it, and on a linked one only with
// --replace (else link-exists), unless it names the same set; --add-pool
// on an unlinked lane is --pool; --remove-pool never leaves an empty link.
func (e linkEdit) apply(key string, cur lane.Config) ([]string, bool, error) {
	pools := machine.SortedSet(cur.Pools)
	linked := len(pools) > 0
	switch {
	case e.set != nil:
		if linked && !machine.SameSet(pools, e.set) && !e.replace {
			return nil, false, &machine.Refusal{Msg: fmt.Sprintf("link-exists: %q is linked to %s; changing a link is the user's call: ask them", key, machine.SetText(pools))}
		}
		pools = machine.SortedSet(e.set)
	case e.unlink:
		pools = nil
	default:
		if e.add != nil {
			pools = machine.SortedSet(append(pools, e.add...))
		}
		if e.remove != nil && linked {
			gone := map[string]bool{}
			for _, k := range e.remove {
				gone[k] = true
			}
			var left []string
			for _, k := range pools {
				if !gone[k] {
					left = append(left, k)
				}
			}
			if len(left) == 0 {
				return nil, false, &machine.Refusal{Msg: fmt.Sprintf("--remove-pool would leave %q linked to no pool; use --unlink to remove the link", key)}
			}
			pools = left
		}
	}
	quiet := cur.QuietMachine
	if e.quiet != nil {
		quiet = *e.quiet
	}
	return pools, quiet, nil
}
```

In `internal/lane/config.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 2) Replace:

```go
	return c, nil
}

// UpdateConfig loads the config, applies fn and stores the result, all
// inside one hold of the registry lock, so two writers changing different
// fields cannot lose each other's change. The write goes through a temp
```

with:

```go
	return c, nil
}

// ErrNoChange, returned by an UpdateConfig callback, means "nothing to
// write": UpdateConfig stores nothing and returns the config it read with
// ErrNoChange, so a compare-and-set that finds the value already in place
// rewrites nothing.
var ErrNoChange = errors.New("no change")

// UpdateConfig loads the config, applies fn and stores the result, all
// inside one hold of the registry lock, so two writers changing different
// fields cannot lose each other's change. The write goes through a temp
```

(2 of 2) Replace:

```go
			return err
		}
		if err := fn(&c); err != nil {
			return err
		}
		c.Schema = ConfigSchema
```

with:

```go
			return err
		}
		if err := fn(&c); err != nil {
			if errors.Is(err, ErrNoChange) {
				out = c
			}
			return err
		}
		c.Schema = ConfigSchema
```

Create `internal/machine/link.go`:

```go
package machine

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/deblasis/incoda/internal/lane"
)

// SortedSet returns names sorted with duplicates dropped; nil stays nil.
func SortedSet(names []string) []string {
	if names == nil {
		return nil
	}
	seen := map[string]bool{}
	out := []string{}
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// SameSet reports whether a and b name the same keys, in any order.
func SameSet(a, b []string) bool {
	a, b = SortedSet(a), SortedSet(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Subset reports whether every key of a is in b.
func Subset(a, b []string) bool {
	in := map[string]bool{}
	for _, k := range b {
		in[k] = true
	}
	for _, k := range a {
		if !in[k] {
			return false
		}
	}
	return true
}

// SetText renders a set of pools the way links are printed: sorted and
// comma-separated, or "(none)".
func SetText(names []string) string {
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(SortedSet(names), ",")
}

// NotPools returns the names in names that are not registered pools,
// sorted.
func (r *Registry) NotPools(names []string) []string {
	var out []string
	for _, n := range SortedSet(names) {
		if !r.IsPool(n) {
			out = append(out, n)
		}
	}
	return out
}

// NotAPool is the refusal for a name that is not a registered pool where
// one is required (a link, a --pool set).
func NotAPool(r *Registry, names []string) *Refusal {
	what := fmt.Sprintf("%q is not a pool", names[0])
	if len(names) > 1 {
		quoted := make([]string, len(names))
		for i, n := range names {
			quoted[i] = fmt.Sprintf("%q", n)
		}
		what = strings.Join(quoted, ", ") + " are not pools"
	}
	return &Refusal{Msg: fmt.Sprintf("pool-mismatch: %s on this machine (pools: %s)", what, strings.Join(r.Pools, ", "))}
}

// LinkResult is what a link write found and left.
type LinkResult struct {
	// Old and New are the lane's config before and after; they are equal
	// when nothing was written.
	Old, New lane.Config
	// Changed is set when the config was written.
	Changed bool
	// Registry is machine.json as read under machine.lock.
	Registry *Registry
}

// WriteLink changes key's link (its pools and quiet_machine, and any other
// field fn sets in the same step) as spec 4.4 orders it: machine.lock
// first, then key's registry lock, one load-modify-store inside that hold.
// Under machine.lock it re-reads machine.json and refuses a key that is a
// pool (pools never link and carry no quiet_machine). fn gets the registry
// and the config read inside the hold, so a compare-and-set sees the value
// a concurrent writer left; it returns lane.ErrNoChange to write nothing,
// or a refusal. When the pools change, event=link by=<by> old=<pools>
// new=<pools> goes to the lane's lane.log.
//
// The lane is created when missing (a link on a fresh key is a project
// lane's first config). Registry lock waits stay inside o's --wait budget.
func WriteLink(stateDir, key, by string, o Options, fn func(reg *Registry, c *lane.Config) error) (LinkResult, error) {
	lk, err := AcquireLock(stateDir, o.lockOptions("link"))
	if err != nil {
		return LinkResult{}, err
	}
	defer lk.Release()
	reg, err := ReadRegistry(stateDir)
	if err != nil {
		return LinkResult{}, err
	}
	if reg.IsPool(key) {
		return LinkResult{}, &Refusal{Msg: fmt.Sprintf("pool-mismatch: %q is a pool; a pool never links other pools and carries no quiet_machine", key)}
	}
	q, err := lane.Open(stateDir, key)
	if err != nil {
		return LinkResult{}, stateErrorf("cannot open queue %q: %s", key, esc(err))
	}
	defer q.Close()
	q.SetBudget(o.Start, o.Wait)
	res := LinkResult{Registry: reg}
	cfg, err := q.UpdateConfig(func(c *lane.Config) error {
		res.Old = *c
		res.Old.Pools = append([]string(nil), c.Pools...)
		return fn(reg, c)
	})
	if errors.Is(err, lane.ErrNoChange) {
		res.New = cfg
		return res, nil
	}
	if err != nil {
		var rf *Refusal
		var ns *lane.NewerSchemaError
		switch {
		case errors.As(err, &rf):
			return res, err
		case errors.As(err, &ns):
			return res, stateErrorf("%s", esc(err))
		}
		return res, &StateError{Msg: fmt.Sprintf("queue %q: %s", key, esc(err))}
	}
	res.New, res.Changed = cfg, true
	if !SameSet(res.Old.Pools, cfg.Pools) {
		q.Logf("queue=%s event=link pid=%d by=%s old=%s new=%s", key, os.Getpid(), by,
			strings.Join(SortedSet(res.Old.Pools), ","), strings.Join(SortedSet(cfg.Pools), ","))
	}
	return res, nil
}

// LastLinker reads the pid of the last event=link line in a lane's log, so
// a lost compare-and-set can name the process that won it. It reports
// false when the log has none.
func LastLinker(stateDir, key string) (int, bool) {
	b, err := os.ReadFile(lane.LogPath(lane.LaneDir(stateDir, key)))
	if err != nil {
		return 0, false
	}
	lines := strings.Split(string(b), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if !strings.Contains(lines[i], " event=link ") {
			continue
		}
		for _, f := range strings.Fields(lines[i]) {
			if v, ok := strings.CutPrefix(f, "pid="); ok {
				var pid int
				if _, err := fmt.Sscanf(v, "%d", &pid); err == nil && pid > 0 {
					return pid, true
				}
			}
		}
	}
	return 0, false
}

// LinkedLine is the informational line of a link a run or init wrote:
// "<key> -> <pools>", plus ", quiet_machine" when that is set too.
func LinkedLine(key string, pools []string, quiet bool) string {
	s := key + " -> " + SetText(pools)
	if quiet {
		s += ", quiet_machine"
	}
	return s
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run (bash), one at a time:

- `go test ./internal/machine/ -run 'TestSets|TestWriteLink|TestConcurrentFirstLinksToOneSetNeverEscalate' -count=1`
- `go test ./internal/cli/ -run 'TestPoolsValue|TestLinkEdit' -count=1`
- `go test . -run 'TestConfigLinkFlags|TestConcurrentConfigFirstLinksNeverEscalate|TestConfigRefusesControlCharacters' -count=1`

Expected: `ok` for each package.

- [ ] **Step 5: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 6: Commit**

```bash
git add config_test.go internal/cli/config.go internal/cli/linkflags.go internal/cli/linkflags_test.go internal/lane/config.go internal/machine/link.go internal/machine/link_test.go
git commit -F - <<'MSG'
feat: config links a queue to pools under machine.lock

config --pool, --replace, --add-pool, --remove-pool, --unlink and
--quiet-machine write a project lane's link as a compare-and-set under
machine.lock and the lane's registry lock, validated against
machine.json. The same set is an already linked no-op, a different one
needs --replace, a pool never links, and every change is echoed and
logged as event=link. The text checks of config are one table.
MSG
```


---

### Task 4: A lane closed while a run waits on it ends that run

Spec 2.5: `closed` is re-checked after each enroll (Enroll checks it under the registry lock) and on every poll while waiting (the poll re-reads `config.json`); a lane that becomes closed while a run waits on it, a pool included, ends that run: it releases everything and exits 120 with `incoda: closed-while-waiting: "<key>": <closed text>`. Enroll also refuses a missing `--reason` on a lane that requires one (spec 4.5, for pools reached through a link) and, as plan 1 carried forward, fails closed on a config written by a newer incoda; the waiting poll surfaces a newer config as exit 122. The integration test uses the `tests` pool directly, so it needs no link.

**Files:**
- Modify: `internal/cli/run.go`
- Modify: `internal/lane/acquire.go`
- Modify: `internal/lane/config.go`
- Modify: `internal/lane/queue.go`
- Test: `config_test.go`
- Test: `internal/lane/config_test.go`
- Test: `internal/lane/kill_test.go`

**Interfaces:**
- Consumes: `lane.Acquire`, `lane.AcquireOptions` (plan 2b), `lane.NewerSchemaError` (plan 1).
- Produces: `type lane.ClosedError struct{ Key, Text string }` (`queue "K" is closed: <escaped text>`), `type lane.ReasonRequiredError struct{ Key string }`; `AcquireOptions.Check func() error` (called every poll before the position; its error ends the wait); `cli.waitingCheck(laneDir, key string) error`.

- [ ] **Step 1: Write the failing tests**

Append to the end of `internal/lane/config_test.go`:

```go

// TestEnrollRefusesClosedReasonlessAndNewer: Enroll checks the lane's rules
// under the registry lock (spec 2.5, 4.5), so a run that read the config
// before it changed is still refused, and creates no ticket. A config
// written by a newer incoda fails closed.
func TestEnrollRefusesClosedReasonlessAndNewer(t *testing.T) {
	q, err := Open(t.TempDir(), "rules")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	tickets := func() int {
		n := 0
		entries, _ := os.ReadDir(q.Dir)
		for _, e := range entries {
			if _, ok := parseTicketName(e.Name()); ok {
				n++
			}
		}
		return n
	}
	if err := q.SaveConfig(Config{Closed: "maintenance"}); err != nil {
		t.Fatal(err)
	}
	var ce *ClosedError
	if _, err := q.Enroll(Ticket{Reason: "r"}); !errors.As(err, &ce) || err.Error() != `queue "rules" is closed: maintenance` {
		t.Fatalf("closed: %v", err)
	}
	if err := q.SaveConfig(Config{RequireReason: true}); err != nil {
		t.Fatal(err)
	}
	var re *ReasonRequiredError
	if _, err := q.Enroll(Ticket{Reason: "  "}); !errors.As(err, &re) || !strings.HasPrefix(err.Error(), `queue "rules" requires --reason: `) {
		t.Fatalf("reason: %v", err)
	}
	if n := tickets(); n != 0 {
		t.Fatalf("a refused enrollment left %d ticket(s)", n)
	}
	en, err := q.Enroll(Ticket{Reason: "nightly"})
	if err != nil {
		t.Fatalf("with a reason it enrolls: %v", err)
	}
	en.Release(0)
	if err := os.WriteFile(filepath.Join(q.Dir, "config.json"), []byte(`{"schema":9}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var ns *NewerSchemaError
	if _, err := q.Enroll(Ticket{Reason: "r"}); !errors.As(err, &ns) {
		t.Fatalf("newer: %v", err)
	}
}
```

Append to the end of `internal/lane/kill_test.go`:

```go

// TestAcquireCheckEndsTheWait: the per-poll Check ends a wait with its
// error, as a lane closed while the run waits on it does.
func TestAcquireCheckEndsTheWait(t *testing.T) {
	q, err := Open(t.TempDir(), "check")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	h, err := q.Enroll(Ticket{})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Release(0)
	w, err := q.Enroll(Ticket{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Release(0)
	stop := errors.New("closed while waiting")
	polls := 0
	err = w.Acquire(context.Background(), AcquireOptions{Wait: 5 * time.Second, Poll: 10 * time.Millisecond, Check: func() error {
		if polls++; polls == 3 {
			return stop
		}
		return nil
	}})
	if !errors.Is(err, stop) || polls != 3 {
		t.Fatalf("Acquire = %v after %d polls", err, polls)
	}
}
```

In `config_test.go`, replace:

```go
	}
}

func TestRequireReason(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
```

with:

```go
	}
}

// TestClosedWhileWaiting: a lane closed while a run waits on it ends that
// run on its next poll with closed-while-waiting (exit 120) and nothing
// left behind; the holder already admitted keeps running. A config written
// by a newer incoda while a run waits fails it closed (122). The tests pool
// is used directly, so no link is involved.
func TestClosedWhileWaiting(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	holder, _ := startHolder(t, incoda, stamp, state, "tests", "holder", 4000, "50ms")
	defer func() { _ = holder.Process.Kill(); _ = holder.Wait() }()
	waitFor(t, incoda, state, "tests", func(q queueReport) bool { return len(q.Holders) == 1 })

	wait := func(t *testing.T) (*exec.Cmd, *syncBuffer) {
		t.Helper()
		var out syncBuffer
		w := exec.Command(incoda, "run", "--queue", "tests", "--wait", "60s", "--poll", "50ms",
			"--", stamp, filepath.Join(t.TempDir(), "w.txt"), "w", "10")
		w.Env = laneEnv(state)
		w.Stdout, w.Stderr = &out, &out
		if err := w.Start(); err != nil {
			t.Fatal(err)
		}
		waitFor(t, incoda, state, "tests", func(q queueReport) bool { return len(q.Waiting) == 1 })
		return w, &out
	}
	w, out := wait(t)
	if o, code := runIncoda(t, incoda, state, "config", "tests", "--close", "maintenance"); code != 0 {
		t.Fatalf("close: %d\n%s", code, o)
	}
	if code := exitCodeOf(w.Wait()); code != 120 || !strings.Contains(out.String(), `incoda: closed-while-waiting: "tests": maintenance`) {
		t.Fatalf("want exit 120 closed-while-waiting, got %d:\n%s", code, out.String())
	}
	if n := countTickets(t, state, "tests"); n != 1 {
		t.Fatalf("only the holder's ticket stays, found %d", n)
	}
	if o, code := runIncoda(t, incoda, state, "config", "tests", "--open"); code != 0 {
		t.Fatalf("open: %d\n%s", code, o)
	}

	w, out = wait(t)
	if err := os.WriteFile(filepath.Join(laneDir(state, "tests"), "config.json"), []byte(`{"schema":9,"slots":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := exitCodeOf(w.Wait()); code != 122 || !strings.Contains(out.String(), "incoda: machine-state: ") || !strings.Contains(out.String(), "newer incoda") {
		t.Fatalf("want exit 122 machine-state, got %d:\n%s", code, out.String())
	}
	if err := holder.Wait(); err != nil {
		t.Fatalf("the admitted holder must finish normally: %v", err)
	}
}

func TestRequireReason(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
```

- [ ] **Step 2: Run the tests to see them fail**

Run (bash), one at a time:

- `go test ./internal/lane/ -run 'TestEnrollRefusesClosedReasonlessAndNewer|TestAcquireCheckEndsTheWait' -count=1 -timeout 120s`
- `go test . -run 'TestClosedWhileWaiting' -count=1 -timeout 120s`

Expected: the `internal/lane` test build fails (`undefined: ClosedError`, `undefined: ReasonRequiredError`, `unknown field Check in struct literal of type AcquireOptions`); the root `TestClosedWhileWaiting` fails with `want exit 120 closed-while-waiting, got -1`: the waiter kept waiting and acquired `tests` once the holder left.

- [ ] **Step 3: Implement**

A malformed (not newer) config during a wait is left to the admission rule, which already falls back to the safe width.

In `internal/cli/run.go`, make these 3 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 3) Replace:

```go
			// way, so it gets the same message and the same usage exit
			// rather than masquerading as unusable state.
			var sd *lane.SlotsDisagreement
			if errors.As(err, &sd) {
				rc = ExitUsage
				return usagef("%v", err)
			}
			if errors.Is(err, lane.ErrRegistryBusy) {
				rc = ExitTimeout
```

with:

```go
			// way, so it gets the same message and the same usage exit
			// rather than masquerading as unusable state.
			var sd *lane.SlotsDisagreement
			var ce *lane.ClosedError
			var re *lane.ReasonRequiredError
			var ns *lane.NewerSchemaError
			switch {
			case errors.As(err, &sd), errors.As(err, &ce), errors.As(err, &re):
				rc = ExitUsage
				return usagef("%v", err)
			case errors.As(err, &ns):
				rc = ExitState
				return exitWith(ExitState, "machine-state: %v", err)
			}
			if errors.Is(err, lane.ErrRegistryBusy) {
				rc = ExitTimeout
```

(2 of 3) Replace:

```go
				return lane.KillRequest{}, false
			},
			Unpooled: countUnpooled,
			OnWait: func(pos, effSlots int, live []lane.Entry, waited time.Duration) {
				if *quiet {
					return
```

with:

```go
				return lane.KillRequest{}, false
			},
			Unpooled: countUnpooled,
			// A lane closed while this run waits on it ends the wait
			// (spec 2.5); the config is re-read without a lock (every
			// write is a rename). A config written by a newer incoda
			// fails closed.
			Check: func() error { return waitingCheck(pt.q.Dir, key) },
			OnWait: func(pos, effSlots int, live []lane.Entry, waited time.Duration) {
				if *quiet {
					return
```

(3 of 3) Replace:

```go
	return nil
}

// logKill records the kill on every queue the run held, next to the
// request the killer left, so the history reads request then outcome.
func logKill(parts []*lanePart, req lane.KillRequest) {
```

with:

```go
	return nil
}

// waitingCheck is the per-poll config check of a waiting run: closed while
// waiting refuses (exit 120), a config written by a newer incoda fails
// closed (122). A config that cannot be read is left to the admission
// rule, which falls back to the safe width.
func waitingCheck(laneDir, key string) error {
	cfg, err := lane.ReadConfig(laneDir)
	var ns *lane.NewerSchemaError
	switch {
	case errors.As(err, &ns):
		return &machine.StateError{Msg: "machine-state: " + textsafe.Escape(err.Error())}
	case err == nil && cfg.Closed != "":
		return &machine.Refusal{Msg: fmt.Sprintf("closed-while-waiting: %q: %s", key, textsafe.Escape(cfg.Closed))}
	}
	return nil
}

// logKill records the kill on every queue the run held, next to the
// request the killer left, so the history reads request then outcome.
func logKill(parts []*lanePart, req lane.KillRequest) {
```

In `internal/lane/acquire.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 2) Replace:

```go
	// it already holds: a kill addressed to one of those must end the wait
	// on the next one, not sit unread until every key is held.
	Killed func() (KillRequest, bool)
	// Unpooled, when set, is called on every poll after the position. It
	// returns how many of this lane's slots are held by holders that have
	// no ticket here: unpooled runs of an older incoda counted on a pool
```

with:

```go
	// it already holds: a kill addressed to one of those must end the wait
	// on the next one, not sit unread until every key is held.
	Killed func() (KillRequest, bool)
	// Check, when set, is called on every poll before the position. An
	// error ends the wait and is returned as is: a lane closed while this
	// run waits on it, a config written by a newer incoda (spec 2.5).
	Check func() error
	// Unpooled, when set, is called on every poll after the position. It
	// returns how many of this lane's slots are held by holders that have
	// no ticket here: unpooled runs of an older incoda counted on a pool
```

(2 of 2) Replace:

```go
		if opt.Killed != nil {
			if req, ok := opt.Killed(); ok {
				return &KilledError{Request: req}
			}
		}
		idx, slots, live, err := e.Position()
```

with:

```go
		if opt.Killed != nil {
			if req, ok := opt.Killed(); ok {
				return &KilledError{Request: req}
			}
		}
		if opt.Check != nil {
			if err := opt.Check(); err != nil {
				return err
			}
		}
		idx, slots, live, err := e.Position()
```

In `internal/lane/config.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 2) Replace:

```go
	"path/filepath"

	"github.com/deblasis/incoda/internal/atomicfile"
)

const configName = "config.json"
```

with:

```go
	"path/filepath"

	"github.com/deblasis/incoda/internal/atomicfile"
	"github.com/deblasis/incoda/internal/textsafe"
)

const configName = "config.json"
```

(2 of 2) Replace:

```go
		e.Key, e.Configured, e.Asked, advice)
}

// LoadConfig reads the queue's config. A missing file is the zero Config
// and no error; a file that cannot be parsed is an error, because a queue
// that silently forgot it was closed would let the old key back in.
```

with:

```go
		e.Key, e.Configured, e.Asked, advice)
}

// ClosedError is Enroll's refusal on a closed lane. Enroll checks under the
// registry lock, so a lane closed between a run's plan and its enrollment
// still refuses it (spec 2.5).
type ClosedError struct{ Key, Text string }

func (e *ClosedError) Error() string {
	return fmt.Sprintf("queue %q is closed: %s", e.Key, textsafe.Escape(e.Text))
}

// ReasonRequiredError is Enroll's refusal of a ticket with no reason on a
// lane that requires one (spec 4.5).
type ReasonRequiredError struct{ Key string }

func (e *ReasonRequiredError) Error() string {
	return fmt.Sprintf("queue %q requires --reason: say what this job is so status can answer \"whose is that and why\"", e.Key)
}

// LoadConfig reads the queue's config. A missing file is the zero Config
// and no error; a file that cannot be parsed is an error, because a queue
// that silently forgot it was closed would let the old key back in.
```

In `internal/lane/queue.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 2) Replace:

```go
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/deblasis/incoda/internal/lockfile"
```

with:

```go
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/lockfile"
```

(2 of 2) Replace:

```go
		// --exclusive, which is explicit, visible in status, and still
		// narrows to 1 on purpose. A missing or broken config leaves an
		// unset count at 1, the safe direction.
		cfg, cfgErr := q.LoadConfig()
		switch {
		case cfgErr == nil && cfg.Slots > 0 && t.Slots >= 1 && t.Slots != cfg.Slots:
			return NewSlotsDisagreement(q.Key, cfg.Slots, t.Slots, t.Exclusive)
```

with:

```go
		// --exclusive, which is explicit, visible in status, and still
		// narrows to 1 on purpose. A missing or broken config leaves an
		// unset count at 1, the safe direction.
		//
		// The rules are checked here, under the registry lock, as well as
		// before: a lane closed, or made to require a reason, after the
		// run read its config still refuses it (spec 2.5, 4.5). A config
		// written by a newer incoda may carry rules this binary does not
		// know, so it fails closed.
		cfg, cfgErr := q.LoadConfig()
		var ns *NewerSchemaError
		switch {
		case errors.As(cfgErr, &ns):
			return cfgErr
		case cfgErr == nil && cfg.Closed != "":
			return &ClosedError{Key: q.Key, Text: cfg.Closed}
		case cfgErr == nil && cfg.RequireReason && strings.TrimSpace(t.Reason) == "":
			return &ReasonRequiredError{Key: q.Key}
		}
		switch {
		case cfgErr == nil && cfg.Slots > 0 && t.Slots >= 1 && t.Slots != cfg.Slots:
			return NewSlotsDisagreement(q.Key, cfg.Slots, t.Slots, t.Exclusive)
```

- [ ] **Step 4: Run the tests to see them pass**

Run (bash), one at a time:

- `go test ./internal/lane/ -run 'TestEnrollRefusesClosedReasonlessAndNewer|TestAcquireCheckEndsTheWait' -count=1`
- `go test . -run 'TestClosedWhileWaiting' -count=1`

Expected: `ok` for each package.

- [ ] **Step 5: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 6: Commit**

```bash
git add config_test.go internal/cli/run.go internal/lane/acquire.go internal/lane/config.go internal/lane/config_test.go internal/lane/kill_test.go internal/lane/queue.go
git commit -F - <<'MSG'
feat: a lane closed while a run waits on it ends that run

Enroll refuses a closed lane, a missing reason on a lane that requires
one, and a config written by a newer incoda, all under the registry
lock. A waiting run re-reads its lane's config on every poll: closed
ends it with closed-while-waiting (exit 120), a newer config fails it
closed (exit 122), and everything it took is released.
MSG
```


---

### Task 5: runplan: the lane set in the total order, and its rules

Spec 2.4 and 4.5. A new package computes what a run takes: the named keys plus each named project key's linked pools, deduplicated, ordered project lanes first, then pools, each by key; every linked pool must resolve to a registered pool with a readable config (else exit 122 `machine-state: queue "<project>" links "<pool>": <reason>`); and the rules: a closed named key first, then for every lane in order closed (linked pools), `require_reason` and the `--slots` disagreement (named keys only; a named pool is checked against its configured count, 1 when unset, never written). A pool reached through a link carries `via` (the named project keys whose link brings it), which gives the refusal and busy texts their path: `queue "vm" is closed: maintenance (pool, via cap-e2e)`. A pool an ancestor holds (P) and the run does not name is passed through and not re-checked. The package reads `machine.json` and `config.json` files only: it never opens, creates or locks a lane. Unlinked keys still plan as before (no pools); Task 7 refuses them. Nothing calls the package yet; Task 6 does.

**Files:**
- Create: `internal/runplan/runplan.go`
- Test: `internal/runplan/runplan_test.go` (new)

**Interfaces:**
- Consumes: `machine.Registry`, `machine.ReadRegistry`, `machine.SortedSet` (Task 3), `lane.ReadConfig`, `lane.NewSlotsDisagreement`.
- Produces: package `runplan`: `type Request struct { Named []string; Slots int; Exclusive bool; Reason string; Held map[string]bool }`; `type Lane struct { Key string; Pool, Named bool; Via []string; Cfg lane.Config }` with `Role() string` (`""`, `"pool"`, `"pool, via a,b"`) and `StatusKey() string`; `type Plan struct { Lanes []Lane; Generation int64; Links map[string][]string }`; `func Less(a, b Lane) bool`; `func Make(stateDir string, reg *machine.Registry, req Request) (*Plan, error)` (errors are `*machine.Refusal` or `*machine.StateError`).

- [ ] **Step 1: Write the failing tests**

The test writes `machine.json` and lane configs directly (`machineDir`), so it needs no migration.

Create `internal/runplan/runplan_test.go`:

```go
package runplan

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
)

// machineDir writes a migrated-looking state directory: machine.json with
// the four bootstrap pools at generation 3, and a config.json per lane in
// configs (a nil value writes no file).
func machineDir(t *testing.T, configs map[string]string) (string, *machine.Registry) {
	t.Helper()
	state := t.TempDir()
	reg := `{"schema":1,"layout":2,"generation":3,"pools":["builds","computer-use","tests","vm"]}`
	if err := os.WriteFile(machine.RegistryPath(state), []byte(reg), 0o644); err != nil {
		t.Fatal(err)
	}
	for k, body := range configs {
		dir := lane.LaneDir(state, k)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := machine.ReadRegistry(state)
	if err != nil {
		t.Fatal(err)
	}
	return state, r
}

func keysOf(p *Plan) string {
	var out []string
	for _, l := range p.Lanes {
		s := l.Key
		if r := l.Role(); r != "" {
			s += "(" + r + ")"
		}
		out = append(out, s)
	}
	return strings.Join(out, " ")
}

// TestLaneSetAndTotalOrder: the named keys plus each named project's
// linked pools, deduplicated, project lanes first then pools, each sorted
// (spec 2.4).
func TestLaneSetAndTotalOrder(t *testing.T) {
	state, reg := machineDir(t, map[string]string{
		"kungfoo-gate": `{"schema":2,"pools":["tests"]}`,
		"cap-e2e":      `{"schema":2,"pools":["tests","computer-use"]}`,
		"builds":       `{"schema":2,"slots":1}`,
	})
	for _, c := range []struct {
		named []string
		want  string
	}{
		{[]string{"kungfoo-gate"}, "kungfoo-gate tests(pool, via kungfoo-gate)"},
		{[]string{"kungfoo-gate", "builds"}, "kungfoo-gate builds(pool) tests(pool, via kungfoo-gate)"},
		{[]string{"kungfoo-gate", "cap-e2e"}, "cap-e2e kungfoo-gate computer-use(pool, via cap-e2e) tests(pool, via cap-e2e,kungfoo-gate)"},
		{[]string{"tests", "kungfoo-gate"}, "kungfoo-gate tests(pool, via kungfoo-gate)"},
		{[]string{"plain"}, "plain"},
	} {
		p, err := Make(state, reg, Request{Named: c.named})
		if err != nil {
			t.Fatalf("%v: %v", c.named, err)
		}
		if got := keysOf(p); got != c.want {
			t.Errorf("%v:\n got %s\nwant %s", c.named, got, c.want)
		}
		if p.Generation != 3 {
			t.Errorf("generation %d", p.Generation)
		}
	}
	p, _ := Make(state, reg, Request{Named: []string{"tests", "kungfoo-gate"}})
	if l := p.Lanes[1]; !l.Named || !l.Pool || l.StatusKey() != "kungfoo-gate" {
		t.Fatalf("a pool named and linked is both: %+v", l)
	}
	if strings.Join(p.Links["kungfoo-gate"], ",") != "tests" {
		t.Fatalf("links: %v", p.Links)
	}
}

// TestMakeRules: closed, require_reason and --slots, with the pool path in
// the text; a pool an ancestor holds is not re-checked; links that do not
// resolve and newer configs fail closed; nothing is created.
func TestMakeRules(t *testing.T) {
	state, reg := machineDir(t, map[string]string{
		"cap-e2e":       `{"schema":2,"pools":["computer-use","vm"]}`,
		"vm":            `{"schema":2,"slots":1,"closed":"maintenance"}`,
		"builds":        `{"schema":2,"slots":1,"require_reason":true}`,
		"kungfoo-build": `{"schema":2,"pools":["builds"],"slots":2}`,
		"dangling":      `{"schema":2,"pools":["printer"]}`,
		"broken-link":   `{"schema":2,"pools":["tests"]}`,
		"tests":         `{`,
		"newer":         `{"schema":9}`,
		"shut":          `{"schema":2,"closed":"use cap-e2e","pools":["vm"]}`,
	})
	for _, c := range []struct {
		req   Request
		state bool
		want  string
	}{
		{Request{Named: []string{"cap-e2e"}}, false, `queue "vm" is closed: maintenance (pool, via cap-e2e)`},
		{Request{Named: []string{"vm"}}, false, `queue "vm" is closed: maintenance (pool)`},
		{Request{Named: []string{"shut"}}, false, `queue "shut" is closed: use cap-e2e`},
		{Request{Named: []string{"kungfoo-build"}}, false, `queue "builds" requires --reason (pool, via kungfoo-build): say what this job is so status can answer "whose is that and why"`},
		{Request{Named: []string{"builds"}, Reason: "x", Slots: 2}, false, `queue "builds" is configured for 1 slot(s); --slots 2 is not allowed to disagree.`},
		{Request{Named: []string{"kungfoo-build"}, Reason: "x", Slots: 1}, false, `queue "kungfoo-build" is configured for 2 slot(s); --slots 1 is not allowed to disagree.`},
		{Request{Named: []string{"dangling"}}, true, `machine-state: queue "dangling" links "printer": it is not a pool on this machine`},
		{Request{Named: []string{"broken-link"}}, true, `machine-state: queue "broken-link" links "tests": config `},
		{Request{Named: []string{"newer"}}, true, "machine-state: "},
	} {
		_, err := Make(state, reg, c.req)
		var rf *machine.Refusal
		var se *machine.StateError
		switch {
		case c.state && errors.As(err, &se) && strings.HasPrefix(se.Msg, c.want):
		case !c.state && errors.As(err, &rf) && strings.HasPrefix(rf.Msg, c.want):
		default:
			t.Errorf("%v: got %v, want %q", c.req.Named, err, c.want)
		}
	}
	if _, err := Make(state, reg, Request{Named: []string{"kungfoo-build"}, Reason: "x", Slots: 2}); err != nil {
		t.Fatalf("--slots agreeing with the project lane is fine and never checked against its pools: %v", err)
	}
	if _, err := Make(state, reg, Request{Named: []string{"cap-e2e"}, Held: map[string]bool{"vm": true}}); err != nil {
		t.Fatalf("a pool an ancestor holds is passed through, not re-checked: %v", err)
	}
	if _, err := Make(state, reg, Request{Named: []string{"vm"}, Held: map[string]bool{"vm": true}}); err == nil {
		t.Fatal("a pool the run names is checked, held or not")
	}
	if _, err := Make(state, reg, Request{Named: []string{"typo-key"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lane.LaneDir(state, "typo-key")); !os.IsNotExist(err) {
		t.Fatal("planning must not create a lane")
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run (bash): `go test ./internal/runplan/ -count=1 -timeout 120s`

Expected: the test build fails: `undefined: Plan`, `undefined: Make`, `undefined: Request` (the package has no code yet).

- [ ] **Step 3: Implement**

Create `internal/runplan/runplan.go`:

```go
// Package runplan works out what a run takes (spec 2.4, 2.5): its lane
// set, which is the named keys plus each named project key's linked pools,
// deduplicated; the one total order every run joins them in (project lanes
// sorted by key, then pools sorted by key); and the rules every lane of the
// set is checked against before any ticket exists.
//
// It reads machine.json and the lanes' config.json files and nothing
// else: it never opens, creates or locks a lane, so a refused run leaves no
// directory, ticket or log behind (spec 4.1). Its refusals are
// *machine.Refusal (exit 120) and *machine.StateError (exit 122).
package runplan

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/textsafe"
)

// Request is what a run asks for.
type Request struct {
	// Named are the --queue keys, validated.
	Named []string
	// Slots is --slots (0 when not given), Exclusive --exclusive and
	// Reason --reason.
	Slots     int
	Exclusive bool
	Reason    string
	// Held is the set of keys a verified ancestor holds (P, spec 2.6). A
	// pool in it that the run does not name is passed through and not
	// re-checked: the ancestor's ticket was admitted under its rules
	// (spec 4.5).
	Held map[string]bool
}

// Lane is one lane of a plan.
type Lane struct {
	Key string
	// Pool is the lane's kind, from machine.json.
	Pool bool
	// Named is set when --queue names the lane.
	Named bool
	// Via are, for a pool, the named project keys whose link brings it
	// into the run, sorted; empty when the pool is only named directly.
	Via []string
	// Cfg is the lane's config as read at plan time.
	Cfg lane.Config
}

// Role says how a pool lane is reached, for busy, holder, timeout and
// refusal lines (spec 5.1): "pool, via cap-gate", "pool" for direct use,
// and "" for a project lane, whose lines stay as they were.
func (l Lane) Role() string {
	switch {
	case !l.Pool:
		return ""
	case len(l.Via) == 0:
		return "pool"
	}
	return "pool, via " + strings.Join(l.Via, ",")
}

// StatusKey is the key a timeout line sends the caller to: the run's own
// project key for a pool reached through a link, else the lane itself.
func (l Lane) StatusKey() string {
	if len(l.Via) > 0 {
		return l.Via[0]
	}
	return l.Key
}

// Plan is a run's lane set in total order and what it was computed from.
type Plan struct {
	Lanes []Lane
	// Generation is machine.json's generation at plan time.
	Generation int64
	// Links is each named project key's link (its pools, sorted) as read
	// at plan time; an unlinked key maps to nil.
	Links map[string][]string
}

// Less is the total order of spec 2.4: project lanes before pools, each
// group by key in byte order.
func Less(a, b Lane) bool {
	if a.Pool != b.Pool {
		return !a.Pool
	}
	return a.Key < b.Key
}

// Make computes the plan for req against reg and the lanes' configs.
func Make(stateDir string, reg *machine.Registry, req Request) (*Plan, error) {
	p := &Plan{Generation: reg.Generation, Links: map[string][]string{}}
	lanes := map[string]*Lane{}
	named := append([]string(nil), req.Named...)
	sort.Strings(named)

	// Named keys first: a closed lane is refused for being closed before
	// anything else is said about it (spec 4.1).
	for _, k := range named {
		cfg, err := readConfig(stateDir, k)
		if err != nil {
			return nil, err
		}
		l := &Lane{Key: k, Pool: reg.IsPool(k), Named: true, Cfg: cfg}
		if cfg.Closed != "" {
			return nil, closedRefusal(*l)
		}
		lanes[k] = l
	}

	// Each named project key brings its linked pools. Every one must
	// resolve to a registered pool with a readable config: the lane set
	// never shrinks silently (spec 2.5).
	for _, k := range named {
		l := lanes[k]
		if l.Pool {
			continue
		}
		link := machine.SortedSet(l.Cfg.Pools)
		p.Links[k] = link
		for _, pool := range link {
			pl, err := linkedPool(stateDir, reg, lanes, k, pool)
			if err != nil {
				return nil, err
			}
			pl.Via = append(pl.Via, k)
		}
	}

	for _, l := range lanes {
		p.Lanes = append(p.Lanes, *l)
	}
	sort.Slice(p.Lanes, func(i, j int) bool { return Less(p.Lanes[i], p.Lanes[j]) })

	for _, l := range p.Lanes {
		if l.Pool && !l.Named && req.Held[l.Key] {
			continue
		}
		if !l.Named && l.Cfg.Closed != "" {
			return nil, closedRefusal(l)
		}
		if l.Cfg.RequireReason && strings.TrimSpace(req.Reason) == "" {
			return nil, reasonRefusal(l)
		}
		if !l.Named {
			continue
		}
		// --slots applies to named keys. A pool ticket always carries the
		// pool's configured count (a pool without one is one slot wide), so
		// a --slots on a named pool is only checked against it, never
		// written (spec 2.4).
		configured := l.Cfg.Slots
		if l.Pool && configured < 1 {
			configured = 1
		}
		if configured > 0 && req.Slots >= 1 && req.Slots != configured {
			return nil, &machine.Refusal{Msg: lane.NewSlotsDisagreement(l.Key, configured, req.Slots, req.Exclusive).Error()}
		}
	}
	return p, nil
}

// linkedPool returns the plan lane of pool, which project key links,
// reading its config the first time.
func linkedPool(stateDir string, reg *machine.Registry, lanes map[string]*Lane, project, pool string) (*Lane, error) {
	if pl := lanes[pool]; pl != nil {
		return pl, nil
	}
	if !reg.IsPool(pool) {
		return nil, linkStateError(project, pool, "it is not a pool on this machine")
	}
	cfg, err := lane.ReadConfig(lane.LaneDir(stateDir, pool))
	if err != nil {
		return nil, linkStateError(project, pool, textsafe.Escape(err.Error()))
	}
	pl := &Lane{Key: pool, Pool: true, Cfg: cfg}
	lanes[pool] = pl
	return pl, nil
}

func linkStateError(project, pool, reason string) *machine.StateError {
	return &machine.StateError{Msg: fmt.Sprintf("machine-state: queue %q links %q: %s", project, pool, reason)}
}

// readConfig reads a named key's config without opening the lane. A
// config written by a newer incoda fails closed with machine-state; any
// other unreadable config fails the run as it always did.
func readConfig(stateDir, key string) (lane.Config, error) {
	cfg, err := lane.ReadConfig(lane.LaneDir(stateDir, key))
	var ns *lane.NewerSchemaError
	switch {
	case errors.As(err, &ns):
		return cfg, &machine.StateError{Msg: "machine-state: " + textsafe.Escape(err.Error())}
	case err != nil:
		return cfg, &machine.StateError{Msg: fmt.Sprintf("queue %q: %s", key, textsafe.Escape(err.Error()))}
	}
	return cfg, nil
}

// withRole appends " (<role>)" for a pool.
func withRole(l Lane) string {
	if r := l.Role(); r != "" {
		return " (" + r + ")"
	}
	return ""
}

// closedRefusal is the closed refusal of spec 4.5: today's text, plus the
// path for a pool.
func closedRefusal(l Lane) *machine.Refusal {
	return &machine.Refusal{Msg: fmt.Sprintf("queue %q is closed: %s%s", l.Key, textsafe.Escape(l.Cfg.Closed), withRole(l))}
}

// reasonRefusal is the require_reason refusal of spec 4.5.
func reasonRefusal(l Lane) *machine.Refusal {
	return &machine.Refusal{Msg: fmt.Sprintf("queue %q requires --reason%s: say what this job is so status can answer \"whose is that and why\"", l.Key, withRole(l))}
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run (bash): `go test ./internal/runplan/ -count=1`

Expected: `ok` for each package.

- [ ] **Step 5: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 6: Commit**

```bash
git add internal/runplan/runplan.go internal/runplan/runplan_test.go
git commit -F - <<'MSG'
feat: runplan computes a run's lane set in the total order and checks its rules

The lane set is the named keys plus each named project key's linked
pools, deduplicated, in one total order: project lanes, then pools,
each by key. Every linked pool must resolve. Closed, require_reason and
--slots are checked before any ticket, a pool's refusals name the path
that brought it, and a pool an ancestor holds is not re-checked.
Planning reads files only and never creates a lane.
MSG
```


---

### Task 6: A run takes its linked pools after its project lanes

Spec 2.4, 2.7 and 5.1. `run` now runs on the plan: it opens and enrolls the plan's lanes one at a time in the total order (a lane is enrolled only once the one before it is held), skipping lanes a verified ancestor holds, as before. A pool ticket records `via`; every ticket records `wait` (the `--wait` as given, empty when not given). `--exclusive` holds the lanes the run names and does not propagate to linked pools; `--slots` is written only on project lanes. Pool lines name their role: the busy line `queue "tests" busy (pool, via cap-gate; 1 slot(s), 1 ahead of you)`, holder lines ending ` via cap-gate`, the 121 line `queue "tests" (pool, via cap-gate) still busy after 30m0s. Check \`incoda status --queue cap-gate\`...` (the run's own project key) and `acquired queue "tests" (pool, via cap-gate; pid N)`. Project-lane lines are byte-identical. The enqueue line of a pool ticket logs `via=`. Unlinked project keys still run as before until Task 7.

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/lane/queue.go`
- Modify: `internal/lane/ticket.go`
- Test: `integration_test.go`
- Test: `pools_test.go` (new)

**Interfaces:**
- Consumes: `runplan.Make`, `runplan.Lane.Role`, `runplan.Lane.StatusKey`, `runplan.Less` (Task 5); `waitValue` (plan 1).
- Produces: `lane.Ticket.Via []string` (`json:"via,omitempty"`) and `lane.Ticket.Wait string` (`json:"wait,omitempty"`); `waitValue.raw` (the text given); `lanePart` gains `l runplan.Lane` and loses `cfg`; `cli.viaText(via []string) string`. Root test helpers `mustRun(t, incoda, state string, wantCode int, args ...string) string` and `inOrder(t, out string, wants ...string)`; `ticketPayload` gains `Exclusive`, `Via`, `Wait`.

- [ ] **Step 1: Write the failing tests**

In `integration_test.go`, replace:

```go
// ---- helpers ----

type ticketPayload struct {
	PID     int      `json:"pid"`
	Slots   int      `json:"slots"`
	Command []string `json:"command"`
	Dir     string   `json:"cwd"`
}

type entry struct {
```

with:

```go
// ---- helpers ----

type ticketPayload struct {
	PID       int      `json:"pid"`
	Slots     int      `json:"slots"`
	Exclusive bool     `json:"exclusive"`
	Command   []string `json:"command"`
	Dir       string   `json:"cwd"`
	Via       []string `json:"via"`
	Wait      string   `json:"wait"`
}

type entry struct {
```

Create `pools_test.go`:

```go
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// mustRun runs incoda and fails the test unless it exits wantCode.
func mustRun(t *testing.T, incoda, state string, wantCode int, args ...string) string {
	t.Helper()
	out, code := runIncoda(t, incoda, state, args...)
	if code != wantCode {
		t.Fatalf("incoda %s: exit %d, want %d\n%s", strings.Join(args, " "), code, wantCode, out)
	}
	return out
}

// inOrder fails unless every want appears in out, each after the one
// before it.
func inOrder(t *testing.T, out string, wants ...string) {
	t.Helper()
	at := 0
	for _, w := range wants {
		i := strings.Index(out[at:], w)
		if i < 0 {
			t.Fatalf("missing %q (in order) in:\n%s", w, out)
		}
		at += i + len(w)
	}
}

// TestRunTakesItsLinkedPools: a run on a linked project lane also takes
// its pools, one at a time in the total order, so two projects linked to
// one pool exclude each other there (spec 2.4). A pool ticket records via
// and the run's --wait (2.7); busy, holder and timeout lines name the
// pool's role (5.1); --exclusive stays on the project lane.
func TestRunTakesItsLinkedPools(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	for _, k := range []string{"kf-gate", "other-gate"} {
		mustRun(t, incoda, state, 0, "config", k, "--pool", "tests")
	}
	h := exec.Command(incoda, "run", "--queue", "kf-gate", "--exclusive", "--wait", "60s", "--poll", "50ms", "--quiet",
		"--", stamp, filepath.Join(t.TempDir(), "h.txt"), "h", "4000")
	h.Env = laneEnv(state)
	if err := h.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Process.Kill(); _ = h.Wait() }()
	waitFor(t, incoda, state, "tests", func(q queueReport) bool { return len(q.Holders) == 1 })
	pool := statusJSON(t, incoda, state, "tests").Queues[0].Holders[0].Ticket
	if strings.Join(pool.Via, ",") != "kf-gate" || pool.Wait != "60s" || pool.Exclusive || pool.PID != h.Process.Pid {
		t.Fatalf("pool ticket: %+v", pool)
	}
	if own := statusJSON(t, incoda, state, "kf-gate").Queues[0].Holders[0].Ticket; !own.Exclusive || len(own.Via) != 0 {
		t.Fatalf("project ticket: %+v", own)
	}

	out := mustRun(t, incoda, state, 121, "run", "--queue", "other-gate", "--wait", "0", "--poll", "50ms",
		"--", stamp, filepath.Join(t.TempDir(), "o.txt"), "o", "10")
	inOrder(t, out,
		`incoda: queue "tests" busy (pool, via other-gate; 1 slot(s), 1 ahead of you), waited 0s`,
		"incoda:   holder pid ", " via kf-gate\n",
		`incoda: queue "tests" (pool, via other-gate) still busy after 0s. Check `+"`incoda status --queue other-gate`")
	if countTickets(t, state, "other-gate") != 0 || countTickets(t, state, "tests") != 1 {
		t.Fatal("a timed-out run releases every lane it took")
	}
	out = mustRun(t, incoda, state, 121, "run", "--queue", "tests", "--wait", "0", "--", stamp, filepath.Join(t.TempDir(), "d.txt"), "d", "10")
	inOrder(t, out, `incoda: queue "tests" busy (pool; 1 slot(s), 1 ahead of you)`,
		`incoda: queue "tests" (pool) still busy after 0s. Check `+"`incoda status --queue tests`")
	log, _ := os.ReadFile(filepath.Join(laneDir(state, "tests"), "lane.log"))
	if !strings.Contains(string(log), " via=kf-gate ") || !strings.Contains(string(log), " via=other-gate ") {
		t.Fatalf("enqueue lines of pool tickets carry via:\n%s", log)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("holder: %v", err)
	}

	out = mustRun(t, incoda, state, 0, "run", "--queue", "kf-gate,builds", "--poll", "50ms",
		"--", stamp, filepath.Join(t.TempDir(), "b.txt"), "b", "10")
	inOrder(t, out, `acquired queue "kf-gate" (pid `, `acquired queue "builds" (pool; pid `, `acquired queue "tests" (pool, via kf-gate; pid `)
}

// TestPoolRulesBindLinkedRuns: a linked run is refused up front on a
// closed pool, and without --reason on a pool that requires one, with the
// pool's path in the text (spec 4.5); nothing is enrolled anywhere.
func TestPoolRulesBindLinkedRuns(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	mustRun(t, incoda, state, 0, "config", "cap-e2e", "--pool", "computer-use,vm")
	mustRun(t, incoda, state, 0, "config", "vm", "--close", "maintenance")
	out := mustRun(t, incoda, state, 120, "run", "--queue", "cap-e2e", "--", stamp, filepath.Join(t.TempDir(), "x.txt"), "x", "10")
	if !strings.Contains(out, `incoda: queue "vm" is closed: maintenance (pool, via cap-e2e)`) {
		t.Fatalf("closed pool:\n%s", out)
	}
	mustRun(t, incoda, state, 0, "config", "builds", "--require-reason")
	mustRun(t, incoda, state, 0, "config", "kf-build", "--pool", "builds")
	out = mustRun(t, incoda, state, 120, "run", "--queue", "kf-build", "--", stamp, filepath.Join(t.TempDir(), "y.txt"), "y", "10")
	if !strings.Contains(out, `incoda: queue "builds" requires --reason (pool, via kf-build): say what this job is`) {
		t.Fatalf("reason pool:\n%s", out)
	}
	for _, k := range []string{"cap-e2e", "computer-use", "vm", "kf-build", "builds"} {
		if n := countTickets(t, state, k); n != 0 {
			t.Fatalf("%s holds %d ticket(s) after a refusal", k, n)
		}
	}
	mustRun(t, incoda, state, 0, "run", "--queue", "kf-build", "--reason", "nightly", "--quiet",
		"--", stamp, filepath.Join(t.TempDir(), "z.txt"), "z", "10")
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run (bash): `go test . -run 'TestRunTakesItsLinkedPools|TestPoolRulesBindLinkedRuns' -count=1 -timeout 120s`

Expected: `TestRunTakesItsLinkedPools` fails with `timed out waiting for queue "tests" to reach the expected state` (the run takes no pool), and `TestPoolRulesBindLinkedRuns` exits 0 (`acquired queue "cap-e2e"`) where 120 was wanted.

- [ ] **Step 3: Implement**

A lane an ancestor holds is logged `event=reenter` with `lane.AppendLog`, which writes only into an existing lane directory: re-entry never creates a lane.

In `internal/cli/cli.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 2) Replace:

```go
type waitValue struct {
	d   time.Duration
	set bool
}

func (w *waitValue) String() string {
```

with:

```go
type waitValue struct {
	d   time.Duration
	set bool
	// raw is the value as given, recorded on tickets (spec 2.7) and
	// repeated in printed fix lines.
	raw string
}

func (w *waitValue) String() string {
```

(2 of 2) Replace:

```go
	if s == "" {
		return errors.New("empty duration")
	}
	if n, err := strconv.Atoi(s); err == nil {
		if n < 0 {
			w.d, w.set = -1, true
```

with:

```go
	if s == "" {
		return errors.New("empty duration")
	}
	w.raw = s
	if n, err := strconv.Atoi(s); err == nil {
		if n < 0 {
			w.d, w.set = -1, true
```

In `internal/cli/run.go`, make these 11 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 11) Replace:

```go
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/textsafe"
)

// lanePart is one key of a run: its queue handle and, once enrolled, the
// ticket. A single-key run is the list with one element.
type lanePart struct {
	key string
	q   *lane.Queue
	en  *lane.Enrollment
	cfg lane.Config
}

func cmdRun(args []string, _, stderr io.Writer) error {
```

with:

```go
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/runplan"
	"github.com/deblasis/incoda/internal/textsafe"
)

// lanePart is one lane of a run: its place in the plan, its queue handle
// and, once enrolled, the ticket. A single-key run without links is the
// list with one element.
type lanePart struct {
	l   runplan.Lane
	key string
	q   *lane.Queue
	en  *lane.Enrollment
}

func cmdRun(args []string, _, stderr io.Writer) error {
```

(2 of 11) Replace:

```go
		return err
	}

	// Every queue is opened and its config checked before any ticket
	// exists, so a closed or reason-requiring key refuses with nothing to
	// undo. Keys already held by a parent incoda are skipped (re-entrancy):
	// a recipe that takes its own lane must not deadlock when an agent
	// wraps the whole recipe in run from outside. The parent says which
	// keys it holds through INCODA_HELD, and a nested run on one of them
	// rides the parent's ticket instead of queueing behind it.
	//
	// Inherited lanes come from the environment incoda was started with.
	// Each entry is probed: dead and malformed ones are dropped, live ones
```

with:

```go
		return err
	}

	// The plan (runplan) is the lane set in its total order, with every
	// rule checked before any ticket exists and before any lane is opened,
	// so a closed or reason-requiring lane refuses with nothing to undo and
	// a typo leaves nothing behind. Lanes already held by a parent incoda
	// are skipped (re-entrancy): a recipe that takes its own lane must not
	// deadlock when an agent wraps the whole recipe in run from outside.
	// The parent says which lanes it holds through INCODA_HELD, and a
	// nested run on one of them rides the parent's ticket instead of
	// queueing behind it.
	//
	// Inherited lanes come from the environment incoda was started with.
	// Each entry is probed: dead and malformed ones are dropped, live ones
```

(3 of 11) Replace:

```go
	reportDropped(dir, inherited, *quiet, stderr, p)
	pass := inherited.PassKeys()
	live := inherited.LiveKeys()
	var parts, toTake []*lanePart
	defer func() {
		for _, pt := range parts {
			pt.q.Close()
		}
	}()
	for _, key := range keys {
		q, err := lane.Open(dir, key)
		if err != nil {
			return exitWith(ExitState, "%v", err)
		}
```

with:

```go
	reportDropped(dir, inherited, *quiet, stderr, p)
	pass := inherited.PassKeys()
	live := inherited.LiveKeys()
	plan, err := runplan.Make(dir, reg, runplan.Request{
		Named: keys, Slots: *slots, Exclusive: *exclusive, Reason: *reason, Held: pass,
	})
	if err != nil {
		return machineExit(err)
	}
	var parts, toTake []*lanePart
	defer func() {
		for _, pt := range parts {
			pt.q.Close()
		}
	}()
	for _, l := range plan.Lanes {
		if pass[l.Key] {
			if !*quiet {
				fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
					p.Dim(fmt.Sprintf("queue %q is already held by a parent incoda; running inside its lane", l.Key)))
			}
			lane.AppendLog(lane.LaneDir(dir, l.Key), "queue=%s event=reenter pid=%d cmd=%s", l.Key, os.Getpid(), textsafe.LogValue(lane.Ticket{Command: argv}.CommandString()))
			continue
		}
		q, err := lane.Open(dir, l.Key)
		if err != nil {
			return exitWith(ExitState, "%v", err)
		}
```

(4 of 11) Replace:

```go
		// budget: a stopped incoda keeping a registry lock costs this run
		// its budget, never more.
		q.SetBudget(start, wait.d)
		pt := &lanePart{key: key, q: q}
		parts = append(parts, pt)
		cfg, err := q.LoadConfig()
		var ns *lane.NewerSchemaError
		if errors.As(err, &ns) {
			return exitWith(ExitState, "machine-state: %v", err)
		}
		if err != nil {
			return exitWith(ExitState, "queue %q: %v", key, err)
		}
		pt.cfg = cfg
		if cfg.Closed != "" {
			return usagef("queue %q is closed: %s", key, textsafe.Escape(cfg.Closed))
		}
		if cfg.RequireReason && strings.TrimSpace(*reason) == "" {
			return usagef("queue %q requires --reason: say what this job is so status can answer \"whose is that and why\"", key)
		}
		if cfg.Slots > 0 && *slots >= 1 && *slots != cfg.Slots {
			return usagef("%v", lane.NewSlotsDisagreement(key, cfg.Slots, *slots, *exclusive))
		}
		if pass[key] {
			if !*quiet {
				fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
					p.Dim(fmt.Sprintf("queue %q is already held by a parent incoda; running inside its lane", key)))
			}
			q.Logf("queue=%s event=reenter pid=%d cmd=%s", key, os.Getpid(), textsafe.LogValue(lane.Ticket{Command: argv}.CommandString()))
			continue
		}
		toTake = append(toTake, pt)
	}
	// The sorted-order argument that makes multi-key runs deadlock-free
	// stops at a nested run: a parent holding "b" whose recipe now takes "a"
	// is acquiring out of order, and two such parents can each wait on the
	// other's key until --wait expires. It cannot be prevented from here
	// (the parent's key is already held), so it is said out loud.
	for _, pt := range toTake {
		for h := range live {
			if pt.key < h && !*quiet {
				fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
					p.Yellow(fmt.Sprintf("warning: taking %q while a parent incoda holds %q acquires out of sorted order; two nested runs shaped like this can wait on each other until --wait expires", pt.key, h)))
			}
```

with:

```go
		// budget: a stopped incoda keeping a registry lock costs this run
		// its budget, never more.
		q.SetBudget(start, wait.d)
		pt := &lanePart{l: l, key: l.Key, q: q}
		parts = append(parts, pt)
		toTake = append(toTake, pt)
	}
	// The total order that makes multi-lane runs deadlock-free stops at a
	// nested run: a parent holding "b" whose recipe now takes "a" is
	// acquiring out of order, and two such parents can each wait on the
	// other's lane until --wait expires. It cannot be prevented from here
	// (the parent's lane is already held), so it is said out loud.
	for _, pt := range toTake {
		for h := range live {
			if runplan.Less(pt.l, runplan.Lane{Key: h, Pool: reg.IsPool(h)}) && !*quiet {
				fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
					p.Yellow(fmt.Sprintf("warning: taking %q while a parent incoda holds %q acquires out of sorted order; two nested runs shaped like this can wait on each other until --wait expires", pt.key, h)))
			}
```

(5 of 11) Replace:

```go
	ctx, stop := signal.NotifyContext(context.Background(), interruptSignals()...)
	defer stop()

	// Keys are taken one at a time in sorted order (resolveKeys sorted
	// them). Every multi-key caller orders the same way, so two of them can
	// never each hold what the other waits for: the classic lock-ordering
	// argument, and the whole reason a list is allowed at all. The budget
	// started with the command, so machine.lock and migration waits above
	// have already spent part of it.
	for _, pt := range toTake {
		en, err := pt.q.Enroll(lane.Ticket{
			Slots:     *slots,
			Exclusive: *exclusive,
			Command:   argv,
			Reason:    *reason,
			Owner:     *owner,
			Hostname:  host,
			Dir:       cwd,
		})
		if err != nil {
			// The queue's config can change between the pre-check above and
			// this enrollment; the refusal is the same caller mistake either
```

with:

```go
	ctx, stop := signal.NotifyContext(context.Background(), interruptSignals()...)
	defer stop()

	// Lanes are taken one at a time in the plan's total order: project
	// lanes sorted by key, then pools sorted by key (spec 2.4). Every run
	// orders the same way, so two of them can never each hold what the
	// other waits for: the classic lock-ordering argument. A lane is
	// enrolled only once the one before it is held; enrolling them all and
	// then waiting would let a waiting ticket block later arrivals. The
	// budget started with the command, so machine.lock and migration waits
	// above have already spent part of it.
	for _, pt := range toTake {
		t := lane.Ticket{
			// --exclusive holds a lane the run names; it does not
			// propagate to the pools a link brings (spec 2.4).
			Exclusive: *exclusive && (pt.l.Named || !pt.l.Pool),
			Command:   argv,
			Reason:    *reason,
			Owner:     *owner,
			Hostname:  host,
			Dir:       cwd,
			Via:       pt.l.Via,
			Wait:      wait.raw,
		}
		if !pt.l.Pool {
			// --slots applies to named project lanes only. A pool ticket
			// carries the pool's configured count; the plan has already
			// refused a --slots that disagrees with a named pool.
			t.Slots = *slots
		}
		en, err := pt.q.Enroll(t)
		if err != nil {
			// The queue's config can change between the pre-check above and
			// this enrollment; the refusal is the same caller mistake either
```

(6 of 11) Replace:

```go
			}
		}
		key := pt.key
		// On a pool, unpooled runs of an older incoda (strays and orphan
		// records, spec 2.3) hold slots too. Each poll rescans them,
		// deletes stray lanes that have fully died, and refuses at once
```

with:

```go
			}
		}
		key := pt.key
		role := pt.l.Role()
		// On a pool, unpooled runs of an older incoda (strays and orphan
		// records, spec 2.3) hold slots too. Each poll rescans them,
		// deletes stray lanes that have fully died, and refuses at once
```

(7 of 11) Replace:

```go
				if ahead < 0 {
					ahead = len(live)
				}
				fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
					p.Yellow(fmt.Sprintf("queue %q busy (%d slot(s), %d ahead of you), waited %s%s",
						key, effSlots, ahead, waited.Round(time.Second), waitBudget(wait.d))))
				for i, e := range live {
					if i >= effSlots {
						break
```

with:

```go
				if ahead < 0 {
					ahead = len(live)
				}
				// A pool's busy line names its role (spec 5.1); a project
				// lane's line is unchanged.
				rolePart := ""
				if role != "" {
					rolePart = role + "; "
				}
				fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
					p.Yellow(fmt.Sprintf("queue %q busy (%s%d slot(s), %d ahead of you), waited %s%s",
						key, rolePart, effSlots, ahead, waited.Round(time.Second), waitBudget(wait.d))))
				for i, e := range live {
					if i >= effSlots {
						break
```

(8 of 11) Replace:

```go
						continue
					}
					fmt.Fprintf(stderr, "%s   %s\n", p.Dim("incoda:"),
						p.Dim(fmt.Sprintf("holder pid %d in %s: %s", e.Ticket.PID, textsafe.Escape(e.Ticket.Dir), textsafe.Escape(e.Ticket.CommandString()))))
				}
				for _, u := range unpooled {
					fmt.Fprintf(stderr, "%s   %s\n", p.Dim("incoda:"), p.Dim(u.Line()))
```

with:

```go
						continue
					}
					fmt.Fprintf(stderr, "%s   %s\n", p.Dim("incoda:"),
						p.Dim(fmt.Sprintf("holder pid %d in %s: %s%s", e.Ticket.PID, textsafe.Escape(e.Ticket.Dir), textsafe.Escape(e.Ticket.CommandString()), viaText(e.Ticket.Via))))
				}
				for _, u := range unpooled {
					fmt.Fprintf(stderr, "%s   %s\n", p.Dim("incoda:"), p.Dim(u.Line()))
```

(9 of 11) Replace:

```go
			if errors.Is(acqErr, lane.ErrTimeout) {
				rc = ExitTimeout
				pt.q.Logf("queue=%s event=giveup pid=%d waited=%s", key, os.Getpid(), wait.d)
				return exitWith(ExitTimeout,
					"queue %q still busy after %s. Check `incoda status --queue %s`. Do NOT bypass the lane; surface the wait and coordinate instead",
					key, wait.d, key)
			}
			rc = ExitState
			return exitWith(ExitState, "%v", acqErr)
```

with:

```go
			if errors.Is(acqErr, lane.ErrTimeout) {
				rc = ExitTimeout
				pt.q.Logf("queue=%s event=giveup pid=%d waited=%s", key, os.Getpid(), wait.d)
				named := fmt.Sprintf("queue %q", key)
				if role != "" {
					named += " (" + role + ")"
				}
				return exitWith(ExitTimeout,
					"%s still busy after %s. Check `incoda status --queue %s`. Do NOT bypass the lane; surface the wait and coordinate instead",
					named, wait.d, pt.l.StatusKey())
			}
			rc = ExitState
			return exitWith(ExitState, "%v", acqErr)
```

(10 of 11) Replace:

```go
			// config floors the effective width regardless. On a queue with
			// no configured count the minimum still rules.
			inForce := "the smallest value is in force"
			if pt.cfg.Slots > 0 {
				inForce = fmt.Sprintf("the configured %d is in force", pt.cfg.Slots)
			}
			fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
				p.Yellow(fmt.Sprintf("warning: participants on queue %q disagree about --slots; %s", key, inForce)))
		}
		if !*quiet {
			fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
				p.Green(fmt.Sprintf("acquired queue %q (pid %d)", key, os.Getpid())))
		}
	}

```

with:

```go
			// config floors the effective width regardless. On a queue with
			// no configured count the minimum still rules.
			inForce := "the smallest value is in force"
			if pt.l.Cfg.Slots > 0 {
				inForce = fmt.Sprintf("the configured %d is in force", pt.l.Cfg.Slots)
			}
			fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
				p.Yellow(fmt.Sprintf("warning: participants on queue %q disagree about --slots; %s", key, inForce)))
		}
		if !*quiet {
			what := fmt.Sprintf("acquired queue %q (pid %d)", key, os.Getpid())
			if role != "" {
				what = fmt.Sprintf("acquired queue %q (%s; pid %d)", key, role, os.Getpid())
			}
			fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"), p.Green(what))
		}
	}

```

(11 of 11) Replace:

```go
		return &exitCode{code: res.Code}
	}
	return nil
}

// waitingCheck is the per-poll config check of a waiting run: closed while
```

with:

```go
		return &exitCode{code: res.Code}
	}
	return nil
}

// viaText is the " via <keys>" a holder line of a pool ticket ends with
// (spec 5.1); empty for a ticket taken directly.
func viaText(via []string) string {
	if len(via) == 0 {
		return ""
	}
	return " via " + strings.Join(via, ",")
}

// waitingCheck is the per-poll config check of a waiting run: closed while
```

In `internal/lane/queue.go`, replace:

```go
	if en.ticket.Exclusive {
		extra += " exclusive=true"
	}
	q.Logf("queue=%s event=enqueue pid=%d slots=%d%s%s cmd=%s", q.Key, en.ticket.PID, en.ticket.Slots, extra, en.ticket.attribution(), textsafe.LogValue(en.ticket.CommandString()))
	return en, nil
}
```

with:

```go
	if en.ticket.Exclusive {
		extra += " exclusive=true"
	}
	if len(en.ticket.Via) > 0 {
		extra += " via=" + strings.Join(en.ticket.Via, ",")
	}
	q.Logf("queue=%s event=enqueue pid=%d slots=%d%s%s cmd=%s", q.Key, en.ticket.PID, en.ticket.Slots, extra, en.ticket.attribution(), textsafe.LogValue(en.ticket.CommandString()))
	return en, nil
}
```

In `internal/lane/ticket.go`, replace:

```go
	Owner    string `json:"owner,omitempty"`
	Hostname string `json:"hostname"`
	Dir      string `json:"cwd"`
}

// attribution is the k=v block every lifecycle line (enqueue/acquire/release)
```

with:

```go
	Owner    string `json:"owner,omitempty"`
	Hostname string `json:"hostname"`
	Dir      string `json:"cwd"`
	// Via are, on a pool ticket, the run's named project keys that link to
	// the pool (spec 2.7); empty when the run names the pool directly.
	// Status and watch group pool holders by it.
	Via []string `json:"via,omitempty"`
	// Wait is the run's --wait as given; empty when it was not given.
	Wait string `json:"wait,omitempty"`
}

// attribution is the k=v block every lifecycle line (enqueue/acquire/release)
```

- [ ] **Step 4: Run the tests to see them pass**

Run (bash): `go test . -run 'TestRunTakesItsLinkedPools|TestPoolRulesBindLinkedRuns' -count=1`

Expected: `ok` for each package.

- [ ] **Step 5: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 6: Commit**

```bash
git add integration_test.go internal/cli/cli.go internal/cli/run.go internal/lane/queue.go internal/lane/ticket.go pools_test.go
git commit -F - <<'MSG'
feat: a run takes its linked pools after its project lanes, one at a time

run plans its lane set and takes it in the total order: project lanes,
then the pools their links bring, each enrolled only once the one before
it is held. Pool tickets record via, every ticket records the --wait it
was given, --exclusive stays on the named lanes, and the busy, holder,
timeout and acquired lines of a pool name how it was reached.
MSG
```


---

### Task 7: A run on an unlinked project lane is refused with its suggestion

Spec 4.1 and 3.5. After the closed check and before any ticket, a project lane with no link refuses with exit 120 and the exact block of spec 4.1: the pools on this machine (descriptions escaped and cut to 60 columns), the suggestion from the name table, and the run line that links it to the suggestion (`--pool <suggestion>`, every other flag the caller passed, fix-line rules of Task 2), or with no suggestion the ask-the-user lines. A suggestion carrying `quiet_machine` says so and adds `--wait '5m'` when the caller gave none. Several unlinked keys print one `incoda config KEY --pool <suggestion>` line per key and then the run line without `--pool`; any key without a suggestion makes the whole block ask the user. The check reads `config.json` without opening the lane, so a typo leaves nothing behind. From here on every test that runs on a project key links it first (`linkTestKeys`, Task 3) and every test whose run is the command that migrates uses a pool; Step 5 adapts the existing tests.

**Files:**
- Modify: `internal/cli/run.go`
- Modify: `internal/fixline/fixline.go`
- Create: `internal/runplan/refusals.go`
- Modify: `internal/runplan/runplan.go`
- Test: `config_test.go`
- Test: `held_test.go`
- Test: `integration_test.go`
- Test: `internal/cli/env_test.go`
- Test: `internal/runplan/runplan_test.go`
- Test: `kill_test.go`
- Test: `migrate_crash_test.go`
- Test: `migrate_test.go`
- Test: `oldbin_test.go`
- Test: `oldkill_test.go`
- Test: `pools_test.go`
- Test: `process_group_test.go`
- Test: `reentry_test.go`
- Test: `stoppedrun_test.go`
- Test: `strays_test.go`

**Interfaces:**
- Consumes: `fixline.Run`, `fixline.RunLines`, `fixline.Line` (Task 2), `machine.Suggest` (plan 2a), `machine.Registry.NotPools` (Task 3), `linkTestKeys` (Task 3).
- Produces: `runplan.Request` gains `Fix fixline.Run` and `WaitGiven bool`; `type runplan.Suggestion struct { machine.Suggestion; Usable bool; Why string }` with `Text() string`; `func runplan.Suggest(reg *machine.Registry, key string) Suggestion` (usable only when every suggested pool is registered); `func runplan.PoolRows(stateDir string, reg *machine.Registry) []string`; unexported `unlinkedRefusal`, `fixFor`, `namedOrder`, `configLine`, `sortLanes`, `refusal`; `cli.carriedFlags(fs *flag.FlagSet, wait *waitValue) []fixline.Flag`. `fixline.Run.Fields` prints `flags: (none)` when there are none.

- [ ] **Step 1: Write the failing tests**

In `internal/runplan/runplan_test.go`, make these 3 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 3) Replace:

```go
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
)
```

with:

```go
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/fixline"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
)
```

(2 of 3) Replace:

```go
		{[]string{"kungfoo-gate", "builds"}, "kungfoo-gate builds(pool) tests(pool, via kungfoo-gate)"},
		{[]string{"kungfoo-gate", "cap-e2e"}, "cap-e2e kungfoo-gate computer-use(pool, via cap-e2e) tests(pool, via cap-e2e,kungfoo-gate)"},
		{[]string{"tests", "kungfoo-gate"}, "kungfoo-gate tests(pool, via kungfoo-gate)"},
		{[]string{"plain"}, "plain"},
	} {
		p, err := Make(state, reg, Request{Named: c.named})
		if err != nil {
```

with:

```go
		{[]string{"kungfoo-gate", "builds"}, "kungfoo-gate builds(pool) tests(pool, via kungfoo-gate)"},
		{[]string{"kungfoo-gate", "cap-e2e"}, "cap-e2e kungfoo-gate computer-use(pool, via cap-e2e) tests(pool, via cap-e2e,kungfoo-gate)"},
		{[]string{"tests", "kungfoo-gate"}, "kungfoo-gate tests(pool, via kungfoo-gate)"},
		{[]string{"builds"}, "builds(pool)"},
	} {
		p, err := Make(state, reg, Request{Named: c.named})
		if err != nil {
```

(3 of 3) Replace:

```go
	if _, err := Make(state, reg, Request{Named: []string{"vm"}, Held: map[string]bool{"vm": true}}); err == nil {
		t.Fatal("a pool the run names is checked, held or not")
	}
	if _, err := Make(state, reg, Request{Named: []string{"typo-key"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lane.LaneDir(state, "typo-key")); !os.IsNotExist(err) {
		t.Fatal("planning must not create a lane")
```

with:

```go
	if _, err := Make(state, reg, Request{Named: []string{"vm"}, Held: map[string]bool{"vm": true}}); err == nil {
		t.Fatal("a pool the run names is checked, held or not")
	}
	var rf *machine.Refusal
	if _, err := Make(state, reg, Request{Named: []string{"typo-key"}}); !errors.As(err, &rf) || !strings.HasPrefix(rf.Msg, "unlinked: typo-key\n") {
		t.Fatalf("an unlinked key is refused: %v", err)
	}
	if _, err := os.Stat(lane.LaneDir(state, "typo-key")); !os.IsNotExist(err) {
		t.Fatal("planning must not create a lane")
```

Then append to the end of `internal/runplan/runplan_test.go`:

```go

// spec4Pools is the pool configs of the spec 4.1 example.
var spec4Pools = map[string]string{
	"builds":       `{"schema":2,"slots":1,"description":"heavy compiler/toolchain builds (LLVM links, toolchain rebuilds)"}`,
	"computer-use": `{"schema":2,"slots":1,"description":"drives the desktop, a browser or a dev server"}`,
	"tests":        `{"schema":2,"slots":1,"description":"test suites and gates"}`,
	"vm":           `{"schema":2,"slots":1,"description":"the VM host"}`,
}

const spec4Rows = `incoda: pools on this machine:
incoda:   builds        1 slot   heavy compiler/toolchain builds (LLVM links, toolchain re...
incoda:   computer-use  1 slot   drives the desktop, a browser or a dev server
incoda:   tests         1 slot   test suites and gates
incoda:   vm            1 slot   the VM host
`

// TestUnlinkedRefusal: the texts of spec 4.1, byte for byte, as the CLI
// prints them ("incoda: " before the message). POSIX quoting.
func TestUnlinkedRefusal(t *testing.T) {
	if fixline.Native() != fixline.POSIX {
		t.Skip("the expected lines are POSIX sh")
	}
	configs := map[string]string{
		"linked-gate": `{"schema":2,"pools":["tests"]}`,
		"strict-gate": `{"schema":2,"require_reason":true}`,
	}
	for k, v := range spec4Pools {
		configs[k] = v
	}
	state, reg := machineDir(t, configs)
	fix := func(argv ...string) fixline.Run {
		return fixline.Run{Flags: []fixline.Flag{{Name: "reason", Value: "wintty gate"}}, Argv: argv, Dir: "/src", Here: "/src"}
	}
	for _, c := range []struct {
		name string
		req  Request
		want string
	}{
		{"suggestion", Request{Named: []string{"wintty-gate"}, Reason: "wintty gate", Fix: fix("just", "gate")}, `incoda: unlinked: wintty-gate
incoda: queue "wintty-gate" is not linked to any pool; every project queue names the machine-wide pools its jobs use.
` + spec4Rows + `incoda: suggested: tests (name matches *-gate)
incoda: to link it to the suggestion (stored; every later run on this queue takes these pools):
incoda:   incoda run --queue wintty-gate --pool tests --reason 'wintty gate' -- 'just' 'gate'
incoda: if the suggestion does not fit, ask the user; they run: incoda link wintty-gate
`},
		{"no suggestion", Request{Named: []string{"polymatto"}, Fix: fix("pnpm", "build")}, `incoda: unlinked: polymatto
incoda: queue "polymatto" is not linked to any pool; every project queue names the machine-wide pools its jobs use.
` + spec4Rows + `incoda: suggested: none (no name pattern matches)
incoda: ask the user which pools this queue's jobs use; they run: incoda link polymatto
incoda: (incoda init links every queue in one pass)
incoda: do not pick a pool yourself, and never because it is free.
`},
		{"quiet suggestion adds --wait 5m", Request{Named: []string{"kungfoo-measure"}, Reason: "wintty gate", Fix: fix("just", "measure")}, `incoda: unlinked: kungfoo-measure
incoda: queue "kungfoo-measure" is not linked to any pool; every project queue names the machine-wide pools its jobs use.
` + spec4Rows + `incoda: suggested: tests, quiet_machine (name matches *-measure)
incoda: to link it to the suggestion (stored; every later run on this queue takes these pools and quiet-machine):
incoda:   incoda run --queue kungfoo-measure --pool tests --reason 'wintty gate' --wait '5m' -- 'just' 'measure'
incoda: (--wait 5m added: quiet-machine holds every pool it has drained while it waits for the rest)
incoda: if the suggestion does not fit, ask the user; they run: incoda link kungfoo-measure
`},
		{"several keys", Request{Named: []string{"cap-gate", "cap-e2e", "builds"}, Reason: "wintty gate", Fix: fix("just", "e2e")}, `incoda: unlinked: cap-e2e, cap-gate
incoda: queues "cap-e2e", "cap-gate" are not linked to any pool; every project queue names the machine-wide pools its jobs use.
` + spec4Rows + `incoda: suggested: cap-e2e -> computer-use,tests (name matches *-e2e); cap-gate -> tests (name matches *-gate)
incoda: to link them to the suggestions (stored; every later run on these queues takes these pools), then run:
incoda:   incoda config cap-e2e --pool computer-use,tests
incoda:   incoda config cap-gate --pool tests
incoda:   incoda run --queue cap-e2e,cap-gate,builds --reason 'wintty gate' -- 'just' 'e2e'
incoda: if a suggestion does not fit, ask the user; they run: incoda link cap-e2e, incoda link cap-gate
`},
		{"several keys, one without a suggestion", Request{Named: []string{"cap-gate", "polymatto"}, Fix: fix("x")}, `incoda: unlinked: cap-gate, polymatto
incoda: queues "cap-gate", "polymatto" are not linked to any pool; every project queue names the machine-wide pools its jobs use.
` + spec4Rows + `incoda: suggested: cap-gate -> tests (name matches *-gate); polymatto: none (no name pattern matches)
incoda: ask the user which pools these queues' jobs use; they run: incoda link cap-gate, incoda link polymatto
incoda: (incoda init links every queue in one pass)
incoda: do not pick a pool yourself, and never because it is free.
`},
		{"one unlinked key next to a linked one", Request{Named: []string{"linked-gate", "new-gate"}, Reason: "wintty gate", Fix: fix("x")}, `incoda: unlinked: new-gate
incoda: queue "new-gate" is not linked to any pool; every project queue names the machine-wide pools its jobs use.
` + spec4Rows + `incoda: suggested: tests (name matches *-gate)
incoda: to link it to the suggestion (stored; every later run on this queue takes these pools), then run:
incoda:   incoda config new-gate --pool tests
incoda:   incoda run --queue linked-gate,new-gate --reason 'wintty gate' -- 'x'
incoda: if the suggestion does not fit, ask the user; they run: incoda link new-gate
`},
		{"a lane requires a reason the run lacks", Request{Named: []string{"strict-gate"}, Fix: fixline.Run{Argv: []string{"x"}, Dir: "/src", Here: "/src"}}, `incoda: unlinked: strict-gate
incoda: queue "strict-gate" is not linked to any pool; every project queue names the machine-wide pools its jobs use.
` + spec4Rows + `incoda: suggested: tests (name matches *-gate)
incoda: to link it to the suggestion (stored; every later run on this queue takes these pools):
incoda: no runnable command (queue "strict-gate" requires --reason and this run has none); run it with these fields:
incoda:   queue: strict-gate
incoda:   pool: tests
incoda:   flags: (none)
incoda:   cwd: /src
incoda:   command: x
incoda: if the suggestion does not fit, ask the user; they run: incoda link strict-gate
`},
	} {
		_, err := Make(state, reg, c.req)
		var rf *machine.Refusal
		if !errors.As(err, &rf) {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got := "incoda: " + rf.Msg + "\n"; got != c.want {
			t.Errorf("%s:\n got:\n%s\nwant:\n%s", c.name, got, c.want)
		}
	}
}

func TestSuggest(t *testing.T) {
	_, reg := machineDir(t, nil)
	if s := Suggest(reg, "cap-e2e"); !s.Usable || s.Text() != "computer-use,tests" {
		t.Fatalf("%+v", s)
	}
	if s := Suggest(reg, "kf-measure"); !s.Usable || s.Text() != "tests, quiet_machine" {
		t.Fatalf("%+v", s)
	}
	if s := Suggest(reg, "polymatto"); s.Usable || s.Why != "no name pattern matches" {
		t.Fatalf("%+v", s)
	}
	reg.Pools = []string{"builds", "tests"}
	if s := Suggest(reg, "cap-e2e"); s.Usable || s.Why != "the suggested pools computer-use,tests are not all pools on this machine" {
		t.Fatalf("a suggestion naming a missing pool is not usable: %+v", s)
	}
}
```

In `pools_test.go`, replace:

```go
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)
```

with:

```go
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)
```

Then append to the end of `pools_test.go`:

```go

// TestUnlinkedRunIsRefusedAndLeavesNothingBehind: a project lane with no
// link refuses every run with exit 120 before any ticket (spec 4.1), with
// the suggestion and the line that makes it; the check reads config.json
// without opening the lane, so a typo leaves no directory behind. A closed
// lane is refused for being closed first.
func TestUnlinkedRunIsRefusedAndLeavesNothingBehind(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	mustRun(t, incoda, state, 0, "config", "seed")
	marker := filepath.Join(t.TempDir(), "m.txt")
	out := mustRun(t, incoda, state, 120, "run", "--queue", "wintty-gate", "--reason", "wintty gate", "--", stamp, marker, "m", "1")
	if !strings.HasPrefix(out, "incoda: unlinked: wintty-gate\nincoda: queue \"wintty-gate\" is not linked to any pool;") ||
		!strings.Contains(out, "incoda: suggested: tests (name matches *-gate)\n") ||
		!strings.Contains(out, "incoda: if the suggestion does not fit, ask the user; they run: incoda link wintty-gate\n") {
		t.Fatalf("unlinked refusal:\n%s", out)
	}
	want := fmt.Sprintf("incoda:   incoda run --queue wintty-gate --pool tests --reason 'wintty gate' -- '%s' '%s' 'm' '1'\n", stamp, marker)
	if runtime.GOOS != "windows" && !strings.Contains(out, want) {
		t.Fatalf("missing the run line with the suggestion %q in:\n%s", want, out)
	}
	out = mustRun(t, incoda, state, 120, "run", "--queue", "typo-kee", "--", stamp, marker, "m", "1")
	if !strings.Contains(out, "incoda: suggested: none (no name pattern matches)\nincoda: ask the user which pools this queue's jobs use; they run: incoda link typo-kee\n") {
		t.Fatalf("no suggestion:\n%s", out)
	}
	for _, k := range []string{"wintty-gate", "typo-kee"} {
		if _, err := os.Stat(laneDir(state, k)); !os.IsNotExist(err) {
			t.Fatalf("a refused run on %s left its lane behind", k)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("the command must not have run")
	}
	mustRun(t, incoda, state, 0, "config", "old-gate", "--close", "retired: use new-gate")
	out = mustRun(t, incoda, state, 120, "run", "--queue", "old-gate", "--", stamp, marker, "m", "1")
	if !strings.HasPrefix(out, `incoda: queue "old-gate" is closed: retired: use new-gate`) || strings.Contains(out, "unlinked") {
		t.Fatalf("closed comes first:\n%s", out)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run (bash), one at a time:

- `go test ./internal/runplan/ -count=1 -timeout 120s`
- `go test . -run 'TestUnlinkedRunIsRefusedAndLeavesNothingBehind' -count=1 -timeout 120s`

Expected: the `internal/runplan` test build fails (`unknown field Fix in struct literal of type Request`, `undefined: Suggest`); the root `TestUnlinkedRunIsRefusedAndLeavesNothingBehind` exits 0 (`acquired queue "wintty-gate"`) where 120 was wanted.

- [ ] **Step 3: Implement**

In `internal/cli/run.go`, make these 5 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 5) Replace:

```go
import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
```

with:

```go
import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
```

(2 of 5) Replace:

```go

	"github.com/deblasis/incoda/internal/child"
	"github.com/deblasis/incoda/internal/colorize"
	"github.com/deblasis/incoda/internal/held"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
```

with:

```go

	"github.com/deblasis/incoda/internal/child"
	"github.com/deblasis/incoda/internal/colorize"
	"github.com/deblasis/incoda/internal/fixline"
	"github.com/deblasis/incoda/internal/held"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
```

(3 of 5) Replace:

```go
	reportDropped(dir, inherited, *quiet, stderr, p)
	pass := inherited.PassKeys()
	live := inherited.LiveKeys()
	plan, err := runplan.Make(dir, reg, runplan.Request{
		Named: keys, Slots: *slots, Exclusive: *exclusive, Reason: *reason, Held: pass,
	})
	if err != nil {
		return machineExit(err)
```

with:

```go
	reportDropped(dir, inherited, *quiet, stderr, p)
	pass := inherited.PassKeys()
	live := inherited.LiveKeys()
	here, _ := os.Getwd()
	plan, err := runplan.Make(dir, reg, runplan.Request{
		Named: keys, Slots: *slots, Exclusive: *exclusive, Reason: *reason, Held: pass,
		Fix:       fixline.Run{Flags: carriedFlags(fs, wait), Argv: argv, Dir: here, Here: here},
		WaitGiven: wait.set,
	})
	if err != nil {
		return machineExit(err)
```

(4 of 5) Replace:

```go
	}

	host, _ := os.Hostname()
	cwd, _ := os.Getwd()

	// From here on every ticket must be released on every exit path, in
	// reverse acquisition order. The OS lock covers the paths we cannot
```

with:

```go
	}

	host, _ := os.Hostname()
	cwd := here

	// From here on every ticket must be released on every exit path, in
	// reverse acquisition order. The OS lock covers the paths we cannot
```

(5 of 5) Replace:

```go
	return nil
}

// viaText is the " via <keys>" a holder line of a pool ticket ends with
// (spec 5.1); empty for a ticket taken directly.
func viaText(via []string) string {
```

with:

```go
	return nil
}

// carriedFlags is every flag the caller gave except --queue and --pool, in
// flag-name order, as a printed fix line repeats them (spec 2.6); --wait
// keeps the text it was given.
func carriedFlags(fs *flag.FlagSet, wait *waitValue) []fixline.Flag {
	var out []fixline.Flag
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "queue", "pool", "pools":
			return
		case "wait":
			out = append(out, fixline.Flag{Name: "wait", Value: wait.raw})
			return
		}
		b, ok := f.Value.(interface{ IsBoolFlag() bool })
		out = append(out, fixline.Flag{Name: f.Name, Value: f.Value.String(), Bool: ok && b.IsBoolFlag()})
	})
	return out
}

// viaText is the " via <keys>" a holder line of a pool ticket ends with
// (spec 5.1); empty for a ticket taken directly.
func viaText(via []string) string {
```

In `internal/fixline/fixline.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 2) Replace:

```go
}

// Fields are the run line's parts for NoRunnable: queue, pool (when set),
// flags (every carried flag but the reason), reason (when set), cwd and the
// command, one escaped line each.
func (r Run) Fields() []Field {
	fs := []Field{{"queue", strings.Join(r.Queue, ",")}}
	if r.Pool != nil {
```

with:

```go
}

// Fields are the run line's parts for NoRunnable: queue, pool (when set),
// flags (every carried flag but the reason, or "(none)"), reason (when
// set), cwd and the command, one escaped line each.
func (r Run) Fields() []Field {
	fs := []Field{{"queue", strings.Join(r.Queue, ",")}}
	if r.Pool != nil {
```

(2 of 2) Replace:

```go
		default:
			flags = append(flags, "--"+f.Name+" "+f.Value)
		}
	}
	fs = append(fs, Field{"flags", strings.Join(flags, " ")})
	if hasReason {
```

with:

```go
		default:
			flags = append(flags, "--"+f.Name+" "+f.Value)
		}
	}
	if len(flags) == 0 {
		flags = []string{"(none)"}
	}
	fs = append(fs, Field{"flags", strings.Join(flags, " ")})
	if hasReason {
```

Create `internal/runplan/refusals.go`:

```go
package runplan

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/deblasis/incoda/internal/fixline"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/textsafe"
)

// quietWait is the --wait a printed line adds for a suggestion that
// carries quiet_machine when the caller gave none (spec 4.1, 2.8).
const quietWait = "5m"

// refusal joins the lines of one refusal block; the CLI prints "incoda: "
// before the first, and every later line carries it too.
func refusal(lines []string) *machine.Refusal {
	return &machine.Refusal{Msg: strings.Join(lines, "\nincoda: ")}
}

// Suggestion is the link spec 3.5 suggests for a key, usable only when
// every pool it names is registered on this machine.
type Suggestion struct {
	machine.Suggestion
	// Usable is false when the key matches no pattern or names a pool
	// this machine does not have; Why then says which.
	Usable bool
	Why    string
}

// Suggest looks up key's suggestion against reg.
func Suggest(reg *machine.Registry, key string) Suggestion {
	s, ok := machine.Suggest(key)
	if !ok {
		return Suggestion{Why: "no name pattern matches"}
	}
	if missing := reg.NotPools(s.Pools); len(missing) > 0 {
		return Suggestion{Suggestion: s, Why: fmt.Sprintf("the suggested pools %s are not all pools on this machine", strings.Join(s.Pools, ","))}
	}
	return Suggestion{Suggestion: s, Usable: true}
}

// Text is the suggestion as printed: "tests", or "tests, quiet_machine".
func (s Suggestion) Text() string {
	t := strings.Join(s.Pools, ",")
	if s.QuietMachine {
		t += ", quiet_machine"
	}
	return t
}

// PoolRows are the "pools on this machine" lines: key, slots and the
// description, escaped and cut to 60 columns.
func PoolRows(stateDir string, reg *machine.Registry) []string {
	width := 0
	for _, p := range reg.Pools {
		width = max(width, len(p))
	}
	var rows []string
	for _, p := range reg.Pools {
		cfg, err := lane.ReadConfig(lane.LaneDir(stateDir, p))
		slots, desc := "? slots", "(config.json unreadable)"
		if err == nil {
			n := max(cfg.Slots, 1)
			slots = fmt.Sprintf("%d slots", n)
			if n == 1 {
				slots = "1 slot"
			}
			desc = cut(textsafe.Escape(cfg.Description), 60)
		}
		row := fmt.Sprintf("  %-*s  %-8s %s", width, p, slots, desc)
		rows = append(rows, strings.TrimRight(row, " "))
	}
	return rows
}

// cut shortens s to n characters, the last three of them "...".
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-3]) + "..."
}

// quoted renders keys as "a", "b".
func quoted(keys []string) string {
	q := make([]string, len(keys))
	for i, k := range keys {
		q[i] = fmt.Sprintf("%q", k)
	}
	return strings.Join(q, ", ")
}

// fixFor is the caller's run line with the queue and pool the fix sets,
// plus --wait 5m when quiet is set and the caller gave no --wait. It
// returns the line and the reason no runnable line may be printed, if a
// lane in it requires a reason and the run has none.
func fixFor(stateDir string, reg *machine.Registry, req Request, queue, pool []string, quiet bool) (fixline.Run, string) {
	r := req.Fix
	r.Queue, r.Pool = queue, pool
	r.Flags = append([]fixline.Flag(nil), req.Fix.Flags...)
	if quiet && !req.WaitGiven {
		r.Flags = append(r.Flags, fixline.Flag{Name: "wait", Value: quietWait})
	}
	why := ""
	if strings.TrimSpace(req.Reason) == "" {
		for _, k := range append(append([]string(nil), queue...), pool...) {
			if cfg, err := lane.ReadConfig(lane.LaneDir(stateDir, k)); err == nil && cfg.RequireReason {
				why = fmt.Sprintf("queue %q requires --reason and this run has none", k)
				break
			}
		}
	}
	return r, why
}

// namedOrder is the run's named keys in the total order: project keys,
// then pools, each sorted.
func namedOrder(reg *machine.Registry, named []string) []string {
	ls := make([]Lane, len(named))
	for i, k := range named {
		ls[i] = Lane{Key: k, Pool: reg.IsPool(k)}
	}
	sortLanes(ls)
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.Key
	}
	return out
}

// configLine is "incoda config KEY --pool S [--quiet-machine]".
func configLine(sh fixline.Shell, key string, s Suggestion) string {
	w := []fixline.Word{fixline.Lit("incoda"), fixline.Lit("config"), fixline.Key(key), fixline.Lit("--pool"), fixline.Key(strings.Join(s.Pools, ","))}
	if s.QuietMachine {
		w = append(w, fixline.Lit("--quiet-machine"))
	}
	return fixline.Line{Words: w}.Render(sh).Text
}

// unlinkedRefusal is the unlinked refusal of spec 4.1 for the named
// project keys in keys, which have no link. projects counts the run's
// named project keys: with one, the fix is that run with --pool set to the
// suggestion (its first link); with several, one config line per key and
// then the run line without --pool.
func unlinkedRefusal(stateDir string, reg *machine.Registry, req Request, keys []string, projects int) *machine.Refusal {
	sh := fixline.Native()
	lines := []string{"unlinked: " + strings.Join(keys, ", ")}
	if len(keys) == 1 {
		lines = append(lines, fmt.Sprintf("queue %q is not linked to any pool; every project queue names the machine-wide pools its jobs use.", keys[0]))
	} else {
		lines = append(lines, fmt.Sprintf("queues %s are not linked to any pool; every project queue names the machine-wide pools its jobs use.", quoted(keys)))
	}
	lines = append(lines, "pools on this machine:")
	lines = append(lines, PoolRows(stateDir, reg)...)

	sugg := make([]Suggestion, len(keys))
	all := true
	for i, k := range keys {
		sugg[i] = Suggest(reg, k)
		all = all && sugg[i].Usable
	}
	links := make([]string, len(keys))
	for i, k := range keys {
		links[i] = "incoda link " + k
	}
	askUser := []string{
		"(incoda init links every queue in one pass)",
		"do not pick a pool yourself, and never because it is free.",
	}

	if len(keys) == 1 {
		s, k := sugg[0], keys[0]
		if !s.Usable {
			lines = append(lines, fmt.Sprintf("suggested: none (%s)", s.Why),
				fmt.Sprintf("ask the user which pools this queue's jobs use; they run: incoda link %s", k))
			return refusal(append(lines, askUser...))
		}
		lines = append(lines, fmt.Sprintf("suggested: %s (name matches %s)", s.Text(), s.Pattern))
		takes := "these pools"
		if s.QuietMachine {
			takes = "these pools and quiet-machine"
		}
		if projects == 1 {
			lines = append(lines, fmt.Sprintf("to link it to the suggestion (stored; every later run on this queue takes %s):", takes))
			run, why := fixFor(stateDir, reg, req, namedOrder(reg, req.Named), s.Pools, s.QuietMachine)
			lines = append(lines, fixline.RunLines(sh, run, why, "run it")...)
		} else {
			lines = append(lines, fmt.Sprintf("to link it to the suggestion (stored; every later run on this queue takes %s), then run:", takes),
				"  "+configLine(sh, k, s))
			run, why := fixFor(stateDir, reg, req, namedOrder(reg, req.Named), nil, s.QuietMachine)
			lines = append(lines, fixline.RunLines(sh, run, why, "run it")...)
		}
		if s.QuietMachine && !req.WaitGiven {
			lines = append(lines, "(--wait 5m added: quiet-machine holds every pool it has drained while it waits for the rest)")
		}
		return refusal(append(lines, fmt.Sprintf("if the suggestion does not fit, ask the user; they run: incoda link %s", k)))
	}

	parts := make([]string, len(keys))
	quiet := false
	for i, k := range keys {
		if sugg[i].Usable {
			parts[i] = fmt.Sprintf("%s -> %s (name matches %s)", k, sugg[i].Text(), sugg[i].Pattern)
			quiet = quiet || sugg[i].QuietMachine
		} else {
			parts[i] = fmt.Sprintf("%s: none (%s)", k, sugg[i].Why)
		}
	}
	lines = append(lines, "suggested: "+strings.Join(parts, "; "))
	if !all {
		lines = append(lines, "ask the user which pools these queues' jobs use; they run: "+strings.Join(links, ", "))
		return refusal(append(lines, askUser...))
	}
	lines = append(lines, "to link them to the suggestions (stored; every later run on these queues takes these pools), then run:")
	for i, k := range keys {
		lines = append(lines, "  "+configLine(sh, k, sugg[i]))
	}
	run, why := fixFor(stateDir, reg, req, namedOrder(reg, req.Named), nil, quiet)
	lines = append(lines, fixline.RunLines(sh, run, why, "run it")...)
	if quiet && !req.WaitGiven {
		lines = append(lines, "(--wait 5m added: quiet-machine holds every pool it has drained while it waits for the rest)")
	}
	return refusal(append(lines, "if a suggestion does not fit, ask the user; they run: "+strings.Join(links, ", ")))
}
```

In `internal/runplan/runplan.go`, make these 5 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 5) Replace:

```go
	"sort"
	"strings"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/textsafe"
```

with:

```go
	"sort"
	"strings"

	"github.com/deblasis/incoda/internal/fixline"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/textsafe"
```

(2 of 5) Replace:

```go
	// re-checked: the ancestor's ticket was admitted under its rules
	// (spec 4.5).
	Held map[string]bool
}

// Lane is one lane of a plan.
```

with:

```go
	// re-checked: the ancestor's ticket was admitted under its rules
	// (spec 4.5).
	Held map[string]bool
	// Fix is the caller's own run line for printed fix lines: every flag
	// it gave except --queue and --pool, its argv, and its directory as
	// Dir and Here. Queue and Pool are set by each refusal. WaitGiven says
	// whether --wait was among the flags.
	Fix       fixline.Run
	WaitGiven bool
}

// Lane is one lane of a plan.
```

(3 of 5) Replace:

```go
	return a.Key < b.Key
}

// Make computes the plan for req against reg and the lanes' configs.
func Make(stateDir string, reg *machine.Registry, req Request) (*Plan, error) {
	p := &Plan{Generation: reg.Generation, Links: map[string][]string{}}
```

with:

```go
	return a.Key < b.Key
}

func sortLanes(ls []Lane) { sort.Slice(ls, func(i, j int) bool { return Less(ls[i], ls[j]) }) }

// Make computes the plan for req against reg and the lanes' configs.
func Make(stateDir string, reg *machine.Registry, req Request) (*Plan, error) {
	p := &Plan{Generation: reg.Generation, Links: map[string][]string{}}
```

(4 of 5) Replace:

```go
			return nil, closedRefusal(*l)
		}
		lanes[k] = l
	}

	// Each named project key brings its linked pools. Every one must
```

with:

```go
			return nil, closedRefusal(*l)
		}
		lanes[k] = l
	}

	// A project lane with no link refuses the run before any ticket, with
	// its suggestion and the line that makes it (spec 4.1).
	var unlinked []string
	projects := 0
	for _, k := range named {
		if l := lanes[k]; !l.Pool {
			projects++
			if len(l.Cfg.Pools) == 0 {
				unlinked = append(unlinked, k)
			}
		}
	}
	if len(unlinked) > 0 {
		return nil, unlinkedRefusal(stateDir, reg, req, unlinked, projects)
	}

	// Each named project key brings its linked pools. Every one must
```

(5 of 5) Replace:

```go
	for _, l := range lanes {
		p.Lanes = append(p.Lanes, *l)
	}
	sort.Slice(p.Lanes, func(i, j int) bool { return Less(p.Lanes[i], p.Lanes[j]) })

	for _, l := range p.Lanes {
		if l.Pool && !l.Named && req.Held[l.Key] {
```

with:

```go
	for _, l := range lanes {
		p.Lanes = append(p.Lanes, *l)
	}
	sortLanes(p.Lanes)

	for _, l := range p.Lanes {
		if l.Pool && !l.Named && req.Held[l.Key] {
```

- [ ] **Step 4: Link the project keys of the existing tests**

Every existing test that runs a new binary on a project key now links that key first; the tests whose run is the command that migrates a state directory run on the `builds` pool instead (no link can exist before the migration); `TestUnknownStrayKeyCountsOnEveryPool` now expects a linked project run to wait for the stray through its pool (it is charged on every pool, spec 2.3); `TestRunLeavesOwnEnvironmentAlone` runs on `builds`.

In `config_test.go`, make these 6 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 6) Replace:

```go
func TestQueueConfigSuppliesSlots(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	if out, code := runIncoda(t, incoda, state, "config", "cfgslots", "--slots", "2", "--description", "CPU and RAM"); code != 0 {
```

with:

```go
func TestQueueConfigSuppliesSlots(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "cfgslots")
	stamps := t.TempDir()

	if out, code := runIncoda(t, incoda, state, "config", "cfgslots", "--slots", "2", "--description", "CPU and RAM"); code != 0 {
```

(2 of 6) Replace:

```go
func TestClosedQueueRefusesRuns(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	msg := "retired: use wintty-build for builds and wintty-desktop for harnesses"
```

with:

```go
func TestClosedQueueRefusesRuns(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "old")
	stamps := t.TempDir()

	msg := "retired: use wintty-build for builds and wintty-desktop for harnesses"
```

(3 of 6) Replace:

```go
func TestRequireReason(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	if out, code := runIncoda(t, incoda, state, "config", "strict", "--require-reason"); code != 0 {
```

with:

```go
func TestRequireReason(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "strict")
	stamps := t.TempDir()

	if out, code := runIncoda(t, incoda, state, "config", "strict", "--require-reason"); code != 0 {
```

(4 of 6) Replace:

```go
func TestMultiKeyAcquiresEveryQueue(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	holder := exec.Command(incoda, "run", "--queue", "mk-b,mk-a", "--wait", "60s", "--poll", "50ms", "--quiet",
```

with:

```go
func TestMultiKeyAcquiresEveryQueue(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "mk-a", "mk-b")
	stamps := t.TempDir()

	holder := exec.Command(incoda, "run", "--queue", "mk-b,mk-a", "--wait", "60s", "--poll", "50ms", "--quiet",
```

(5 of 6) Replace:

```go
func TestExclusiveRunWaitsForAnEmptyQueue(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "excl", "--slots", "2"); code != 0 {
		t.Fatalf("config: %s", out)
```

with:

```go
func TestExclusiveRunWaitsForAnEmptyQueue(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "excl")
	stamps := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "excl", "--slots", "2"); code != 0 {
		t.Fatalf("config: %s", out)
```

(6 of 6) Replace:

```go
func TestLogLineStaysOneLine(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	marker := filepath.Join(t.TempDir(), "m.txt")
	if out, code := runIncoda(t, incoda, state, "run", "--queue", "oneline", "--quiet", "--", stamp, marker, "a\nb", "1"); code != 0 {
		t.Fatalf("run: exit %d\n%s", code, out)
```

with:

```go
func TestLogLineStaysOneLine(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "oneline")
	marker := filepath.Join(t.TempDir(), "m.txt")
	if out, code := runIncoda(t, incoda, state, "run", "--queue", "oneline", "--quiet", "--", stamp, marker, "a\nb", "1"); code != 0 {
		t.Fatalf("run: exit %d\n%s", code, out)
```

In `held_test.go`, make these 3 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 3) Replace:

```go
func TestDeadHeldEntryIsDropped(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	marker := filepath.Join(t.TempDir(), "m.txt")
	// The lane must exist for the drop to be logged in it: a bogus key in
	// the environment never creates a lane directory.
```

with:

```go
func TestDeadHeldEntryIsDropped(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "hd")
	marker := filepath.Join(t.TempDir(), "m.txt")
	// The lane must exist for the drop to be logged in it: a bogus key in
	// the environment never creates a lane directory.
```

(2 of 3) Replace:

```go
func TestBareHeldKeyIsMalformed(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	out, code := runWithHeld(t, incoda, state, "hk",
		"run", "--queue", "hk", "--", stamp, filepath.Join(t.TempDir(), "m"), "m", "1")
	if code != 0 || !strings.Contains(out, "incoda: held-dropped: hk (malformed)") {
```

with:

```go
func TestBareHeldKeyIsMalformed(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "hk")
	out, code := runWithHeld(t, incoda, state, "hk",
		"run", "--queue", "hk", "--", stamp, filepath.Join(t.TempDir(), "m"), "m", "1")
	if code != 0 || !strings.Contains(out, "incoda: held-dropped: hk (malformed)") {
```

(3 of 3) Replace:

```go
	}
	incoda, stamp := binaries(t)
	state := t.TempDir()
	holder, _ := startHolder(t, incoda, stamp, state, "na", "holder", 20000, "50ms")
	defer func() { _ = holder.Process.Kill(); _ = holder.Wait() }()
	waitFor(t, incoda, state, "na", func(q queueReport) bool { return len(q.Holders) == 1 })
```

with:

```go
	}
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "na")
	holder, _ := startHolder(t, incoda, stamp, state, "na", "holder", 20000, "50ms")
	defer func() { _ = holder.Process.Kill(); _ = holder.Wait() }()
	waitFor(t, incoda, state, "na", func(q queueReport) bool { return len(q.Holders) == 1 })
```

In `integration_test.go`, make these 10 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 10) Replace:

```go
func TestMutualExclusionAcrossDifferentWorkingDirectories(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	const n = 5
```

with:

```go
func TestMutualExclusionAcrossDifferentWorkingDirectories(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "shared")
	stamps := t.TempDir()

	const n = 5
```

(2 of 10) Replace:

```go
func TestSlotsAllowExactlyN(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	const n = 6
```

with:

```go
func TestSlotsAllowExactlyN(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "twolane")
	stamps := t.TempDir()

	const n = 6
```

(3 of 10) Replace:

```go
func TestConfiguredSlotsAreHonored(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	const n = 6
```

with:

```go
func TestConfiguredSlotsAreHonored(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "cfglane")
	stamps := t.TempDir()

	const n = 6
```

(4 of 10) Replace:

```go
func TestDisagreeingSlotsRefusedOnConfiguredQueue(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	cfg := exec.Command(incoda, "config", "cfgref", "--slots", "3")
```

with:

```go
func TestDisagreeingSlotsRefusedOnConfiguredQueue(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "cfgref")
	stamps := t.TempDir()

	cfg := exec.Command(incoda, "config", "cfgref", "--slots", "3")
```

(5 of 10) Replace:

```go
func TestDisagreeingSlotsRefusedAtEnrollAfterConfigChange(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	// racea,raceb both start at slots 1 so the pre-check passes.
```

with:

```go
func TestDisagreeingSlotsRefusedAtEnrollAfterConfigChange(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "racea", "raceb")
	stamps := t.TempDir()

	// racea,raceb both start at slots 1 so the pre-check passes.
```

(6 of 10) Replace:

```go
func TestHardKilledHolderFreesTheLane(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	victim := exec.Command(incoda, "run", "--queue", "crash", "--wait", "60s", "--poll", "50ms", "--quiet",
```

with:

```go
func TestHardKilledHolderFreesTheLane(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "crash")
	stamps := t.TempDir()

	victim := exec.Command(incoda, "run", "--queue", "crash", "--wait", "60s", "--poll", "50ms", "--quiet",
```

(7 of 10) Replace:

```go
func TestFIFOOrder(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	holder := exec.Command(incoda, "run", "--queue", "fifo", "--wait", "60s", "--poll", "50ms", "--quiet",
```

with:

```go
func TestFIFOOrder(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "fifo")
	stamps := t.TempDir()

	holder := exec.Command(incoda, "run", "--queue", "fifo", "--wait", "60s", "--poll", "50ms", "--quiet",
```

(8 of 10) Replace:

```go
func TestWaitTimeoutExitCode(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	holder := exec.Command(incoda, "run", "--queue", "busy", "--wait", "60s", "--poll", "50ms", "--quiet",
```

with:

```go
func TestWaitTimeoutExitCode(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "busy")
	stamps := t.TempDir()

	holder := exec.Command(incoda, "run", "--queue", "busy", "--wait", "60s", "--poll", "50ms", "--quiet",
```

(9 of 10) Replace:

```go
func TestExitCodePassthrough(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	for _, want := range []int{0, 1, 7, 42} {
```

with:

```go
func TestExitCodePassthrough(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "codes")
	stamps := t.TempDir()

	for _, want := range []int{0, 1, 7, 42} {
```

(10 of 10) Replace:

```go
func TestForceReleaseRefusesLiveHolderFromCLI(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	holder := exec.Command(incoda, "run", "--queue", "fr", "--wait", "60s", "--poll", "50ms", "--quiet",
```

with:

```go
func TestForceReleaseRefusesLiveHolderFromCLI(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "fr")
	stamps := t.TempDir()

	holder := exec.Command(incoda, "run", "--queue", "fr", "--wait", "60s", "--poll", "50ms", "--quiet",
```

In `internal/cli/env_test.go`, replace:

```go
	t.Setenv("INCODA_DIR", t.TempDir())
	t.Setenv("INCODA_HELD", "")
	os.Unsetenv("INCODA_HELD")
	if code := Main([]string{"run", "--queue", "envq", "--quiet", "--", "true"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("run exited %d", code)
	}
	if v, ok := os.LookupEnv("INCODA_HELD"); ok {
```

with:

```go
	t.Setenv("INCODA_DIR", t.TempDir())
	t.Setenv("INCODA_HELD", "")
	os.Unsetenv("INCODA_HELD")
	// A pool: it runs without a link on a fresh state directory.
	if code := Main([]string{"run", "--queue", "builds", "--quiet", "--", "true"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("run exited %d", code)
	}
	if v, ok := os.LookupEnv("INCODA_HELD"); ok {
```

In `kill_test.go`, make these 5 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 5) Replace:

```go
func TestKillHolderCooperatively(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()

	holder, holderErr := startHolder(t, incoda, stamp, state, "kill", "victim", 30000, "50ms")
	waitFor(t, incoda, state, "kill", func(q queueReport) bool { return len(q.Holders) == 1 })
```

with:

```go
func TestKillHolderCooperatively(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "kill")

	holder, holderErr := startHolder(t, incoda, stamp, state, "kill", "victim", 30000, "50ms")
	waitFor(t, incoda, state, "kill", func(q queueReport) bool { return len(q.Holders) == 1 })
```

(2 of 5) Replace:

```go
func TestKillWaiterCancelsIt(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()

	holder, _ := startHolder(t, incoda, stamp, state, "kw", "h", 4000, "50ms")
	defer func() { _ = holder.Wait() }()
```

with:

```go
func TestKillWaiterCancelsIt(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "kw")

	holder, _ := startHolder(t, incoda, stamp, state, "kw", "h", 4000, "50ms")
	defer func() { _ = holder.Wait() }()
```

(3 of 5) Replace:

```go
func TestKillDuringMultiKeyWait(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()

	blocker, _ := startHolder(t, incoda, stamp, state, "mk2-b", "blocker", 30000, "50ms")
	defer func() { _ = blocker.Process.Kill(); _ = blocker.Wait() }()
```

with:

```go
func TestKillDuringMultiKeyWait(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "mk2-a", "mk2-b")

	blocker, _ := startHolder(t, incoda, stamp, state, "mk2-b", "blocker", 30000, "50ms")
	defer func() { _ = blocker.Process.Kill(); _ = blocker.Wait() }()
```

(4 of 5) Replace:

```go
func TestKillForceTerminates(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()

	// A holder polling every 10 s stands in for one that never answers: it
	// cannot notice the request before the killer gives up on it, so the
```

with:

```go
func TestKillForceTerminates(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "kf")

	// A holder polling every 10 s stands in for one that never answers: it
	// cannot notice the request before the killer gives up on it, so the
```

(5 of 5) Replace:

```go
func TestKillReasonEscaped(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()

	holder, holderErr := startHolder(t, incoda, stamp, state, "killesc", "victim", 30000, "50ms")
	waitFor(t, incoda, state, "killesc", func(q queueReport) bool { return len(q.Holders) == 1 })
```

with:

```go
func TestKillReasonEscaped(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "killesc")

	holder, holderErr := startHolder(t, incoda, stamp, state, "killesc", "victim", 30000, "50ms")
	waitFor(t, incoda, state, "killesc", func(q queueReport) bool { return len(q.Holders) == 1 })
```

In `migrate_crash_test.go`, replace:

```go
				seedOldLayout(t, state)
			}
			pause := filepath.Join(t.TempDir(), "go")
			cmd := exec.Command(bin, "run", "--queue", "newq", "--wait", "60s", "--poll", "50ms", "--", stamp, filepath.Join(t.TempDir(), "s"), "s", "1")
			cmd.Env = append(laneEnv(state), "INCODA_TEST_NO_EXCHANGE=1", "INCODA_TEST_PAUSE_AT="+tc.pauseAt, "INCODA_TEST_PAUSE_FILE="+pause)
			var errBuf syncBuffer
			cmd.Stderr = &errBuf
```

with:

```go
				seedOldLayout(t, state)
			}
			pause := filepath.Join(t.TempDir(), "go")
			// A pool, so the run needs no link: it is the command that
			// migrates. The seeded builds lane requires a reason.
			cmd := exec.Command(bin, "run", "--queue", "builds", "--reason", "fence race", "--wait", "60s", "--poll", "50ms", "--", stamp, filepath.Join(t.TempDir(), "s"), "s", "1")
			cmd.Env = append(laneEnv(state), "INCODA_TEST_NO_EXCHANGE=1", "INCODA_TEST_PAUSE_AT="+tc.pauseAt, "INCODA_TEST_PAUSE_FILE="+pause)
			var errBuf syncBuffer
			cmd.Stderr = &errBuf
```

In `migrate_test.go`, make these 3 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 3) Replace:

```go
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
```

with:

```go
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
```

(2 of 3) Replace:

```go
	if out, code := runIncoda(t, incoda, state, "config", "x"); code != 0 {
		t.Fatalf("config: %d\n%s", code, out)
	}
	q := filepath.Join(state, "queues")
	if err := os.Remove(q); err != nil {
		t.Fatal(err)
```

with:

```go
	if out, code := runIncoda(t, incoda, state, "config", "x"); code != 0 {
		t.Fatalf("config: %d\n%s", code, out)
	}
	linkTestKeys(t, incoda, state, "y")
	q := filepath.Join(state, "queues")
	if err := os.Remove(q); err != nil {
		t.Fatal(err)
```

(3 of 3) Replace:

```go
	incoda, stamp := binaries(t)
	state := t.TempDir()
	release := holdOldTicket(t, filepath.Join(state, "queues"), "held", 999999, "zig", "build")
	cmd := exec.Command(incoda, "run", "--queue", "newq", "--wait", "60s", "--poll", "50ms", "--", stamp, filepath.Join(t.TempDir(), "s"), "s", "1")
	cmd.Env = laneEnv(state)
	var errBuf syncBuffer
	cmd.Stderr = &errBuf
```

with:

```go
	incoda, stamp := binaries(t)
	state := t.TempDir()
	release := holdOldTicket(t, filepath.Join(state, "queues"), "held", 999999, "zig", "build")
	// A pool, so the run needs no link: it is the command that migrates.
	cmd := exec.Command(incoda, "run", "--queue", "builds", "--wait", "60s", "--poll", "50ms", "--", stamp, filepath.Join(t.TempDir(), "s"), "s", "1")
	cmd.Env = laneEnv(state)
	var errBuf syncBuffer
	cmd.Stderr = &errBuf
```

In `oldbin_test.go`, make these 3 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 3) Replace:

```go
			defer func() { _ = o.Process.Kill(); _ = o.Wait() }()
			waitForTicket(t, filepath.Join(state, "queues", "oldq"))

			out, code := runIncoda(t, incoda, state, "run", "--queue", "newq", "--wait", "60s", "--poll", "50ms", "--", stamp, filepath.Join(stamps, "new.txt"), "new", "10")
			if code != 0 {
				t.Fatalf("new run: exit %d\n%s", code, out)
			}
```

with:

```go
			defer func() { _ = o.Process.Kill(); _ = o.Wait() }()
			waitForTicket(t, filepath.Join(state, "queues", "oldq"))

			// A pool, so the run needs no link: it is the command that
			// migrates.
			out, code := runIncoda(t, incoda, state, "run", "--queue", "builds", "--wait", "60s", "--poll", "50ms", "--", stamp, filepath.Join(stamps, "new.txt"), "new", "10")
			if code != 0 {
				t.Fatalf("new run: exit %d\n%s", code, out)
			}
```

(2 of 3) Replace:

```go
	}

	pause := filepath.Join(t.TempDir(), "go")
	m := exec.Command(bin, "run", "--queue", "newq", "--wait", "60s", "--poll", "50ms", "--", stamp, filepath.Join(stamps, "new.txt"), "new", "10")
	m.Env = append(laneEnv(state), "INCODA_TEST_PAUSE_AT=M3", "INCODA_TEST_PAUSE_FILE="+pause)
	var mErr syncBuffer
	m.Stderr = &mErr
```

with:

```go
	}

	pause := filepath.Join(t.TempDir(), "go")
	// A pool, so the run needs no link: it is the command that migrates.
	m := exec.Command(bin, "run", "--queue", "builds", "--wait", "60s", "--poll", "50ms", "--", stamp, filepath.Join(stamps, "new.txt"), "new", "10")
	m.Env = append(laneEnv(state), "INCODA_TEST_PAUSE_AT=M3", "INCODA_TEST_PAUSE_FILE="+pause)
	var mErr syncBuffer
	m.Stderr = &mErr
```

(3 of 3) Replace:

```go
	defer func() { _ = o.Process.Kill(); _ = o.Wait() }()
	waitForTicket(t, filepath.Join(state, "queues", "outer"))

	out, code := runIncoda(t, incoda, state, "run", "--queue", "m", "--wait", "60s", "--poll", "50ms", "--", stamp, filepath.Join(t.TempDir(), "m.txt"), "m", "10")
	if code != 0 {
		t.Fatalf("the migrator must finish once the old run ends: %d\n%s", code, out)
	}
```

with:

```go
	defer func() { _ = o.Process.Kill(); _ = o.Wait() }()
	waitForTicket(t, filepath.Join(state, "queues", "outer"))

	// A pool, so the run needs no link: it is the command that migrates.
	out, code := runIncoda(t, incoda, state, "run", "--queue", "builds", "--wait", "60s", "--poll", "50ms", "--", stamp, filepath.Join(t.TempDir(), "m.txt"), "m", "10")
	if code != 0 {
		t.Fatalf("the migrator must finish once the old run ends: %d\n%s", code, out)
	}
```

In `oldkill_test.go`, replace:

```go
		t.Fatalf("seed the old layout: %d\n%s", code, out)
	}
	pause := filepath.Join(t.TempDir(), "go")
	m := exec.Command(bin, "run", "--queue", "newq", "--wait", "60s", "--poll", "50ms", "--", stamp, filepath.Join(t.TempDir(), "new.txt"), "new", "10")
	m.Env = append(laneEnv(state), "INCODA_TEST_PAUSE_AT=M3", "INCODA_TEST_PAUSE_FILE="+pause)
	var mErr syncBuffer
	m.Stderr = &mErr
```

with:

```go
		t.Fatalf("seed the old layout: %d\n%s", code, out)
	}
	pause := filepath.Join(t.TempDir(), "go")
	// A pool, so the run needs no link: it is the command that migrates.
	m := exec.Command(bin, "run", "--queue", "builds", "--wait", "60s", "--poll", "50ms", "--", stamp, filepath.Join(t.TempDir(), "new.txt"), "new", "10")
	m.Env = append(laneEnv(state), "INCODA_TEST_PAUSE_AT=M3", "INCODA_TEST_PAUSE_FILE="+pause)
	var mErr syncBuffer
	m.Stderr = &mErr
```

In `process_group_test.go`, make these 3 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 3) Replace:

```go
	incoda, _ := binaries(t)
	tree := treeBinary(t)
	state := t.TempDir()
	out := filepath.Join(t.TempDir(), "tree.txt")

	holder := exec.Command(incoda, "run", "--queue", "pg", "--quiet", "--poll", "50ms", "--", tree, out)
```

with:

```go
	incoda, _ := binaries(t)
	tree := treeBinary(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "pg")
	out := filepath.Join(t.TempDir(), "tree.txt")

	holder := exec.Command(incoda, "run", "--queue", "pg", "--quiet", "--poll", "50ms", "--", tree, out)
```

(2 of 3) Replace:

```go
	incoda, _ := binaries(t)
	tree := treeBinary(t)
	state := t.TempDir()
	out := filepath.Join(t.TempDir(), "tree.txt")

	holder := exec.Command(incoda, "run", "--queue", "pgk", "--quiet", "--poll", "50ms", "--", tree, out)
```

with:

```go
	incoda, _ := binaries(t)
	tree := treeBinary(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "pgk")
	out := filepath.Join(t.TempDir(), "tree.txt")

	holder := exec.Command(incoda, "run", "--queue", "pgk", "--quiet", "--poll", "50ms", "--", tree, out)
```

(3 of 3) Replace:

```go
	incoda, _ := binaries(t)
	tree := treeBinary(t)
	state := t.TempDir()
	out := filepath.Join(t.TempDir(), "tree.txt")
	cmd := exec.Command(incoda, "run", "--queue", "outer", "--quiet", "--",
		incoda, "run", "--queue", "inner", "--quiet", "--", tree, out)
```

with:

```go
	incoda, _ := binaries(t)
	tree := treeBinary(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "inner", "outer")
	out := filepath.Join(t.TempDir(), "tree.txt")
	cmd := exec.Command(incoda, "run", "--queue", "outer", "--quiet", "--",
		incoda, "run", "--queue", "inner", "--quiet", "--", tree, out)
```

In `reentry_test.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 2) Replace:

```go
func TestReentrantRunPassesThrough(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()
	marker := filepath.Join(stamps, "inner.txt")

```

with:

```go
func TestReentrantRunPassesThrough(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "other", "re")
	stamps := t.TempDir()
	marker := filepath.Join(stamps, "inner.txt")

```

(2 of 2) Replace:

```go
func TestReleaseRecordsJobStats(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	cmd := exec.Command(incoda, "run", "--queue", "acct", "--quiet", "--owner", "test-session",
```

with:

```go
func TestReleaseRecordsJobStats(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "acct")
	stamps := t.TempDir()

	cmd := exec.Command(incoda, "run", "--queue", "acct", "--quiet", "--owner", "test-session",
```

In `stoppedrun_test.go`, replace:

```go
	runnerSentinel(t)
	incoda, _ := binaries(t)
	state := t.TempDir()
	started := filepath.Join(t.TempDir(), "started")
	c := exec.Command(incoda, "run", "--queue", "jc", "--poll", "50ms", "--quiet", "--", "sh", "-c", `touch "$0"; sleep 3`, started)
	c.Env = laneEnv(state)
```

with:

```go
	runnerSentinel(t)
	incoda, _ := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "jc")
	started := filepath.Join(t.TempDir(), "started")
	c := exec.Command(incoda, "run", "--queue", "jc", "--poll", "50ms", "--quiet", "--", "sh", "-c", `touch "$0"; sleep 3`, started)
	c.Env = laneEnv(state)
```

In `strays_test.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 2) Replace:

```go
}

// TestUnknownStrayKeyCountsOnEveryPool: an unpooled run on a key that is
// no lane of the new layout counts on every pool, and on nothing else: a
// project key does not wait for it.
func TestUnknownStrayKeyCountsOnEveryPool(t *testing.T) {
	incoda, stamp := binaries(t)
	o, state := startOldRunAfterFenceDeletion(t, "v0.6.0", "oldjob", stamp, filepath.Join(t.TempDir(), "old.txt"), "old", "30000")
```

with:

```go
}

// TestUnknownStrayKeyCountsOnEveryPool: an unpooled run on a key that is
// no lane of the new layout counts on every pool, so a run on a linked
// project lane waits for it through its pool too.
func TestUnknownStrayKeyCountsOnEveryPool(t *testing.T) {
	incoda, stamp := binaries(t)
	o, state := startOldRunAfterFenceDeletion(t, "v0.6.0", "oldjob", stamp, filepath.Join(t.TempDir(), "old.txt"), "old", "30000")
```

(2 of 2) Replace:

```go
			t.Fatalf("pool %s: want exit 121 naming the unpooled run, got %d:\n%s", pool, code, out)
		}
	}
	if out, code := runIncoda(t, incoda, state, "run", "--queue", "proj", "--wait", "0", "--", stamp, filepath.Join(t.TempDir(), "q.txt"), "q", "1"); code != 0 {
		t.Fatalf("a project key is not charged: %d\n%s", code, out)
	}
}

```

with:

```go
			t.Fatalf("pool %s: want exit 121 naming the unpooled run, got %d:\n%s", pool, code, out)
		}
	}
	mustRun(t, incoda, state, 0, "config", "proj", "--pool", "tests")
	out, code := runIncoda(t, incoda, state, "run", "--queue", "proj", "--wait", "300ms", "--poll", "50ms", "--", stamp, filepath.Join(t.TempDir(), "q.txt"), "q", "1")
	if code != 121 || !strings.Contains(out, fmt.Sprintf("unpooled run by an older incoda: pid %d, key oldjob", o.Process.Pid)) {
		t.Fatalf("a linked project run waits for it through its pool: %d\n%s", code, out)
	}
}

```

- [ ] **Step 5: Run the tests to see them pass**

Run (bash), one at a time:

- `go test ./internal/runplan/ -count=1`
- `go test . -run 'TestUnlinkedRunIsRefusedAndLeavesNothingBehind' -count=1`

Expected: `ok` for each package. Then run the whole root package, since this task touches many of its tests: `go test . -count=1` (about four minutes); expected `ok`.

- [ ] **Step 6: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 7: Commit**

```bash
git add config_test.go held_test.go integration_test.go internal/cli/env_test.go internal/cli/run.go internal/fixline/fixline.go internal/runplan/refusals.go internal/runplan/runplan.go internal/runplan/runplan_test.go kill_test.go migrate_crash_test.go migrate_test.go oldbin_test.go oldkill_test.go pools_test.go process_group_test.go reentry_test.go stoppedrun_test.go strays_test.go
git commit -F - <<'MSG'
feat: a run on an unlinked project lane is refused with its suggestion

After the closed check and before any ticket, a project lane with no
link refuses with exit 120: the pools on this machine, the suggestion
from the name table and the run line that makes it the first link, or
the ask-the-user lines when there is none. Several unlinked keys get one
config line each. The check reads config.json only, so a typo leaves no
lane behind. Tests link their project keys first.
MSG
```


---

### Task 8: run --pool: a subset of the link, and a first link only to the suggestion

Spec 4.2. `--pool a,b` (alias `--pools`) is a set of registered pools applying to every named project key. On a linked key it must be a non-empty subset of the link and the run takes only those pools (else `pool-mismatch:` with the line that names the extra pools next to the queue for this run only). With no named project key it is refused (`pool-mismatch: --pool needs a project key; "builds" is a pool` and the line without `--pool`); next to a project key a named pool ignores it (`--pool ignored for pool key "builds"`). On an unlinked key the run makes a first link only when the set equals the key's suggestion, as a compare-and-set under machine.lock then the key's registry lock before any ticket: still unlinked, it writes `pools` (and `quiet_machine` when the suggestion has it), logs `event=link by=run old= new=<set>` and prints `incoda: linked: KEY -> <set> (stored; every later run on KEY takes these pools)`; already linked to the same set, it goes on; linked to another set, it refuses `link-conflict: "KEY" was just linked to tests by pid N; rerun without --pool`. Any other set is `link-needs-user:` before any lock or ticket. If any named key would be refused, the whole run is refused before anything is written.

**Files:**
- Modify: `internal/cli/run.go`
- Modify: `internal/runplan/refusals.go`
- Modify: `internal/runplan/runplan.go`
- Test: `internal/cli/run_test.go` (new)
- Test: `internal/runplan/runplan_test.go`
- Test: `pools_test.go`

**Interfaces:**
- Consumes: `machine.WriteLink`, `machine.LastLinker`, `machine.LinkedLine`, `machine.NotAPool` (Task 3); `runplan.Suggest`, `fixFor`, `namedOrder` (Task 7); `poolsValue` (Task 3).
- Produces: `runplan.Request.Pool []string`; `runplan.Plan` gains `Pool []string`, `FirstLinks []FirstLink`, `Notes []string`; `type runplan.FirstLink struct { Key string; Pools []string; Quiet bool }`; unexported `linkNeedsUser`, `poolMismatch`, `noProjectRefusal`, `resolvePool`. In `cli`: `planWithFirstLinks(dir, reg, req, o, quiet, stderr, p) (*runplan.Plan, error)` and the test seam `beforeFirstLink func(dir, key string)`.

- [ ] **Step 1: Write the failing tests**

`TestPrintedUnlinkedLineRunsAsPrinted` pastes the printed line into `/bin/sh` with this test's own build first on PATH and only `/usr/bin:/bin` after it, so no incoda installed on the machine can answer.

Append to the end of `internal/runplan/runplan_test.go`:

```go

// TestPoolFlag: --pool takes a subset of each named project key's link;
// on an unlinked key only the suggestion becomes a first link; anything
// else is refused before anything is written (spec 4.2).
func TestPoolFlag(t *testing.T) {
	if fixline.Native() != fixline.POSIX {
		t.Skip("the expected lines are POSIX sh")
	}
	configs := map[string]string{
		"polymatto": `{"schema":2,"pools":["builds","computer-use","tests"]}`,
		"kf-gate":   `{"schema":2,"pools":["tests"]}`,
	}
	for k, v := range spec4Pools {
		configs[k] = v
	}
	state, reg := machineDir(t, configs)
	fix := fixline.Run{Flags: []fixline.Flag{{Name: "reason", Value: "prod build"}}, Argv: []string{"pnpm", "build"}, Dir: "/src", Here: "/src"}
	req := func(named []string, pool ...string) Request {
		return Request{Named: named, Pool: pool, Reason: "prod build", Fix: fix}
	}
	p, err := Make(state, reg, req([]string{"polymatto"}, "tests"))
	if err != nil || keysOf(p) != "polymatto tests(pool, via polymatto)" || len(p.FirstLinks) != 0 {
		t.Fatalf("a subset of the link: %v %v", keysOf(p), err)
	}
	p, err = Make(state, reg, req([]string{"polymatto", "builds"}, "tests"))
	if err != nil || keysOf(p) != "polymatto builds(pool) tests(pool, via polymatto)" || strings.Join(p.Notes, "|") != `--pool ignored for pool key "builds"` {
		t.Fatalf("a named pool next to a project key ignores --pool: %v %v %v", keysOf(p), p.Notes, err)
	}
	p, err = Make(state, reg, req([]string{"cap-e2e"}, "tests", "computer-use"))
	if err != nil || len(p.FirstLinks) != 1 || p.FirstLinks[0].Key != "cap-e2e" || strings.Join(p.FirstLinks[0].Pools, ",") != "computer-use,tests" ||
		keysOf(p) != "cap-e2e computer-use(pool, via cap-e2e) tests(pool, via cap-e2e)" {
		t.Fatalf("a first link equal to the suggestion: %v %+v %v", keysOf(p), p.FirstLinks, err)
	}
	if p, err := Make(state, reg, req([]string{"kf-measure"}, "tests")); err != nil || !p.FirstLinks[0].Quiet {
		t.Fatalf("the first link carries quiet_machine with its suggestion: %+v %v", p, err)
	}
	for _, c := range []struct {
		name string
		req  Request
		want string
	}{
		{"not part of the link", req([]string{"polymatto"}, "vm"), `incoda: pool-mismatch: "polymatto" is linked to builds,computer-use,tests; --pool vm is not part of it
incoda: to also hold vm for this run only, name it next to the queue (no link change):
incoda:   incoda run --queue polymatto,vm --reason 'prod build' -- 'pnpm' 'build'
incoda: changing the link is the user's call; ask them.
`},
		{"partly part of the link", req([]string{"polymatto"}, "tests", "vm"), `incoda: pool-mismatch: "polymatto" is linked to builds,computer-use,tests; --pool vm is not part of it
incoda: to also hold vm for this run only, name it next to the queue (no link change):
incoda:   incoda run --queue polymatto,vm --pool tests --reason 'prod build' -- 'pnpm' 'build'
incoda: changing the link is the user's call; ask them.
`},
		{"no project key", req([]string{"builds"}, "tests"), `incoda: pool-mismatch: --pool needs a project key; "builds" is a pool
incoda: rerun without --pool:
incoda:   incoda run --queue builds --reason 'prod build' -- 'pnpm' 'build'
`},
		{"not a pool", req([]string{"polymatto"}, "printer"), `incoda: pool-mismatch: "printer" is not a pool on this machine (pools: builds, computer-use, tests, vm)
`},
		{"first link other than the suggestion", req([]string{"cap-e2e"}, "tests"), `incoda: link-needs-user: "cap-e2e" suggests computer-use,tests; a first link from run must equal it
incoda: run it with the suggestion instead (stored; every later run on this queue takes these pools):
incoda:   incoda run --queue cap-e2e --pool computer-use,tests --reason 'prod build' -- 'pnpm' 'build'
incoda: ask the user for anything else; they run: incoda link cap-e2e
`},
		{"first link without a suggestion", req([]string{"polymatto-x"}, "tests"), `incoda: link-needs-user: "polymatto-x" has no suggested pools; ask the user; they run: incoda link polymatto-x
`},
		{"one key refused refuses the run", req([]string{"cap-gate", "polymatto"}, "vm"), `incoda: link-needs-user: "cap-gate" suggests tests; a first link from run must equal it
incoda: run it with the suggestion instead (stored; every later run on this queue takes these pools):
incoda:   incoda run --queue cap-gate,polymatto --pool tests --reason 'prod build' -- 'pnpm' 'build'
incoda: ask the user for anything else; they run: incoda link cap-gate
`},
	} {
		_, err := Make(state, reg, c.req)
		var rf *machine.Refusal
		if !errors.As(err, &rf) {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got := "incoda: " + rf.Msg + "\n"; got != c.want {
			t.Errorf("%s:\n got:\n%s\nwant:\n%s", c.name, got, c.want)
		}
	}
}
```

Create `internal/cli/run_test.go`:

```go
package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
)

// TestFirstLinkConflict: a run whose first link loses the compare-and-set
// to a different link refuses with link-conflict, naming the winner, and
// takes nothing (spec 4.2).
func TestFirstLinkConflict(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /usr/bin/true")
	}
	dir := t.TempDir()
	t.Setenv("INCODA_DIR", dir)
	if code := Main([]string{"config", "seed"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("migrate: %d", code)
	}
	saved := beforeFirstLink
	defer func() { beforeFirstLink = saved }()
	beforeFirstLink = func(dir, key string) {
		_, err := machine.WriteLink(dir, key, "test", machine.Options{Start: time.Now(), Wait: 5 * time.Second}, func(_ *machine.Registry, c *lane.Config) error {
			c.Pools = []string{"builds"}
			return nil
		})
		if err != nil {
			t.Error(err)
		}
	}
	var stderr bytes.Buffer
	code := Main([]string{"run", "--queue", "race-gate", "--pool", "tests", "--", "true"}, io.Discard, &stderr)
	want := fmt.Sprintf(`incoda: link-conflict: "race-gate" was just linked to builds by pid %d; rerun without --pool`, os.Getpid())
	if code != ExitUsage || !strings.Contains(stderr.String(), want) {
		t.Fatalf("want 120 and %q, got %d:\n%s", want, code, stderr.String())
	}
	cfg, err := lane.ReadConfig(lane.LaneDir(dir, "race-gate"))
	if err != nil || machine.SetText(cfg.Pools) != "builds" {
		t.Fatalf("the winner's link stays: %+v %v", cfg, err)
	}
	for _, k := range []string{"race-gate", "builds", "tests"} {
		entries, _ := os.ReadDir(lane.LaneDir(dir, k))
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".ticket") {
				t.Fatalf("%s holds a ticket after the refusal", k)
			}
		}
	}
}
```

Append to the end of `pools_test.go`:

```go

// TestRunPoolMakesTheFirstLinkOnlyToTheSuggestion: the line the unlinked
// refusal prints, run as printed, links the key to its suggestion (a
// compare-and-set, logged by=run, announced linked:) and runs; later runs
// take the link without --pool. A --pool set other than the suggestion is
// refused link-needs-user and writes nothing. Concurrent first links to
// the same set all run (spec 4.2, 9).
func TestRunPoolMakesTheFirstLinkOnlyToTheSuggestion(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	mustRun(t, incoda, state, 0, "config", "seed")
	out := mustRun(t, incoda, state, 120, "run", "--queue", "cap-e2e", "--pool", "tests", "--", stamp, filepath.Join(t.TempDir(), "a.txt"), "a", "1")
	if !strings.HasPrefix(out, `incoda: link-needs-user: "cap-e2e" suggests computer-use,tests; a first link from run must equal it`) {
		t.Fatalf("link-needs-user:\n%s", out)
	}
	if _, err := os.Stat(laneDir(state, "cap-e2e")); !os.IsNotExist(err) {
		t.Fatal("a refused first link writes nothing")
	}
	out = mustRun(t, incoda, state, 0, "run", "--queue", "cap-e2e", "--pool", "computer-use,tests", "--", stamp, filepath.Join(t.TempDir(), "b.txt"), "b", "1")
	if !strings.Contains(out, "incoda: linked: cap-e2e -> computer-use,tests (stored; every later run on cap-e2e takes these pools)\n") {
		t.Fatalf("linked line:\n%s", out)
	}
	inOrder(t, out, `acquired queue "cap-e2e" (pid `, `acquired queue "computer-use" (pool, via cap-e2e; pid `, `acquired queue "tests" (pool, via cap-e2e; pid `)
	log, _ := os.ReadFile(filepath.Join(laneDir(state, "cap-e2e"), "lane.log"))
	if !strings.Contains(string(log), " event=link pid=") || !strings.Contains(string(log), " by=run old= new=computer-use,tests") {
		t.Fatalf("lane.log:\n%s", log)
	}
	out = mustRun(t, incoda, state, 0, "run", "--queue", "cap-e2e", "--", stamp, filepath.Join(t.TempDir(), "c.txt"), "c", "1")
	inOrder(t, out, `acquired queue "computer-use" (pool, via cap-e2e; pid `, `acquired queue "tests" (pool, via cap-e2e; pid `)

	// Agents racing the same printed line: every run succeeds and exactly
	// one of them writes the link.
	done := make(chan string, 4)
	for i := 0; i < 4; i++ {
		go func(i int) {
			o, code := runIncoda(t, incoda, state, "run", "--queue", "race-gate", "--pool", "tests", "--wait", "60s", "--poll", "50ms",
				"--", stamp, filepath.Join(t.TempDir(), "r.txt"), "r", "50")
			if code != 0 {
				o = "FAILED " + o
			}
			done <- o
		}(i)
	}
	linked := 0
	for i := 0; i < 4; i++ {
		o := <-done
		if strings.HasPrefix(o, "FAILED ") {
			t.Fatalf("a racing first link failed:\n%s", o)
		}
		linked += strings.Count(o, "incoda: linked: race-gate -> tests")
	}
	if linked != 1 {
		t.Fatalf("%d runs wrote the link, want exactly 1", linked)
	}
}

// TestPrintedUnlinkedLineRunsAsPrinted: the run line of the unlinked
// refusal, pasted into sh with this test's own incoda first on PATH, makes
// the first link and runs the command with its exact arguments.
func TestPrintedUnlinkedLineRunsAsPrinted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the printed line is PowerShell there")
	}
	incoda, stamp := binaries(t)
	state := t.TempDir()
	marker := filepath.Join(t.TempDir(), "it's here.txt")
	out := mustRun(t, incoda, state, 120, "run", "--queue", "wintty-gate", "--reason", "wintty's gate", "--", stamp, marker, "a b", "1")
	line := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "incoda:   incoda run ") {
			line = strings.TrimPrefix(l, "incoda:   ")
		}
	}
	if line == "" {
		t.Fatalf("no run line:\n%s", out)
	}
	sh := exec.Command("/bin/sh", "-c", line)
	// PATH holds this test's build first and system directories only, so
	// no incoda installed on the machine can answer.
	sh.Env = append(laneEnv(state), "PATH="+filepath.Dir(incoda)+":/usr/bin:/bin")
	b, err := sh.CombinedOutput()
	if err != nil || !strings.Contains(string(b), "incoda: linked: wintty-gate -> tests") {
		t.Fatalf("the printed line %s: %v\n%s", line, err, b)
	}
	if iv, ok := readInterval(t, marker); !ok || iv.label != "a b" {
		t.Fatalf("the command ran with its exact arguments: %+v %v", iv, ok)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run (bash), one at a time:

- `go test ./internal/runplan/ -run 'TestPoolFlag' -count=1 -timeout 120s`
- `go test ./internal/cli/ -run 'TestFirstLinkConflict' -count=1 -timeout 120s`
- `go test . -run 'TestRunPoolMakesTheFirstLinkOnlyToTheSuggestion|TestPrintedUnlinkedLineRunsAsPrinted' -count=1 -timeout 120s`

Expected: the `internal/runplan` test build fails (`unknown field Pool in struct literal of type Request`, `p.FirstLinks undefined`, `p.Notes undefined`), the `internal/cli` one too (`undefined: beforeFirstLink`), and the root `TestRunPoolMakesTheFirstLinkOnlyToTheSuggestion` fails with `flag provided but not defined: -pool`.

- [ ] **Step 3: Implement**

In `internal/cli/run.go`, make these 3 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 3) Replace:

```go
	noColor := fs.Bool("no-color", false, "never emit ANSI color, even on a terminal (the NO_COLOR environment variable does the same)")
	wait := &waitValue{d: 30 * time.Minute}
	fs.Var(wait, "wait", "max time to queue: a Go duration (30m) or bare seconds (1800); 0 fails immediately, negative waits forever")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: incoda run --queue KEY[,KEY...] [--slots N] [--exclusive] [--wait DUR] [--reason TEXT] [--owner WHO] [--] <cmd...>\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
```

with:

```go
	noColor := fs.Bool("no-color", false, "never emit ANSI color, even on a terminal (the NO_COLOR environment variable does the same)")
	wait := &waitValue{d: 30 * time.Minute}
	fs.Var(wait, "wait", "max time to queue: a Go duration (30m) or bare seconds (1800); 0 fails immediately, negative waits forever")
	pool := &poolsValue{}
	fs.Var(pool, "pool", "take only these of each named project key's linked pools (comma-separated); on an unlinked key, a set equal to its suggestion becomes its first link")
	fs.Var(pool, "pools", "alias of --pool")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: incoda run --queue KEY[,KEY...] [--pool P,P] [--slots N] [--exclusive] [--wait DUR] [--reason TEXT] [--owner WHO] [--] <cmd...>\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
```

(2 of 3) Replace:

```go
	pass := inherited.PassKeys()
	live := inherited.LiveKeys()
	here, _ := os.Getwd()
	plan, err := runplan.Make(dir, reg, runplan.Request{
		Named: keys, Slots: *slots, Exclusive: *exclusive, Reason: *reason, Held: pass,
		Fix:       fixline.Run{Flags: carriedFlags(fs, wait), Argv: argv, Dir: here, Here: here},
		WaitGiven: wait.set,
	})
	if err != nil {
		return machineExit(err)
	}
```

with:

```go
	pass := inherited.PassKeys()
	live := inherited.LiveKeys()
	here, _ := os.Getwd()
	req := runplan.Request{
		Named: keys, Slots: *slots, Exclusive: *exclusive, Reason: *reason, Held: pass,
		Fix:       fixline.Run{Flags: carriedFlags(fs, wait), Argv: argv, Dir: here, Here: here},
		WaitGiven: wait.set,
	}
	if pool.set {
		req.Pool = pool.keys
	}
	plan, err := planWithFirstLinks(dir, reg, req, machine.Options{
		Start: start, Wait: wait.d, Poll: *poll, Chain: chain, Stderr: stderr,
	}, *quiet, stderr, p)
	if err != nil {
		return machineExit(err)
	}
```

(3 of 3) Replace:

```go
	return nil
}

// carriedFlags is every flag the caller gave except --queue and --pool, in
// flag-name order, as a printed fix line repeats them (spec 2.6); --wait
// keeps the text it was given.
```

with:

```go
	return nil
}

// planWithFirstLinks plans the run and, when its --pool set equals an
// unlinked key's suggestion, writes that first link (spec 4.2) and plans
// again. Each first link is a compare-and-set under machine.lock, then the
// key's registry lock, before any ticket: still unlinked, it is written
// and logged event=link by=run and the run says linked:; already linked to
// the same set (another run won the race), the run goes on; linked to a
// different set, it refuses with link-conflict.
func planWithFirstLinks(dir string, reg *machine.Registry, req runplan.Request, o machine.Options, quiet bool, stderr io.Writer, p colorize.Palette) (*runplan.Plan, error) {
	for {
		plan, err := runplan.Make(dir, reg, req)
		if err != nil {
			return nil, err
		}
		if len(plan.FirstLinks) == 0 {
			if !quiet {
				for _, n := range plan.Notes {
					fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"), p.Dim(n))
				}
			}
			return plan, nil
		}
		for _, fl := range plan.FirstLinks {
			beforeFirstLink(dir, fl.Key)
			res, err := machine.WriteLink(dir, fl.Key, "run", o, func(_ *machine.Registry, c *lane.Config) error {
				switch {
				case len(c.Pools) == 0:
					c.Pools, c.QuietMachine = fl.Pools, c.QuietMachine || fl.Quiet
					return nil
				case machine.SameSet(c.Pools, fl.Pools):
					return lane.ErrNoChange
				}
				by := "another process"
				if pid, ok := machine.LastLinker(dir, fl.Key); ok {
					by = fmt.Sprintf("pid %d", pid)
				}
				return &machine.Refusal{Msg: fmt.Sprintf("link-conflict: %q was just linked to %s by %s; rerun without --pool", fl.Key, machine.SetText(c.Pools), by)}
			})
			if err != nil {
				return nil, err
			}
			if res.Changed {
				// Printed even with --quiet: it records a stored change.
				fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"), p.Green(fmt.Sprintf("linked: %s (stored; every later run on %s takes these pools)",
					machine.LinkedLine(fl.Key, res.New.Pools, res.New.QuietMachine), fl.Key)))
			}
		}
	}
}

// beforeFirstLink is a seam for tests; production never changes it.
var beforeFirstLink = func(dir, key string) {}

// carriedFlags is every flag the caller gave except --queue and --pool, in
// flag-name order, as a printed fix line repeats them (spec 2.6); --wait
// keeps the text it was given.
```

Append to the end of `internal/runplan/refusals.go`:

```go

// linkNeedsUser refuses a --pool first link on an unlinked key that is not
// its suggestion (spec 4.2): run makes a first link only when the set
// equals the suggestion; anything else is the user's.
func linkNeedsUser(stateDir string, reg *machine.Registry, req Request, key string, s Suggestion) *machine.Refusal {
	switch {
	case s.Pattern == "":
		return refusal([]string{fmt.Sprintf("link-needs-user: %q has no suggested pools; ask the user; they run: incoda link %s", key, key)})
	case !s.Usable:
		return refusal([]string{fmt.Sprintf("link-needs-user: %q has no usable suggestion (%s); ask the user; they run: incoda link %s", key, s.Why, key)})
	}
	lines := []string{
		fmt.Sprintf("link-needs-user: %q suggests %s; a first link from run must equal it", key, strings.Join(s.Pools, ",")),
		"run it with the suggestion instead (stored; every later run on this queue takes these pools):",
	}
	run, why := fixFor(stateDir, reg, req, namedOrder(reg, req.Named), s.Pools, s.QuietMachine)
	lines = append(lines, fixline.RunLines(fixline.Native(), run, why, "run it")...)
	if s.QuietMachine && !req.WaitGiven {
		lines = append(lines, "(--wait 5m added: quiet-machine holds every pool it has drained while it waits for the rest)")
	}
	return refusal(append(lines, "ask the user for anything else; they run: incoda link "+key))
}

// poolMismatch refuses a --pool set that is not part of key's link (spec
// 4.2). The fix holds the extra pools for this run only, by naming them
// next to the queue, and keeps the part of the set the link allows.
func poolMismatch(stateDir string, reg *machine.Registry, req Request, key string, link []string) *machine.Refusal {
	in := map[string]bool{}
	for _, k := range link {
		in[k] = true
	}
	var extra, keep []string
	for _, k := range req.Pool {
		if in[k] {
			keep = append(keep, k)
		} else {
			extra = append(extra, k)
		}
	}
	lines := []string{
		fmt.Sprintf("pool-mismatch: %q is linked to %s; --pool %s is not part of it", key, strings.Join(link, ","), strings.Join(extra, ",")),
		fmt.Sprintf("to also hold %s for this run only, name it next to the queue (no link change):", strings.Join(extra, ",")),
	}
	queue := namedOrder(reg, append(append([]string(nil), req.Named...), extra...))
	run, why := fixFor(stateDir, reg, req, queue, keep, false)
	lines = append(lines, fixline.RunLines(fixline.Native(), run, why, "run it")...)
	return refusal(append(lines, "changing the link is the user's call; ask them."))
}

// noProjectRefusal refuses --pool on a run that names only pools (spec
// 4.2).
func noProjectRefusal(stateDir string, reg *machine.Registry, req Request, pools []string) *machine.Refusal {
	what := fmt.Sprintf("%q is a pool", pools[0])
	if len(pools) > 1 {
		what = quoted(pools) + " are pools"
	}
	lines := []string{fmt.Sprintf("pool-mismatch: --pool needs a project key; %s", what), "rerun without --pool:"}
	run, why := fixFor(stateDir, reg, req, namedOrder(reg, req.Named), nil, false)
	return refusal(append(lines, fixline.RunLines(fixline.Native(), run, why, "run it")...))
}
```

In `internal/runplan/runplan.go`, make these 5 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 5) Replace:

```go
	// re-checked: the ancestor's ticket was admitted under its rules
	// (spec 4.5).
	Held map[string]bool
	// Fix is the caller's own run line for printed fix lines: every flag
	// it gave except --queue and --pool, its argv, and its directory as
	// Dir and Here. Queue and Pool are set by each refusal. WaitGiven says
```

with:

```go
	// re-checked: the ancestor's ticket was admitted under its rules
	// (spec 4.5).
	Held map[string]bool
	// Pool is the --pool set (alias --pools), sorted; nil when not given.
	// It applies to every named project key (spec 4.2).
	Pool []string
	// Fix is the caller's own run line for printed fix lines: every flag
	// it gave except --queue and --pool, its argv, and its directory as
	// Dir and Here. Queue and Pool are set by each refusal. WaitGiven says
```

(2 of 5) Replace:

```go
	// Links is each named project key's link (its pools, sorted) as read
	// at plan time; an unlinked key maps to nil.
	Links map[string][]string
}

// Less is the total order of spec 2.4: project lanes before pools, each
```

with:

```go
	// Links is each named project key's link (its pools, sorted) as read
	// at plan time; an unlinked key maps to nil.
	Links map[string][]string
	// Pool is the request's --pool set, kept for the verify points.
	Pool []string
	// FirstLinks are the first links the run makes before any ticket (a
	// --pool set equal to an unlinked key's suggestion, spec 4.2). The
	// plan's lanes already take those pools; the caller writes the links
	// and plans again.
	FirstLinks []FirstLink
	// Notes are informational lines for the caller to print.
	Notes []string
}

// FirstLink is a link run writes: Key gets Pools, and quiet_machine when
// the suggestion carries it.
type FirstLink struct {
	Key   string
	Pools []string
	Quiet bool
}

// Less is the total order of spec 2.4: project lanes before pools, each
```

(3 of 5) Replace:

```go

// Make computes the plan for req against reg and the lanes' configs.
func Make(stateDir string, reg *machine.Registry, req Request) (*Plan, error) {
	p := &Plan{Generation: reg.Generation, Links: map[string][]string{}}
	lanes := map[string]*Lane{}
	named := append([]string(nil), req.Named...)
	sort.Strings(named)
```

with:

```go

// Make computes the plan for req against reg and the lanes' configs.
func Make(stateDir string, reg *machine.Registry, req Request) (*Plan, error) {
	p := &Plan{Generation: reg.Generation, Links: map[string][]string{}, Pool: req.Pool}
	lanes := map[string]*Lane{}
	named := append([]string(nil), req.Named...)
	sort.Strings(named)
```

(4 of 5) Replace:

```go
		lanes[k] = l
	}

	// A project lane with no link refuses the run before any ticket, with
	// its suggestion and the line that makes it (spec 4.1).
	var unlinked []string
	projects := 0
	for _, k := range named {
		if l := lanes[k]; !l.Pool {
			projects++
			if len(l.Cfg.Pools) == 0 {
				unlinked = append(unlinked, k)
			}
		}
	}
	if len(unlinked) > 0 {
		return nil, unlinkedRefusal(stateDir, reg, req, unlinked, projects)
	}

	// Each named project key brings its linked pools. Every one must
	// resolve to a registered pool with a readable config: the lane set
	// never shrinks silently (spec 2.5).
	for _, k := range named {
		l := lanes[k]
		if l.Pool {
			continue
		}
		link := machine.SortedSet(l.Cfg.Pools)
		p.Links[k] = link
		for _, pool := range link {
			pl, err := linkedPool(stateDir, reg, lanes, k, pool)
			if err != nil {
				return nil, err
```

with:

```go
		lanes[k] = l
	}

	var unlinked, projects, pools []string
	for _, k := range named {
		if l := lanes[k]; l.Pool {
			pools = append(pools, k)
		} else {
			projects = append(projects, k)
			if len(l.Cfg.Pools) == 0 {
				unlinked = append(unlinked, k)
			}
		}
	}

	// --pool is a set of registered pools for the named project keys
	// (spec 4.2).
	if req.Pool != nil {
		if bad := reg.NotPools(req.Pool); len(bad) > 0 {
			return nil, machine.NotAPool(reg, bad)
		}
		if len(projects) == 0 {
			return nil, noProjectRefusal(stateDir, reg, req, pools)
		}
		for _, k := range pools {
			p.Notes = append(p.Notes, fmt.Sprintf("--pool ignored for pool key %q", k))
		}
	}

	// A project lane with no link refuses the run before any ticket, with
	// its suggestion and the line that makes it (spec 4.1). With --pool,
	// only a set equal to the suggestion becomes its first link (4.2).
	if len(unlinked) > 0 && req.Pool == nil {
		return nil, unlinkedRefusal(stateDir, reg, req, unlinked, len(projects))
	}

	// Each named project key brings its linked pools (with --pool, only
	// that subset). Every linked pool must resolve to a registered pool
	// with a readable config: the lane set never shrinks silently (spec
	// 2.5). If any key is refused, the whole run is refused before
	// anything is written.
	for _, k := range projects {
		l := lanes[k]
		link := machine.SortedSet(l.Cfg.Pools)
		p.Links[k] = link
		use := link
		switch {
		case len(link) == 0:
			s := Suggest(reg, k)
			if !s.Usable || !machine.SameSet(s.Pools, req.Pool) {
				return nil, linkNeedsUser(stateDir, reg, req, k, s)
			}
			p.FirstLinks = append(p.FirstLinks, FirstLink{Key: k, Pools: s.Pools, Quiet: s.QuietMachine})
			use = req.Pool
		case req.Pool != nil:
			if !machine.Subset(req.Pool, link) {
				return nil, poolMismatch(stateDir, reg, req, k, link)
			}
			use = req.Pool
		}
		for _, pool := range link {
			if _, err := resolvePool(stateDir, reg, k, pool); err != nil {
				return nil, err
			}
		}
		for _, pool := range use {
			pl, err := linkedPool(stateDir, reg, lanes, k, pool)
			if err != nil {
				return nil, err
```

(5 of 5) Replace:

```go
	if pl := lanes[pool]; pl != nil {
		return pl, nil
	}
	if !reg.IsPool(pool) {
		return nil, linkStateError(project, pool, "it is not a pool on this machine")
	}
	cfg, err := lane.ReadConfig(lane.LaneDir(stateDir, pool))
	if err != nil {
		return nil, linkStateError(project, pool, textsafe.Escape(err.Error()))
	}
	pl := &Lane{Key: pool, Pool: true, Cfg: cfg}
	lanes[pool] = pl
	return pl, nil
}

func linkStateError(project, pool, reason string) *machine.StateError {
```

with:

```go
	if pl := lanes[pool]; pl != nil {
		return pl, nil
	}
	cfg, err := resolvePool(stateDir, reg, project, pool)
	if err != nil {
		return nil, err
	}
	pl := &Lane{Key: pool, Pool: true, Cfg: cfg}
	lanes[pool] = pl
	return pl, nil
}

// resolvePool reads the config of a pool project links, failing closed
// when pool is not registered or its config cannot be read.
func resolvePool(stateDir string, reg *machine.Registry, project, pool string) (lane.Config, error) {
	if !reg.IsPool(pool) {
		return lane.Config{}, linkStateError(project, pool, "it is not a pool on this machine")
	}
	cfg, err := lane.ReadConfig(lane.LaneDir(stateDir, pool))
	if err != nil {
		return lane.Config{}, linkStateError(project, pool, textsafe.Escape(err.Error()))
	}
	return cfg, nil
}

func linkStateError(project, pool, reason string) *machine.StateError {
```

- [ ] **Step 4: Run the tests to see them pass**

Run (bash), one at a time:

- `go test ./internal/runplan/ -run 'TestPoolFlag' -count=1`
- `go test ./internal/cli/ -run 'TestFirstLinkConflict' -count=1`
- `go test . -run 'TestRunPoolMakesTheFirstLinkOnlyToTheSuggestion|TestPrintedUnlinkedLineRunsAsPrinted' -count=1`

Expected: `ok` for each package.

- [ ] **Step 5: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/run.go internal/cli/run_test.go internal/runplan/refusals.go internal/runplan/runplan.go internal/runplan/runplan_test.go pools_test.go
git commit -F - <<'MSG'
feat: run --pool takes a subset of a link and makes a first link only to the suggestion

--pool is a set of registered pools for every named project key: on a
linked key a subset of its link, on an unlinked key a first link only
when it equals the suggestion, written as a compare-and-set under
machine.lock and announced linked:. A lost race to the same set goes
on, a lost race to another set is link-conflict, any other set is
link-needs-user, and --pool with no project key is pool-mismatch.
MSG
```


---

### Task 9: Verify after each enroll and before the child; replan on a change

Spec 2.5. At each verify point, after each enroll before waiting on that lane and once after the last lane is acquired before the child starts, the run re-reads `machine.json` and its named project keys' configs and replans if (1) the generation moved and a lane of the plan changed kind or left the registry, or (2) a named project's link changed beyond the run's `--pool` subset. A replan releases every ticket the run holds, logs `event=replan` on each, prints `incoda: replan: <what changed>`, and plans again from scratch inside the same `--wait` budget (it loses its FIFO places). A linked pool that stops resolving fails closed with `machine-state:`. Trigger 3 (quiet pools) arrives with quiet-machine in plan 3b. The enroll loop becomes a `takeLane` closure (same indentation, so the diff stays reviewable) inside a plan, enroll, verify loop.

**Files:**
- Modify: `internal/cli/run.go`
- Modify: `internal/runplan/runplan.go`
- Test: `internal/cli/run_test.go`
- Test: `internal/runplan/runplan_test.go`
- Test: `pools_test.go`

**Interfaces:**
- Consumes: `runplan.Make`, `runplan.Plan` (Tasks 5 to 8), `machine.ReadRegistry`.
- Produces: `func (p *runplan.Plan) Changed(stateDir string) (string, error)`; in `cli`, the test seam `atVerify func(dir, key string)` (the lane just enrolled, `""` before the final verify).

- [ ] **Step 1: Write the failing tests**

In `internal/runplan/runplan_test.go`, replace:

```go

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
```

with:

```go

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
```

Then append to the end of `internal/runplan/runplan_test.go`:

```go

// TestChanged: the replan triggers of spec 2.5 and nothing else.
func TestChanged(t *testing.T) {
	state, reg := machineDir(t, map[string]string{
		"p-gate": `{"schema":2,"pools":["builds","tests"]}`,
	})
	plan := func(pool ...string) *Plan {
		t.Helper()
		p, err := Make(state, reg, Request{Named: []string{"p-gate"}, Pool: pool})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	setReg := func(gen int, pools string) {
		body := fmt.Sprintf(`{"schema":1,"layout":2,"generation":%d,"pools":[%s]}`, gen, pools)
		if err := os.WriteFile(machine.RegistryPath(state), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	setLink := func(body string) {
		if err := os.WriteFile(filepath.Join(lane.LaneDir(state, "p-gate"), "config.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	changed := func(p *Plan) string {
		t.Helper()
		why, err := p.Changed(state)
		if err != nil {
			t.Fatal(err)
		}
		return why
	}
	p, sub := plan(), plan("tests")
	if why := changed(p); why != "" {
		t.Fatalf("nothing changed: %q", why)
	}
	setReg(4, `"builds","computer-use","tests","vm","printer"`)
	if why := changed(p); why != "" {
		t.Fatalf("a new generation that leaves the plan's kinds alone is no trigger: %q", why)
	}
	setReg(5, `"builds","computer-use","tests","p-gate"`)
	if why := changed(p); why != `queue "p-gate" became a pool` {
		t.Fatalf("kind change: %q", why)
	}
	setReg(6, `"computer-use","tests","vm"`)
	if why := changed(p); why != `pool "builds" left the registry` {
		t.Fatalf("pool removed: %q", why)
	}
	setReg(3, `"builds","computer-use","tests","vm"`)
	setLink(`{"schema":2,"pools":["tests","vm"]}`)
	if why := changed(p); why != `the link of "p-gate" changed: builds,tests -> tests,vm` {
		t.Fatalf("link change: %q", why)
	}
	if why := changed(sub); why != "" {
		t.Fatalf("a link change that keeps the --pool subset is no trigger: %q", why)
	}
	setLink(`{"schema":2,"pools":["vm"]}`)
	if why := changed(sub); why != `the link of "p-gate" changed: builds,tests -> vm` {
		t.Fatalf("a link change beyond the --pool subset: %q", why)
	}
	setLink(`{"schema":2,"pools":["builds","tests"]}`)
	if err := os.MkdirAll(lane.LaneDir(state, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lane.LaneDir(state, "tests"), "config.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	var se *machine.StateError
	if _, err := p.Changed(state); !errors.As(err, &se) || !strings.HasPrefix(se.Msg, `machine-state: queue "p-gate" links "tests": `) {
		t.Fatalf("a linked pool that stops resolving fails closed: %v", err)
	}
}
```

Append to the end of `internal/cli/run_test.go`:

```go

// relink sets key's link to pools, the way a user's config --replace does.
func relink(t *testing.T, dir, key string, pools ...string) {
	t.Helper()
	_, err := machine.WriteLink(dir, key, "test", machine.Options{Start: time.Now(), Wait: 5 * time.Second}, func(_ *machine.Registry, c *lane.Config) error {
		c.Pools = pools
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// laneLog is a lane's lane.log.
func laneLog(dir, key string) string {
	b, _ := os.ReadFile(lane.LogPath(lane.LaneDir(dir, key)))
	return string(b)
}

// TestReplanAtEachVerifyPoint: a link that changes after an enroll, or
// before the final verify, makes the run release everything, say what
// changed, log event=replan and plan again; it then takes the new link's
// pools (spec 2.5).
func TestReplanAtEachVerifyPoint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /usr/bin/true")
	}
	for _, c := range []struct {
		name, at string
	}{
		{"after the enroll on the project lane", "v-gate"},
		{"at the final verify", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("INCODA_DIR", dir)
			if code := Main([]string{"config", "v-gate", "--pool", "tests"}, io.Discard, io.Discard); code != 0 {
				t.Fatalf("link: %d", code)
			}
			saved := atVerify
			defer func() { atVerify = saved }()
			done := false
			atVerify = func(dir, key string) {
				if key == c.at && !done {
					done = true
					relink(t, dir, "v-gate", "builds")
				}
			}
			var stderr bytes.Buffer
			if code := Main([]string{"run", "--queue", "v-gate", "--", "true"}, io.Discard, &stderr); code != 0 {
				t.Fatalf("run: %d\n%s", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), `incoda: replan: the link of "v-gate" changed: tests -> builds`+"\n") {
				t.Fatalf("missing the replan line:\n%s", stderr.String())
			}
			acq := fmt.Sprintf("event=acquire pid=%d", os.Getpid())
			if !strings.Contains(laneLog(dir, "builds"), acq) || !strings.Contains(laneLog(dir, "v-gate"), "event=replan pid=") {
				t.Fatalf("the replanned run takes builds and logs the replan:\nbuilds:\n%s\nv-gate:\n%s", laneLog(dir, "builds"), laneLog(dir, "v-gate"))
			}
		})
	}
}

// TestReplanWhenAPoolLeavesTheRegistry: a linked pool removed from
// machine.json while the run holds its project lane makes it replan, and
// the new plan fails closed: the lane set never shrinks silently.
func TestReplanWhenAPoolLeavesTheRegistry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /usr/bin/true")
	}
	dir := t.TempDir()
	t.Setenv("INCODA_DIR", dir)
	if code := Main([]string{"config", "w-gate", "--pool", "vm"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("link: %d", code)
	}
	saved := atVerify
	defer func() { atVerify = saved }()
	atVerify = func(dir, key string) {
		if key != "w-gate" {
			return
		}
		lk, err := machine.AcquireLock(dir, machine.LockOptions{Op: "test", Start: time.Now(), Wait: 5 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		defer lk.Release()
		if _, err := machine.UpdateRegistry(dir, lk, func(r *machine.Registry) error {
			r.Pools = []string{"builds", "computer-use", "tests"}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	var stderr bytes.Buffer
	code := Main([]string{"run", "--queue", "w-gate", "--", "true"}, io.Discard, &stderr)
	want := "incoda: replan: pool \"vm\" left the registry\nincoda: machine-state: queue \"w-gate\" links \"vm\": it is not a pool on this machine\n"
	if code != ExitState || !strings.HasSuffix(stderr.String(), want) {
		t.Fatalf("want 122 and\n%s\ngot %d:\n%s", want, code, stderr.String())
	}
	if strings.Contains(laneLog(dir, "w-gate"), fmt.Sprintf("event=acquire pid=%d", os.Getpid())) {
		t.Fatal("the run must not have acquired anything")
	}
}
```

Append to the end of `pools_test.go`:

```go

// TestRelinkWhileWaitingReplans: a run waiting on its pool whose link the
// user changes meanwhile notices at its next verify point (after it gets
// the old pool), releases everything, says replan:, and runs under the
// new link (spec 2.5).
func TestRelinkWhileWaitingReplans(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	mustRun(t, incoda, state, 0, "config", "kf-gate", "--pool", "tests")
	mustRun(t, incoda, state, 0, "config", "p-gate", "--pool", "tests")
	h := exec.Command(incoda, "run", "--queue", "kf-gate", "--poll", "50ms", "--quiet", "--", stamp, filepath.Join(t.TempDir(), "h.txt"), "h", "2000")
	h.Env = laneEnv(state)
	if err := h.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Process.Kill(); _ = h.Wait() }()
	waitFor(t, incoda, state, "tests", func(q queueReport) bool { return len(q.Holders) == 1 })
	var out syncBuffer
	w := exec.Command(incoda, "run", "--queue", "p-gate", "--wait", "60s", "--poll", "50ms", "--", stamp, filepath.Join(t.TempDir(), "w.txt"), "w", "10")
	w.Env = laneEnv(state)
	w.Stdout, w.Stderr = &out, &out
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, incoda, state, "tests", func(q queueReport) bool { return len(q.Waiting) == 1 })
	mustRun(t, incoda, state, 0, "config", "p-gate", "--pool", "builds", "--replace")
	if err := w.Wait(); err != nil {
		t.Fatalf("waiter: %v\n%s", err, out.String())
	}
	inOrder(t, out.String(), `acquired queue "tests" (pool, via p-gate; pid `,
		`incoda: replan: the link of "p-gate" changed: tests -> builds`, `acquired queue "builds" (pool, via p-gate; pid `)
	log, _ := os.ReadFile(filepath.Join(laneDir(state, "p-gate"), "lane.log"))
	if !strings.Contains(string(log), "event=replan pid=") {
		t.Fatalf("lane.log:\n%s", log)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run (bash), one at a time:

- `go test ./internal/runplan/ -run 'TestChanged' -count=1 -timeout 120s`
- `go test ./internal/cli/ -run 'TestReplanAtEachVerifyPoint|TestReplanWhenAPoolLeavesTheRegistry' -count=1 -timeout 120s`
- `go test . -run 'TestRelinkWhileWaitingReplans' -count=1 -timeout 120s`

Expected: the `internal/runplan` test build fails (`p.Changed undefined`), the `internal/cli` one too (`undefined: atVerify`), and the root `TestRelinkWhileWaitingReplans` fails with `missing "incoda: replan: the link of \"p-gate\" changed: tests -> builds"`: the run kept the pool it planned.

- [ ] **Step 3: Implement**

The queues are closed by a deferred function declared before `release`, so they close after the tickets are released.

In `internal/cli/run.go`, make these 9 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 9) Replace:

```go
	if pool.set {
		req.Pool = pool.keys
	}
	plan, err := planWithFirstLinks(dir, reg, req, machine.Options{
		Start: start, Wait: wait.d, Poll: *poll, Chain: chain, Stderr: stderr,
	}, *quiet, stderr, p)
	if err != nil {
		return machineExit(err)
	}
	var parts, toTake []*lanePart
	defer func() {
		for _, pt := range parts {
			pt.q.Close()
		}
	}()
	for _, l := range plan.Lanes {
		if pass[l.Key] {
			if !*quiet {
				fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
					p.Dim(fmt.Sprintf("queue %q is already held by a parent incoda; running inside its lane", l.Key)))
			}
			lane.AppendLog(lane.LaneDir(dir, l.Key), "queue=%s event=reenter pid=%d cmd=%s", l.Key, os.Getpid(), textsafe.LogValue(lane.Ticket{Command: argv}.CommandString()))
			continue
		}
		q, err := lane.Open(dir, l.Key)
		if err != nil {
			return exitWith(ExitState, "%v", err)
		}
		// Every registry lock wait of this run stays inside its --wait
		// budget: a stopped incoda keeping a registry lock costs this run
		// its budget, never more.
		q.SetBudget(start, wait.d)
		pt := &lanePart{l: l, key: l.Key, q: q}
		parts = append(parts, pt)
		toTake = append(toTake, pt)
	}
	// The total order that makes multi-lane runs deadlock-free stops at a
	// nested run: a parent holding "b" whose recipe now takes "a" is
	// acquiring out of order, and two such parents can each wait on the
	// other's lane until --wait expires. It cannot be prevented from here
	// (the parent's lane is already held), so it is said out loud.
	for _, pt := range toTake {
		for h := range live {
			if runplan.Less(pt.l, runplan.Lane{Key: h, Pool: reg.IsPool(h)}) && !*quiet {
				fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
					p.Yellow(fmt.Sprintf("warning: taking %q while a parent incoda holds %q acquires out of sorted order; two nested runs shaped like this can wait on each other until --wait expires", pt.key, h)))
			}
		}
	}
	if len(toTake) == 0 {
		// Every key is the parent's. Nothing to enroll, nothing to watch:
		// a kill addressed to the parent takes this process with it.
		res, runErr := child.Run(argv, os.Stdin, os.Stdout, os.Stderr, nil, child.Options{
			Env:      childEnv(startEnv, held.Format(inherited.L)),
			OwnGroup: len(inherited.L) == 0,
		})
		if runErr != nil {
			return exitWith(ExitSpawn, "cannot run %q: %v", argv[0], runErr)
		}
		if res.Code != 0 {
			return &exitCode{code: res.Code}
		}
		return nil
	}

	host, _ := os.Hostname()
	cwd := here

```

with:

```go
	if pool.set {
		req.Pool = pool.keys
	}
	host, _ := os.Hostname()
	cwd := here

```

(2 of 9) Replace:

```go
	// caller does not wait a poll interval for nothing.
	rc := ExitOK
	var stats lane.Stats
	released := false
	release := func() {
		if released {
```

with:

```go
	// caller does not wait a poll interval for nothing.
	rc := ExitOK
	var stats lane.Stats
	var toTake []*lanePart
	// Deferred first, so it runs last: the queues close after release.
	defer func() {
		for _, pt := range toTake {
			pt.q.Close()
		}
	}()
	released := false
	release := func() {
		if released {
```

(3 of 9) Replace:

```go
	// then waiting would let a waiting ticket block later arrivals. The
	// budget started with the command, so machine.lock and migration waits
	// above have already spent part of it.
	for _, pt := range toTake {
		t := lane.Ticket{
			// --exclusive holds a lane the run names; it does not
			// propagate to the pools a link brings (spec 2.4).
```

with:

```go
	// then waiting would let a waiting ticket block later arrivals. The
	// budget started with the command, so machine.lock and migration waits
	// above have already spent part of it.
	//
	// takeLane enrolls on one lane, verifies the plan (spec 2.5), and waits
	// for the lane. It returns what changed when the plan must be made
	// again.
	takeLane := func(plan *runplan.Plan, pt *lanePart) (string, error) {
		t := lane.Ticket{
			// --exclusive holds a lane the run names; it does not
			// propagate to the pools a link brings (spec 2.4).
```

(4 of 9) Replace:

```go
			switch {
			case errors.As(err, &sd), errors.As(err, &ce), errors.As(err, &re):
				rc = ExitUsage
				return usagef("%v", err)
			case errors.As(err, &ns):
				rc = ExitState
				return exitWith(ExitState, "machine-state: %v", err)
			}
			if errors.Is(err, lane.ErrRegistryBusy) {
				rc = ExitTimeout
				return exitWith(ExitTimeout, "cannot enter queue %q within --wait: %v. Check `incoda status --queue %s`. Do NOT bypass the lane; surface the wait and coordinate instead", pt.key, err, pt.key)
			}
			rc = ExitState
			return exitWith(ExitState, "cannot enter queue %q: %v", pt.key, err)
		}
		pt.en = en

		// One --wait budget covers the whole list: a caller asked to wait
		// thirty minutes for the job, not thirty per key.
```

with:

```go
			switch {
			case errors.As(err, &sd), errors.As(err, &ce), errors.As(err, &re):
				rc = ExitUsage
				return "", usagef("%v", err)
			case errors.As(err, &ns):
				rc = ExitState
				return "", exitWith(ExitState, "machine-state: %v", err)
			}
			if errors.Is(err, lane.ErrRegistryBusy) {
				rc = ExitTimeout
				return "", exitWith(ExitTimeout, "cannot enter queue %q within --wait: %v. Check `incoda status --queue %s`. Do NOT bypass the lane; surface the wait and coordinate instead", pt.key, err, pt.key)
			}
			rc = ExitState
			return "", exitWith(ExitState, "cannot enter queue %q: %v", pt.key, err)
		}
		pt.en = en

		// A verify point: after each enroll, before waiting on the lane.
		atVerify(dir, pt.key)
		if why, err := plan.Changed(dir); err != nil {
			rc = ExitState
			return "", machineExit(err)
		} else if why != "" {
			return why, nil
		}

		// One --wait budget covers the whole list: a caller asked to wait
		// thirty minutes for the job, not thirty per key.
```

(5 of 9) Replace:

```go
		if acqErr != nil {
			if errors.Is(acqErr, context.Canceled) {
				rc = ExitInterrupt
				return exitWith(ExitInterrupt, "interrupted while queueing on %q", key)
			}
			var killed *lane.KilledError
			if errors.As(acqErr, &killed) {
				rc = ExitKilled
				logKill(toTake, killed.Request)
				return exitWith(ExitKilled, "%s", p.Red(fmt.Sprintf("cancelled while queued on %q by %s: %s",
					key, textsafe.Escape(killed.Request.By), textsafe.Escape(killed.Request.Reason))))
			}
			var rf *machine.Refusal
```

with:

```go
		if acqErr != nil {
			if errors.Is(acqErr, context.Canceled) {
				rc = ExitInterrupt
				return "", exitWith(ExitInterrupt, "interrupted while queueing on %q", key)
			}
			var killed *lane.KilledError
			if errors.As(acqErr, &killed) {
				rc = ExitKilled
				logKill(toTake, killed.Request)
				return "", exitWith(ExitKilled, "%s", p.Red(fmt.Sprintf("cancelled while queued on %q by %s: %s",
					key, textsafe.Escape(killed.Request.By), textsafe.Escape(killed.Request.Reason))))
			}
			var rf *machine.Refusal
```

(6 of 9) Replace:

```go
				if se != nil {
					rc = ExitState
				}
				return machineExit(acqErr)
			}
			if errors.Is(acqErr, lane.ErrTimeout) {
				rc = ExitTimeout
```

with:

```go
				if se != nil {
					rc = ExitState
				}
				return "", machineExit(acqErr)
			}
			if errors.Is(acqErr, lane.ErrTimeout) {
				rc = ExitTimeout
```

(7 of 9) Replace:

```go
				if role != "" {
					named += " (" + role + ")"
				}
				return exitWith(ExitTimeout,
					"%s still busy after %s. Check `incoda status --queue %s`. Do NOT bypass the lane; surface the wait and coordinate instead",
					named, wait.d, pt.l.StatusKey())
			}
			rc = ExitState
			return exitWith(ExitState, "%v", acqErr)
		}

		if _, _, live, err := en.Position(); err == nil && lane.SlotsDisagree(live) {
```

with:

```go
				if role != "" {
					named += " (" + role + ")"
				}
				return "", exitWith(ExitTimeout,
					"%s still busy after %s. Check `incoda status --queue %s`. Do NOT bypass the lane; surface the wait and coordinate instead",
					named, wait.d, pt.l.StatusKey())
			}
			rc = ExitState
			return "", exitWith(ExitState, "%v", acqErr)
		}

		if _, _, live, err := en.Position(); err == nil && lane.SlotsDisagree(live) {
```

(8 of 9) Replace:

```go
				what = fmt.Sprintf("acquired queue %q (%s; pid %d)", key, role, os.Getpid())
			}
			fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"), p.Green(what))
		}
	}

```

with:

```go
				what = fmt.Sprintf("acquired queue %q (%s; pid %d)", key, role, os.Getpid())
			}
			fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"), p.Green(what))
		}
		return "", nil
	}

	for {
		plan, err := planWithFirstLinks(dir, reg, req, machine.Options{
			Start: start, Wait: wait.d, Poll: *poll, Chain: chain, Stderr: stderr,
		}, *quiet, stderr, p)
		if err != nil {
			return machineExit(err)
		}
		for _, l := range plan.Lanes {
			if pass[l.Key] {
				if !*quiet {
					fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
						p.Dim(fmt.Sprintf("queue %q is already held by a parent incoda; running inside its lane", l.Key)))
				}
				lane.AppendLog(lane.LaneDir(dir, l.Key), "queue=%s event=reenter pid=%d cmd=%s", l.Key, os.Getpid(), textsafe.LogValue(lane.Ticket{Command: argv}.CommandString()))
				continue
			}
			q, err := lane.Open(dir, l.Key)
			if err != nil {
				return exitWith(ExitState, "%v", err)
			}
			// Every registry lock wait of this run stays inside its --wait
			// budget: a stopped incoda keeping a registry lock costs this
			// run its budget, never more.
			q.SetBudget(start, wait.d)
			toTake = append(toTake, &lanePart{l: l, key: l.Key, q: q})
		}
		// The total order that makes multi-lane runs deadlock-free stops
		// at a nested run: a parent holding "b" whose recipe now takes "a"
		// is acquiring out of order, and two such parents can each wait on
		// the other's lane until --wait expires. It cannot be prevented
		// from here (the parent's lane is already held), so it is said out
		// loud.
		for _, pt := range toTake {
			for h := range live {
				if runplan.Less(pt.l, runplan.Lane{Key: h, Pool: reg.IsPool(h)}) && !*quiet {
					fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
						p.Yellow(fmt.Sprintf("warning: taking %q while a parent incoda holds %q acquires out of sorted order; two nested runs shaped like this can wait on each other until --wait expires", pt.key, h)))
				}
			}
		}
		if len(toTake) == 0 {
			// Every lane is the parent's. Nothing to enroll, nothing to
			// watch: a kill addressed to the parent takes this process with
			// it.
			res, runErr := child.Run(argv, os.Stdin, os.Stdout, os.Stderr, nil, child.Options{
				Env:      childEnv(startEnv, held.Format(inherited.L)),
				OwnGroup: len(inherited.L) == 0,
			})
			if runErr != nil {
				return exitWith(ExitSpawn, "cannot run %q: %v", argv[0], runErr)
			}
			if res.Code != 0 {
				return &exitCode{code: res.Code}
			}
			return nil
		}

		why := ""
		for _, pt := range toTake {
			if why, err = takeLane(plan, pt); err != nil {
				return err
			}
			if why != "" {
				break
			}
		}
		if why == "" {
			// The final verify, after the last lane is acquired and before
			// the child starts. Later link or registry changes do not
			// affect the running job (spec 2.5).
			atVerify(dir, "")
			if why, err = plan.Changed(dir); err != nil {
				rc = ExitState
				return machineExit(err)
			}
		}
		if why == "" {
			break
		}
		// A replan releases every ticket this run holds, says what
		// changed, and plans again from scratch inside the same --wait
		// budget. The run loses its FIFO places (spec 2.5).
		for i := len(toTake) - 1; i >= 0; i-- {
			pt := toTake[i]
			if pt.en != nil {
				pt.q.Logf("queue=%s event=replan pid=%d why=%s", pt.key, os.Getpid(), textsafe.LogValue(why))
				pt.en.Release(ExitOK)
			}
			pt.q.Close()
		}
		toTake = nil
		fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"), p.Yellow("replan: "+why))
		if reg, err = machine.ReadRegistry(dir); err != nil {
			return machineExit(err)
		}
	}

```

(9 of 9) Replace:

```go
	}
}

// beforeFirstLink is a seam for tests; production never changes it.
var beforeFirstLink = func(dir, key string) {}

// carriedFlags is every flag the caller gave except --queue and --pool, in
// flag-name order, as a printed fix line repeats them (spec 2.6); --wait
```

with:

```go
	}
}

// Seams for tests; production never changes them. atVerify runs before
// each verify point with the lane just enrolled, or "" before the final
// verify.
var (
	beforeFirstLink = func(dir, key string) {}
	atVerify        = func(dir, key string) {}
)

// carriedFlags is every flag the caller gave except --queue and --pool, in
// flag-name order, as a printed fix line repeats them (spec 2.6); --wait
```

Append to the end of `internal/runplan/runplan.go`:

```go

// Changed is a verify point of spec 2.5: after each enroll, before waiting
// on that lane, and once more after the last lane is acquired. It re-reads
// machine.json and the named project keys' configs and says what changed
// that makes the plan wrong, or "" when nothing does:
//
//  1. generation moved and a lane of the plan changed kind or left the
//     registry;
//  2. a named project key's link changed, unless the run's --pool subset
//     is still part of it (configs are re-read whatever the generation
//     says, since links live in config.json).
//
// Every pool the plan reaches through a link must still resolve; one that
// does not fails closed with machine-state, as at plan time.
func (p *Plan) Changed(stateDir string) (string, error) {
	reg, err := machine.ReadRegistry(stateDir)
	if errors.Is(err, machine.ErrNoRegistry) {
		return "", &machine.StateError{Msg: "machine-state: machine.json: it disappeared while this run was planning; run incoda doctor"}
	}
	if err != nil {
		return "", err
	}
	if reg.Generation != p.Generation {
		for _, l := range p.Lanes {
			switch now := reg.IsPool(l.Key); {
			case l.Pool && !now:
				return fmt.Sprintf("pool %q left the registry", l.Key), nil
			case !l.Pool && now:
				return fmt.Sprintf("queue %q became a pool", l.Key), nil
			}
		}
	}
	keys := make([]string, 0, len(p.Links))
	for k := range p.Links {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cfg, err := readConfig(stateDir, k)
		if err != nil {
			return "", err
		}
		was, now := p.Links[k], machine.SortedSet(cfg.Pools)
		if machine.SameSet(was, now) {
			continue
		}
		if p.Pool != nil && len(was) > 0 && len(now) > 0 && machine.Subset(p.Pool, now) {
			continue
		}
		return fmt.Sprintf("the link of %q changed: %s -> %s", k, machine.SetText(was), machine.SetText(now)), nil
	}
	for _, l := range p.Lanes {
		if l.Pool && len(l.Via) > 0 {
			if _, err := resolvePool(stateDir, reg, l.Via[0], l.Key); err != nil {
				return "", err
			}
		}
	}
	return "", nil
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run (bash), one at a time:

- `go test ./internal/runplan/ -run 'TestChanged' -count=1`
- `go test ./internal/cli/ -run 'TestReplanAtEachVerifyPoint|TestReplanWhenAPoolLeavesTheRegistry' -count=1`
- `go test . -run 'TestRelinkWhileWaitingReplans' -count=1`

Expected: `ok` for each package.

- [ ] **Step 5: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/run.go internal/cli/run_test.go internal/runplan/runplan.go internal/runplan/runplan_test.go pools_test.go
git commit -F - <<'MSG'
feat: a run verifies its plan after each enroll and replans when a kind or a link changed

After each enroll and once more before the child starts, a run re-reads
machine.json and its project keys' configs. A lane that changed kind or
left the registry, or a link that changed beyond the run's --pool
subset, makes it release everything, say replan:, log event=replan and
plan again inside the same --wait budget. A linked pool that no longer
resolves fails closed.
MSG
```


---

### Task 10: Unpooled runs on a project key count on that lane too

Plan 2b charged unpooled holders (live stray tickets and orphan records of an older incoda, spec 2.3) on pool acquisitions only, because project runs took no pools yet. Now that every project run takes its linked pools, a stray on a linked project key K is charged on K's pools through those acquisitions (`ChargedPools`, unchanged). This task decides the open question of the index: a stray on project key K is also charged on lane K itself, since the older incoda that held K used K's own width as well (the safe direction; spec 2.3 names only the pools). Every acquisition poll, on project lanes too, now rescans unpooled holders. It also adds the two tests plan 2b carried: FIFO with two waiters behind an unpooled holder, and an orphan record holding a lane end to end.

**Files:**
- Modify: `internal/cli/run.go`
- Modify: `internal/machine/unpooled.go`
- Test: `internal/machine/unpooled_test.go`
- Test: `strays_test.go`

**Interfaces:**
- Consumes: `machine.ScanUnpooled`, `machine.ChargedPools`, `machine.UpgradeBlocked` (plan 2b); `holdOldTicket` (root tests, plan 2a); `procinfo.Lookup` (plan 2b).
- Produces: `machine.ChargedTo(stateDir string, reg *Registry, key string, us []Unpooled) []Unpooled` now also answers for a project key (the holders on that key).

- [ ] **Step 1: Write the failing tests**

The FIFO test waits for each waiter's busy line rather than for `status`: status counts positions, not unpooled holders, so it shows the first waiter as a holder (a plan 5 item of the index).

In `internal/machine/unpooled_test.go`, replace:

```go
		return strings.Join(s, ",")
	}
	for _, tc := range []struct{ pool, want string }{
		{"builds", "2,3"}, {"tests", "1,3"}, {"vm", "1,3"}, {"computer-use", "3"}, {"linked", ""},
	} {
		if got := pids(ChargedTo(state, reg, tc.pool, us)); got != tc.want {
			t.Fatalf("ChargedTo(%s) = %s, want %s", tc.pool, got, tc.want)
```

with:

```go
		return strings.Join(s, ",")
	}
	for _, tc := range []struct{ pool, want string }{
		{"builds", "2,3"}, {"tests", "1,3"}, {"vm", "1,3"}, {"computer-use", "3"}, {"linked", "1"}, {"unlinked", ""},
	} {
		if got := pids(ChargedTo(state, reg, tc.pool, us)); got != tc.want {
			t.Fatalf("ChargedTo(%s) = %s, want %s", tc.pool, got, tc.want)
```

In `strays_test.go`, replace:

```go
	"time"

	"github.com/deblasis/incoda/internal/machine"
)

// startOldRunAfterFenceDeletion migrates a state directory, deletes the
```

with:

```go
	"time"

	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/procinfo"
)

// startOldRunAfterFenceDeletion migrates a state directory, deletes the
```

Then append to the end of `strays_test.go`:

```go

// TestFIFOWithAnUnpooledHolder: two new runs queue on a pool behind an
// unpooled run of an older incoda (a live stray ticket). Both name it on
// their busy line, neither starts while it runs, and once it is gone they
// are served in arrival order, one at a time.
func TestFIFOWithAnUnpooledHolder(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	mustRun(t, incoda, state, 0, "config", "seed")
	release := holdOldTicket(t, filepath.Join(machine.StraysDir(state), "1"), "builds", 999999, "zig", "build")
	stamps := t.TempDir()
	var outs [2]syncBuffer
	var ws [2]*exec.Cmd
	for i := range ws {
		label := fmt.Sprintf("w%d", i)
		ws[i] = exec.Command(incoda, "run", "--queue", "builds", "--wait", "60s", "--poll", "50ms",
			"--", stamp, filepath.Join(stamps, label+".txt"), label, "300")
		ws[i].Env = laneEnv(state)
		ws[i].Stdout, ws[i].Stderr = &outs[i], &outs[i]
		if err := ws[i].Start(); err != nil {
			t.Fatal(err)
		}
		defer func(c *exec.Cmd) { _ = c.Process.Kill(); _ = c.Wait() }(ws[i])
		// The busy line comes after the first poll, so the waiter is
		// enrolled before the next one starts. (status shows the first
		// waiter as a holder: it counts positions, not unpooled holders.)
		waitForText(t, &outs[i], "incoda:   unpooled run by an older incoda: pid 999999, key builds\n")
	}
	time.Sleep(300 * time.Millisecond)
	for i := range ws {
		if _, ok := readInterval(t, filepath.Join(stamps, fmt.Sprintf("w%d.txt", i))); ok {
			t.Fatalf("w%d ran beside the unpooled holder", i)
		}
	}
	freed := time.Now().UnixNano()
	release()
	for i, w := range ws {
		if err := w.Wait(); err != nil {
			t.Fatalf("w%d: %v\n%s", i, err, outs[i].String())
		}
	}
	w0, _ := readInterval(t, filepath.Join(stamps, "w0.txt"))
	w1, _ := readInterval(t, filepath.Join(stamps, "w1.txt"))
	if w0.enter < freed || w1.enter < w0.exit {
		t.Fatalf("FIFO behind the unpooled holder violated: freed %d, w0 %+v, w1 %+v", freed, w0, w1)
	}
}

// TestUnpooledHolderOnAProjectKeyCountsOnThatLane: an older incoda's run
// on project key K used K's own width, so a new run on K counts it on K
// itself before it reaches K's pools (a plan 3 decision; spec 2.3 charges
// the pools).
func TestUnpooledHolderOnAProjectKeyCountsOnThatLane(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	mustRun(t, incoda, state, 0, "config", "p-gate", "--pool", "tests")
	holdOldTicket(t, filepath.Join(machine.StraysDir(state), "1"), "p-gate", 999999, "just", "gate")
	out := mustRun(t, incoda, state, 121, "run", "--queue", "p-gate", "--wait", "300ms", "--poll", "50ms",
		"--", stamp, filepath.Join(t.TempDir(), "s.txt"), "s", "1")
	inOrder(t, out, `incoda: queue "p-gate" busy (1 slot(s), 0 ahead of you)`,
		"incoda:   unpooled run by an older incoda: pid 999999, key p-gate\n", `incoda: queue "p-gate" still busy after 300ms.`)
}

// TestOrphanRecordHoldsALaneUntilItsTreeIsGone: the record of an older
// incoda's job that a kill is ending counts as a held slot on its key's
// pool while any recorded process still runs; once the tree is gone the
// next run proceeds and the record is deleted (spec 3.2).
func TestOrphanRecordHoldsALaneUntilItsTreeIsGone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("orphan records are written by the Unix old-holder kill")
	}
	incoda, stamp := binaries(t)
	state := t.TempDir()
	mustRun(t, incoda, state, 0, "config", "seed")
	job := exec.Command("sleep", "30")
	if err := job.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = job.Process.Kill(); _ = job.Wait() }()
	p, err := procinfo.Lookup(job.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	rec := fmt.Sprintf(`{"key":"builds","pid":999999,"command":["zig","build"],"descendants":[{"pid":%d,"start":%d}],"groups":[],"by_pid":1,"at":"2026-10-02T00:00:00Z"}`, p.PID, p.Start)
	if err := os.MkdirAll(machine.OrphansDir(state), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(machine.OrphansDir(state), "999999-1.orphan")
	if err := os.WriteFile(path, []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}
	out := mustRun(t, incoda, state, 121, "run", "--queue", "builds", "--wait", "300ms", "--poll", "50ms",
		"--", stamp, filepath.Join(t.TempDir(), "a.txt"), "a", "1")
	if !strings.Contains(out, "incoda:   unpooled run by an older incoda: pid 999999, key builds\n") {
		t.Fatalf("the orphan record holds builds:\n%s", out)
	}
	_ = job.Process.Kill()
	_ = job.Wait()
	mustRun(t, incoda, state, 0, "run", "--queue", "builds", "--wait", "10s", "--poll", "50ms",
		"--", stamp, filepath.Join(t.TempDir(), "b.txt"), "b", "1")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("an acquisition deletes the record of an empty tree")
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run (bash), one at a time:

- `go test ./internal/machine/ -run 'TestChargedPools' -count=1 -timeout 120s`
- `go test . -run 'TestFIFOWithAnUnpooledHolder|TestUnpooledHolderOnAProjectKeyCountsOnThatLane|TestOrphanRecordHoldsALaneUntilItsTreeIsGone|TestUnknownStrayKeyCountsOnEveryPool' -count=1 -timeout 120s`

Expected: `TestChargedPools` fails with `ChargedTo(linked) = , want 1`, and the root `TestUnpooledHolderOnAProjectKeyCountsOnThatLane` fails with `missing "incoda: queue \"p-gate\" busy (1 slot(s), 0 ahead of you)"`: the run acquired `p-gate` and waited only on `tests`. The other three root tests pass already.

- [ ] **Step 3: Implement**

In `internal/cli/run.go`, replace:

```go
		}
		key := pt.key
		role := pt.l.Role()
		// On a pool, unpooled runs of an older incoda (strays and orphan
		// records, spec 2.3) hold slots too. Each poll rescans them,
		// deletes stray lanes that have fully died, and refuses at once
		// when one of them is this run's own ancestor: waiting for it
		// would never end.
		var unpooled []machine.Unpooled
		var countUnpooled func() (int, error)
		if reg.IsPool(key) {
			countUnpooled = func() (int, error) {
				// The probes of one poll wait for a stray's registry
				// lock at most PollProbeWait and never past the budget:
				// a lock held for ever reads as one held slot.
				var end time.Time
				if wait.d >= 0 {
					end = start.Add(wait.d)
				}
				all, err := machine.ScanUnpooled(dir, true, lane.ProbeDeadline(end, lane.PollProbeWait))
				if err != nil {
					return 0, &machine.StateError{Msg: "machine-state: cannot scan for unpooled runs: " + textsafe.Escape(err.Error())}
				}
				mine := machine.ChargedTo(dir, reg, key, all)
				if !chain.Skip {
					for _, u := range mine {
						if !u.Unknown && chain.Contains(u.PID) {
							return 0, machine.UpgradeBlocked(u.PID, u.Key)
						}
					}
				}
				unpooled = mine
				return len(mine), nil
			}
		}
		acqErr := en.Acquire(ctx, lane.AcquireOptions{
			Wait:   budget,
```

with:

```go
		}
		key := pt.key
		role := pt.l.Role()
		// Unpooled runs of an older incoda (strays and orphan records,
		// spec 2.3) hold slots too: on a pool as ChargedPools says, on a
		// project lane when they ran on that very key. Each poll rescans
		// them, deletes stray lanes that have fully died, and refuses at
		// once when one of them is this run's own ancestor: waiting for it
		// would never end.
		var unpooled []machine.Unpooled
		countUnpooled := func() (int, error) {
			// The probes of one poll wait for a stray's registry
			// lock at most PollProbeWait and never past the budget:
			// a lock held for ever reads as one held slot.
			var end time.Time
			if wait.d >= 0 {
				end = start.Add(wait.d)
			}
			all, err := machine.ScanUnpooled(dir, true, lane.ProbeDeadline(end, lane.PollProbeWait))
			if err != nil {
				return 0, &machine.StateError{Msg: "machine-state: cannot scan for unpooled runs: " + textsafe.Escape(err.Error())}
			}
			mine := machine.ChargedTo(dir, reg, key, all)
			if !chain.Skip {
				for _, u := range mine {
					if !u.Unknown && chain.Contains(u.PID) {
						return 0, machine.UpgradeBlocked(u.PID, u.Key)
					}
				}
			}
			unpooled = mine
			return len(mine), nil
		}
		acqErr := en.Acquire(ctx, lane.AcquireOptions{
			Wait:   budget,
```

In `internal/machine/unpooled.go`, replace:

```go
	return append([]string(nil), reg.Pools...)
}

// ChargedTo returns the holders of us that count on pool. A key that is
// not a pool is charged nothing: unpooled holders count on pools only.
func ChargedTo(stateDir string, reg *Registry, pool string, us []Unpooled) []Unpooled {
	if !reg.IsPool(pool) {
		return nil
	}
	var out []Unpooled
	for _, u := range us {
		for _, p := range ChargedPools(stateDir, reg, u.Key) {
			if p == pool {
				out = append(out, u)
				break
			}
```

with:

```go
	return append([]string(nil), reg.Pools...)
}

// ChargedTo returns the holders of us that count on lane key. On a pool
// they are the holders ChargedPools charges to it (spec 2.3). On a project
// lane they are the holders on that very key: an older incoda that held
// project key K used K's own width as well as its pools, so a new run on K
// counts it against K's slots too, before it reaches the pools (the safe
// direction; spec 2.3 names only the pools).
func ChargedTo(stateDir string, reg *Registry, key string, us []Unpooled) []Unpooled {
	var out []Unpooled
	for _, u := range us {
		if !reg.IsPool(key) {
			if u.Key == key {
				out = append(out, u)
			}
			continue
		}
		for _, p := range ChargedPools(stateDir, reg, u.Key) {
			if p == key {
				out = append(out, u)
				break
			}
```

- [ ] **Step 4: Run the tests to see them pass**

Run (bash), one at a time:

- `go test ./internal/machine/ -run 'TestChargedPools' -count=1`
- `go test . -run 'TestFIFOWithAnUnpooledHolder|TestUnpooledHolderOnAProjectKeyCountsOnThatLane|TestOrphanRecordHoldsALaneUntilItsTreeIsGone|TestUnknownStrayKeyCountsOnEveryPool' -count=1`

Expected: `ok` for each package.

- [ ] **Step 5: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/run.go internal/machine/unpooled.go internal/machine/unpooled_test.go strays_test.go
git commit -F - <<'MSG'
feat: unpooled runs on a project key count on that lane too

An older incoda's run on project key K used K's own width, so a new run
on K now counts it on lane K as well as on K's pools, and every
acquisition poll rescans unpooled holders. Tests cover two waiters
served in order behind an unpooled holder and an orphan record that
holds a pool until its tree is gone.
MSG
```


---

## Self-review against the spec

Every in-scope clause handled here, and the task that implements and tests it (plan 3b's self-review maps the rest of plan 3):

- **2.2 kinds.** Kind from `machine.json` only (`runplan.Make` uses `Registry.IsPool`; Task 5). A project's own slots still apply inside its pools: the project lane is its own lane in the set (Task 6). Pools carry no `pools` or `quiet_machine`: `WriteLink` refuses a pool (Task 3, `TestWriteLink`, `TestConfigLinkFlags`).
- **2.4 lane set and order.** Named keys plus each named project key's whole link or `--pool` subset, deduplicated (Tasks 5 and 8, `TestLaneSetAndTotalOrder`, `TestPoolFlag`); `--queue` mixing project and pool keys (`kungfoo-gate,builds` in `TestRunTakesItsLinkedPools`); total order, projects then pools (`Less`, `inOrder` checks of the acquired lines); one lane enrolled at a time after the previous is held (Task 6); one `--wait` budget from process start across machine.lock, first links, replans and lanes (Tasks 8 and 9 pass `start`); `--slots` only on named project keys and checked, never written, on a named pool (`TestMakeRules`); `--exclusive` does not propagate (`TestRunTakesItsLinkedPools` reads both tickets).
- **2.5 plan, enroll, verify.** Verify after each enroll and once before the child (Task 9, `TestReplanAtEachVerifyPoint` both subtests, `TestRelinkWhileWaitingReplans`); triggers 1 and 2 (`TestChanged`); replan releases everything, prints `replan:`, logs `event=replan`, shares the budget (Task 9); closed re-checked at Enroll and on every poll with `closed-while-waiting:` (Task 4, `TestClosedWhileWaiting`, `TestEnrollRefusesClosedReasonlessAndNewer`); every linked pool must resolve at plan time and at each verify (`TestMakeRules`, `TestChanged`, `TestReplanWhenAPoolLeavesTheRegistry`). Trigger 3: plan 3b Task 6.
- **2.7** `via` and `wait` on tickets (Task 6). `quiet`: plan 3b Task 6. `root` and `pgid`: plan 4.
- **3.5 suggestions in the unlinked refusal** (Task 7) and as the only first link `run` makes (Task 8).
- **4.1** run never prompts (no terminal read anywhere in `run`); the unlinked refusal with and without a suggestion, the multi-key form, the quiet `--wait 5m` addition, escaped descriptions cut to 60 columns, config read without opening the lane (Task 7, `TestUnlinkedRefusal` byte for byte, `TestUnlinkedRunIsRefusedAndLeavesNothingBehind`, `TestPrintedUnlinkedLineRunsAsPrinted`). The nested form and its trailer: plan 4.
- **4.2 `--pool`** in full: set semantics, registered pools, no project key, ignored next to a project key, first link only equal to the suggestion with `link-needs-user:` otherwise, compare-and-set under machine.lock then the registry lock, `linked:`, `link-conflict:`, `pool-mismatch:` with its fix line, multi-key handling (Task 8, `TestPoolFlag`, `TestFirstLinkConflict`, `TestRunPoolMakesTheFirstLinkOnlyToTheSuggestion` including the first-link race of spec 9).
- **4.3 `config` link flags** (Task 3, `TestConfigLinkFlags`, `TestConcurrentConfigFirstLinksNeverEscalate`, `TestLinkEdit`). `link`, `init`, `pools`: plan 3b.
- **4.4** config writes are locked read-modify-write (plan 1); link writes also hold machine.lock first (`WriteLink`, Task 3).
- **4.5** closed and require_reason of a pool bind a run that enrolls on it, with `(pool, via K)` in the text; a pool an ancestor holds is not re-checked; a pool's description never binds (Task 5 `TestMakeRules`, Task 6 `TestPoolRulesBindLinkedRuns`).
- **4.6** bad-text on every `config` text write, one table (Task 3, table-driven `TestConfigRefusesControlCharacters`). `pools add` and `init`: plan 3b.
- **5.1** prefixes `unlinked:`, `link-conflict:`, `pool-mismatch:`, `link-exists:`, `link-needs-user:`, `closed-while-waiting:`, `machine-state:` for unresolved links (Tasks 3 to 9); the busy, holder and 121 lines for pools (Task 6). `kind-busy:`, `pool-linked:`, `needs-terminal:` and `incoda help`: plan 3b.
- **2.6 fix lines** as a shared builder with POSIX and PowerShell quoting, the directory part, carried flags and no placeholders, and the spec 9 reproduction test against a stub (Task 2).

Carried items:

- **From plan 1:** `Enroll` fails closed on `NewerSchemaError` and the waiting poll surfaces it (Task 4); the `config` text checks are table-driven (Task 3); the closed refusal is rewritten per 4.5 (Tasks 5 and 6).
- **From plan 2b:** bounded registry locks in Enroll, Release, Position, MarkAcquired, `RequestKill`, `WaitGone`, `ForceRelease`, `SaveConfig` and `LockAll` (Task 1, `TestRegistryWaitsAreBounded`, `TestOwnLaneOperationsAreBoundedByTheirWait`); strays on project keys decided and charged (Task 10, `TestUnpooledHolderOnAProjectKeyCountsOnThatLane`); the FIFO test with two waiters behind an unpooled holder (`TestFIFOWithAnUnpooledHolder`); the end-to-end orphan-as-holder test (`TestOrphanRecordHoldsALaneUntilItsTreeIsGone`). Unpooled charging now reaches project runs through their pools (Tasks 6 and 10; `TestUnknownStrayKeyCountsOnEveryPool` updated).

Left to plan 3b: kind changes (`pools add|remove`, `kind-busy:`, `pool-linked:`), `incoda pools` and `--json`, `incoda link`, `incoda init` (interactive, `--print`, `--apply-suggestions`), quiet-machine (2.8 at top level, trigger 3, the `quiet` ticket field), `needs-terminal:`, bad-text on `pools add` and `init`, doctor's unlinked lines, and `incoda help`.

Left to plan 4: the nested ordering rule, non-blocking acquisition, `out-of-order-busy:`, the nested unlinked refusal and the outer trailer, self-wait, the sibling check, `root` and `pgid`, nested quiet. Today's nested behaviour stays: pass-through by verified P, and the out-of-order warning, now judged in the total order.

Left to plan 5: `status --tree`, the watch pool tree, the `status --json` additions (including `pools`, built on plan 3b's `report.BuildPools`), docs and release notes; and the plan 5 items of the index (status shows a waiter held off only by unpooled holders as holding, which Task 10's FIFO test works around).
