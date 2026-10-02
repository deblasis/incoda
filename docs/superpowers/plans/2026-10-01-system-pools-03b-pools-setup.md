# System pools, plan 3b: Kind changes, setup commands and quiet machine

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Refreshed against bba4ccb** (plan 3a landed as `e40deba..bcc4580`, index updated in `bba4ccb`). Every Replace block was re-applied in order to a scratch copy of that commit; each occurs exactly once when reached and every task builds, vets (darwin, linux, windows, with and without `incoda_crashpoints`) and passes its tests. What changed:
> - New **Task 0** carries plan 3a's follow-ups (index, "Carried forward from plan 3a"): run's signal context now reaches `machine.Ensure`, so Ctrl-C during the wait for `machine.lock` or for older runs of the state upgrade exits 130 (as 6829fc4 did for first links); a `--quiet-machine`-only `config` change is logged as `event=link ... quiet_machine=...` and echoed; a comment at `linkEdit.named()`; a test of the several-keys unlinked refusal with a quiet suggestion.
> - Task 5: `internal/machine/link.go` blocks rewritten. Since bbe31a2 the pool check lives in `writeLinkHeld`, so the `project` flag goes through it (`WriteLink` and `WriteFirstLinks` pass `true`, `WriteStep` `false`). Its interactive `init` no longer prints the placeholder `incoda link KEY`: it names each key it left unlinked.
> - Task 6: the third `run.go` block starts at the ticket literal (since 6655267 `takeLane` opens with the `busy` closure). `lineRefused` now adds every pool when a printed run line takes quiet-machine (plan 3a's b001b41 lane-set check), with `TestQuietFixLineChecksEveryPool`.
> - Every printed command of this plan goes through `internal/fixline`: `machine.configCmd` (Task 1, the `pools add` refusals) and `runplan.LinkLine` (Task 3, used by `link`, `init --print`, `init --apply-suggestions`, interactive `init` and doctor). Tests compare the POSIX text and the PowerShell form where they print one (`TestConfigCmd`, `TestLinkLine`, root helper `linkCmd`).
> - Expected texts that changed with it: `TestAddPool` (the two refusals build their command with `configCmd`), `TestLinkNeedsATerminal`, `TestInitPrint` (doctor line) and `TestInitApplySuggestions` (through `linkCmd`), `TestInitAsks` (the `left unlinked:` line names `incoda link polymatto`). Plan 3a's own tests change only in Task 0 (`TestConfigLinkFlags`: two more `event=link` lines and the quiet_machine echo).

**Goal:** The user's setup commands for pools (`pools add|remove`, `incoda pools`, `incoda link`, `incoda init` interactive, `--print` and `--apply-suggestions`), quiet-machine at top level, doctor's unlinked lanes, and `incoda help` documenting every prefix.

**Architecture:** Kind changes are `machine.AddPool` and `machine.RemovePool`: `machine.lock`, then the lane's registry lock held across the ticket check and the `machine.json` write (`lane.Queue.UpdateIdle`). The setup commands talk to a person only through a terminal seam (`cli.openTerminal`) with a line-based picker; each answer is a compare-and-set under `machine.lock` (`machine.WriteLink`, `machine.WriteStep`) against the value the question showed. `runplan` gains `UnlinkedLanes` and quiet-machine (every pool in the plan, exclusive `quiet` tickets, replan trigger 3). `report.BuildPools` is the pool view `incoda pools --json` prints and plan 5's `status --json` will reuse.

**Tech Stack:** Go 1.27, `golang.org/x/sys`, standard library. Tests: `go test`, integration tests that build and run the real binary, in-process `cli.Main` tests with an injected terminal.

**Spec:** `docs/superpowers/specs/2026-10-01-system-pools-design.md` (sections 2.8 at top level, 2.5 trigger 3, 2.7 `quiet`, 3.4, 3.5 where suggestions appear in `init`, 4.3 `link`, `init`, `pools` and `--wait`, 4.6 for `pools add` and `init`, 5.1 `kind-busy:`, `pool-linked:`, `needs-terminal:` and `incoda help`, 5.5 unlinked lanes). Plan index: `docs/superpowers/plans/2026-10-01-system-pools-00-index.md`. Plan 3a (`...-03a-pools-run.md`) has landed before this plan: it names `runplan`, `fixline`, `machine.WriteLink`, `linkTestKeys` and the other helpers this plan uses.

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
- Every printed run, config, link or kill command is built by `internal/fixline` (plan 3a Task 2): POSIX sh on Unix, PowerShell on Windows, no placeholders. This plan's are `machine.configCmd` (Task 1), `runplan.LinkLine` (Task 3) and the `ConfigLine` of plan 3a's `configLine` (Task 4); a command with no variable word (`incoda init`, `incoda init --print`, `incoda doctor`) is literal text, which is what fixline renders for it in both shells. Every printed run line is checked by `lineRefused` against every lane it would take (plan 3a, b001b41), the pools quiet-machine brings included (Task 6). Usage and help texts are documentation and may say `KEY`.
- An interrupt (SIGINT, SIGTERM) ends every wait of a run with exit 130, including the wait for `machine.lock` and for older runs during the state upgrade (Task 0). Setup commands keep the default signal behavior: an interrupted `init` keeps the steps already applied.
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

Plan 3a's decisions hold. In addition:

- **`incoda pools` (the list) only reads**, like status: no `--wait`, no migration; on an unmigrated layout it prints the banner and `no pools registered yet: the next mutating incoda command registers builds, computer-use, tests, vm`. `--wait` (default 1m) is on `pools add` and `pools remove`, as on `config`, `link` and `init`. `pools --json` is `{"schema": 1, "migrated": ..., "generation": N, "pools": [{"key", "slots", "description", "holders", "waiting", "linked"}]}`, `holders` and `waiting` being the ticket entries `status --json` uses.
- **`pools add` on an existing pool** is refused (exit 120, `pools add: "K" is already a pool; change its slots or description with incoda config K`). A conversion keeps the lane's fields; `--slots` (else the lane's count, else 1) and `--description` are written in the same hold. A lane with `quiet_machine` is `kind-busy:` as well as a linked one (pools carry neither, spec 2.2). A registry lock held past the budget during a kind change is exit 121 `kind-busy: "K": ...`. `pools remove` of a pool that has no lane directory updates `machine.json` only. Both log `event=kind` in the lane's `lane.log` and in `machine.log`.
- **The picker** is line-based: the pools are listed with a number, `[x]` marks the selection (the current link, else a usable suggestion), numbers toggle, an empty line confirms, `q` or the end of input cancels. It works in every console and a test drives it through the seam. `incoda link` sets the pools only (quiet_machine stays `incoda config KEY --quiet-machine`).
- **Interactive `init`** asks, in order, each pool's cap, other resources (name, then slots, then description, each re-asked until valid), then a link per unlinked open lane (an empty selection leaves the lane unlinked). Its compare-and-set for a cap is against the count shown; its link step sets `quiet_machine` when the chosen set equals a suggestion that carries it. `q` or the end of input stops `init` with exit 120 `init: stopped; the answers before this one are applied`, as Ctrl-C does. Its result lines (`linked K -> P`, `left unlinked: ...`) go to stdout, questions to the terminal.
- **`init --print`** refuses `--wait` (it takes no lock) and, on a layout not upgraded yet, reads the old `queues/` with the bootstrap pools as the registry to be. `init --apply-suggestions` leaves a lane alone when its suggestion names a pool this machine lacks (`left unlinked: K (no suggestion: ...)`), and a lane linked or closed meanwhile.
- **doctor** prints one `attention:` line per unlinked open lane (spec 5.5), with its suggestion or the ask-the-user text; it still exits 0.
- **Quiet machine.** From config, the first named project lane (in key order) with `quiet_machine` names the source. The informational line comes with each wait notice (the first, then every 60s), `holding nothing` before any pool is held. A pool only quiet-machine brings has the role `pool, quiet-machine`. Quiet tickets are exclusive and record `quiet: true`; the enqueue line logs `quiet=true`. Trigger 3 is checked after trigger 1, so a pool of the plan that left the registry still reports `left the registry`.
- **Interrupts during the upgrade** (Task 0). Only run passes a context to `machine.Ensure`; an interrupt ends the upgrade's waits at the points where a spent `--wait` already ends them, so it leaves what a timeout leaves and the next mutating command resumes. The line is `incoda: interrupted while waiting for machine.lock or the state upgrade` (exit 130).
- **quiet_machine is part of the link** (Task 0). Any write that changes it logs `event=link by=<by> old=<pools> new=<pools> quiet_machine=<new>`; `config` echoes a change of it as `link: tests -> tests, quiet_machine` (both sides carry `, quiet_machine` when set). A change of the pools alone logs and echoes as plan 3a does.
- **Printed `incoda link` lines name the key.** Interactive `init` ends with `left unlinked: a, b; runs on them are refused until they are linked (the user runs: incoda link a, incoda link b)` instead of a `KEY` placeholder. On Windows every printed key is PowerShell-quoted (`incoda link 'cap-gate'`), as fixline quotes keys; plan 3a's refusals keep their own text.
- **`incoda help`** documents the prefixes and informational lines this binary prints after plan 3; plan 4 adds its own (`out-of-order-busy:`, `self-wait:`, `quiet-nested:`, `nested-refused:`, `held-lost:`, `exclusive-ignored:`).

## File structure

| File | Responsibility |
|---|---|
| `internal/lane/config.go` | `ErrLaneBusy`, `UpdateIdle` |
| `internal/lane/ticket.go`, `queue.go` | `Ticket.Quiet`, `quiet=true` in the enqueue log |
| `internal/machine/kind.go` | `AddPool`, `RemovePool`, `LinkedFrom`, `PoolChange`, `KindResult` |
| `internal/machine/link.go` | `ErrLinkMoved`, `WriteStep`, the project flag through `writeLinkHeld`, `event=link` on a quiet_machine change |
| `internal/machine/options.go`, `idle.go`, `migrate.go` | `Options.sleep`: the upgrade's waits end on an interrupt |
| `internal/cli/state.go` | `mutatingStateCtx` |
| `internal/cli/config.go`, `linkflags.go` | the quiet_machine echo, the `named()` comment |
| `internal/runplan/refusals.go` | `LinkLine`; `lineRefused` sees the pools quiet-machine brings |
| `internal/report/pools.go` | `Pools`, `Pool`, `BuildPools` |
| `internal/runplan/unlinked.go` | `Unlinked`, `UnlinkedLanes`, `ConfigLine`, `SlotNotes` |
| `internal/runplan/runplan.go` | quiet-machine in `Make`, trigger 3 in `Changed`, the `pool, quiet-machine` role |
| `internal/cli/pools.go` | `incoda pools`, `pools add`, `pools remove` |
| `internal/cli/terminal.go` | the terminal seam and the picker |
| `internal/cli/link.go` | `incoda link`, `askLink`, `pickLink` |
| `internal/cli/initcmd.go` | `incoda init`, `--print`, `--apply-suggestions` |
| `internal/cli/misc.go` | doctor's unlinked lines |
| `internal/cli/run.go` | the signal context before the upgrade, `--quiet-machine`, quiet tickets, the quiet line |
| `internal/cli/cli.go` | dispatch of `pools`, `link`, `init`; the help text |
| root `pools_test.go`, `init_test.go` (new), `config_test.go`, `integration_test.go`, `busyregistry_unix_test.go`; `internal/cli` `link_test.go`, `init_test.go`, `help_test.go` (new), `run_test.go`; `internal/machine` `kind_test.go`, `interrupt_test.go` (new); `internal/runplan/runplan_test.go` | tests |


---

### Task 0: Plan 3a follow-ups: interrupts during the upgrade, quiet_machine as a link change

Plan 3a's final review routed four items to this plan, to land first (plan index, "Carried forward from plan 3a"; the fifth, the stale Replace blocks, is folded into Tasks 5 and 6).

1. A run installs its signal context only after `mutatingState`, so `machine.Ensure` waits for `machine.lock`, and during the state upgrade for older runs (M2, M5, and Windows' not-idle wait), outside it: Ctrl-C there never reaches the exit 130 path that every other queueing wait of a run has, and nothing says what was interrupted. The run now installs its signal context before `mutatingState` and passes it through `machine.Ensure` (`Options.Ctx`, as commit 6829fc4 did for first links). Every sleep inside the upgrade goes through `Options.sleep`, which returns `machine.ErrInterrupted` once the context ends, at the same points that already return a timeout, so an interrupted upgrade leaves exactly what an upgrade that ran out of `--wait` leaves (the recovery rows of plan 2a resume it). The run maps it to exit 130 `incoda: interrupted while waiting for machine.lock or the state upgrade`. Every other command passes no context and is unchanged.
2. `incoda config KEY --quiet-machine[=false]` alone changes the link (quiet_machine is part of it, spec 4.4) but logged and echoed nothing. `machine.WriteLink` now logs `event=link` whenever the pools or quiet_machine change, with ` quiet_machine=true|false` appended when quiet_machine moved, and `config` echoes the change as `link: tests -> tests, quiet_machine` (both sides carry `, quiet_machine` when it is set; a change of the pools alone echoes as before).
3. A comment at `linkEdit.named()` says why `--remove-pool` is left out: a name being removed need not be a registered pool.
4. A test for the unlinked refusal of several keys where one suggestion carries quiet_machine: the config line with `--quiet-machine`, the run line with `--wait '5m'`, and the note.

**Files:**
- Modify: `internal/machine/options.go`
- Modify: `internal/machine/idle.go`
- Modify: `internal/machine/migrate.go`
- Modify: `internal/machine/link.go`
- Modify: `internal/cli/state.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/config.go`
- Modify: `internal/cli/linkflags.go`
- Test: `internal/machine/interrupt_test.go` (new)
- Test: `busyregistry_unix_test.go`
- Test: `config_test.go`
- Test: `internal/runplan/runplan_test.go`

**Interfaces:**
- Consumes: `machine.Options.Ctx`, `machine.ErrInterrupted` (commit 6829fc4); `holdLockElsewhere` (busyregistry_test.go); `holdTicket`, `takeLock` (internal/machine/idle_test.go); `spec4Pools`, `spec4Rows` (internal/runplan/runplan_test.go).
- Produces: `func (o Options) sleep() error` in `machine`; `func mutatingStateCtx(ctx context.Context, start time.Time, wait, poll time.Duration, chain procinfo.Chain, stderr io.Writer) (string, *machine.Registry, error)` and `func linkText(c lane.Config) string` in `cli`; root test helper `interruptOnLine(t, c *exec.Cmd, line string, sig syscall.Signal) (int, string)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/machine/interrupt_test.go`:

```go
package machine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
)

// TestWaitIdleEndsWhenItsContextDoes: the upgrade's wait for older runs
// (M2, M5) ends with ErrInterrupted between polls once its context ends,
// long before the --wait budget.
func TestWaitIdleEndsWhenItsContextDoes(t *testing.T) {
	state := t.TempDir()
	holdTicket(t, lane.QueuesDir(state), "builds", 4711, "zig", "build")
	lk := takeLock(t, state)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	err := waitIdle(state, lk, Options{Start: start, Wait: 10 * time.Second, Poll: 50 * time.Millisecond, Ctx: ctx}, phaseM2)
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("want ErrInterrupted, got %v", err)
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("the interrupted wait took %v", el)
	}
}

// TestNotIdleWaitEndsWhenItsContextDoes: the wait for a directory Windows
// will not move yet ends the same way.
func TestNotIdleWaitEndsWhenItsContextDoes(t *testing.T) {
	state := t.TempDir()
	lk := takeLock(t, state)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var w notIdleWait
	start := time.Now()
	if err := w.wait(state, lk, Options{Start: start, Wait: time.Minute, Poll: 2 * time.Second, Ctx: ctx}); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("want ErrInterrupted, got %v", err)
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("an ended context must not wait a poll: %v", el)
	}
}
```

Append to the end of `busyregistry_unix_test.go`:

```go

// interruptOnLine starts c in its own process group, sends sig once a
// stderr line contains line, and returns the exit code and stderr. The
// run must end within 3s of the signal.
func interruptOnLine(t *testing.T, c *exec.Cmd, line string, sig syscall.Signal) (int, string) {
	t.Helper()
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	errPipe, err := c.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Only this run's own group, started above with Setpgid.
		_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	})
	done := make(chan error, 1)
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(errPipe)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
		done <- c.Wait()
	}()
	var got []string
	deadline := time.After(15 * time.Second)
	for seen := false; !seen; {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("%s: run ended before %q:\n%s", sig, line, strings.Join(got, "\n"))
			}
			got = append(got, l)
			seen = strings.Contains(l, line)
		case <-deadline:
			t.Fatalf("%s: no %q within 15s:\n%s", sig, line, strings.Join(got, "\n"))
		}
	}
	sent := time.Now()
	if err := c.Process.Signal(sig); err != nil {
		t.Fatal(err)
	}
	for l := range lines {
		got = append(got, l)
	}
	var werr error
	select {
	case werr = <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: run did not end after the signal:\n%s", sig, strings.Join(got, "\n"))
	}
	if d := time.Since(sent); d > 3*time.Second {
		t.Fatalf("%s: run took %s to end after the signal", sig, d)
	}
	code := 0
	var ee *exec.ExitError
	if errors.As(werr, &ee) {
		code = ee.ExitCode()
	}
	return code, strings.Join(got, "\n")
}

// TestMigrationLockWaitIsInterruptible: a run that must upgrade the state
// directory waits for machine.lock another process keeps; SIGINT or
// SIGTERM ends it promptly with 130 and the interrupted line, long before
// its --wait, and nothing is migrated. The run is on a pool: no link can
// exist before the upgrade.
func TestMigrationLockWaitIsInterruptible(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	holdLockElsewhere(t, machine.LockPath(state))
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		c := exec.Command(incoda, "run", "--queue", "builds", "--wait", "30s", "--poll", "50ms", "--",
			stamp, filepath.Join(t.TempDir(), "s.txt"), "x", "1")
		c.Env = laneEnv(state)
		code, out := interruptOnLine(t, c, "incoda: waiting for machine.lock", sig)
		if code != 130 || !strings.Contains(out, "incoda: interrupted while waiting for machine.lock or the state upgrade") {
			t.Fatalf("%s: want exit 130 and the interrupted line, got %d:\n%s", sig, code, out)
		}
		if _, err := os.Stat(machine.RegistryPath(state)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: an interrupted run migrated: %v", sig, err)
		}
	}
}
```

In `config_test.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 2) Replace:

```go
	step(0, "  quiet machine: yes\n", "--quiet-machine")
	if pools, quiet := cfgOf(); strings.Join(pools, ",") != "tests" || !quiet {
		t.Fatalf("stored: %v %v", pools, quiet)
	}
	step(0, "  quiet machine: no\n", "--quiet-machine=false")
```

with:

```go
	if out := step(0, "  quiet machine: yes\n", "--quiet-machine"); !strings.Contains(out, "link: tests -> tests, quiet_machine\n") {
		t.Fatalf("a quiet_machine change alone is echoed:\n%s", out)
	}
	if pools, quiet := cfgOf(); strings.Join(pools, ",") != "tests" || !quiet {
		t.Fatalf("stored: %v %v", pools, quiet)
	}
	if out := step(0, "  quiet machine: no\n", "--quiet-machine=false"); !strings.Contains(out, "link: tests, quiet_machine -> tests\n") {
		t.Fatalf("a quiet_machine change alone is echoed:\n%s", out)
	}
	if out := step(0, "  quiet machine: no\n", "--quiet-machine=false"); strings.Contains(out, "link: ") {
		t.Fatalf("an unchanged quiet_machine echoes nothing:\n%s", out)
	}
```

(2 of 2) Replace:

```go
	for _, want := range []string{" by=config old= new=tests", " by=config old=tests new=builds", " by=config old=builds new=builds,tests",
		" by=config old=builds,tests new=tests", " by=config old=tests new=\n"} {
		if !strings.Contains(string(b)+"\n", want) {
			t.Fatalf("lane.log lacks %q:\n%s", want, b)
		}
	}
	if n := strings.Count(string(b), " event=link "); n != 5 {
		t.Fatalf("%d event=link lines, want one per change (5):\n%s", n, b)
	}
```

with:

```go
	for _, want := range []string{" by=config old= new=tests", " by=config old=tests new=builds", " by=config old=builds new=builds,tests",
		" by=config old=builds,tests new=tests", " by=config old=tests new=tests quiet_machine=true\n",
		" by=config old=tests new=tests quiet_machine=false\n", " by=config old=tests new=\n"} {
		if !strings.Contains(string(b)+"\n", want) {
			t.Fatalf("lane.log lacks %q:\n%s", want, b)
		}
	}
	if n := strings.Count(string(b), " event=link "); n != 7 {
		t.Fatalf("%d event=link lines, want one per change (7):\n%s", n, b)
	}
```

Append to the end of `internal/runplan/runplan_test.go`:

```go

// TestUnlinkedRefusalSeveralKeysWithAQuietSuggestion: with several unlinked
// keys, a suggestion that carries quiet_machine puts --quiet-machine on its
// config line and --wait '5m' on the run line, with the note (spec 4.1).
// POSIX quoting.
func TestUnlinkedRefusalSeveralKeysWithAQuietSuggestion(t *testing.T) {
	if fixline.Native() != fixline.POSIX {
		t.Skip("the expected lines are POSIX sh")
	}
	state, reg := machineDir(t, spec4Pools)
	req := Request{Named: []string{"kungfoo-measure", "cap-gate"}, Reason: "wintty gate",
		Fix: fixline.Run{Flags: []fixline.Flag{{Name: "reason", Value: "wintty gate"}}, Argv: []string{"just", "measure"}, Dir: "/src", Here: "/src"}}
	want := `incoda: unlinked: cap-gate, kungfoo-measure
incoda: queues "cap-gate", "kungfoo-measure" are not linked to any pool; every project queue names the machine-wide pools its jobs use.
` + spec4Rows + `incoda: suggested: cap-gate -> tests (name matches *-gate); kungfoo-measure -> tests, quiet_machine (name matches *-measure)
incoda: to link them to the suggestions (stored; every later run on these queues takes these pools), then run:
incoda:   incoda config cap-gate --pool tests
incoda:   incoda config kungfoo-measure --pool tests --quiet-machine
incoda:   incoda run --queue cap-gate,kungfoo-measure --reason 'wintty gate' --wait '5m' -- 'just' 'measure'
incoda: (--wait 5m added: quiet-machine holds every pool it has drained while it waits for the rest)
incoda: if a suggestion does not fit, ask the user; they run: incoda link cap-gate, incoda link kungfoo-measure
`
	_, err := Make(state, reg, req)
	var rf *machine.Refusal
	if !errors.As(err, &rf) {
		t.Fatalf("want a refusal, got %v", err)
	}
	if got := "incoda: " + rf.Msg + "\n"; got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run (bash), one at a time:

- `go test ./internal/machine/ -run 'TestWaitIdleEndsWhenItsContextDoes|TestNotIdleWaitEndsWhenItsContextDoes' -count=1 -timeout 120s`
- `go test . -run 'TestMigrationLockWaitIsInterruptible|TestConfigLinkFlags' -count=1 -timeout 120s`
- `go test ./internal/runplan/ -run 'TestUnlinkedRefusalSeveralKeysWithAQuietSuggestion' -count=1 -timeout 120s`

Expected: `TestWaitIdleEndsWhenItsContextDoes` fails with `want ErrInterrupted, got upgrade-timeout: ...` once its 10s budget is spent, and `TestNotIdleWaitEndsWhenItsContextDoes` with `want ErrInterrupted, got <nil>` after one 2s poll; `TestMigrationLockWaitIsInterruptible` fails with `want exit 130 and the interrupted line, got -1` (the run dies by the signal: no signal context is installed yet); `TestConfigLinkFlags` fails with `a quiet_machine change alone is echoed`. `TestUnlinkedRefusalSeveralKeysWithAQuietSuggestion` passes already: it pins plan 3a's behavior before Task 6 changes the lane-set check of printed lines.

- [ ] **Step 3: Implement**

In `internal/machine/options.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 2) Replace:

```go
	// Ctx, when set, lets an interrupt end a machine.lock wait
	// (ErrInterrupted); nil waits as before.
	Ctx context.Context
}
```

with:

```go
	// Ctx, when set, lets an interrupt end a machine.lock wait and the
	// state upgrade's waits for older runs (ErrInterrupted); nil waits as
	// before.
	Ctx context.Context
}
```

(2 of 2) Replace:

```go
func (o Options) stderr() io.Writer {
```

with:

```go
// sleep waits one poll. When Ctx ends first it returns ErrInterrupted at
// once, so every wait of the state upgrade ends on an interrupt at the
// points where it already ends when the --wait budget is spent.
func (o Options) sleep() error {
	if o.Ctx == nil {
		time.Sleep(o.poll())
		return nil
	}
	t := time.NewTimer(o.poll())
	defer t.Stop()
	select {
	case <-o.Ctx.Done():
		return ErrInterrupted
	case <-t.C:
		return nil
	}
}

func (o Options) stderr() io.Writer {
```

In `internal/machine/idle.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 2) Replace:

```go
// note. It returns nil once no ticket the phase covers is live.
```

with:

```go
// note. It returns nil once no ticket the phase covers is live, and
// ErrInterrupted when o.Ctx ends first.
```

(2 of 2) Replace:

```go
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return &Timeout{Msg: joinLines(upgradeTimeoutLines(bs, ph, o.Wait))}
		}
		time.Sleep(o.poll())
	}
}
```

with:

```go
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return &Timeout{Msg: joinLines(upgradeTimeoutLines(bs, ph, o.Wait))}
		}
		if err := o.sleep(); err != nil {
			return err
		}
	}
}
```

In `internal/machine/migrate.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 2) Replace:

```go
// found), then sleeps one poll, or gives up when the --wait budget is
// spent.
```

with:

```go
// found), then sleeps one poll, or gives up when the --wait budget is
// spent (ErrInterrupted when o.Ctx ends first).
```

(2 of 2) Replace:

```go
			"upgrade the older incoda on PATH; see incoda doctor",
		})}
	}
	time.Sleep(o.poll())
	return nil
}
```

with:

```go
			"upgrade the older incoda on PATH; see incoda doctor",
		})}
	}
	return o.sleep()
}
```

In `internal/machine/link.go`, make these 3 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 3) Replace:

```go
// or a refusal. When the pools change, event=link by=<by> old=<pools>
// new=<pools> goes to the lane's lane.log.
```

with:

```go
// or a refusal. When the pools or quiet_machine change, event=link
// by=<by> old=<pools> new=<pools> goes to the lane's lane.log, with
// quiet_machine=<new value> appended when quiet_machine moved.
```

(2 of 3) Replace:

```go
// registry lock and logs event=link when the pools change.
```

with:

```go
// registry lock and logs event=link when the pools or quiet_machine
// change.
```

(3 of 3) Replace:

```go
	res.New, res.Changed = cfg, true
	if !SameSet(res.Old.Pools, cfg.Pools) {
		// Pool names come from config.json as stored (or hand-edited), so
		// they go through LogValue; an empty side stays empty (old=).
		q.Logf("queue=%s event=link pid=%d by=%s old=%s new=%s", key, os.Getpid(), by,
			logSet(res.Old.Pools), logSet(cfg.Pools))
	}
	return res, nil
}
```

with:

```go
	res.New, res.Changed = cfg, true
	// quiet_machine is part of the link (spec 4.4): a change of it alone
	// is a link change too.
	quietMoved := res.Old.QuietMachine != cfg.QuietMachine
	if !SameSet(res.Old.Pools, cfg.Pools) || quietMoved {
		// Pool names come from config.json as stored (or hand-edited), so
		// they go through LogValue; an empty side stays empty (old=).
		line := fmt.Sprintf("queue=%s event=link pid=%d by=%s old=%s new=%s", key, os.Getpid(), by,
			logSet(res.Old.Pools), logSet(cfg.Pools))
		if quietMoved {
			line += fmt.Sprintf(" quiet_machine=%v", cfg.QuietMachine)
		}
		q.Logf("%s", line)
	}
	return res, nil
}
```

In `internal/cli/state.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 2) Replace:

```go
import (
	"errors"
	"fmt"
```

with:

```go
import (
	"context"
	"errors"
	"fmt"
```

(2 of 2) Replace:

```go
// It returns the registry the command runs under.
func mutatingState(start time.Time, wait, poll time.Duration, chain procinfo.Chain, stderr io.Writer) (string, *machine.Registry, error) {
	d, err := stateDir()
	if err != nil {
		return "", nil, err
	}
	v, _, _ := versionInfo()
	reg, err := machine.Ensure(d, machine.Options{
		Start: start, Wait: wait, Poll: poll, Chain: chain,
		By: "incoda " + v, Stderr: stderr, Path: startGetenv("PATH"),
	})
	if err != nil {
		return "", nil, machineExit(err)
	}
	return d, reg, nil
}
```

with:

```go
// It returns the registry the command runs under.
func mutatingState(start time.Time, wait, poll time.Duration, chain procinfo.Chain, stderr io.Writer) (string, *machine.Registry, error) {
	return mutatingStateCtx(context.Background(), start, wait, poll, chain, stderr)
}

// mutatingStateCtx is mutatingState for run, whose waits all end on an
// interrupt: when ctx ends during the wait for machine.lock or for older
// runs of the state upgrade, it returns exit 130 (spec 2.4: every queueing
// wait of a run). What the upgrade did before the interrupt stays, as
// after a timeout at the same point; the next mutating command resumes it.
func mutatingStateCtx(ctx context.Context, start time.Time, wait, poll time.Duration, chain procinfo.Chain, stderr io.Writer) (string, *machine.Registry, error) {
	d, err := stateDir()
	if err != nil {
		return "", nil, err
	}
	v, _, _ := versionInfo()
	reg, err := machine.Ensure(d, machine.Options{
		Start: start, Wait: wait, Poll: poll, Chain: chain,
		By: "incoda " + v, Stderr: stderr, Path: startGetenv("PATH"), Ctx: ctx,
	})
	if errors.Is(err, machine.ErrInterrupted) {
		return "", nil, exitWith(ExitInterrupt, "interrupted while waiting for machine.lock or the state upgrade")
	}
	if err != nil {
		return "", nil, machineExit(err)
	}
	return d, reg, nil
}
```

In `internal/cli/run.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 2) Replace:

```go
	chain := procinfo.ParentChain()
	dir, reg, err := mutatingState(start, wait.d, *poll, chain, stderr)
	if err != nil {
		return err
	}
```

with:

```go
	chain := procinfo.ParentChain()
	// An interrupt ends every wait of a run from here on: machine.lock and
	// the state upgrade (machine.Ensure), a first link, and every lane.
	ctx, stop := signal.NotifyContext(context.Background(), interruptSignals()...)
	defer stop()
	dir, reg, err := mutatingStateCtx(ctx, start, wait.d, *poll, chain, stderr)
	if err != nil {
		return err
	}
```

(2 of 2) Replace:

```go
	defer release()

	ctx, stop := signal.NotifyContext(context.Background(), interruptSignals()...)
	defer stop()

```

with:

```go
	defer release()

```

In `internal/cli/config.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 2) Replace:

```go
		switch {
		case !machine.SameSet(res.Old.Pools, res.New.Pools):
			fmt.Fprintf(stdout, "link: %s -> %s\n", textsafe.Escape(machine.SetText(res.Old.Pools)), textsafe.Escape(machine.SetText(res.New.Pools)))
```

with:

```go
		switch {
		case res.Old.QuietMachine != res.New.QuietMachine:
			// quiet_machine is part of the link (spec 4.4): a change of
			// it is echoed like a change of the pools.
			fmt.Fprintf(stdout, "link: %s -> %s\n", linkText(res.Old), linkText(res.New))
		case !machine.SameSet(res.Old.Pools, res.New.Pools):
			fmt.Fprintf(stdout, "link: %s -> %s\n", textsafe.Escape(machine.SetText(res.Old.Pools)), textsafe.Escape(machine.SetText(res.New.Pools)))
```

(2 of 2) Replace:

```go
func yesNo(b bool) string {
```

with:

```go
// linkText is a link as config echoes a quiet_machine change: the pools,
// escaped (they are read back from config.json), and ", quiet_machine"
// when it is set.
func linkText(c lane.Config) string {
	s := textsafe.Escape(machine.SetText(c.Pools))
	if c.QuietMachine {
		s += ", quiet_machine"
	}
	return s
}

func yesNo(b bool) string {
```

In `internal/cli/linkflags.go`, replace:

```go
// named is every pool the edit adds to a link; each must be registered.
```

with:

```go
// named is every pool the edit adds to a link; each must be registered.
// The --remove-pool names are left out on purpose: a name being removed
// need not be a registered pool, so a link that names something no longer
// registered (a hand edit, a rebuilt registry) can still be cleaned up.
```

- [ ] **Step 4: Run the tests to see them pass**

Run (bash), one at a time:

- `go test ./internal/machine/ -run 'TestWaitIdleEndsWhenItsContextDoes|TestNotIdleWaitEndsWhenItsContextDoes|TestWriteLink' -count=1`
- `go test . -run 'TestMigrationLockWaitIsInterruptible|TestFirstLinkWaitIsInterruptible|TestEnrollOnABusyRegistryIsInterruptible|TestConfigLinkFlags' -count=1`
- `go test ./internal/runplan/ -run 'TestUnlinkedRefusalSeveralKeysWithAQuietSuggestion' -count=1`

Expected: `ok` for each package.

- [ ] **Step 5: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 6: Commit**

```bash
git add busyregistry_unix_test.go config_test.go internal/cli/config.go internal/cli/linkflags.go internal/cli/run.go internal/cli/state.go internal/machine/idle.go internal/machine/interrupt_test.go internal/machine/link.go internal/machine/migrate.go internal/machine/options.go internal/runplan/runplan_test.go
git commit -F - <<'MSG'
fix: an interrupt ends a run's upgrade waits, and quiet_machine is a link change

A run installs its signal context before the state upgrade and passes it
through machine.Ensure, so Ctrl-C or SIGTERM during the wait for
machine.lock or for older runs ends it with 130, as every other
queueing wait of a run does. A change of quiet_machine alone is logged
as event=link and echoed by config like a change of the pools.
MSG
```


---

### Task 1: pools add and pools remove change a lane's kind

Spec 3.4. `incoda pools add NAME --slots N --description TEXT` creates a pool or converts the project lane NAME; `incoda pools remove NAME` turns a pool into a project lane with no link. Both run under machine.lock, then the lane's registry lock in one hold that checks the lane has no ticket and writes `machine.json` (generation bumped) before the lock is released (`lane.Queue.UpdateIdle`); Enroll takes the same lock, so every lane a run holds or waits on keeps its kind. Refusals: `kind-busy: "NAME" has live tickets`, `... links pools; unlink it first`, `... sets quiet_machine; clear it first: ...` (pools carry neither, spec 2.2), `pool-linked: "NAME" is linked from cap-gate, kungfoo-gate`, and exit 122 `machine-state:` for an unreadable config. A description passes the bad-text check (spec 4.6). Both log `event=kind` in the lane's log and in machine.log. A run planned before the change replans at its next verify point (Task 9 of plan 3a).

**Files:**
- Modify: `internal/cli/cli.go`
- Create: `internal/cli/pools.go`
- Modify: `internal/lane/config.go`
- Create: `internal/machine/kind.go`
- Test: `config_test.go`
- Test: `internal/machine/kind_test.go` (new)
- Test: `pools_test.go`

**Interfaces:**
- Consumes: `machine.AcquireLock`, `machine.UpdateRegistry`, `machine.Options`; `checkTexts` (plan 3a Task 3); `Queue.SetBudget` (plan 3a Task 1); `fixline.Line`, `fixline.Key`, `fixline.Lit` (plan 3a Task 2).
- Produces: `lane.ErrLaneBusy`; `func (q *Queue) UpdateIdle(fn func(*Config) error, after func(Config) error) (Config, error)`; package `machine`: `type PoolChange struct { Slots int; Description *string }`, `type KindResult struct { Registry *Registry; Config lane.Config; Converted bool }`, `func AddPool(stateDir, name string, ch PoolChange, o Options) (KindResult, error)`, `func RemovePool(stateDir, name string, o Options) (KindResult, error)`, `func LinkedFrom(stateDir, pool string) []string`, `configCmd(key string, flags ...string) string` and `configCmdFor(sh fixline.Shell, key string, flags ...string) string` (the printed config commands of the refusals). In `cli`: `cmdPools` (subcommands `add`, `remove`), `poolName`.

- [ ] **Step 1: Write the failing tests**

Create `internal/machine/kind_test.go`:

```go
package machine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/fixline"
	"github.com/deblasis/incoda/internal/lane"
)

func kindOpts() Options {
	return Options{Start: time.Now(), Wait: 10 * time.Second, Poll: 20 * time.Millisecond}
}

func writeConfig(t *testing.T, state, key, body string) {
	t.Helper()
	if err := os.MkdirAll(lane.LaneDir(state, key), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lane.LaneDir(state, key), "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func refusedWith(t *testing.T, err error, want string) {
	t.Helper()
	var rf *Refusal
	if !errors.As(err, &rf) || rf.Msg != want {
		t.Fatalf("want refusal %q, got %v", want, err)
	}
}

// TestAddPool: a new pool, a converted project lane keeping its fields,
// each with a new generation; the kind-busy, already-a-pool and
// machine-state refusals (spec 3.4).
func TestAddPool(t *testing.T) {
	state, reg := migrated(t)
	gen := reg.Generation
	desc := "the GPU"
	res, err := AddPool(state, "gpu", PoolChange{Slots: 2, Description: &desc}, kindOpts())
	if err != nil || res.Converted || !res.Registry.IsPool("gpu") || res.Registry.Generation != gen+1 || res.Config.Slots != 2 || res.Config.Description != "the GPU" {
		t.Fatalf("new pool: %+v %v", res, err)
	}
	writeConfig(t, state, "printer", `{"schema":2,"slots":3,"description":"office","require_reason":true}`)
	res, err = AddPool(state, "printer", PoolChange{}, kindOpts())
	if err != nil || !res.Converted || res.Config.Slots != 3 || res.Config.Description != "office" || !res.Config.RequireReason {
		t.Fatalf("conversion keeps the lane's fields: %+v %v", res, err)
	}
	refusedWith(t, func() error { _, err := AddPool(state, "gpu", PoolChange{}, kindOpts()); return err }(),
		`pools add: "gpu" is already a pool; change its slots or description with `+configCmd("gpu"))
	writeConfig(t, state, "cap-gate", `{"schema":2,"pools":["tests"]}`)
	refusedWith(t, func() error { _, err := AddPool(state, "cap-gate", PoolChange{}, kindOpts()); return err }(),
		`kind-busy: "cap-gate" links pools; unlink it first`)
	writeConfig(t, state, "kf-measure", `{"schema":2,"quiet_machine":true}`)
	refusedWith(t, func() error { _, err := AddPool(state, "kf-measure", PoolChange{}, kindOpts()); return err }(),
		`kind-busy: "kf-measure" sets quiet_machine; clear it first: `+configCmd("kf-measure", "--quiet-machine=false"))
	q, err := lane.Open(state, "busy")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	en, err := q.Enroll(lane.Ticket{})
	if err != nil {
		t.Fatal(err)
	}
	refusedWith(t, func() error { _, err := AddPool(state, "busy", PoolChange{}, kindOpts()); return err }(),
		`kind-busy: "busy" has live tickets`)
	en.Release(0)
	writeConfig(t, state, "broken", `{`)
	var se *StateError
	if _, err := AddPool(state, "broken", PoolChange{}, kindOpts()); !errors.As(err, &se) || !strings.HasPrefix(se.Msg, `machine-state: queue "broken" has an unreadable config.json: `) {
		t.Fatalf("an unreadable config fails closed: %v", err)
	}
	r, _ := ReadRegistry(state)
	if strings.Join(r.Pools, ",") != "builds,computer-use,gpu,printer,tests,vm" || r.Generation != gen+2 {
		t.Fatalf("only the two successful adds changed machine.json: %+v", r)
	}
	b, _ := os.ReadFile(MachineLogPath(state))
	if !strings.Contains(string(b), "event=kind pid=") || !strings.Contains(string(b), " key=printer kind=pool by=pools-add ") {
		t.Fatalf("machine.log:\n%s", b)
	}
}

// TestConfigCmd: the config commands a kind-busy or already-a-pool refusal
// prints are fixline lines, POSIX on Unix and PowerShell on Windows.
func TestConfigCmd(t *testing.T) {
	if got := configCmdFor(fixline.POSIX, "kf-measure", "--quiet-machine=false"); got != "incoda config kf-measure --quiet-machine=false" {
		t.Fatalf("POSIX: %q", got)
	}
	if got := configCmdFor(fixline.PowerShell, "gpu"); got != "incoda config 'gpu'" {
		t.Fatalf("PowerShell: %q", got)
	}
}

// TestRemovePool: refused while a lane links the pool or it has a ticket;
// otherwise the pool becomes a project lane with a new generation.
func TestRemovePool(t *testing.T) {
	state, reg := migrated(t)
	writeConfig(t, state, "kungfoo-gate", `{"schema":2,"pools":["tests"]}`)
	writeConfig(t, state, "cap-gate", `{"schema":2,"pools":["builds","tests"]}`)
	refusedWith(t, func() error { _, err := RemovePool(state, "tests", kindOpts()); return err }(),
		`pool-linked: "tests" is linked from cap-gate, kungfoo-gate`)
	refusedWith(t, func() error { _, err := RemovePool(state, "nope", kindOpts()); return err }(),
		`pools remove: "nope" is not a pool`)
	q, err := lane.Open(state, "vm")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	en, err := q.Enroll(lane.Ticket{})
	if err != nil {
		t.Fatal(err)
	}
	refusedWith(t, func() error { _, err := RemovePool(state, "vm", kindOpts()); return err }(),
		`kind-busy: "vm" has live tickets`)
	en.Release(0)
	res, err := RemovePool(state, "vm", kindOpts())
	if err != nil || res.Registry.IsPool("vm") || res.Registry.Generation != reg.Generation+1 {
		t.Fatalf("remove: %+v %v", res, err)
	}
	if !lane.Exists(state, "vm") {
		t.Fatal("the lane stays, as a project lane")
	}
}

// TestUpdateIdleHoldsTheRegistryLockAcrossTheWrite: the callback that
// writes machine.json runs inside the same hold as the ticket check, so an
// Enroll cannot slip in between.
func TestUpdateIdleHoldsTheRegistryLockAcrossTheWrite(t *testing.T) {
	state, _ := migrated(t)
	q, err := lane.Open(state, "x")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	other, err := lane.Open(state, "x")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.SetBudget(time.Now(), 0)
	_, err = q.UpdateIdle(nil, func(lane.Config) error {
		if _, err := other.Enroll(lane.Ticket{}); !errors.Is(err, lane.ErrRegistryBusy) {
			t.Fatalf("an Enroll during the hold must wait for it: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
```

Append to the end of `pools_test.go`:

```go

// TestPoolsAddAndRemove: a human adds a pool, a project links it and its
// runs take it; removing it is refused while it is linked or held; once
// it is free and unlinked it becomes a project lane, which refuses runs
// until linked (spec 3.4).
func TestPoolsAddAndRemove(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	out := mustRun(t, incoda, state, 0, "pools", "add", "gpu", "--slots", "2", "--description", "the GPU")
	if !strings.Contains(out, `pools add: "gpu" is a pool now (2 slot(s); a new lane); machine.json generation 2`) {
		t.Fatalf("pools add:\n%s", out)
	}
	mustRun(t, incoda, state, 0, "config", "ml-train", "--pool", "gpu")
	out = mustRun(t, incoda, state, 0, "run", "--queue", "ml-train", "--", stamp, filepath.Join(t.TempDir(), "a.txt"), "a", "1")
	inOrder(t, out, `acquired queue "ml-train" (pid `, `acquired queue "gpu" (pool, via ml-train; pid `)
	out = mustRun(t, incoda, state, 120, "pools", "remove", "gpu")
	if !strings.Contains(out, `incoda: pool-linked: "gpu" is linked from ml-train`) {
		t.Fatalf("pool-linked:\n%s", out)
	}
	mustRun(t, incoda, state, 0, "config", "ml-train", "--unlink")
	h, _ := startHolder(t, incoda, stamp, state, "gpu", "h", 1500, "50ms")
	defer func() { _ = h.Process.Kill(); _ = h.Wait() }()
	waitFor(t, incoda, state, "gpu", func(q queueReport) bool { return len(q.Holders) == 1 })
	out = mustRun(t, incoda, state, 120, "pools", "remove", "gpu")
	if !strings.Contains(out, `incoda: kind-busy: "gpu" has live tickets`) {
		t.Fatalf("kind-busy:\n%s", out)
	}
	if err := h.Wait(); err != nil {
		t.Fatal(err)
	}
	out = mustRun(t, incoda, state, 0, "pools", "remove", "gpu")
	if !strings.Contains(out, `pools remove: "gpu" is a project lane now, with no link`) {
		t.Fatalf("pools remove:\n%s", out)
	}
	out = mustRun(t, incoda, state, 120, "run", "--queue", "gpu", "--", stamp, filepath.Join(t.TempDir(), "b.txt"), "b", "1")
	if !strings.HasPrefix(out, "incoda: unlinked: gpu\n") {
		t.Fatalf("a removed pool is an unlinked project lane:\n%s", out)
	}
	mustRun(t, incoda, state, 0, "config", "cap-gate", "--pool", "tests")
	out = mustRun(t, incoda, state, 120, "pools", "add", "cap-gate")
	if !strings.Contains(out, `incoda: kind-busy: "cap-gate" links pools; unlink it first`) {
		t.Fatalf("kind-busy links:\n%s", out)
	}
	log, _ := os.ReadFile(filepath.Join(state, "machine.log"))
	if strings.Count(string(log), "event=kind ") != 2 {
		t.Fatalf("machine.log records each kind change:\n%s", log)
	}
}
```

In `config_test.go`, replace:

```go
		{[]string{"config", "badtext", "--close", "two\nlines"}, "incoda: bad-text: closed contains control characters"},
		{[]string{"config", "badtext", "--close", "tab\there"}, "incoda: bad-text: closed contains control characters"},
		{[]string{"config", "badtext", "--description", strings.Repeat("x", 201)}, "incoda: bad-text: description is longer than 200 characters"},
	} {
		out, code := runIncoda(t, incoda, state, c.args...)
		if code != 120 || !strings.Contains(out, c.want) {
```

with:

```go
		{[]string{"config", "badtext", "--close", "two\nlines"}, "incoda: bad-text: closed contains control characters"},
		{[]string{"config", "badtext", "--close", "tab\there"}, "incoda: bad-text: closed contains control characters"},
		{[]string{"config", "badtext", "--description", strings.Repeat("x", 201)}, "incoda: bad-text: description is longer than 200 characters"},
		{[]string{"pools", "add", "badtext", "--description", "bell\a"}, "incoda: bad-text: description contains control characters"},
	} {
		out, code := runIncoda(t, incoda, state, c.args...)
		if code != 120 || !strings.Contains(out, c.want) {
```

- [ ] **Step 2: Run the tests to see them fail**

Run (bash), one at a time:

- `go test ./internal/machine/ -run 'TestAddPool|TestRemovePool|TestUpdateIdleHoldsTheRegistryLockAcrossTheWrite|TestConfigCmd' -count=1 -timeout 120s`
- `go test . -run 'TestPoolsAddAndRemove|TestConfigRefusesControlCharacters' -count=1 -timeout 120s`

Expected: the `internal/machine` test build fails (`undefined: AddPool`, `undefined: PoolChange`, `undefined: configCmd`); the root tests fail with `incoda: unknown command "pools"`.

- [ ] **Step 3: Implement**

In `internal/cli/cli.go`, replace:

```go
		err = cmdQueues(rest, stdout, stderr)
	case "config":
		err = cmdConfig(rest, stdout, stderr)
	case "kill":
		err = cmdKill(rest, stdout, stderr)
	case "force-release":
```

with:

```go
		err = cmdQueues(rest, stdout, stderr)
	case "config":
		err = cmdConfig(rest, stdout, stderr)
	case "pools":
		err = cmdPools(rest, stdout, stderr)
	case "kill":
		err = cmdKill(rest, stdout, stderr)
	case "force-release":
```

Create `internal/cli/pools.go`:

```go
package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/procinfo"
)

// cmdPools is incoda pools: add and remove change a lane's kind (spec 3.4),
// a human act.
func cmdPools(args []string, stdout, stderr io.Writer) error {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "add":
		return cmdPoolsAdd(args, stdout, stderr)
	case "remove":
		return cmdPoolsRemove(args, stdout, stderr)
	}
	return usagef("usage: incoda pools add NAME [--slots N] [--description TEXT] [--wait DUR] | incoda pools remove NAME [--wait DUR]")
}

// poolName takes the positional pool name in front of the flags.
func poolName(cmd string, args []string) (string, []string, error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", nil, usagef("incoda pools %s needs a pool name", cmd)
	}
	if err := lane.ValidateKey(args[0]); err != nil {
		return "", nil, usagef("invalid pool name: %v", err)
	}
	return args[0], args[1:], nil
}

func cmdPoolsAdd(args []string, stdout, stderr io.Writer) error {
	start := time.Now()
	name, args, err := poolName("add", args)
	if err != nil {
		return err
	}
	fs := newFlagSet("pools add", stderr)
	slots := fs.Int("slots", 0, "the pool's machine-wide cap; 0 keeps a converted lane's count, else 1")
	desc := fs.String("description", "", "one line saying what the pool guards, shown in status and in refusals")
	wait := &waitValue{d: time.Minute}
	fs.Var(wait, "wait", "how long to wait for machine.lock and a state upgrade: a Go duration (1m) or bare seconds; negative waits forever")
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for pools add"}
	}
	if fs.NArg() > 0 {
		return usagef("pools add takes one name; unexpected %q", fs.Arg(0))
	}
	if *slots < 0 {
		return usagef("--slots must be at least 1, got %d", *slots)
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if err := checkTexts(given, map[string]string{"description": *desc}); err != nil {
		return err
	}
	chain := procinfo.ParentChain()
	dir, _, err := mutatingState(start, wait.d, 200*time.Millisecond, chain, stderr)
	if err != nil {
		return err
	}
	ch := machine.PoolChange{Slots: *slots}
	if given["description"] {
		ch.Description = desc
	}
	res, err := machine.AddPool(dir, name, ch, machine.Options{Start: start, Wait: wait.d, Poll: 200 * time.Millisecond, Chain: chain, Stderr: stderr})
	if err != nil {
		return machineExit(err)
	}
	was := "a new lane"
	if res.Converted {
		was = "a project lane until now"
	}
	fmt.Fprintf(stdout, "pools add: %q is a pool now (%d slot(s); %s); machine.json generation %d\n", name, res.Config.Slots, was, res.Registry.Generation)
	return nil
}

func cmdPoolsRemove(args []string, stdout, stderr io.Writer) error {
	start := time.Now()
	name, args, err := poolName("remove", args)
	if err != nil {
		return err
	}
	fs := newFlagSet("pools remove", stderr)
	wait := &waitValue{d: time.Minute}
	fs.Var(wait, "wait", "how long to wait for machine.lock and a state upgrade: a Go duration (1m) or bare seconds; negative waits forever")
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for pools remove"}
	}
	if fs.NArg() > 0 {
		return usagef("pools remove takes one name; unexpected %q", fs.Arg(0))
	}
	chain := procinfo.ParentChain()
	dir, _, err := mutatingState(start, wait.d, 200*time.Millisecond, chain, stderr)
	if err != nil {
		return err
	}
	res, err := machine.RemovePool(dir, name, machine.Options{Start: start, Wait: wait.d, Poll: 200 * time.Millisecond, Chain: chain, Stderr: stderr})
	if err != nil {
		return machineExit(err)
	}
	fmt.Fprintf(stdout, "pools remove: %q is a project lane now, with no link, so runs on it are refused until it is linked; machine.json generation %d\n", name, res.Registry.Generation)
	return nil
}
```

In `internal/lane/config.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 2) Replace:

```go
	"fmt"
	"os"
	"path/filepath"

	"github.com/deblasis/incoda/internal/atomicfile"
	"github.com/deblasis/incoda/internal/textsafe"
```

with:

```go
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/deblasis/incoda/internal/atomicfile"
	"github.com/deblasis/incoda/internal/textsafe"
```

(2 of 2) Replace:

```go
	return out, err
}

// SaveConfig replaces the known fields with c and keeps unknown ones.
func (q *Queue) SaveConfig(c Config) error {
	_, err := q.UpdateConfig(func(cur *Config) error {
```

with:

```go
	return out, err
}

// ErrLaneBusy is UpdateIdle's refusal: the lane has a live ticket.
var ErrLaneBusy = errors.New("the lane has live tickets")

// UpdateIdle is the hold of a kind change (spec 3.4): inside one hold of
// the registry lock it refuses with ErrLaneBusy while the lane has a live
// ticket (dead ones are reaped, as every scan does), applies fn to the
// config and stores it (fn nil, or returning ErrNoChange, stores nothing),
// and then runs after, which writes machine.json, before the lock is
// released. Enroll takes the same lock, so no ticket can appear between
// the check and the registry write, and every lane a run holds or waits on
// keeps its kind while that run's ticket exists.
func (q *Queue) UpdateIdle(fn func(*Config) error, after func(Config) error) (Config, error) {
	var out Config
	err := q.withRegistry(func() error {
		live, _, err := q.scanLocked(time.Now())
		if err != nil {
			return err
		}
		if len(live) > 0 {
			return ErrLaneBusy
		}
		if fn != nil {
			c, err := q.LoadConfig()
			if err != nil {
				return err
			}
			switch err := fn(&c); {
			case errors.Is(err, ErrNoChange):
			case err != nil:
				return err
			default:
				c.Schema = ConfigSchema
				b, err := json.MarshalIndent(c, "", "  ")
				if err != nil {
					return err
				}
				if err := atomicfile.Write(filepath.Join(q.Dir, configName), append(b, '\n'), 0o644); err != nil {
					return err
				}
			}
			out = c
		}
		return after(out)
	})
	return out, err
}

// SaveConfig replaces the known fields with c and keeps unknown ones.
func (q *Queue) SaveConfig(c Config) error {
	_, err := q.UpdateConfig(func(cur *Config) error {
```

Create `internal/machine/kind.go`:

```go
package machine

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/deblasis/incoda/internal/fixline"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/textsafe"
)

// PoolChange is what AddPool asks for. Slots 0 keeps the lane's count (1
// for a lane without one); Description nil keeps the lane's description.
type PoolChange struct {
	Slots       int
	Description *string
}

// KindResult is what a kind change left.
type KindResult struct {
	Registry *Registry
	Config   lane.Config
	// Converted is set when AddPool turned an existing project lane into a
	// pool, rather than creating one.
	Converted bool
}

// AddPool is incoda pools add (spec 3.4): it creates the pool name, or
// converts the project lane name into one. Under machine.lock, then the
// lane's registry lock in one hold (lane.UpdateIdle): the lane must have
// no live ticket and no link or quiet_machine of its own (kind-busy, exit
// 120) and a readable config (machine-state, exit 122); its config gets
// the slots and description asked for, and machine.json gains the pool
// with a new generation before the lock is released.
func AddPool(stateDir, name string, ch PoolChange, o Options) (KindResult, error) {
	lk, err := AcquireLock(stateDir, o.lockOptions("pools"))
	if err != nil {
		return KindResult{}, err
	}
	defer lk.Release()
	reg, err := ReadRegistry(stateDir)
	if err != nil {
		return KindResult{}, err
	}
	if reg.IsPool(name) {
		return KindResult{}, &Refusal{Msg: fmt.Sprintf("pools add: %q is already a pool; change its slots or description with %s", name, configCmd(name))}
	}
	res := KindResult{Converted: lane.Exists(stateDir, name)}
	q, err := lane.Open(stateDir, name)
	if err != nil {
		return KindResult{}, stateErrorf("cannot open queue %q: %s", name, esc(err))
	}
	defer q.Close()
	q.SetBudget(o.Start, o.Wait)
	res.Config, err = q.UpdateIdle(func(c *lane.Config) error {
		switch {
		case len(c.Pools) > 0:
			return &Refusal{Msg: fmt.Sprintf("kind-busy: %q links pools; unlink it first", name)}
		case c.QuietMachine:
			return &Refusal{Msg: fmt.Sprintf("kind-busy: %q sets quiet_machine; clear it first: %s", name, configCmd(name, "--quiet-machine=false"))}
		}
		if ch.Slots > 0 {
			c.Slots = ch.Slots
		} else if c.Slots < 1 {
			c.Slots = 1
		}
		if ch.Description != nil {
			c.Description = *ch.Description
		}
		return nil
	}, func(lane.Config) error {
		r, err := UpdateRegistry(stateDir, lk, func(r *Registry) error {
			r.Pools = append(r.Pools, name)
			return nil
		})
		res.Registry = r
		return err
	})
	if err != nil {
		return KindResult{}, kindError(name, err)
	}
	logKind(stateDir, name, "pool", "pools-add", res.Registry)
	return res, nil
}

// RemovePool is incoda pools remove (spec 3.4): the pool becomes a project
// lane with no link, so runs on it are then refused until it is linked.
// It is refused while any lane links it (pool-linked) or while it has a
// ticket (kind-busy). Links are written under machine.lock too, so "no
// lane links it" cannot race a new link.
func RemovePool(stateDir, name string, o Options) (KindResult, error) {
	lk, err := AcquireLock(stateDir, o.lockOptions("pools"))
	if err != nil {
		return KindResult{}, err
	}
	defer lk.Release()
	reg, err := ReadRegistry(stateDir)
	if err != nil {
		return KindResult{}, err
	}
	if !reg.IsPool(name) {
		return KindResult{}, &Refusal{Msg: fmt.Sprintf("pools remove: %q is not a pool", name)}
	}
	if from := LinkedFrom(stateDir, name); len(from) > 0 {
		return KindResult{}, &Refusal{Msg: fmt.Sprintf("pool-linked: %q is linked from %s", name, strings.Join(from, ", "))}
	}
	drop := func() error {
		r, err := UpdateRegistry(stateDir, lk, func(r *Registry) error {
			var keep []string
			for _, p := range r.Pools {
				if p != name {
					keep = append(keep, p)
				}
			}
			r.Pools = keep
			return nil
		})
		reg = r
		return err
	}
	if !lane.Exists(stateDir, name) {
		// A pool registered with no lane yet (a rebuild named it): there
		// is no ticket to wait for.
		if err := drop(); err != nil {
			return KindResult{}, err
		}
		logKind(stateDir, name, "project", "pools-remove", reg)
		return KindResult{Registry: reg}, nil
	}
	q, err := lane.OpenIn(lane.LanesDir(stateDir), name, lane.Existing)
	if err != nil {
		return KindResult{}, stateErrorf("cannot open queue %q: %s", name, esc(err))
	}
	defer q.Close()
	q.SetBudget(o.Start, o.Wait)
	if _, err := q.UpdateIdle(nil, func(lane.Config) error { return drop() }); err != nil {
		return KindResult{}, kindError(name, err)
	}
	logKind(stateDir, name, "project", "pools-remove", reg)
	return KindResult{Registry: reg}, nil
}

// LinkedFrom lists the lanes whose config links pool, sorted. A config that
// cannot be read links nothing.
func LinkedFrom(stateDir, pool string) []string {
	keys, _ := lane.ListQueues(stateDir)
	var out []string
	for _, k := range keys {
		cfg, err := lane.ReadConfig(lane.LaneDir(stateDir, k))
		if err != nil {
			continue
		}
		for _, p := range cfg.Pools {
			if p == pool {
				out = append(out, k)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// kindError maps what a kind change's hold returned to its refusal.
func kindError(name string, err error) error {
	var rf *Refusal
	var se *StateError
	switch {
	case errors.As(err, &rf), errors.As(err, &se):
		return err
	case errors.Is(err, lane.ErrLaneBusy):
		return &Refusal{Msg: fmt.Sprintf("kind-busy: %q has live tickets", name)}
	case errors.Is(err, lane.ErrRegistryBusy):
		return &Timeout{Msg: fmt.Sprintf("kind-busy: %q: %s", name, esc(err))}
	}
	return stateErrorf("queue %q has an unreadable config.json: %s; fix or delete it first", name, esc(err))
}

// logKind records a kind change in the lane's lane.log and in machine.log.
func logKind(stateDir, name, kind, by string, reg *Registry) {
	lane.AppendLog(lane.LaneDir(stateDir, name), "queue=%s event=kind pid=%d kind=%s by=%s generation=%d", name, os.Getpid(), kind, by, reg.Generation)
	appendMachineLog(stateDir, "event=kind pid=%d key=%s kind=%s by=%s generation=%d", os.Getpid(), textsafe.LogValue(name), kind, by, reg.Generation)
}

// configCmd is "incoda config KEY [FLAG...]" as this platform's shell
// takes it, built by internal/fixline like every printed command. key is
// a validated key and flags are literal words, so a line always renders.
func configCmd(key string, flags ...string) string {
	return configCmdFor(fixline.Native(), key, flags...)
}

// configCmdFor is configCmd with the shell fixed, so both forms can be
// tested on any host.
func configCmdFor(sh fixline.Shell, key string, flags ...string) string {
	w := []fixline.Word{fixline.Lit("incoda"), fixline.Lit("config"), fixline.Key(key)}
	for _, f := range flags {
		w = append(w, fixline.Lit(f))
	}
	return fixline.Line{Words: w}.Render(sh).Text
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run (bash), one at a time:

- `go test ./internal/machine/ -run 'TestAddPool|TestRemovePool|TestUpdateIdleHoldsTheRegistryLockAcrossTheWrite|TestConfigCmd' -count=1`
- `go test . -run 'TestPoolsAddAndRemove|TestConfigRefusesControlCharacters' -count=1`

Expected: `ok` for each package.

- [ ] **Step 5: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 6: Commit**

```bash
git add config_test.go internal/cli/cli.go internal/cli/pools.go internal/lane/config.go internal/machine/kind.go internal/machine/kind_test.go pools_test.go
git commit -F - <<'MSG'
feat: pools add and remove change a lane's kind under machine.lock

pools add creates a pool or converts a project lane, pools remove makes
a pool a project lane with no link. Each takes machine.lock, then the
lane's registry lock in one hold that refuses a lane with tickets and
writes machine.json with a new generation. A linked or quiet project
lane is kind-busy, a linked pool is pool-linked.
MSG
```


---

### Task 2: incoda pools lists the pools

Spec 4.3: `incoda pools` lists every pool with its slots, held and waiting counts, the lanes linked to it and its description; `incoda pools --json` gives the same as JSON with the field names `status --json` will use for its pools (spec 5.2: `key`, `slots`, `holders`, `waiting`, `linked`). It only reads, like status: no lock waited for past one view deadline, no migration (an unmigrated directory says the pools are not registered yet). The JSON builder lives in `internal/report` so plan 5 can reuse it for `status --json`.

**Files:**
- Modify: `internal/cli/pools.go`
- Create: `internal/report/pools.go`
- Test: `pools_test.go`

**Interfaces:**
- Consumes: `machine.Inspect`, `machine.ReadRegistry`, `machine.LinkedFrom` (Task 1), `lane.Queue.ObserveBy`.
- Produces: package `report`: `type Pools struct { Schema int; Migrated bool; Generation int64; Pools []Pool; Banner string }`, `type Pool struct { Key string; Slots int; Description string; Holders, Waiting []lane.Entry; Linked []string; ProbeError string }`, `func BuildPools(stateDir string) (*Pools, error)`; `cli.cmdPoolsList`.

- [ ] **Step 1: Write the failing tests**

In `pools_test.go`, replace:

```go
package main

import (
	"fmt"
	"os"
	"os/exec"
```

with:

```go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
```

Then append to the end of `pools_test.go`:

```go

// TestPoolsList: incoda pools lists every pool with its slots, held and
// waiting counts, linked lanes and description; --json gives the same with
// the status --json field names. It never migrates.
func TestPoolsList(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	out := mustRun(t, incoda, state, 0, "pools")
	if !strings.Contains(out, "no pools registered yet: the next mutating incoda command registers builds, computer-use, tests, vm") {
		t.Fatalf("an unmigrated directory has no pools yet:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(state, "machine.json")); !os.IsNotExist(err) {
		t.Fatal("pools must not migrate")
	}
	mustRun(t, incoda, state, 0, "config", "tests", "--description", "test suites and gates")
	mustRun(t, incoda, state, 0, "config", "kf-gate", "--pool", "tests")
	mustRun(t, incoda, state, 0, "config", "cap-gate", "--pool", "tests")
	h := exec.Command(incoda, "run", "--queue", "cap-gate", "--poll", "50ms", "--quiet", "--", stamp, filepath.Join(t.TempDir(), "h.txt"), "h", "3000")
	h.Env = laneEnv(state)
	if err := h.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Process.Kill(); _ = h.Wait() }()
	waitFor(t, incoda, state, "tests", func(q queueReport) bool { return len(q.Holders) == 1 })
	out = mustRun(t, incoda, state, 0, "pools", "--no-color")
	want := `POOL          SLOTS  HELD  WAITING  LINKED             DESCRIPTION
builds        1      0     0        -
computer-use  1      0     0        -
tests         1      1     0        cap-gate, kf-gate  test suites and gates
vm            1      0     0        -
`
	if out != want {
		t.Fatalf("pools:\n%s\nwant:\n%s", out, want)
	}
	cmd := exec.Command(incoda, "pools", "--json")
	cmd.Env = laneEnv(state)
	b, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Schema int `json:"schema"`
		Pools  []struct {
			Key     string   `json:"key"`
			Slots   int      `json:"slots"`
			Linked  []string `json:"linked"`
			Holders []entry  `json:"holders"`
			Waiting []entry  `json:"waiting"`
		} `json:"pools"`
	}
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	tests := rep.Pools[2]
	if rep.Schema != 1 || len(rep.Pools) != 4 || tests.Key != "tests" || strings.Join(tests.Linked, ",") != "cap-gate,kf-gate" ||
		len(tests.Holders) != 1 || strings.Join(tests.Holders[0].Ticket.Via, ",") != "cap-gate" || tests.Waiting == nil {
		t.Fatalf("pools --json:\n%s", b)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run (bash): `go test . -run 'TestPoolsList' -count=1 -timeout 120s`

Expected: `TestPoolsList` fails with `incoda pools: exit 120, want 0` and the `pools add|remove` usage line.

- [ ] **Step 3: Implement**

In `internal/cli/pools.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

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
	"encoding/json"
	"flag"
	"fmt"
	"io"
```

(2 of 2) Replace:

```go
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/procinfo"
)

// cmdPools is incoda pools: add and remove change a lane's kind (spec 3.4),
// a human act.
func cmdPools(args []string, stdout, stderr io.Writer) error {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "add":
		return cmdPoolsAdd(args, stdout, stderr)
	case "remove":
		return cmdPoolsRemove(args, stdout, stderr)
	}
	return usagef("usage: incoda pools add NAME [--slots N] [--description TEXT] [--wait DUR] | incoda pools remove NAME [--wait DUR]")
}

// poolName takes the positional pool name in front of the flags.
```

with:

```go
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/report"
	"github.com/deblasis/incoda/internal/textsafe"
)

// cmdPools is incoda pools: with no subcommand it lists the pools (spec
// 4.3); add and remove change a lane's kind (spec 3.4), a human act.
func cmdPools(args []string, stdout, stderr io.Writer) error {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "":
		return cmdPoolsList(args, stdout, stderr)
	case "add":
		return cmdPoolsAdd(args, stdout, stderr)
	case "remove":
		return cmdPoolsRemove(args, stdout, stderr)
	}
	return usagef("usage: incoda pools [--json] | incoda pools add NAME [--slots N] [--description TEXT] [--wait DUR] | incoda pools remove NAME [--wait DUR]")
}

// cmdPoolsList lists every pool: key, slots, held, waiting, the lanes
// linked to it and its description. It only reads, like status.
func cmdPoolsList(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("pools", stderr)
	asJSON := fs.Bool("json", false, "emit the pools as JSON, with the field names of status --json")
	noColor := fs.Bool("no-color", false, "never emit ANSI color, even on a terminal (the NO_COLOR environment variable does the same)")
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for pools"}
	}
	if fs.NArg() > 0 {
		return usagef("unknown pools subcommand %q; try add or remove", fs.Arg(0))
	}
	dir, err := lane.StateDir()
	if err != nil {
		return exitWith(ExitState, "cannot resolve state directory: %v", err)
	}
	rep, err := report.BuildPools(dir)
	if err != nil {
		return machineExit(err)
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	printBanner(stderr, rep.Banner)
	p := paletteFor(stdout, *noColor)
	if !rep.Migrated {
		fmt.Fprintf(stdout, "%s\n", p.Dim("no pools registered yet: the next mutating incoda command registers "+strings.Join(machine.BootstrapPools(), ", ")))
		return nil
	}
	rows := [][]string{{"POOL", "SLOTS", "HELD", "WAITING", "LINKED", "DESCRIPTION"}}
	for _, pr := range rep.Pools {
		linked := "-"
		if len(pr.Linked) > 0 {
			linked = strings.Join(pr.Linked, ", ")
		}
		held := fmt.Sprint(len(pr.Holders))
		if pr.ProbeError != "" {
			held = "?"
		}
		rows = append(rows, []string{pr.Key, fmt.Sprint(pr.Slots), held, fmt.Sprint(len(pr.Waiting)), linked, textsafe.Escape(pr.Description)})
	}
	width := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			width[i] = max(width[i], len(c))
		}
	}
	for n, r := range rows {
		var b strings.Builder
		for i, c := range r {
			if i == len(r)-1 {
				b.WriteString(c)
				continue
			}
			fmt.Fprintf(&b, "%-*s  ", width[i], c)
		}
		line := strings.TrimRight(b.String(), " ")
		if n == 0 {
			line = p.Dim(line)
		}
		fmt.Fprintln(stdout, line)
	}
	return nil
}

// poolName takes the positional pool name in front of the flags.
```

Create `internal/report/pools.go`:

```go
package report

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
)

// Pools is the shape `incoda pools --json` emits: the pools of machine.json
// with the field names status --json uses for its pools (spec 5.2).
// Fields are only ever added.
type Pools struct {
	Schema int `json:"schema"`
	// Migrated is false on a layout not upgraded yet, which has no pools.
	Migrated   bool   `json:"migrated"`
	Generation int64  `json:"generation"`
	Pools      []Pool `json:"pools"`
	// Banner is the read-only banner of a layout not upgraded yet; display
	// text, not part of the JSON.
	Banner string `json:"-"`
}

// Pool is one pool: its key, its width, who holds and waits on it, the
// lanes linked to it, and its description.
type Pool struct {
	Key         string       `json:"key"`
	Slots       int          `json:"slots"`
	Description string       `json:"description,omitempty"`
	Holders     []lane.Entry `json:"holders"`
	Waiting     []lane.Entry `json:"waiting"`
	Linked      []string     `json:"linked"`
	// ProbeError is set when the pool could not be read because another
	// process kept its registry lock past the view's deadline.
	ProbeError string `json:"probe_error,omitempty"`
}

// BuildPools reads every pool. It only reads: no lane is created, nothing
// is migrated, no lock is waited for past one view deadline.
func BuildPools(stateDir string) (*Pools, error) {
	v, err := machine.Inspect(stateDir)
	if err != nil {
		return nil, err
	}
	out := &Pools{Schema: 1, Migrated: v.Migrated, Pools: []Pool{}, Banner: v.Banner}
	if !v.Migrated {
		return out, nil
	}
	reg, err := machine.ReadRegistry(stateDir)
	if err != nil {
		return nil, err
	}
	out.Generation = reg.Generation
	deadline := time.Now().Add(lane.ViewProbeWait)
	for _, key := range reg.Pools {
		pr := Pool{Key: key, Slots: 1, Holders: []lane.Entry{}, Waiting: []lane.Entry{}, Linked: machine.LinkedFrom(stateDir, key)}
		if pr.Linked == nil {
			pr.Linked = []string{}
		}
		if !lane.Exists(stateDir, key) {
			out.Pools = append(out.Pools, pr)
			continue
		}
		q, err := lane.OpenIn(lane.LanesDir(stateDir), key, lane.Existing)
		if errors.Is(err, os.ErrNotExist) {
			out.Pools = append(out.Pools, pr)
			continue
		}
		if err != nil {
			return nil, err
		}
		snap, err := q.ObserveBy(0, deadline)
		q.Close()
		if errors.Is(err, lane.ErrRegistryBusy) {
			pr.ProbeError = lane.ErrRegistryBusy.Error()
			out.Pools = append(out.Pools, pr)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("cannot read pool %q: %w", key, err)
		}
		pr.Slots = snap.EffectiveSlots
		pr.Description = snap.Config.Description
		pr.Holders, pr.Waiting = snap.Holders, snap.Waiting
		out.Pools = append(out.Pools, pr)
	}
	return out, nil
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run (bash): `go test . -run 'TestPoolsList' -count=1`

Expected: `ok` for each package.

- [ ] **Step 5: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/pools.go internal/report/pools.go pools_test.go
git commit -F - <<'MSG'
feat: incoda pools lists the pools, their holders and the lanes linked to them

incoda pools prints each pool's slots, held and waiting counts, linked
lanes and description, and --json gives the same with the status --json
field names. It only reads and never migrates.
MSG
```


---

### Task 3: incoda link: the user picks a lane's pools in a terminal

Spec 4.3. `incoda link KEY` shows every pool with its slots and description and the current link (else the suggestion) marked; the person toggles by number and confirms with an empty line. It needs a terminal on stdin and stderr, else exit 120 `needs-terminal: ask the user to run incoda link KEY in a terminal` before it touches anything. No lock is held while the picker is open; on confirm the link is written under machine.lock and the registry lock only if it is still the value shown (`machine.ErrLinkMoved` from the compare-and-set callback), otherwise the new value is shown and the question asked again. An empty selection, `q` or the end of input writes nothing and exits 120. The terminal is a seam (`openTerminal`): tests inject input and read what was shown, never a real terminal, and the end of input cancels, so no test can wait for an answer.

**Files:**
- Modify: `internal/cli/cli.go`
- Create: `internal/cli/link.go`
- Create: `internal/cli/terminal.go`
- Modify: `internal/machine/link.go`
- Modify: `internal/runplan/refusals.go`
- Test: `internal/cli/link_test.go` (new)
- Test: `internal/runplan/runplan_test.go`
- Test: `pools_test.go`

**Interfaces:**
- Consumes: `machine.WriteLink` (plan 3a Task 3), `runplan.Suggest`, `runplan.PoolRows` (plan 3a Task 7), `colorize.IsTerminal`, `configError` (plan 3a Task 3).
- Produces: `machine.ErrLinkMoved`; in `cli`: `type terminal struct { in *bufio.Reader; out io.Writer }`, the seam `openTerminal func() (*terminal, bool)`, `errCancelled`, `(t *terminal) line(prompt string) (string, error)`, `(t *terminal) pick(title string, choices, rows, pre []string) ([]string, error)`, `cmdLink`, `pickLink(tm, dir, reg, key, shown) ([]string, error)`; in `runplan`: `func LinkLine(sh fixline.Shell, key string) string`. Test helpers `fakeTerminal(t, input io.Reader) *bytes.Buffer`, `onFirstRead`, `linkOf`; root `linkCmd(key string) string`.

- [ ] **Step 1: Write the failing tests**

Create `internal/cli/link_test.go`:

```go
package cli

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
)

// fakeTerminal makes openTerminal answer with input and collect what the
// command shows. It never touches a real terminal, and the end of input
// cancels, so no test can wait for an answer.
func fakeTerminal(t *testing.T, input io.Reader) *bytes.Buffer {
	t.Helper()
	var shown bytes.Buffer
	saved := openTerminal
	t.Cleanup(func() { openTerminal = saved })
	openTerminal = func() (*terminal, bool) { return &terminal{in: bufio.NewReader(input), out: &shown}, true }
	return &shown
}

// onFirstRead runs fn on the first Read, before it reads from r: a change
// another process makes while the picker is open.
type onFirstRead struct {
	r    io.Reader
	fn   func()
	done bool
}

func (o *onFirstRead) Read(p []byte) (int, error) {
	if !o.done {
		o.done = true
		o.fn()
	}
	return o.r.Read(p)
}

func linkOf(t *testing.T, dir, key string) string {
	t.Helper()
	cfg, err := lane.ReadConfig(lane.LaneDir(dir, key))
	if err != nil {
		t.Fatal(err)
	}
	return machine.SetText(cfg.Pools)
}

// TestLinkPicker: incoda link shows every pool with the suggestion marked;
// enter takes the marks, numbers toggle, an empty choice or q writes
// nothing and exits 120 (spec 4.3).
func TestLinkPicker(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INCODA_DIR", dir)
	if code := Main([]string{"config", "seed"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("migrate: %d", code)
	}
	shown := fakeTerminal(t, strings.NewReader("\n"))
	var out bytes.Buffer
	if code := Main([]string{"link", "cap-e2e"}, &out, io.Discard); code != 0 {
		t.Fatalf("link: %d\n%s", code, shown)
	}
	wantShown := `link cap-e2e: not linked yet; choose the pools its jobs use (suggested: computer-use,tests, marked)
  1 [ ] builds        1 slot
  2 [x] computer-use  1 slot
  3 [x] tests         1 slot
  4 [ ] vm            1 slot
toggle by number (1-4, several separated by spaces), enter to confirm, q to cancel: `
	if shown.String() != wantShown || out.String() != "link: (none) -> computer-use,tests\n" || linkOf(t, dir, "cap-e2e") != "computer-use,tests" {
		t.Fatalf("shown:\n%s\nout:\n%s", shown, out.String())
	}
	fakeTerminal(t, strings.NewReader("4 x\n\n"))
	out.Reset()
	if code := Main([]string{"link", "cap-e2e"}, &out, io.Discard); code != 0 || out.String() != "link: computer-use,tests -> computer-use,tests,vm\n" {
		t.Fatalf("toggle: %d %s", code, out.String())
	}
	for _, in := range []string{"2 3 4\n\n", "q\n", ""} {
		fakeTerminal(t, strings.NewReader(in))
		var errOut bytes.Buffer
		if code := Main([]string{"link", "cap-e2e"}, io.Discard, &errOut); code != ExitUsage || !strings.Contains(errOut.String(), "cap-e2e stays linked to computer-use,tests,vm") {
			t.Fatalf("input %q: %d %s", in, code, errOut.String())
		}
	}
	if linkOf(t, dir, "cap-e2e") != "computer-use,tests,vm" {
		t.Fatal("a cancelled or empty choice writes nothing")
	}
}

// TestLinkAsksAgainWhenTheLinkMovedMeanwhile: the link is written only if
// it is still the value the picker showed; otherwise the new value is
// shown and the question asked again.
func TestLinkAsksAgainWhenTheLinkMovedMeanwhile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INCODA_DIR", dir)
	if code := Main([]string{"config", "kf-gate", "--pool", "builds"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("link: %d", code)
	}
	in := &onFirstRead{r: strings.NewReader("4\n\n\n"), fn: func() { relink(t, dir, "kf-gate", "tests") }}
	shown := fakeTerminal(t, in)
	var errOut bytes.Buffer
	if code := Main([]string{"link", "kf-gate"}, io.Discard, &errOut); code != 0 {
		t.Fatalf("link: %d\n%s\n%s", code, shown, errOut.String())
	}
	if !strings.Contains(shown.String(), "incoda: kf-gate was relinked while you chose; asking again\nlink kf-gate: now linked to tests;") {
		t.Fatalf("shown:\n%s", shown)
	}
	if linkOf(t, dir, "kf-gate") != "tests" || !strings.Contains(errOut.String(), "incoda: already linked: kf-gate -> tests") {
		t.Fatalf("the second answer keeps the new link: %s\n%s", linkOf(t, dir, "kf-gate"), errOut.String())
	}
}
```

Append to the end of `pools_test.go`:

```go

// linkCmd is the printed "incoda link KEY" of this platform's shell
// (runplan.LinkLine): PowerShell quotes the key.
func linkCmd(key string) string {
	if runtime.GOOS == "windows" {
		return "incoda link '" + key + "'"
	}
	return "incoda link " + key
}

// TestLinkNeedsATerminal: link is the user's interactive command; without
// a terminal (an agent, a pipe) it refuses before it touches anything.
func TestLinkNeedsATerminal(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	out := mustRun(t, incoda, state, 120, "link", "cap-e2e")
	if out != "incoda: needs-terminal: ask the user to run "+linkCmd("cap-e2e")+" in a terminal\n" {
		t.Fatalf("needs-terminal:\n%s", out)
	}
	if entries, _ := os.ReadDir(state); len(entries) != 0 {
		t.Fatalf("link without a terminal must leave the state directory alone: %v", entries)
	}
}
```

Append to the end of `internal/runplan/runplan_test.go`:

```go

// TestLinkLine: the printed "incoda link KEY" in both shells.
func TestLinkLine(t *testing.T) {
	if got := LinkLine(fixline.POSIX, "cap-e2e"); got != "incoda link cap-e2e" {
		t.Fatalf("POSIX: %q", got)
	}
	if got := LinkLine(fixline.PowerShell, "cap-e2e"); got != "incoda link 'cap-e2e'" {
		t.Fatalf("PowerShell: %q", got)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run (bash), one at a time:

- `go test ./internal/cli/ -run 'TestLinkPicker|TestLinkAsksAgainWhenTheLinkMovedMeanwhile' -count=1 -timeout 120s`
- `go test . -run 'TestLinkNeedsATerminal' -count=1 -timeout 120s`
- `go test ./internal/runplan/ -run 'TestLinkLine' -count=1 -timeout 120s`

Expected: the `internal/cli` test build fails (`undefined: openTerminal`, `undefined: terminal`); the root `TestLinkNeedsATerminal` fails with `incoda: unknown command "link"`; the `internal/runplan` test build fails (`undefined: LinkLine`).

- [ ] **Step 3: Implement**

The picker is line-based (numbers toggle, an empty line confirms) rather than a full-screen widget: it works in every console, PowerShell included, and a test drives it through the seam.

In `internal/cli/cli.go`, replace:

```go
		err = cmdConfig(rest, stdout, stderr)
	case "pools":
		err = cmdPools(rest, stdout, stderr)
	case "kill":
		err = cmdKill(rest, stdout, stderr)
	case "force-release":
```

with:

```go
		err = cmdConfig(rest, stdout, stderr)
	case "pools":
		err = cmdPools(rest, stdout, stderr)
	case "link":
		err = cmdLink(rest, stdout, stderr)
	case "kill":
		err = cmdKill(rest, stdout, stderr)
	case "force-release":
```

Create `internal/cli/link.go`:

```go
package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/fixline"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/runplan"
)

// cmdLink is incoda link KEY (spec 4.3), the user's command: a picker of
// the pools on this machine, the current link (else the suggestion)
// preselected. No lock is held while the picker is open; on confirm the
// link is written under machine.lock and the lane's registry lock only if
// it is still the value shown, else the new value is shown and the
// question asked again. An empty selection writes nothing and exits 120.
func cmdLink(args []string, stdout, stderr io.Writer) error {
	start := time.Now()
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return usagef("usage: incoda link KEY [--wait DUR]")
	}
	key, args := args[0], args[1:]
	if err := lane.ValidateKey(key); err != nil {
		return usagef("invalid queue key: %v", err)
	}
	fs := newFlagSet("link", stderr)
	wait := &waitValue{d: time.Minute}
	fs.Var(wait, "wait", "how long to wait for machine.lock and a state upgrade: a Go duration (1m) or bare seconds; negative waits forever")
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for link"}
	}
	if fs.NArg() > 0 {
		return usagef("link takes one key; unexpected %q", fs.Arg(0))
	}
	tm, ok := openTerminal()
	if !ok {
		return usagef("needs-terminal: ask the user to run %s in a terminal", runplan.LinkLine(fixline.Native(), key))
	}
	chain := procinfo.ParentChain()
	dir, reg, err := mutatingState(start, wait.d, 200*time.Millisecond, chain, stderr)
	if err != nil {
		return err
	}
	if reg.IsPool(key) {
		return usagef("pool-mismatch: %q is a pool; a pool never links other pools", key)
	}
	o := machine.Options{Start: start, Wait: wait.d, Poll: 200 * time.Millisecond, Chain: chain, Stderr: stderr}
	for {
		cfg, err := lane.ReadConfig(lane.LaneDir(dir, key))
		if err := configError(key, err); err != nil {
			return err
		}
		shown := machine.SortedSet(cfg.Pools)
		chosen, err := pickLink(tm, dir, reg, key, shown)
		if errors.Is(err, errCancelled) {
			return usagef("link: cancelled; %s stays linked to %s", key, machine.SetText(shown))
		}
		if len(chosen) == 0 {
			return usagef("link: nothing chosen; %s stays linked to %s", key, machine.SetText(shown))
		}
		res, err := machine.WriteLink(dir, key, "link", o, func(r *machine.Registry, c *lane.Config) error {
			if bad := r.NotPools(chosen); len(bad) > 0 {
				return machine.NotAPool(r, bad)
			}
			if !machine.SameSet(c.Pools, shown) {
				return machine.ErrLinkMoved
			}
			if machine.SameSet(c.Pools, chosen) {
				return lane.ErrNoChange
			}
			c.Pools = chosen
			return nil
		})
		if errors.Is(err, machine.ErrLinkMoved) {
			fmt.Fprintf(tm.out, "incoda: %s was relinked while you chose; asking again\n", key)
			continue
		}
		if err != nil {
			return machineExit(err)
		}
		if res.Changed {
			fmt.Fprintf(stdout, "link: %s -> %s\n", machine.SetText(res.Old.Pools), machine.SetText(res.New.Pools))
		} else {
			fmt.Fprintf(stderr, "incoda: already linked: %s -> %s\n", key, machine.SetText(res.New.Pools))
		}
		return nil
	}
}

// pickLink asks which pools key's jobs use, every pool listed with its
// slots and description; the current link is preselected, else a usable
// suggestion.
func pickLink(tm *terminal, dir string, reg *machine.Registry, key string, shown []string) ([]string, error) {
	pre := shown
	title := fmt.Sprintf("link %s: now linked to %s; choose the pools its jobs use", key, machine.SetText(shown))
	if len(shown) == 0 {
		s := runplan.Suggest(reg, key)
		title = fmt.Sprintf("link %s: not linked yet; choose the pools its jobs use (suggested: none, %s)", key, s.Why)
		if s.Usable {
			pre = s.Pools
			title = fmt.Sprintf("link %s: not linked yet; choose the pools its jobs use (suggested: %s, marked)", key, s.Text())
		}
	}
	rows := runplan.PoolRows(dir, reg)
	for i := range rows {
		rows[i] = strings.TrimPrefix(rows[i], "  ")
	}
	return tm.pick(title, reg.Pools, rows, pre)
}
```

Create `internal/cli/terminal.go`:

```go
package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/deblasis/incoda/internal/colorize"
)

// terminal is how the setup commands (link, init) talk to a person. run
// never reads one (spec 4.1). Nothing holds a lock while it waits for an
// answer.
type terminal struct {
	in  *bufio.Reader
	out io.Writer
}

// openTerminal returns the person's terminal when stdin and stderr are
// both terminals; ok is false otherwise (an agent, a pipe, CI). Tests
// replace it, so no test ever reads a real terminal.
var openTerminal = func() (*terminal, bool) {
	if !colorize.IsTerminal(os.Stdin) || !colorize.IsTerminal(os.Stderr) {
		return nil, false
	}
	return &terminal{in: bufio.NewReader(os.Stdin), out: os.Stderr}, true
}

// errCancelled is a person's q, or the end of input: nothing more is
// written.
var errCancelled = errors.New("cancelled")

// line prints prompt and reads one answer, trimmed. The end of input
// cancels.
func (t *terminal) line(prompt string) (string, error) {
	fmt.Fprint(t.out, prompt)
	s, err := t.in.ReadString('\n')
	if err != nil && (err != io.EOF || s == "") {
		return "", errCancelled
	}
	s = strings.TrimSpace(s)
	if s == "q" {
		return "", errCancelled
	}
	return s, nil
}

// pick shows choices, the ones in pre marked, and lets the person toggle
// them by number until an empty line confirms. It returns the marked
// choices in the order shown. rows[i] is the text shown after choice i's
// mark.
func (t *terminal) pick(title string, choices, rows, pre []string) ([]string, error) {
	marked := map[string]bool{}
	for _, c := range pre {
		marked[c] = true
	}
	for {
		fmt.Fprintln(t.out, title)
		for i, c := range choices {
			mark := " "
			if marked[c] {
				mark = "x"
			}
			fmt.Fprintf(t.out, "  %d [%s] %s\n", i+1, mark, rows[i])
		}
		ans, err := t.line(fmt.Sprintf("toggle by number (1-%d, several separated by spaces), enter to confirm, q to cancel: ", len(choices)))
		if err != nil {
			return nil, err
		}
		if ans == "" {
			var out []string
			for _, c := range choices {
				if marked[c] {
					out = append(out, c)
				}
			}
			return out, nil
		}
		for _, f := range strings.Fields(strings.ReplaceAll(ans, ",", " ")) {
			n, err := strconv.Atoi(f)
			if err != nil || n < 1 || n > len(choices) {
				fmt.Fprintf(t.out, "not a number from 1 to %d: %s\n", len(choices), f)
				continue
			}
			marked[choices[n-1]] = !marked[choices[n-1]]
		}
	}
}
```

In `internal/runplan/refusals.go`, replace:

```go
// configLine is "incoda config KEY --pool S [--quiet-machine]".
```

with:

```go
// LinkLine is "incoda link KEY", the command a person runs to choose a
// key's pools, as sh takes it (fixline; PowerShell quotes the key).
func LinkLine(sh fixline.Shell, key string) string {
	return fixline.Line{Words: []fixline.Word{fixline.Lit("incoda"), fixline.Lit("link"), fixline.Key(key)}}.Render(sh).Text
}

// configLine is "incoda config KEY --pool S [--quiet-machine]".
```

In `internal/machine/link.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 2) Replace:

```go
	return &Refusal{Msg: fmt.Sprintf("pool-mismatch: %s on this machine (pools: %s)", what, strings.Join(r.Pools, ", "))}
}

// LinkResult is what a link write found and left.
type LinkResult struct {
	// Old and New are the lane's config before and after; they are equal
```

with:

```go
	return &Refusal{Msg: fmt.Sprintf("pool-mismatch: %s on this machine (pools: %s)", what, strings.Join(r.Pools, ", "))}
}

// ErrLinkMoved, returned by a WriteLink callback, means the link is no
// longer the value the caller showed a person: nothing is written, and the
// caller shows the new value and asks again (spec 4.3).
var ErrLinkMoved = errors.New("the link changed since it was shown")

// LinkResult is what a link write found and left.
type LinkResult struct {
	// Old and New are the lane's config before and after; they are equal
```

(2 of 2) Replace:

```go
		var rf *Refusal
		var ns *lane.NewerSchemaError
		switch {
		case errors.As(err, &rf):
			return res, err
		case errors.As(err, &ns):
			return res, stateErrorf("%s", esc(err))
```

with:

```go
		var rf *Refusal
		var ns *lane.NewerSchemaError
		switch {
		case errors.As(err, &rf), errors.Is(err, ErrLinkMoved):
			return res, err
		case errors.As(err, &ns):
			return res, stateErrorf("%s", esc(err))
```

- [ ] **Step 4: Run the tests to see them pass**

Run (bash), one at a time:

- `go test ./internal/cli/ -run 'TestLinkPicker|TestLinkAsksAgainWhenTheLinkMovedMeanwhile' -count=1`
- `go test . -run 'TestLinkNeedsATerminal' -count=1`
- `go test ./internal/runplan/ -run 'TestLinkLine' -count=1`

Expected: `ok` for each package.

- [ ] **Step 5: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/cli.go internal/cli/link.go internal/cli/link_test.go internal/cli/terminal.go internal/machine/link.go internal/runplan/refusals.go internal/runplan/runplan_test.go pools_test.go
git commit -F - <<'MSG'
feat: incoda link lets the user pick a lane's pools in a terminal

incoda link KEY lists every pool with the current link or the
suggestion marked, holds no lock while the person chooses, and writes
the choice only if the link is still what it showed, else it asks
again. Without a terminal it refuses with needs-terminal before it
touches anything; an empty choice writes nothing.
MSG
```


---

### Task 4: init --print and init --apply-suggestions; doctor names unlinked lanes

Spec 4.3 and 5.5. `incoda init --print` takes no lock and writes nothing: each unlinked open lane with its suggestion and, only for a lane that has one, the exact `incoda config` command; the rest get `ask the user; they run: incoda link KEY`; a lane whose own slots exceed a suggested pool's is flagged (`kungfoo-build: slots 2 inside builds 1, effective 1`). On a layout not upgraded yet it reads the old `queues/` and the pools the upgrade will register. `incoda init --apply-suggestions` (the user's one-step setup) migrates first if needed, links every unlinked open lane whose suggestion is usable in its own step under machine.lock and the lane's registry lock (a lane linked or closed meanwhile is left alone, an existing link is never changed), sets `quiet_machine` where the table says so, logs `event=link by=init-suggest`, prints `linked cap-gate -> tests` per lane, then the lanes it left unlinked. `doctor` gains an `attention:` line per unlinked lane with its suggestion. Plain `incoda init` prints usage until Task 5.

**Files:**
- Modify: `internal/cli/cli.go`
- Create: `internal/cli/initcmd.go`
- Modify: `internal/cli/misc.go`
- Create: `internal/runplan/unlinked.go`
- Test: `init_test.go` (new)
- Test: `internal/runplan/runplan_test.go`

**Interfaces:**
- Consumes: `runplan.Suggest`, `configLine` (plan 3a Task 7), `machine.WriteLink`, `machine.LinkedLine` (plan 3a Task 3), `readState`, `mutatingState`, `runplan.LinkLine` and the root `linkCmd` (Task 3).
- Produces: package `runplan`: `type Unlinked struct { Key string; Slots int; Suggestion Suggestion }`, `func UnlinkedLanes(root string, reg *machine.Registry) ([]Unlinked, []string)`, `(u Unlinked) ConfigLine(sh fixline.Shell) string`, `(u Unlinked) SlotNotes(poolSlots func(string) int) []string`. In `cli`: `cmdInit`, `initPrint`, `initApply`, `unlinkedAttention(dir string) []string`. Root helpers `stateSum`, `seedLanes`.

- [ ] **Step 1: Write the failing tests**

Append to the end of `internal/runplan/runplan_test.go`:

```go

// TestUnlinkedLanes: open project lanes with no link, with their
// suggestions; closed, linked and pool lanes are left out, unreadable ones
// named apart.
func TestUnlinkedLanes(t *testing.T) {
	state, reg := machineDir(t, map[string]string{
		"builds":        `{"schema":2,"slots":1}`,
		"cap-gate":      `{"schema":2}`,
		"kungfoo-build": `{"schema":2,"slots":2}`,
		"old-gate":      `{"schema":2,"closed":"retired"}`,
		"linked-gate":   `{"schema":2,"pools":["tests"]}`,
		"polymatto":     `{"schema":2}`,
		"broken":        `{`,
	})
	lanes, unreadable := UnlinkedLanes(lane.LanesDir(state), reg)
	var keys []string
	for _, u := range lanes {
		keys = append(keys, u.Key)
	}
	if strings.Join(keys, ",") != "cap-gate,kungfoo-build,polymatto" || strings.Join(unreadable, ",") != "broken" {
		t.Fatalf("%v %v", keys, unreadable)
	}
	kb := lanes[1]
	if got := kb.SlotNotes(func(string) int { return 1 }); strings.Join(got, "|") != "kungfoo-build: slots 2 inside builds 1, effective 1" {
		t.Fatalf("slot notes: %q", got)
	}
	if fixline.Native() == fixline.POSIX && (kb.ConfigLine(fixline.POSIX) != "incoda config kungfoo-build --pool builds" || lanes[2].ConfigLine(fixline.POSIX) != "") {
		t.Fatalf("config lines: %q %q", kb.ConfigLine(fixline.POSIX), lanes[2].ConfigLine(fixline.POSIX))
	}
}
```

Create `init_test.go`:

```go
package main

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stateSum hashes every file under state, path and content, so a test can
// prove a command wrote nothing.
func stateSum(t *testing.T, state string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(state, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%x\x00", p, sha256.Sum256(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// seedLanes makes the lanes of a small spec 6 machine.
func seedLanes(t *testing.T, incoda, state string) {
	t.Helper()
	for _, args := range [][]string{
		{"config", "cap-gate", "--slots", "1"},
		{"config", "kungfoo-build", "--slots", "2"},
		{"config", "kungfoo-measure", "--slots", "1"},
		{"config", "polymatto", "--slots", "1"},
		{"config", "old-gate", "--close", "retired: use cap-gate"},
		{"config", "linked-gate", "--pool", "vm"},
	} {
		mustRun(t, incoda, state, 0, args...)
	}
}

// TestInitPrint: each unlinked open lane with its suggestion and, only for
// a lane that has one, the exact config command; the rest ask the user.
// Slots above a suggested pool's are flagged. No lock, no writes (spec
// 4.3). doctor names the same lanes.
func TestInitPrint(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	seedLanes(t, incoda, state)
	before := stateSum(t, state)
	out := mustRun(t, incoda, state, 0, "init", "--print")
	want := `cap-gate: suggested tests (name matches *-gate)
  incoda config cap-gate --pool tests
kungfoo-build: suggested builds (name matches *-build)
  incoda config kungfoo-build --pool builds
  kungfoo-build: slots 2 inside builds 1, effective 1
kungfoo-measure: suggested tests, quiet_machine (name matches *-measure)
  incoda config kungfoo-measure --pool tests --quiet-machine
polymatto: no suggestion (no name pattern matches)
  ask the user; they run: incoda link polymatto
`
	if runtime.GOOS != "windows" && out != want {
		t.Fatalf("init --print:\n%s\nwant:\n%s", out, want)
	}
	if strings.Contains(out, "incoda config polymatto") {
		t.Fatal("no config line for a lane without a suggestion")
	}
	if stateSum(t, state) != before {
		t.Fatal("init --print must write nothing")
	}
	out, _ = doctor(t, incoda, state)
	mustContain(t, out,
		"attention: unlinked lane cap-gate: runs are refused until it is linked; suggested: tests (incoda init --print shows the command)\n",
		"attention: unlinked lane polymatto: runs are refused until it is linked; no suggestion (no name pattern matches): the user runs "+linkCmd("polymatto")+"\n")
	mustRun(t, incoda, state, 120, "init")
	mustRun(t, incoda, state, 120, "init", "--print", "--wait", "1s")
}

// TestInitApplySuggestions: every unlinked open lane that matches a
// pattern is linked to its suggestion (quiet_machine where the table says
// so), logged by=init-suggest; existing links and closed lanes are left
// alone, and the lanes left unlinked are named (spec 4.3).
func TestInitApplySuggestions(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	seedLanes(t, incoda, state)
	out := mustRun(t, incoda, state, 0, "init", "--apply-suggestions")
	want := `linked cap-gate -> tests
linked kungfoo-build -> builds
linked kungfoo-measure -> tests, quiet_machine
left unlinked: polymatto (no suggestion: no name pattern matches; the user links it with ` + linkCmd("polymatto") + `)
`
	if out != want {
		t.Fatalf("init --apply-suggestions:\n%s\nwant:\n%s", out, want)
	}
	cfg := mustRun(t, incoda, state, 0, "config", "kungfoo-measure")
	if !strings.Contains(cfg, "  pools: tests\n  quiet machine: yes\n") {
		t.Fatalf("kungfoo-measure:\n%s", cfg)
	}
	if cfg := mustRun(t, incoda, state, 0, "config", "linked-gate"); !strings.Contains(cfg, "  pools: vm\n") {
		t.Fatalf("an existing link is never changed:\n%s", cfg)
	}
	if cfg := mustRun(t, incoda, state, 0, "config", "old-gate"); !strings.Contains(cfg, "  pools: (none") {
		t.Fatalf("a closed lane is left alone:\n%s", cfg)
	}
	log, _ := os.ReadFile(filepath.Join(laneDir(state, "cap-gate"), "lane.log"))
	if !strings.Contains(string(log), " by=init-suggest old= new=tests") {
		t.Fatalf("lane.log:\n%s", log)
	}
	out = mustRun(t, incoda, state, 0, "init", "--apply-suggestions")
	if out != "left unlinked: polymatto (no suggestion: no name pattern matches; the user links it with "+linkCmd("polymatto")+")\n" {
		t.Fatalf("a second apply changes nothing:\n%s", out)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run (bash), one at a time:

- `go test ./internal/runplan/ -run 'TestUnlinkedLanes' -count=1 -timeout 120s`
- `go test . -run 'TestInitPrint|TestInitApplySuggestions' -count=1 -timeout 120s`

Expected: the `internal/runplan` test build fails (`undefined: UnlinkedLanes`); the root tests fail with `incoda: unknown command "init"`.

- [ ] **Step 3: Implement**

In `internal/cli/cli.go`, replace:

```go
		err = cmdPools(rest, stdout, stderr)
	case "link":
		err = cmdLink(rest, stdout, stderr)
	case "kill":
		err = cmdKill(rest, stdout, stderr)
	case "force-release":
```

with:

```go
		err = cmdPools(rest, stdout, stderr)
	case "link":
		err = cmdLink(rest, stdout, stderr)
	case "init":
		err = cmdInit(rest, stdout, stderr)
	case "kill":
		err = cmdKill(rest, stdout, stderr)
	case "force-release":
```

Create `internal/cli/initcmd.go`:

```go
package cli

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/deblasis/incoda/internal/fixline"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/runplan"
)

// cmdInit is incoda init (spec 4.3): --print shows each unlinked lane with
// its suggestion and the command that applies it; --apply-suggestions
// applies every suggestion, the user's one-step setup.
func cmdInit(args []string, stdout, stderr io.Writer) error {
	start := time.Now()
	fs := newFlagSet("init", stderr)
	printOnly := fs.Bool("print", false, "show each unlinked open lane, its suggestion and the command that applies it; takes no lock and writes nothing")
	apply := fs.Bool("apply-suggestions", false, "link every unlinked open lane that matches a pattern to its suggestion, each in its own step; never changes an existing link")
	wait := &waitValue{d: time.Minute}
	fs.Var(wait, "wait", "how long to wait for machine.lock and a state upgrade: a Go duration (1m) or bare seconds; negative waits forever")
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for init"}
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	switch {
	case fs.NArg() > 0:
		return usagef("init takes no arguments; unexpected %q", fs.Arg(0))
	case *printOnly && *apply:
		return usagef("--print and --apply-suggestions contradict each other")
	case *printOnly && given["wait"]:
		return usagef("init --print takes no lock, so --wait does not apply")
	case *printOnly:
		return initPrint(stdout, stderr)
	case *apply:
		return initApply(start, wait.d, stdout, stderr)
	}
	return usagef("usage: incoda init --print | incoda init --apply-suggestions [--wait DUR]")
}

// initPrint is init --print: no lock, no writes, no migration. On a layout
// not upgraded yet it reads the old queues/ and the pools the upgrade will
// register.
func initPrint(stdout, stderr io.Writer) error {
	dir, v, err := readState()
	if err != nil {
		return err
	}
	printBanner(stderr, v.Banner)
	reg := &machine.Registry{Pools: machine.BootstrapPools()}
	if v.Migrated {
		if reg, err = machine.ReadRegistry(dir); err != nil {
			return machineExit(err)
		}
	}
	poolSlots := func(pool string) int {
		cfg, err := lane.ReadConfig(filepath.Join(v.Root, pool))
		if err != nil {
			return 1
		}
		return max(cfg.Slots, 1)
	}
	lanes, unreadable := runplan.UnlinkedLanes(v.Root, reg)
	if len(lanes) == 0 {
		fmt.Fprintln(stdout, "every open lane is linked")
	}
	for _, u := range lanes {
		s := u.Suggestion
		if !s.Usable {
			fmt.Fprintf(stdout, "%s: no suggestion (%s)\n  ask the user; they run: %s\n", u.Key, s.Why, runplan.LinkLine(fixline.Native(), u.Key))
			continue
		}
		fmt.Fprintf(stdout, "%s: suggested %s (name matches %s)\n  %s\n", u.Key, s.Text(), s.Pattern, u.ConfigLine(fixline.Native()))
		for _, n := range u.SlotNotes(poolSlots) {
			fmt.Fprintf(stdout, "  %s\n", n)
		}
	}
	for _, k := range unreadable {
		fmt.Fprintf(stdout, "%s: config.json is unreadable; see incoda doctor\n", k)
	}
	return nil
}

// initApply is init --apply-suggestions: it migrates first if needed, then
// links each unlinked open lane that has a usable suggestion in its own
// step under machine.lock and that lane's registry lock (a lane linked or
// closed meanwhile is left alone), logs event=link by=init-suggest, and
// says what it changed and what it left unlinked.
func initApply(start time.Time, wait time.Duration, stdout, stderr io.Writer) error {
	chain := procinfo.ParentChain()
	dir, reg, err := mutatingState(start, wait, 200*time.Millisecond, chain, stderr)
	if err != nil {
		return err
	}
	o := machine.Options{Start: start, Wait: wait, Poll: 200 * time.Millisecond, Chain: chain, Stderr: stderr}
	lanes, unreadable := runplan.UnlinkedLanes(lane.LanesDir(dir), reg)
	var left []runplan.Unlinked
	for _, u := range lanes {
		s := u.Suggestion
		if !s.Usable {
			left = append(left, u)
			continue
		}
		res, err := machine.WriteLink(dir, u.Key, "init-suggest", o, func(_ *machine.Registry, c *lane.Config) error {
			if len(c.Pools) > 0 || c.Closed != "" {
				return lane.ErrNoChange
			}
			c.Pools = s.Pools
			c.QuietMachine = c.QuietMachine || s.QuietMachine
			return nil
		})
		if err != nil {
			return machineExit(err)
		}
		if res.Changed {
			fmt.Fprintf(stdout, "linked %s\n", machine.LinkedLine(u.Key, res.New.Pools, s.QuietMachine))
		}
	}
	for _, u := range left {
		fmt.Fprintf(stdout, "left unlinked: %s (no suggestion: %s; the user links it with %s)\n", u.Key, u.Suggestion.Why, runplan.LinkLine(fixline.Native(), u.Key))
	}
	for _, k := range unreadable {
		fmt.Fprintf(stdout, "left unlinked: %s (config.json is unreadable; see incoda doctor)\n", k)
	}
	return nil
}
```

In `internal/cli/misc.go`, make these 3 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 3) Replace:

```go
	"github.com/deblasis/incoda/internal/colorize"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/lockfile"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/sysinfo"
	"github.com/deblasis/incoda/internal/textsafe"
	"github.com/deblasis/incoda/internal/tui"
```

with:

```go
	"github.com/deblasis/incoda/internal/colorize"
	"github.com/deblasis/incoda/internal/fixline"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/lockfile"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/runplan"
	"github.com/deblasis/incoda/internal/sysinfo"
	"github.com/deblasis/incoda/internal/textsafe"
	"github.com/deblasis/incoda/internal/tui"
```

(2 of 3) Replace:

```go
	if cleanErr != "" {
		attention = append(attention, cleanErr)
	}
	if stateDirSource() == "INCODA_DIR" {
		attention = append(attention, "INCODA_DIR is set: it is a MACHINE-level override, not a per-project one; it splits pools across state directories, so a caller without it set uses a different state directory, forms separate lanes, and stops serialising against this one")
	}
```

with:

```go
	if cleanErr != "" {
		attention = append(attention, cleanErr)
	}
	attention = append(attention, unlinkedAttention(dir)...)
	if stateDirSource() == "INCODA_DIR" {
		attention = append(attention, "INCODA_DIR is set: it is a MACHINE-level override, not a per-project one; it splits pools across state directories, so a caller without it set uses a different state directory, forms separate lanes, and stops serialising against this one")
	}
```

(3 of 3) Replace:

```go
		return exitWith(ExitState, "machine-state: %d problem(s) make runs fail closed; see the problem: lines above", len(h.Problems))
	}
	return nil
}

// probeLocking proves the OS lock is actually enforced rather than merely not
```

with:

```go
		return exitWith(ExitState, "machine-state: %d problem(s) make runs fail closed; see the problem: lines above", len(h.Problems))
	}
	return nil
}

// unlinkedAttention names, on a migrated layout, every open project lane
// with no link and its suggestion (spec 5.5): runs on it are refused until
// it is linked.
func unlinkedAttention(dir string) []string {
	reg, err := machine.ReadRegistry(dir)
	if err != nil {
		return nil
	}
	lanes, _ := runplan.UnlinkedLanes(lane.LanesDir(dir), reg)
	var out []string
	for _, u := range lanes {
		if u.Suggestion.Usable {
			out = append(out, fmt.Sprintf("unlinked lane %s: runs are refused until it is linked; suggested: %s (incoda init --print shows the command)", u.Key, u.Suggestion.Text()))
			continue
		}
		out = append(out, fmt.Sprintf("unlinked lane %s: runs are refused until it is linked; no suggestion (%s): the user runs %s", u.Key, u.Suggestion.Why, runplan.LinkLine(fixline.Native(), u.Key)))
	}
	return out
}

// probeLocking proves the OS lock is actually enforced rather than merely not
```

Create `internal/runplan/unlinked.go`:

```go
package runplan

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/deblasis/incoda/internal/fixline"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
)

// Unlinked is an open project lane with no link: runs on it are refused
// until it is linked (spec 3.5, 4.1).
type Unlinked struct {
	Key        string
	Slots      int
	Suggestion Suggestion
}

// UnlinkedLanes lists the open project lanes under root (lanes/, or the
// old queues/ of a layout not upgraded yet) that have no link, sorted by
// key, with their suggestions against reg. Closed lanes are left out, and
// so are lanes whose config cannot be read; unreadable names those.
func UnlinkedLanes(root string, reg *machine.Registry) (out []Unlinked, unreadable []string) {
	keys, _ := lane.ListIn(root)
	sort.Strings(keys)
	for _, k := range keys {
		if reg.IsPool(k) {
			continue
		}
		cfg, err := lane.ReadConfig(filepath.Join(root, k))
		if err != nil {
			unreadable = append(unreadable, k)
			continue
		}
		if cfg.Closed != "" || len(cfg.Pools) > 0 {
			continue
		}
		out = append(out, Unlinked{Key: k, Slots: max(cfg.Slots, 1), Suggestion: Suggest(reg, k)})
	}
	return out, unreadable
}

// ConfigLine is the incoda config line that links u to its suggestion;
// empty when there is no usable suggestion.
func (u Unlinked) ConfigLine(sh fixline.Shell) string {
	if !u.Suggestion.Usable {
		return ""
	}
	return configLine(sh, u.Key, u.Suggestion)
}

// SlotNotes are the "slots N inside POOL M, effective M" notes of init
// --print (spec 4.3): the lane's own count exceeds a suggested pool's, so
// the pool caps it until it is raised. poolSlots gives each pool's count.
func (u Unlinked) SlotNotes(poolSlots func(pool string) int) []string {
	var out []string
	for _, p := range u.Suggestion.Pools {
		if n := poolSlots(p); u.Slots > n {
			out = append(out, fmt.Sprintf("%s: slots %d inside %s %d, effective %d", u.Key, u.Slots, p, n, n))
		}
	}
	return out
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run (bash), one at a time:

- `go test ./internal/runplan/ -run 'TestUnlinkedLanes' -count=1`
- `go test . -run 'TestInitPrint|TestInitApplySuggestions' -count=1`

Expected: `ok` for each package.

- [ ] **Step 5: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 6: Commit**

```bash
git add init_test.go internal/cli/cli.go internal/cli/initcmd.go internal/cli/misc.go internal/runplan/runplan_test.go internal/runplan/unlinked.go
git commit -F - <<'MSG'
feat: init prints and applies the suggested links; doctor names unlinked lanes

init --print shows each unlinked open lane, its suggestion and the
config command that applies it, flagging lanes wider than their pool,
and writes nothing. init --apply-suggestions links each such lane in its
own locked step and never changes an existing link. doctor names every
unlinked lane with its suggestion.
MSG
```


---

### Task 5: incoda init asks the user, one locked step per answer

Spec 4.3. `incoda init` migrates first if needed (bounded by `--wait`), then asks, holding no lock while asking: each pool's cap, any other shared resources ("a VM, a printer, a device"), and a link for each unlinked open lane with its suggestion marked. Each answer is applied in its own short step under machine.lock and the lane's registry lock, after re-reading the state; if the state changed since the question, the question is asked again with the new value. Ctrl-C keeps the steps already applied and nothing else; `q` or the end of input stops the same way (exit 120). Without a terminal: exit 120 `needs-terminal: ask the user to run incoda init in a terminal`. The link loop of `incoda link` becomes `askLink`, shared by both commands.

**Files:**
- Modify: `internal/cli/initcmd.go`
- Modify: `internal/cli/link.go`
- Modify: `internal/machine/link.go`
- Test: `init_test.go`
- Test: `internal/cli/init_test.go` (new)

**Interfaces:**
- Consumes: `openTerminal`, `terminal.line`, `pickLink` (Task 3), `machine.AddPool` (Task 1), `runplan.UnlinkedLanes` (Task 4), `machine.ErrLinkMoved`.
- Produces: `machine.WriteStep(stateDir, key, by string, o Options, fn func(reg *Registry, c *lane.Config) error) (LinkResult, error)` (WriteLink without the project-only check, for pool caps); in `cli`: `askLink(tm, dir, reg, key, by, o, quiet func([]string) bool) (machine.LinkResult, []string, error)`, `errNothingChosen`, `initAsk`, `askPoolCap`, `askNewPool`.

- [ ] **Step 1: Write the failing tests**

Create `internal/cli/init_test.go`:

```go
package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/fixline"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/runplan"
)

func slotsOf(t *testing.T, dir, key string) int {
	t.Helper()
	cfg, err := lane.ReadConfig(lane.LaneDir(dir, key))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Slots
}

// TestInitAsks: the interactive init asks each pool's cap, other shared
// resources, and a link for each unlinked lane with its suggestion marked;
// each answer is applied as it is given (spec 4.3).
func TestInitAsks(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INCODA_DIR", dir)
	for _, k := range []string{"cap-gate", "polymatto"} {
		if code := Main([]string{"config", k, "--slots", "1"}, io.Discard, io.Discard); code != 0 {
			t.Fatalf("config %s: %d", k, code)
		}
	}
	answers := strings.Join([]string{
		"2",      // builds
		"",       // computer-use
		"x", "3", // tests: a bad answer, then 3
		"",         // vm
		"gpu", "2", // another resource: gpu, 2 slots
		"the \x1bGPU", "the GPU", // a description with ESC is refused, then taken
		"", // no more resources
		"", // cap-gate: take the marked suggestion (tests)
		"", // polymatto: nothing marked, leave it unlinked
	}, "\n") + "\n"
	shown := fakeTerminal(t, strings.NewReader(answers))
	var out bytes.Buffer
	if code := Main([]string{"init"}, &out, io.Discard); code != 0 {
		t.Fatalf("init: %d\n%s", code, shown)
	}
	if slotsOf(t, dir, "builds") != 2 || slotsOf(t, dir, "tests") != 3 || slotsOf(t, dir, "computer-use") != 1 || slotsOf(t, dir, "gpu") != 2 {
		t.Fatalf("caps not applied:\n%s", shown)
	}
	for _, want := range []string{
		"pool builds: 1 slot(s). New cap (enter keeps 1): ",
		"not a whole number of at least 1: x\n",
		"incoda: bad-text: description contains control characters\n",
		"pool gpu added (2 slot(s))\n",
		"link cap-gate: not linked yet; choose the pools its jobs use (suggested: tests, marked)\n",
		"  3 [ ] gpu           2 slots  the GPU\n",
		"link polymatto: not linked yet; choose the pools its jobs use (suggested: none, no name pattern matches)\n",
	} {
		if !strings.Contains(shown.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, shown)
		}
	}
	if out.String() != "linked cap-gate -> tests\nleft unlinked: polymatto; runs on them are refused until they are linked (the user runs: "+runplan.LinkLine(fixline.Native(), "polymatto")+")\n" {
		t.Fatalf("out:\n%s", out.String())
	}
	if linkOf(t, dir, "cap-gate") != "tests" || linkOf(t, dir, "polymatto") != "(none)" {
		t.Fatal("links not applied")
	}
}

// TestInitAsksAgainWhenTheStateChanged: an answer is applied only if the
// value is still the one the question showed; otherwise the question is
// asked again with the new value. The end of input stops init and keeps
// what was applied.
func TestInitAsksAgainWhenTheStateChanged(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INCODA_DIR", dir)
	if code := Main([]string{"config", "seed", "--slots", "1"}, io.Discard, io.Discard); code != 0 {
		t.Fatal("migrate")
	}
	in := &onFirstRead{r: strings.NewReader("2\n\n"), fn: func() {
		_, err := machine.WriteStep(dir, "builds", "test", machine.Options{Start: time.Now(), Wait: 5 * time.Second}, func(_ *machine.Registry, c *lane.Config) error {
			c.Slots = 4
			return nil
		})
		if err != nil {
			t.Error(err)
		}
	}}
	shown := fakeTerminal(t, in)
	var errOut bytes.Buffer
	code := Main([]string{"init"}, io.Discard, &errOut)
	if code != ExitUsage || !strings.Contains(errOut.String(), "incoda: init: stopped; the answers before this one are applied") {
		t.Fatalf("the end of input stops init: %d\n%s", code, errOut.String())
	}
	if !strings.Contains(shown.String(), "incoda: pool builds was changed while you answered; asking again\npool builds: 4 slot(s). New cap (enter keeps 4): ") {
		t.Fatalf("shown:\n%s", shown)
	}
	if slotsOf(t, dir, "builds") != 4 {
		t.Fatal("the stale answer must not be written")
	}
}
```

Append to the end of `init_test.go`:

```go

// TestInitNeedsATerminal: the interactive init refuses without a terminal,
// before it migrates or writes anything.
func TestInitNeedsATerminal(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	out := mustRun(t, incoda, state, 120, "init")
	if out != "incoda: needs-terminal: ask the user to run incoda init in a terminal\n" {
		t.Fatalf("needs-terminal:\n%s", out)
	}
	if entries, _ := os.ReadDir(state); len(entries) != 0 {
		t.Fatalf("init without a terminal must leave the state directory alone: %v", entries)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run (bash), one at a time:

- `go test ./internal/cli/ -run 'TestInitAsks|TestInitAsksAgainWhenTheStateChanged' -count=1 -timeout 120s`
- `go test . -run 'TestInitNeedsATerminal' -count=1 -timeout 120s`

Expected: the `internal/cli` test build fails (`undefined: machine.WriteStep`); the root `TestInitNeedsATerminal` fails because `incoda init` still prints `incoda: usage: incoda init --print | incoda init --apply-suggestions [--wait DUR]`.

- [ ] **Step 3: Implement**

In `internal/cli/initcmd.go`, make these 3 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 3) Replace:

```go
package cli

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/deblasis/incoda/internal/fixline"
```

with:

```go
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/fixline"
```

(2 of 3) Replace:

```go
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/runplan"
)

// cmdInit is incoda init (spec 4.3): --print shows each unlinked lane with
// its suggestion and the command that applies it; --apply-suggestions
// applies every suggestion, the user's one-step setup.
func cmdInit(args []string, stdout, stderr io.Writer) error {
	start := time.Now()
	fs := newFlagSet("init", stderr)
```

with:

```go
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/runplan"
	"github.com/deblasis/incoda/internal/textsafe"
)

// cmdInit is incoda init (spec 4.3): with no flag it asks the user, in a
// terminal, each pool's cap, any other shared resources, and a link for
// each unlinked open lane; --print shows each unlinked lane with its
// suggestion and the command that applies it; --apply-suggestions applies
// every suggestion, the user's one-step setup.
func cmdInit(args []string, stdout, stderr io.Writer) error {
	start := time.Now()
	fs := newFlagSet("init", stderr)
```

(3 of 3) Replace:

```go
	case *apply:
		return initApply(start, wait.d, stdout, stderr)
	}
	return usagef("usage: incoda init --print | incoda init --apply-suggestions [--wait DUR]")
}

// initPrint is init --print: no lock, no writes, no migration. On a layout
```

with:

```go
	case *apply:
		return initApply(start, wait.d, stdout, stderr)
	}
	return initAsk(start, wait.d, stdout, stderr)
}

// initAsk is the interactive init. It migrates first if needed (bounded by
// --wait), then asks with no lock held. Each answer is applied in its own
// short step under machine.lock and the lane's registry lock, after
// re-reading the state; if the state changed since the question, the
// question is asked again with the new value. Ctrl-C keeps the steps
// already applied and nothing else; q stops the same way.
func initAsk(start time.Time, wait time.Duration, stdout, stderr io.Writer) error {
	tm, ok := openTerminal()
	if !ok {
		return usagef("needs-terminal: ask the user to run incoda init in a terminal")
	}
	chain := procinfo.ParentChain()
	dir, reg, err := mutatingState(start, wait, 200*time.Millisecond, chain, stderr)
	if err != nil {
		return err
	}
	o := machine.Options{Start: start, Wait: wait, Poll: 200 * time.Millisecond, Chain: chain, Stderr: stderr}
	stopped := func() error {
		return usagef("init: stopped; the answers before this one are applied")
	}

	fmt.Fprintln(tm.out, "pools are machine-wide caps: each holds this many jobs at once, whichever project they come from.")
	for _, pool := range reg.Pools {
		if err := askPoolCap(tm, dir, pool, o); errors.Is(err, errCancelled) {
			return stopped()
		} else if err != nil {
			return err
		}
	}

	for {
		name, err := tm.line("any other shared resources? a VM, a printer, a device. A pool name to add (enter for none): ")
		if errors.Is(err, errCancelled) {
			return stopped()
		}
		if name == "" {
			break
		}
		if err := askNewPool(tm, dir, name, o); errors.Is(err, errCancelled) {
			return stopped()
		} else if err != nil {
			return err
		}
	}

	if reg, err = machine.ReadRegistry(dir); err != nil {
		return machineExit(err)
	}
	lanes, _ := runplan.UnlinkedLanes(lane.LanesDir(dir), reg)
	if len(lanes) > 0 {
		fmt.Fprintln(tm.out, "now a link for each unlinked lane: enter takes the marked pools; unmark them all to leave a lane unlinked.")
	}
	var left []string
	for _, u := range lanes {
		s := u.Suggestion
		res, _, err := askLink(tm, dir, reg, u.Key, "init", o, func(chosen []string) bool {
			return s.Usable && s.QuietMachine && machine.SameSet(chosen, s.Pools)
		})
		switch {
		case errors.Is(err, errCancelled):
			return stopped()
		case errors.Is(err, errNothingChosen):
			left = append(left, u.Key)
		case err != nil:
			return machineExit(err)
		case res.Changed:
			fmt.Fprintf(stdout, "linked %s\n", machine.LinkedLine(u.Key, res.New.Pools, res.New.QuietMachine))
		}
	}
	if len(left) > 0 {
		// Every printed command is a fixline line with no placeholder.
		links := make([]string, len(left))
		for i, k := range left {
			links[i] = runplan.LinkLine(fixline.Native(), k)
		}
		fmt.Fprintf(stdout, "left unlinked: %s; runs on them are refused until they are linked (the user runs: %s)\n", strings.Join(left, ", "), strings.Join(links, ", "))
	}
	return nil
}

// askPoolCap asks for pool's cap until an answer is applied: an empty
// answer keeps it; a number is written only if the cap is still the one
// shown, else the question is asked again with the new value.
func askPoolCap(tm *terminal, dir, pool string, o machine.Options) error {
	for {
		cfg, err := lane.ReadConfig(lane.LaneDir(dir, pool))
		if err := configError(pool, err); err != nil {
			return err
		}
		shown := max(cfg.Slots, 1)
		ans, err := tm.line(fmt.Sprintf("pool %s: %d slot(s). New cap (enter keeps %d): ", pool, shown, shown))
		if err != nil {
			return err
		}
		if ans == "" {
			return nil
		}
		n, err := strconv.Atoi(ans)
		if err != nil || n < 1 {
			fmt.Fprintf(tm.out, "not a whole number of at least 1: %s\n", ans)
			continue
		}
		_, err = machine.WriteStep(dir, pool, "init", o, func(_ *machine.Registry, c *lane.Config) error {
			if max(c.Slots, 1) != shown {
				return machine.ErrLinkMoved
			}
			if n == shown && c.Slots == n {
				return lane.ErrNoChange
			}
			c.Slots = n
			return nil
		})
		if errors.Is(err, machine.ErrLinkMoved) {
			fmt.Fprintf(tm.out, "incoda: pool %s was changed while you answered; asking again\n", pool)
			continue
		}
		if err != nil {
			return machineExit(err)
		}
		return nil
	}
}

// askNewPool asks for a new pool's cap and description and adds it.
func askNewPool(tm *terminal, dir, name string, o machine.Options) error {
	if err := lane.ValidateKey(name); err != nil {
		fmt.Fprintf(tm.out, "not a valid pool name: %v\n", err)
		return nil
	}
	slots := 0
	for slots == 0 {
		ans, err := tm.line(fmt.Sprintf("pool %s: how many jobs at once (enter for 1): ", name))
		if err != nil {
			return err
		}
		if ans == "" {
			slots = 1
			break
		}
		if n, err := strconv.Atoi(ans); err == nil && n >= 1 {
			slots = n
			break
		}
		fmt.Fprintf(tm.out, "not a whole number of at least 1: %s\n", ans)
	}
	var desc *string
	for {
		ans, err := tm.line(fmt.Sprintf("pool %s: one line saying what it guards (enter for none): ", name))
		if err != nil {
			return err
		}
		if ans == "" {
			break
		}
		if err := textsafe.CheckWrite("description", ans); err != nil {
			fmt.Fprintf(tm.out, "incoda: %v\n", err)
			continue
		}
		desc = &ans
		break
	}
	res, err := machine.AddPool(dir, name, machine.PoolChange{Slots: slots, Description: desc}, o)
	var rf *machine.Refusal
	if errors.As(err, &rf) {
		fmt.Fprintf(tm.out, "incoda: %s\n", rf.Msg)
		return nil
	}
	if err != nil {
		return machineExit(err)
	}
	fmt.Fprintf(tm.out, "pool %s added (%d slot(s))\n", name, res.Config.Slots)
	return nil
}

// initPrint is init --print: no lock, no writes, no migration. On a layout
```

In `internal/cli/link.go`, replace:

```go
		return usagef("pool-mismatch: %q is a pool; a pool never links other pools", key)
	}
	o := machine.Options{Start: start, Wait: wait.d, Poll: 200 * time.Millisecond, Chain: chain, Stderr: stderr}
	for {
		cfg, err := lane.ReadConfig(lane.LaneDir(dir, key))
		if err := configError(key, err); err != nil {
			return err
		}
		shown := machine.SortedSet(cfg.Pools)
		chosen, err := pickLink(tm, dir, reg, key, shown)
		if errors.Is(err, errCancelled) {
			return usagef("link: cancelled; %s stays linked to %s", key, machine.SetText(shown))
		}
		if len(chosen) == 0 {
			return usagef("link: nothing chosen; %s stays linked to %s", key, machine.SetText(shown))
		}
		res, err := machine.WriteLink(dir, key, "link", o, func(r *machine.Registry, c *lane.Config) error {
			if bad := r.NotPools(chosen); len(bad) > 0 {
				return machine.NotAPool(r, bad)
			}
			if !machine.SameSet(c.Pools, shown) {
				return machine.ErrLinkMoved
			}
			if machine.SameSet(c.Pools, chosen) {
				return lane.ErrNoChange
			}
			c.Pools = chosen
			return nil
		})
		if errors.Is(err, machine.ErrLinkMoved) {
			fmt.Fprintf(tm.out, "incoda: %s was relinked while you chose; asking again\n", key)
			continue
		}
		if err != nil {
			return machineExit(err)
		}
		if res.Changed {
			fmt.Fprintf(stdout, "link: %s -> %s\n", machine.SetText(res.Old.Pools), machine.SetText(res.New.Pools))
		} else {
			fmt.Fprintf(stderr, "incoda: already linked: %s -> %s\n", key, machine.SetText(res.New.Pools))
		}
		return nil
	}
}

```

with:

```go
		return usagef("pool-mismatch: %q is a pool; a pool never links other pools", key)
	}
	o := machine.Options{Start: start, Wait: wait.d, Poll: 200 * time.Millisecond, Chain: chain, Stderr: stderr}
	res, shown, err := askLink(tm, dir, reg, key, "link", o, nil)
	switch {
	case errors.Is(err, errCancelled):
		return usagef("link: cancelled; %s stays linked to %s", key, machine.SetText(shown))
	case errors.Is(err, errNothingChosen):
		return usagef("link: nothing chosen; %s stays linked to %s", key, machine.SetText(shown))
	case err != nil:
		return machineExit(err)
	case res.Changed:
		fmt.Fprintf(stdout, "link: %s -> %s\n", machine.SetText(res.Old.Pools), machine.SetText(res.New.Pools))
	default:
		fmt.Fprintf(stderr, "incoda: already linked: %s -> %s\n", key, machine.SetText(res.New.Pools))
	}
	return nil
}

// errNothingChosen is an empty selection in the picker.
var errNothingChosen = errors.New("nothing chosen")

// askLink asks for key's link until an answer is written. The link shown
// is read with no lock held; the answer is a compare-and-set against it
// under machine.lock and the lane's registry lock, and a link that moved
// meanwhile is shown again with its new value (spec 4.3). quiet, when set,
// says whether a chosen set also sets quiet_machine. It returns the write,
// the link last shown, and errCancelled or errNothingChosen when nothing
// was written.
func askLink(tm *terminal, dir string, reg *machine.Registry, key, by string, o machine.Options, quiet func([]string) bool) (machine.LinkResult, []string, error) {
	for {
		cfg, err := lane.ReadConfig(lane.LaneDir(dir, key))
		if err := configError(key, err); err != nil {
			return machine.LinkResult{}, nil, err
		}
		shown := machine.SortedSet(cfg.Pools)
		chosen, err := pickLink(tm, dir, reg, key, shown)
		if err != nil {
			return machine.LinkResult{}, shown, err
		}
		if len(chosen) == 0 {
			return machine.LinkResult{}, shown, errNothingChosen
		}
		res, err := machine.WriteLink(dir, key, by, o, func(r *machine.Registry, c *lane.Config) error {
			if bad := r.NotPools(chosen); len(bad) > 0 {
				return machine.NotAPool(r, bad)
			}
			if !machine.SameSet(c.Pools, shown) {
				return machine.ErrLinkMoved
			}
			q := c.QuietMachine || (quiet != nil && quiet(chosen))
			if machine.SameSet(c.Pools, chosen) && q == c.QuietMachine {
				return lane.ErrNoChange
			}
			c.Pools, c.QuietMachine = chosen, q
			return nil
		})
		if errors.Is(err, machine.ErrLinkMoved) {
			fmt.Fprintf(tm.out, "incoda: %s was relinked while you chose; asking again\n", key)
			continue
		}
		return res, shown, err
	}
}

```

In `internal/machine/link.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file when you reach it). Since plan 3a split `WriteFirstLinks` out (commit bbe31a2), the pool check lives in `writeLinkHeld`, so the project-only flag goes through it; `WriteFirstLinks` passes `true`:

(1 of 2) Replace:

```go
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
	return writeLinkHeld(stateDir, key, by, o, reg, fn)
}

// writeLinkHeld is WriteLink once machine.lock is held and machine.json
// read: it refuses a pool key, then does the load-modify-store under key's
// registry lock and logs event=link when the pools or quiet_machine
// change.
func writeLinkHeld(stateDir, key, by string, o Options, reg *Registry, fn func(reg *Registry, c *lane.Config) error) (LinkResult, error) {
	if reg.IsPool(key) {
```

with:

```go
// The lane is created when missing (a link on a fresh key is a project
// lane's first config). Registry lock waits stay inside o's --wait budget.
func WriteLink(stateDir, key, by string, o Options, fn func(reg *Registry, c *lane.Config) error) (LinkResult, error) {
	return writeStep(stateDir, key, by, o, true, fn)
}

// WriteStep is one answer of the interactive init (spec 4.3) applied to
// any lane, pools included: machine.lock, then the lane's registry lock,
// one load-modify-store that fn may refuse or skip (lane.ErrNoChange), so
// the answer is applied only if the state is still what the question
// showed.
func WriteStep(stateDir, key, by string, o Options, fn func(reg *Registry, c *lane.Config) error) (LinkResult, error) {
	return writeStep(stateDir, key, by, o, false, fn)
}

// writeStep takes machine.lock, reads machine.json under it and hands over
// to writeLinkHeld. project says whether key must be a project lane.
func writeStep(stateDir, key, by string, o Options, project bool, fn func(reg *Registry, c *lane.Config) error) (LinkResult, error) {
	lk, err := AcquireLock(stateDir, o.lockOptions("link"))
	if err != nil {
		return LinkResult{}, err
	}
	defer lk.Release()
	reg, err := ReadRegistry(stateDir)
	if err != nil {
		return LinkResult{}, err
	}
	return writeLinkHeld(stateDir, key, by, o, reg, project, fn)
}

// writeLinkHeld is WriteLink once machine.lock is held and machine.json
// read: when project is set (WriteLink, WriteFirstLinks) it refuses a pool
// key, then does the load-modify-store under key's registry lock and logs
// event=link when the pools or quiet_machine change. WriteStep passes
// false: an init answer may set a pool's cap.
func writeLinkHeld(stateDir, key, by string, o Options, reg *Registry, project bool, fn func(reg *Registry, c *lane.Config) error) (LinkResult, error) {
	if project && reg.IsPool(key) {
```

(2 of 2) Replace:

```go
		res, err := writeLinkHeld(stateDir, fl.Key, by, o, reg, func(_ *Registry, c *lane.Config) error {
```

with:

```go
		res, err := writeLinkHeld(stateDir, fl.Key, by, o, reg, true, func(_ *Registry, c *lane.Config) error {
```

- [ ] **Step 4: Run the tests to see them pass**

Run (bash), one at a time:

- `go test ./internal/cli/ -run 'TestInitAsks|TestInitAsksAgainWhenTheStateChanged' -count=1`
- `go test . -run 'TestInitNeedsATerminal' -count=1`

Expected: `ok` for each package.

- [ ] **Step 5: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 6: Commit**

```bash
git add init_test.go internal/cli/init_test.go internal/cli/initcmd.go internal/cli/link.go internal/machine/link.go
git commit -F - <<'MSG'
feat: incoda init asks the user for caps, resources and links, one locked step per answer

incoda init migrates first, then asks for each pool's cap, other shared
resources and a link for each unlinked lane, holding no lock while it
asks. Each answer is applied in its own step under machine.lock only if
the state is still what the question showed, else it asks again. q or
the end of input stops with the earlier answers kept.
MSG
```


---

### Task 6: Quiet machine

Spec 2.8, 2.7 and 2.5 trigger 3. `run --quiet-machine`, or `quiet_machine: true` in a named project lane's config, enrolls an exclusive `quiet` ticket on every pool in `machine.json` at plan time, one at a time in the total order. Unless `--quiet`, a run that took it from config prints on each wait notice `incoda: quiet-machine (from config of "kungfoo-measure"): holding builds; waiting tests`, so the caller knows to use a short `--wait`. A quiet plan replans when `machine.json`'s pools differ from the set it used (a pool added before the final verify is then covered; one added after it is not, as the spec states). A pool only quiet-machine brings has the role `pool, quiet-machine`. A printed run line (the fix of an unlinked, link-needs-user or pool-mismatch refusal) that takes quiet-machine, from a suggestion, the run's `--quiet-machine` or a named key's stored `quiet_machine`, is checked by `lineRefused` against every pool, so a closed pool outside the link rules the line out (spec 2.6). The nested quiet refusal is plan 4.

**Files:**
- Modify: `internal/cli/run.go`
- Modify: `internal/lane/queue.go`
- Modify: `internal/lane/ticket.go`
- Modify: `internal/runplan/runplan.go`
- Modify: `internal/runplan/refusals.go`
- Test: `integration_test.go`
- Test: `internal/cli/run_test.go`
- Test: `internal/runplan/runplan_test.go`
- Test: `pools_test.go`

**Interfaces:**
- Consumes: `runplan.Make`, `runplan.Plan.Changed` (plan 3a Tasks 5 and 9), `machine.SameSet`, `runplan.fixFor` and `lineRefused` (plan 3a, commit b001b41).
- Produces: `runplan.Request.QuietMachine bool`; `runplan.Lane.Quiet bool`; `runplan.Plan` gains `Quiet bool`, `QuietFrom string`, `RegPools []string`; `lane.Ticket.Quiet bool` (`json:"quiet,omitempty"`), logged `quiet=true`; the `run --quiet-machine` flag; `cli.quietLine(from string, parts []*lanePart, waiting string) string`; `lineRefused` gains a `quiet bool` parameter (fixFor passes its own); root `ticketPayload.Quiet`.

- [ ] **Step 1: Write the failing tests**

Append to the end of `internal/runplan/runplan_test.go`:

```go

// TestQuietMachine: quiet-machine, from the flag or a named project's
// config, takes every pool in machine.json with a quiet ticket, and a quiet
// plan replans when the pool set changes (spec 2.8, 2.5 trigger 3).
func TestQuietMachine(t *testing.T) {
	state, reg := machineDir(t, map[string]string{
		"proj":       `{"schema":2,"pools":["tests"]}`,
		"kf-measure": `{"schema":2,"pools":["tests"],"quiet_machine":true}`,
	})
	p, err := Make(state, reg, Request{Named: []string{"proj"}, QuietMachine: true})
	if err != nil {
		t.Fatal(err)
	}
	want := "proj builds(pool, quiet-machine) computer-use(pool, quiet-machine) tests(pool, via proj) vm(pool, quiet-machine)"
	if got := keysOf(p); got != want || !p.Quiet || p.QuietFrom != "" {
		t.Fatalf("flag:\n got %s\nwant %s", got, want)
	}
	for _, l := range p.Lanes {
		if l.Pool != l.Quiet {
			t.Fatalf("every pool and only the pools are quiet: %+v", l)
		}
	}
	q, err := Make(state, reg, Request{Named: []string{"kf-measure"}})
	if err != nil || !q.Quiet || q.QuietFrom != "kf-measure" || len(q.Lanes) != 5 {
		t.Fatalf("config: %+v %v", q, err)
	}
	plain, err := Make(state, reg, Request{Named: []string{"proj"}})
	if err != nil || plain.Quiet {
		t.Fatal(err)
	}
	body := `{"schema":1,"layout":2,"generation":4,"pools":["builds","computer-use","gpu","tests","vm"]}`
	if err := os.WriteFile(machine.RegistryPath(state), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if why, err := q.Changed(state); err != nil || why != "the pools on this machine changed: builds,computer-use,tests,vm -> builds,computer-use,gpu,tests,vm" {
		t.Fatalf("quiet: %q %v", why, err)
	}
	if why, err := plain.Changed(state); err != nil || why != "" {
		t.Fatalf("a plan without quiet-machine does not care: %q %v", why, err)
	}
}

// TestQuietFixLineChecksEveryPool: a printed run line that takes
// quiet-machine takes every pool, so a closed pool its link never names
// still rules the line out (spec 2.6, 2.8): from a suggestion that carries
// quiet_machine, from the run's --quiet-machine, and from a named key's
// stored quiet_machine.
func TestQuietFixLineChecksEveryPool(t *testing.T) {
	state, reg := machineDir(t, map[string]string{
		"builds":       `{"schema":2,"slots":1}`,
		"computer-use": `{"schema":2,"slots":1}`,
		"tests":        `{"schema":2,"slots":1}`,
		"vm":           `{"schema":2,"slots":1,"closed":"host down"}`,
		"quiet-proj":   `{"schema":2,"pools":["tests"],"quiet_machine":true}`,
	})
	fix := fixline.Run{Argv: []string{"x"}, Dir: "/src", Here: "/src"}
	for _, c := range []struct {
		name string
		req  Request
		lead string
	}{
		{"a suggestion with quiet_machine", Request{Named: []string{"kungfoo-measure"}, Fix: fix}, "unlinked: kungfoo-measure"},
		{"several keys, one suggestion with quiet_machine", Request{Named: []string{"kungfoo-measure", "cap-gate"}, Fix: fix}, "unlinked: cap-gate, kungfoo-measure"},
		{"--quiet-machine on an unlinked key", Request{Named: []string{"cap-gate"}, QuietMachine: true, Fix: fix}, "unlinked: cap-gate"},
		{"a stored quiet_machine", Request{Named: []string{"quiet-proj"}, Pool: []string{"computer-use"}, Fix: fix}, "pool-mismatch:"},
	} {
		_, err := Make(state, reg, c.req)
		var rf *machine.Refusal
		if !errors.As(err, &rf) {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !strings.HasPrefix(rf.Msg, c.lead) || strings.Contains(rf.Msg, "incoda run ") ||
			!strings.Contains(rf.Msg, `no runnable command (queue "vm" is closed (pool, quiet-machine))`) {
			t.Errorf("%s: want no runnable line, ruled out by the closed vm:\n%s", c.name, rf.Msg)
		}
	}
	// Without quiet-machine the same closed pool is not in the line's set.
	_, err := Make(state, reg, Request{Named: []string{"cap-gate"}, Fix: fix})
	var rf *machine.Refusal
	if !errors.As(err, &rf) || !strings.Contains(rf.Msg, "  incoda run --queue cap-gate --pool tests") {
		t.Fatalf("a line without quiet-machine ignores vm: %v", err)
	}
}
```

Append to the end of `internal/cli/run_test.go`:

```go

// TestQuietPlanReplansWhenAPoolIsAdded: a pool added before a quiet run's
// final verify makes it replan and take that pool too (spec 2.8).
func TestQuietPlanReplansWhenAPoolIsAdded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /usr/bin/true")
	}
	dir := t.TempDir()
	t.Setenv("INCODA_DIR", dir)
	if code := Main([]string{"config", "q-measure", "--pool", "tests", "--quiet-machine"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("config: %d", code)
	}
	saved := atVerify
	defer func() { atVerify = saved }()
	done := false
	atVerify = func(dir, key string) {
		if key == "q-measure" && !done {
			done = true
			if _, err := machine.AddPool(dir, "gpu", machine.PoolChange{}, machine.Options{Start: time.Now(), Wait: 5 * time.Second}); err != nil {
				t.Error(err)
			}
		}
	}
	var stderr bytes.Buffer
	if code := Main([]string{"run", "--queue", "q-measure", "--", "true"}, io.Discard, &stderr); code != 0 {
		t.Fatalf("run: %d\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "incoda: replan: the pools on this machine changed: builds,computer-use,tests,vm -> builds,computer-use,gpu,tests,vm\n") {
		t.Fatalf("missing the replan line:\n%s", stderr.String())
	}
	if !strings.Contains(laneLog(dir, "gpu"), fmt.Sprintf("event=enqueue pid=%d slots=1 exclusive=true quiet=true", os.Getpid())) {
		t.Fatalf("the replanned quiet run takes the new pool:\n%s", laneLog(dir, "gpu"))
	}
}
```

In `integration_test.go`, replace:

```go
	Dir       string   `json:"cwd"`
	Via       []string `json:"via"`
	Wait      string   `json:"wait"`
}

type entry struct {
```

with:

```go
	Dir       string   `json:"cwd"`
	Via       []string `json:"via"`
	Wait      string   `json:"wait"`
	Quiet     bool     `json:"quiet"`
}

type entry struct {
```

Append to the end of `pools_test.go`:

```go

// TestQuietMachine: quiet_machine in a project lane's config makes its
// runs take every pool with an exclusive quiet ticket, one at a time in
// the total order, saying what they hold while they wait; --quiet-machine
// does the same for one run, and nothing else runs on any pool meanwhile
// (spec 2.8).
func TestQuietMachine(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	mustRun(t, incoda, state, 0, "config", "kf-measure", "--pool", "tests", "--quiet-machine")
	h, _ := startHolder(t, incoda, stamp, state, "builds", "h", 1500, "50ms")
	defer func() { _ = h.Process.Kill(); _ = h.Wait() }()
	waitFor(t, incoda, state, "builds", func(q queueReport) bool { return len(q.Holders) == 1 })
	out := mustRun(t, incoda, state, 0, "run", "--queue", "kf-measure", "--wait", "30s", "--poll", "50ms",
		"--", stamp, filepath.Join(t.TempDir(), "m.txt"), "m", "10")
	inOrder(t, out,
		`incoda: queue "builds" busy (pool, quiet-machine; 1 slot(s), 1 ahead of you)`,
		`incoda: quiet-machine (from config of "kf-measure"): holding nothing; waiting builds`,
		`acquired queue "builds" (pool, quiet-machine; pid `, `acquired queue "computer-use" (pool, quiet-machine; pid `,
		`acquired queue "tests" (pool, via kf-measure; pid `, `acquired queue "vm" (pool, quiet-machine; pid `)
	for _, pool := range []string{"builds", "computer-use", "tests", "vm"} {
		log, _ := os.ReadFile(filepath.Join(laneDir(state, pool), "lane.log"))
		if !strings.Contains(string(log), " exclusive=true") || !strings.Contains(string(log), " quiet=true") {
			t.Fatalf("%s: a quiet ticket is exclusive:\n%s", pool, log)
		}
	}

	q := exec.Command(incoda, "run", "--queue", "builds", "--quiet-machine", "--poll", "50ms", "--quiet",
		"--", stamp, filepath.Join(t.TempDir(), "q.txt"), "q", "2000")
	q.Env = laneEnv(state)
	if err := q.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = q.Process.Kill(); _ = q.Wait() }()
	waitFor(t, incoda, state, "vm", func(q queueReport) bool { return len(q.Holders) == 1 })
	if tk := statusJSON(t, incoda, state, "vm").Queues[0].Holders[0].Ticket; !tk.Quiet || !tk.Exclusive {
		t.Fatalf("vm ticket: %+v", tk)
	}
	mustRun(t, incoda, state, 121, "run", "--queue", "vm", "--wait", "0", "--", stamp, filepath.Join(t.TempDir(), "v.txt"), "v", "1")
	if err := q.Wait(); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run (bash), one at a time:

- `go test ./internal/runplan/ -run 'TestQuietMachine|TestQuietFixLineChecksEveryPool' -count=1 -timeout 120s`
- `go test ./internal/cli/ -run 'TestQuietPlanReplansWhenAPoolIsAdded' -count=1 -timeout 120s`
- `go test . -run 'TestQuietMachine' -count=1 -timeout 120s`

Expected: the `internal/runplan` test build fails (`unknown field QuietMachine in struct literal of type Request`, `p.Quiet undefined`); `TestQuietPlanReplansWhenAPoolIsAdded` fails with `missing the replan line` and the root `TestQuietMachine` with a missing `queue "builds" busy (pool, quiet-machine; ...)` line: both runs took only their project lane and `tests`.

- [ ] **Step 3: Implement**

In `internal/cli/run.go`, make these 5 replacements, in order (each quoted block occurs exactly once in the file when you reach it). Since commit 6655267 `takeLane` opens with the `busy` closure, so the third block starts at the ticket literal:

(1 of 5) Replace:

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

with:

```go
	noColor := fs.Bool("no-color", false, "never emit ANSI color, even on a terminal (the NO_COLOR environment variable does the same)")
	wait := &waitValue{d: 30 * time.Minute}
	fs.Var(wait, "wait", "max time to queue: a Go duration (30m) or bare seconds (1800); 0 fails immediately, negative waits forever")
	quietMachine := fs.Bool("quiet-machine", false, "take every pool on this machine with an exclusive ticket, for a measurement that needs a quiet machine; use a short --wait (5m) and retry")
	pool := &poolsValue{}
	fs.Var(pool, "pool", "take only these of each named project key's linked pools (comma-separated); on an unlinked key, a set equal to its suggestion becomes its first link")
	fs.Var(pool, "pools", "alias of --pool")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: incoda run --queue KEY[,KEY...] [--pool P,P] [--slots N] [--exclusive] [--quiet-machine] [--wait DUR] [--reason TEXT] [--owner WHO] [--] <cmd...>\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
```

(2 of 5) Replace:

```go
	live := inherited.LiveKeys()
	here, _ := os.Getwd()
	req := runplan.Request{
		Named: keys, Slots: *slots, Exclusive: *exclusive, Reason: *reason, Held: pass,
		Fix:       fixline.Run{Flags: carriedFlags(fs, wait), Argv: argv, Dir: here, Here: here},
		WaitGiven: wait.set,
	}
```

with:

```go
	live := inherited.LiveKeys()
	here, _ := os.Getwd()
	req := runplan.Request{
		Named: keys, Slots: *slots, Exclusive: *exclusive, Reason: *reason, Held: pass, QuietMachine: *quietMachine,
		Fix:       fixline.Run{Flags: carriedFlags(fs, wait), Argv: argv, Dir: here, Here: here},
		WaitGiven: wait.set,
	}
```

(3 of 5) Replace:

```go
		t := lane.Ticket{
			// --exclusive holds a lane the run names; it does not
			// propagate to the pools a link brings (spec 2.4).
			Exclusive: *exclusive && (pt.l.Named || !pt.l.Pool),
			Command:   argv,
			Reason:    *reason,
			Owner:     *owner,
```

with:

```go
		t := lane.Ticket{
			// --exclusive holds a lane the run names; it does not
			// propagate to the pools a link brings (spec 2.4). A
			// quiet-machine ticket is exclusive on every pool (2.8).
			Exclusive: (*exclusive && (pt.l.Named || !pt.l.Pool)) || pt.l.Quiet,
			Quiet:     pt.l.Quiet,
			Command:   argv,
			Reason:    *reason,
			Owner:     *owner,
```

(4 of 5) Replace:

```go
				}
				for _, u := range unpooled {
					fmt.Fprintf(stderr, "%s   %s\n", p.Dim("incoda:"), p.Dim(u.Line()))
				}
			},
		})
```

with:

```go
				}
				for _, u := range unpooled {
					fmt.Fprintf(stderr, "%s   %s\n", p.Dim("incoda:"), p.Dim(u.Line()))
				}
				if plan.QuietFrom != "" {
					// A run that took quiet-machine from config says
					// what it holds while it waits, so the caller knows
					// to use a short --wait (spec 2.8).
					fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"), p.Yellow(quietLine(plan.QuietFrom, toTake, key)))
				}
			},
		})
```

(5 of 5) Replace:

```go
	return out
}

// viaText is the " via <keys>" a holder line of a pool ticket ends with
// (spec 5.1); empty for a ticket taken directly.
func viaText(via []string) string {
```

with:

```go
	return out
}

// quietLine is the informational line of a run that took quiet-machine
// from config: "quiet-machine (from config of "K"): holding builds;
// waiting tests".
func quietLine(from string, parts []*lanePart, waiting string) string {
	var holding []string
	for _, pt := range parts {
		if pt.l.Pool && pt.en != nil && pt.key != waiting {
			holding = append(holding, pt.key)
		}
	}
	h := "nothing"
	if len(holding) > 0 {
		h = strings.Join(holding, ", ")
	}
	return fmt.Sprintf("quiet-machine (from config of %q): holding %s; waiting %s", from, h, waiting)
}

// viaText is the " via <keys>" a holder line of a pool ticket ends with
// (spec 5.1); empty for a ticket taken directly.
func viaText(via []string) string {
```

In `internal/lane/queue.go`, replace:

```go
	if len(en.ticket.Via) > 0 {
		extra += " via=" + strings.Join(en.ticket.Via, ",")
	}
	q.Logf("queue=%s event=enqueue pid=%d slots=%d%s%s cmd=%s", q.Key, en.ticket.PID, en.ticket.Slots, extra, en.ticket.attribution(), textsafe.LogValue(en.ticket.CommandString()))
	return en, nil
}
```

with:

```go
	if len(en.ticket.Via) > 0 {
		extra += " via=" + strings.Join(en.ticket.Via, ",")
	}
	if en.ticket.Quiet {
		extra += " quiet=true"
	}
	q.Logf("queue=%s event=enqueue pid=%d slots=%d%s%s cmd=%s", q.Key, en.ticket.PID, en.ticket.Slots, extra, en.ticket.attribution(), textsafe.LogValue(en.ticket.CommandString()))
	return en, nil
}
```

In `internal/lane/ticket.go`, replace:

```go
	Via []string `json:"via,omitempty"`
	// Wait is the run's --wait as given; empty when it was not given.
	Wait string `json:"wait,omitempty"`
}

// attribution is the k=v block every lifecycle line (enqueue/acquire/release)
```

with:

```go
	Via []string `json:"via,omitempty"`
	// Wait is the run's --wait as given; empty when it was not given.
	Wait string `json:"wait,omitempty"`
	// Quiet marks a quiet-machine ticket on a pool (spec 2.7, 2.8); it is
	// also Exclusive.
	Quiet bool `json:"quiet,omitempty"`
}

// attribution is the k=v block every lifecycle line (enqueue/acquire/release)
```

In `internal/runplan/runplan.go`, make these 7 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 7) Replace:

```go
	// re-checked: the ancestor's ticket was admitted under its rules
	// (spec 4.5).
	Held map[string]bool
	// Pool is the --pool set (alias --pools), sorted; nil when not given.
	// It applies to every named project key (spec 4.2).
	Pool []string
```

with:

```go
	// re-checked: the ancestor's ticket was admitted under its rules
	// (spec 4.5).
	Held map[string]bool
	// QuietMachine is --quiet-machine (spec 2.8).
	QuietMachine bool
	// Pool is the --pool set (alias --pools), sorted; nil when not given.
	// It applies to every named project key (spec 4.2).
	Pool []string
```

(2 of 7) Replace:

```go
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
```

with:

```go
	Via []string
	// Cfg is the lane's config as read at plan time.
	Cfg lane.Config
	// Quiet marks a pool a quiet-machine run takes with an exclusive quiet
	// ticket (spec 2.8).
	Quiet bool
}

// Role says how a pool lane is reached, for busy, holder, timeout and
// refusal lines (spec 5.1): "pool, via cap-gate", "pool" for direct use,
// "pool, quiet-machine" for a pool only quiet-machine brings, and "" for a
// project lane, whose lines stay as they were.
func (l Lane) Role() string {
	switch {
	case !l.Pool:
		return ""
	case len(l.Via) > 0:
		return "pool, via " + strings.Join(l.Via, ",")
	case l.Quiet && !l.Named:
		return "pool, quiet-machine"
	}
	return "pool"
}

// StatusKey is the key a timeout line sends the caller to: the run's own
```

(3 of 7) Replace:

```go
	FirstLinks []FirstLink
	// Notes are informational lines for the caller to print.
	Notes []string
}

// FirstLink is a link run writes: Key gets Pools, and quiet_machine when
```

with:

```go
	FirstLinks []FirstLink
	// Notes are informational lines for the caller to print.
	Notes []string
	// Quiet is set when the run takes quiet-machine: an exclusive quiet
	// ticket on every pool in machine.json at plan time (spec 2.8).
	// QuietFrom names the named project key whose config asked for it, ""
	// when --quiet-machine did. RegPools is machine.json's pools at plan
	// time, which a quiet plan must still match (replan trigger 3).
	Quiet     bool
	QuietFrom string
	RegPools  []string
}

// FirstLink is a link run writes: Key gets Pools, and quiet_machine when
```

(4 of 7) Replace:

```go

// Make computes the plan for req against reg and the lanes' configs.
func Make(stateDir string, reg *machine.Registry, req Request) (*Plan, error) {
	p := &Plan{Generation: reg.Generation, Links: map[string][]string{}, Pool: req.Pool}
	lanes := map[string]*Lane{}
	named := append([]string(nil), req.Named...)
	sort.Strings(named)
```

with:

```go

// Make computes the plan for req against reg and the lanes' configs.
func Make(stateDir string, reg *machine.Registry, req Request) (*Plan, error) {
	p := &Plan{Generation: reg.Generation, Links: map[string][]string{}, Pool: req.Pool,
		Quiet: req.QuietMachine, RegPools: append([]string(nil), reg.Pools...)}
	lanes := map[string]*Lane{}
	named := append([]string(nil), req.Named...)
	sort.Strings(named)
```

(5 of 7) Replace:

```go
				return nil, err
			}
			pl.Via = append(pl.Via, k)
		}
	}

```

with:

```go
				return nil, err
			}
			pl.Via = append(pl.Via, k)
		}
	}

	// Quiet-machine, from the flag or a named project lane's config, takes
	// every pool in machine.json, each with an exclusive quiet ticket.
	for _, k := range projects {
		if lanes[k].Cfg.QuietMachine && p.QuietFrom == "" {
			p.Quiet, p.QuietFrom = true, k
		}
	}
	if p.Quiet {
		for _, pool := range reg.Pools {
			pl := lanes[pool]
			if pl == nil {
				cfg, err := lane.ReadConfig(lane.LaneDir(stateDir, pool))
				if err != nil {
					return nil, &machine.StateError{Msg: fmt.Sprintf("machine-state: quiet-machine takes pool %q: %s", pool, textsafe.Escape(err.Error()))}
				}
				pl = &Lane{Key: pool, Pool: true, Cfg: cfg}
				lanes[pool] = pl
			}
			pl.Quiet = true
		}
	}

```

(6 of 7) Replace:

```go
//     registry;
//  2. a named project key's link changed, unless the run's --pool subset
//     is still part of it (configs are re-read whatever the generation
//     says, since links live in config.json).
//
// Every pool the plan reaches through a link must still resolve; one that
// does not fails closed with machine-state, as at plan time.
```

with:

```go
//     registry;
//  2. a named project key's link changed, unless the run's --pool subset
//     is still part of it (configs are re-read whatever the generation
//     says, since links live in config.json);
//  3. for a quiet plan, machine.json's pools differ from the set the plan
//     used.
//
// Every pool the plan reaches through a link must still resolve; one that
// does not fails closed with machine-state, as at plan time.
```

(7 of 7) Replace:

```go
				return fmt.Sprintf("queue %q became a pool", l.Key), nil
			}
		}
	}
	keys := make([]string, 0, len(p.Links))
	for k := range p.Links {
```

with:

```go
				return fmt.Sprintf("queue %q became a pool", l.Key), nil
			}
		}
	}
	if p.Quiet && !machine.SameSet(reg.Pools, p.RegPools) {
		return fmt.Sprintf("the pools on this machine changed: %s -> %s", machine.SetText(p.RegPools), machine.SetText(reg.Pools)), nil
	}
	keys := make([]string, 0, len(p.Links))
	for k := range p.Links {
```

In `internal/runplan/refusals.go`, make these 3 replacements, in order (each quoted block occurs exactly once in the file when you reach it). A printed run line is checked against every lane it would take (plan 3a, commit b001b41); a line that takes quiet-machine takes every pool, so the check must see them too:

(1 of 3) Replace:

```go
// and the reason no runnable line may be printed, if a lane the line would
// take is closed or requires a reason and the run has none (spec 2.6).
func fixFor(stateDir string, reg *machine.Registry, req Request, queue, pool []string, links map[string][]string, quiet bool) (fixline.Run, string) {
	r := req.Fix
	r.Queue, r.Pool = queue, pool
	r.Flags = append([]fixline.Flag(nil), req.Fix.Flags...)
	if quiet && !req.WaitGiven {
		r.Flags = append(r.Flags, fixline.Flag{Name: "wait", Value: quietWait})
	}
	return r, lineRefused(stateDir, reg, req, queue, pool, links)
}
```

with:

```go
// and the reason no runnable line may be printed, if a lane the line would
// take is closed or requires a reason and the run has none (spec 2.6).
// quiet says a printed config line sets quiet_machine, so the line takes
// every pool.
func fixFor(stateDir string, reg *machine.Registry, req Request, queue, pool []string, links map[string][]string, quiet bool) (fixline.Run, string) {
	r := req.Fix
	r.Queue, r.Pool = queue, pool
	r.Flags = append([]fixline.Flag(nil), req.Fix.Flags...)
	if quiet && !req.WaitGiven {
		r.Flags = append(r.Flags, fixline.Flag{Name: "wait", Value: quietWait})
	}
	return r, lineRefused(stateDir, reg, req, queue, pool, links, quiet)
}
```

(2 of 3) Replace:

```go
// the fix sets it, else as stored), narrowed by the line's --pool. It
// says why the first lane in total order that would refuse the line does,
// or "" when none would.
func lineRefused(stateDir string, reg *machine.Registry, req Request, queue, pool []string, links map[string][]string) string {
```

with:

```go
// the fix sets it, else as stored), narrowed by the line's --pool, plus
// every pool in machine.json when the line takes quiet-machine (quiet: a
// printed config line sets it; the run's --quiet-machine, which the line
// carries; or a named project key's stored quiet_machine, spec 2.8). It
// says why the first lane in total order that would refuse the line does,
// or "" when none would.
func lineRefused(stateDir string, reg *machine.Registry, req Request, queue, pool []string, links map[string][]string, quiet bool) string {
```

(3 of 3) Replace:

```go
		for _, p := range use {
			l := add(p, true)
			l.Via = append(l.Via, k)
		}
	}
	ls := make([]Lane, 0, len(lanes))
```

with:

```go
		for _, p := range use {
			l := add(p, true)
			l.Via = append(l.Via, k)
		}
		quiet = quiet || lanes[k].Cfg.QuietMachine
	}
	if quiet || req.QuietMachine {
		for _, p := range reg.Pools {
			add(p, true).Quiet = true
		}
	}
	ls := make([]Lane, 0, len(lanes))
```

- [ ] **Step 4: Run the tests to see them pass**

Run (bash), one at a time:

- `go test ./internal/runplan/ -run 'TestQuietMachine|TestQuietFixLineChecksEveryPool|TestFixLineChecksTheLaneSetItTakes|TestUnlinkedRefusal' -count=1`
- `go test ./internal/cli/ -run 'TestQuietPlanReplansWhenAPoolIsAdded' -count=1`
- `go test . -run 'TestQuietMachine' -count=1`

Expected: `ok` for each package.

- [ ] **Step 5: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 6: Commit**

```bash
git add integration_test.go internal/cli/run.go internal/cli/run_test.go internal/lane/queue.go internal/lane/ticket.go internal/runplan/refusals.go internal/runplan/runplan.go internal/runplan/runplan_test.go pools_test.go
git commit -F - <<'MSG'
feat: quiet-machine takes every pool with an exclusive quiet ticket

run --quiet-machine, or quiet_machine in a named project lane's config,
takes every pool in machine.json with an exclusive quiet ticket in the
total order, says what it holds while it waits when it came from config,
and replans when the set of pools changes before its final verify. A
printed run line that takes quiet-machine is checked against every pool.
MSG
```


---

### Task 7: incoda help documents the pool commands and every refusal prefix

Spec 5.1: every refusal block starts with a stable prefix documented in `incoda help` (README and AGENT-RULE.md are plan 5). The usage gains `--pool`, `--quiet-machine`, `pools`, the config link flags, `link`, `init` and `pools add|remove`; a paragraph explains pools and links; a table lists each prefix with its exit code, and the informational lines. Prefixes that only plan 4 prints (`out-of-order-busy:`, `self-wait:`, `quiet-nested:`, `nested-refused:`, `held-lost:`, `exclusive-ignored:`) are added by plan 4.

**Files:**
- Modify: `internal/cli/cli.go`
- Test: `internal/cli/help_test.go` (new)

**Interfaces:**
- Consumes: nothing new.
- Produces: the new `rootUsage` text; test `TestHelpDocumentsEveryPrefix`.

- [ ] **Step 1: Write the failing tests**

Create `internal/cli/help_test.go`:

```go
package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// TestHelpDocumentsEveryPrefix: incoda help names every refusal prefix and
// informational line a run, a setup command or the upgrade can print
// (spec 5.1), and every command.
func TestHelpDocumentsEveryPrefix(t *testing.T) {
	var out bytes.Buffer
	if code := Main([]string{"help"}, &out, io.Discard); code != 0 {
		t.Fatalf("help: %d", code)
	}
	for _, want := range []string{
		"unlinked:", "link-conflict:", "pool-mismatch:", "link-exists:", "link-needs-user:",
		"closed-while-waiting:", "upgrade-blocked:", "upgrade-pending:", "kind-busy:", "pool-linked:",
		"needs-terminal:", "bad-text:", "machine-lock-timeout:", "upgrade-timeout:", "machine-state:",
		"replan:", "linked:", "quiet-machine", "upgrade-wait:", "upgrade-warning:", "waiting for machine.lock:",
		"held-dropped:", "migrated:",
		"incoda pools [--json]", "incoda link KEY", "incoda init [--print | --apply-suggestions]",
		"incoda pools add NAME", "incoda pools remove NAME", "--pool P,P", "--quiet-machine",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help does not mention %q", want)
		}
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run (bash): `go test ./internal/cli/ -run 'TestHelpDocumentsEveryPrefix' -count=1 -timeout 120s`

Expected: `TestHelpDocumentsEveryPrefix` fails with `help does not mention "unlinked:"` and one such line for every other prefix and command.

- [ ] **Step 3: Implement**

In `internal/cli/cli.go`, make these 3 replacements, in order (each quoted block occurs exactly once in the file when you reach it):

(1 of 3) Replace:

```go
const rootUsage = `incoda - keyed queueing for heavy processes (builds, GUI/UI test runs)

usage:
  incoda run --queue KEY[,KEY...] [--slots N] [--exclusive] [--wait DUR] [--reason TEXT] [--owner WHO] [--] <cmd...>
  incoda status [--queue KEY] [--all] [--json]
  incoda watch [--queue KEY] [--interval 2s] [--once | --plain]
  incoda queues
  incoda config KEY [--slots N] [--description TEXT] [--require-reason] [--close MSG | --open] [--wait DUR]
  incoda kill --queue KEY --pid N --reason TEXT [--wait 5s] [--force]
  incoda force-release --queue KEY [--live]
  incoda doctor [--rebuild-registry POOL,POOL... [--wait DUR]]
```

with:

```go
const rootUsage = `incoda - keyed queueing for heavy processes (builds, GUI/UI test runs)

usage:
  incoda run --queue KEY[,KEY...] [--pool P,P] [--slots N] [--exclusive] [--quiet-machine] [--wait DUR] [--reason TEXT] [--owner WHO] [--] <cmd...>
  incoda status [--queue KEY] [--all] [--json]
  incoda watch [--queue KEY] [--interval 2s] [--once | --plain]
  incoda queues
  incoda pools [--json]
  incoda config KEY [--slots N] [--description TEXT] [--require-reason] [--close MSG | --open]
                    [--pool P,P [--replace] | --add-pool P,P | --remove-pool P,P | --unlink] [--quiet-machine[=false]] [--wait DUR]
  incoda link KEY [--wait DUR]
  incoda init [--print | --apply-suggestions] [--wait DUR]
  incoda pools add NAME [--slots N] [--description TEXT] [--wait DUR]
  incoda pools remove NAME [--wait DUR]
  incoda kill --queue KEY --pid N --reason TEXT [--wait 5s] [--force]
  incoda force-release --queue KEY [--live]
  incoda doctor [--rebuild-registry POOL,POOL... [--wait DUR]]
```

(2 of 3) Replace:

```go
(exit 122) and registers the machine-wide pools builds, computer-use, tests
and vm in <state>/machine.json. status, watch and queues never upgrade.

exit codes:
  <child>  run passes the command's own exit status through unchanged
  120      usage error (bad flags, missing/invalid queue key, refused force-release),
```

with:

```go
(exit 122) and registers the machine-wide pools builds, computer-use, tests
and vm in <state>/machine.json. status, watch and queues never upgrade.

Pools are machine-wide caps per resource class. Every project queue links
to the pools its jobs use, and a run takes its project lanes, then their
pools, one at a time in one order: project lanes by key, then pools by key.
A project queue with no link refuses runs (unlinked:) until it is linked;
the refusal prints the line that links it to the suggestion of its name.
Any other link is the user's: incoda link KEY, or incoda init for every
queue at once. --pool takes a subset of the link for one run. A pool can
also be named directly (--queue builds). --quiet-machine (or quiet_machine
in a queue's config) takes every pool alone, for measurements: use a short
--wait. run never reads the terminal; link and init need one.

exit codes:
  <child>  run passes the command's own exit status through unchanged
  120      usage error (bad flags, missing/invalid queue key, refused force-release),
```

(3 of 3) Replace:

```go
  124      the run was killed through the lane (incoda kill); stderr says by whom and why
  125      kill: the participant did not acknowledge in time (rerun with --force)
  130      incoda was interrupted while queueing

run 'incoda <command> -h' for per-command flags.
`
```

with:

```go
  124      the run was killed through the lane (incoda kill); stderr says by whom and why
  125      kill: the participant did not acknowledge in time (rerun with --force)
  130      incoda was interrupted while queueing

refusals start with a stable prefix; match it on any line that starts with
"incoda: <prefix>:", since informational lines can come first:
  120  unlinked:              a project queue has no link
       link-conflict:         another process linked the queue first; rerun without --pool
       pool-mismatch:         --pool is not part of the link or names no project key, or a pool links
       link-exists:           config --pool on a linked queue without --replace
       link-needs-user:       run --pool other than the suggestion; the user links it
       closed-while-waiting:  a queue was closed while this run waited on it
       upgrade-blocked:       an older incoda that is an ancestor of this run holds a lane
       upgrade-pending:       force-release --live during the state upgrade
       kind-busy:             a kind change on a queue with tickets, a link or quiet_machine
       pool-linked:           pools remove on a pool that a queue links
       needs-terminal:        link or init without a terminal; ask the user
       bad-text:              a description or closed text with control characters
  121  machine-lock-timeout:  --wait spent waiting for machine.lock
       upgrade-timeout:       --wait spent waiting for runs of an older incoda
  122  machine-state:         machine.json missing, malformed or newer, a config written by a
                              newer incoda, or a link to a pool that does not resolve
informational lines: replan:, linked:, quiet-machine, upgrade-wait:, upgrade-warning:,
waiting for machine.lock:, held-dropped:, migrated:

run 'incoda <command> -h' for per-command flags.
`
```

- [ ] **Step 4: Run the tests to see them pass**

Run (bash): `go test ./internal/cli/ -run 'TestHelpDocumentsEveryPrefix' -count=1`

Expected: `ok` for each package.

- [ ] **Step 5: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/cli.go internal/cli/help_test.go
git commit -F - <<'MSG'
docs: incoda help documents the pool commands and every refusal prefix

The usage lists the pool commands, the link flags, --pool and
--quiet-machine, explains pools and links in a paragraph, and lists
every refusal prefix with its exit code and the informational lines,
so a script or an agent can match them.
MSG
```


---

## Self-review against the spec

Every in-scope clause of plan 3 (both plans), and the task that implements and tests it. `3a-N` is plan 3a's Task N, `3b-N` this plan's.

- **2.2 kinds.** From `machine.json` only (3a-5); a project's own slots apply inside its pools (3a-6); pools carry no `pools` or `quiet_machine`: link writes refuse a pool (3a-3) and `pools add` refuses a lane carrying either (3b-1, `TestAddPool`).
- **2.4 lane set, total order, one-at-a-time acquisition, one `--wait` budget, `--slots` and `--exclusive` scope** (3a-5, 3a-6, 3a-8); the busy, holder and 121 lines for pools (3a-6); an interrupt ends every wait of a run with 130, the first link's `machine.lock` wait (6829fc4) and the upgrade's waits (3b-0, `TestMigrationLockWaitIsInterruptible`, `TestWaitIdleEndsWhenItsContextDoes`, `TestNotIdleWaitEndsWhenItsContextDoes`).
- **2.5 plan, enroll, verify**: verify points and triggers 1 and 2 (3a-9), trigger 3 (3b-6, `TestQuietMachine` in `runplan`, `TestQuietPlanReplansWhenAPoolIsAdded`), replan release, line and log (3a-9), closed after each enroll and on every poll (3a-4), every linked pool resolves or 122 (3a-5, 3a-9).
- **2.7** `via`, `wait` (3a-6), `quiet` (3b-6). `root`, `pgid`: plan 4.
- **2.8 quiet machine at top level**: `run --quiet-machine` and `quiet_machine` config, exclusive quiet tickets on every pool in `machine.json` at plan time in the total order, the informational line, trigger 3 (3b-6, root `TestQuietMachine`); a printed run line that takes quiet-machine is checked against every pool (3b-6, `TestQuietFixLineChecksEveryPool`); the several-keys unlinked refusal with a quiet suggestion (3b-0, `TestUnlinkedRefusalSeveralKeysWithAQuietSuggestion`). The nested refusal: plan 4.
- **3.4 kind changes**: `pools add` and `pools remove`, `kind-busy:` and `pool-linked:`, machine.lock then the registry lock held across the `machine.json` write, generation bumped (3b-1, `TestAddPool`, `TestRemovePool`, `TestUpdateIdleHoldsTheRegistryLockAcrossTheWrite`, `TestPoolsAddAndRemove`).
- **3.5 suggestions**: the table (plan 2a `machine.Suggest`); where they appear: the unlinked refusal (3a-7), `init --print` (3b-4), interactive `init` preselected (3b-5), `init --apply-suggestions` (3b-4); `run --pool` makes a first link only to it (3a-8). `status --tree`, watch and `status --json`: plan 5.
- **4.1** (3a-7). **4.2** (3a-8). **4.4** (3a-3, 3b-1, 3b-5: every link and kind write holds machine.lock first).
- **4.3 setup commands**: `incoda link KEY` with picker, `needs-terminal:`, no lock while the picker is open, compare-and-set on confirm and re-ask (3b-3, `TestLinkPicker`, `TestLinkAsksAgainWhenTheLinkMovedMeanwhile`, `TestLinkNeedsATerminal`); `config` link flags (3a-3), with a `--quiet-machine`-only change logged and echoed as a link change (3b-0, `TestConfigLinkFlags`); `incoda init` interactive, `needs-terminal:`, no lock while asking, each answer its own locked step, re-read and re-ask (3b-5, `TestInitAsks`, `TestInitAsksAgainWhenTheStateChanged`, `TestInitNeedsATerminal`); `init --print` with no config line for a lane without a suggestion and the slots note (3b-4, `TestInitPrint`); `init --apply-suggestions` (3b-4, `TestInitApplySuggestions`); `incoda pools` and `--json` (3b-2, `TestPoolsList`); `--wait` default 1m on `config`, `link`, `init`, `pools add|remove` (3a-3, 3b-1, 3b-3, 3b-4, 3b-5).
- **4.5** closed and require_reason of a pool bind runs that enroll on it, `(pool, via K)` texts, description never binds (3a-5, 3a-6).
- **4.6** bad-text on every new write path: `config` (3a-3), `pools add` (3b-1, a row of the table-driven test), `init` descriptions (3b-5, `TestInitAsks` refuses an ESC and asks again).
- **5.1** the prefixes of plan 3: `unlinked:`, `link-conflict:`, `pool-mismatch:`, `link-exists:`, `link-needs-user:`, `closed-while-waiting:` (3a), `kind-busy:`, `pool-linked:`, `needs-terminal:`, `bad-text:` (3a-3, 3b-1), `machine-state:` for unresolved links (3a-5); `incoda help` documents them all (3b-7, `TestHelpDocumentsEveryPrefix`). Fix lines through `internal/fixline` on every printed run and config line (3a-2, 3a-7, 3a-8, 3b-4), and on this plan's other printed commands: the `pools add` refusals (3b-1, `configCmd`, `TestConfigCmd`) and every `incoda link KEY` (3b-3, `LinkLine`, `TestLinkLine`; used in 3b-3, 3b-4, 3b-5).
- **5.5** unlinked lanes with their suggestions in doctor (3b-4).

Carried items (all routed to plan 3): bounded registry locks (3a-1); strays on project keys decided and charged (3a-10); the FIFO test with two waiters and an unpooled holder and the end-to-end orphan-as-holder test (3a-10); `Enroll` fails closed on `NewerSchemaError` (3a-4); table-driven config text checks (3a-3, extended in 3b-1); the 4.5 closed refusal rewrite (3a-5, 3a-6).

Carried from plan 3a to this plan (index, "Carried forward from plan 3a"): the two stale Replace blocks (3b-5 `writeLinkHeld` with the `project` flag and `WriteFirstLinks` passing `true`; 3b-6 the ticket block after the `busy` closure); the run's context through `machine.Ensure` so Ctrl-C during the upgrade's `machine.lock` wait exits 130 (3b-0); a `--quiet-machine`-only config change logged and echoed (3b-0); the comment at `named()` (3b-0); the multi-key quiet suggestion test (3b-0).

Not specified fully, stated:

- Ctrl-C during `init` is not exercised with a real signal (no test may read a terminal); the end of injected input takes the same path, and every applied answer is already committed when the next question is asked.
- The PowerShell paste of the reproduction test runs only on a Windows host; on Unix the PowerShell rendering is checked as strings.

Left to plan 4: nesting in full (the ordering rule with L and P, non-blocking acquisition, `out-of-order-busy:`, the outer trailer and `nested-refused:`, the nested unlinked refusal with its `incoda config KEY --pool` fix and rebuilt outer command, self-wait, the sibling check, `root` and `pgid`, nested quiet with `quiet-nested:`), and their prefixes in `incoda help`. Plan 4 reuses `fixline` (with `DirUnknownText` and the `rerun the outer job` lead) and the `Ticket.Wait` field.

Left to plan 5: `status --tree`, the watch pool tree (with its quiet line and the unlinked branch), the `status --json` additions (`kind`, `pools`, `quiet_machine`, `runnable`, `blocked_by`, top-level `pools` from `report.BuildPools`, `unlinked`, `strays`), README, docs/DESIGN.md, AGENT-RULE.md (the link rules for agents and every prefix), the demo, release notes, and the plan 5 items of the index.
