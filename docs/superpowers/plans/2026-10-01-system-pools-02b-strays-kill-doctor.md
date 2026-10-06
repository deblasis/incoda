# System pools, plan 2b: Strays, orphans, old-holder kill and doctor

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Count runs of an older incoda that no pool admitted (strays and orphan records) on every new-binary acquisition and clean them up when they die; end an older incoda together with its whole job (`kill --force`, the SIGSTOP walk of spec 3.2); refuse `force-release --live` while the upgrade is pending; add the stray, fence and stopped-holder warnings to plain status; and complete `doctor` (strays, orphan records, unpooled and stopped holders, every incoda on PATH with its version). It also lands every plan 2a follow-up the index routes here.

**Architecture:** `internal/procinfo` gains a process listing (`List`, `Lookup`, `Stopped`) over `sysctl kern.proc.all` on macOS and `/proc/<pid>/stat` on Linux. `internal/machine` gains orphan records (`orphans/<old pid>-<unix-nanos>.orphan`, live while any recorded descendant or group still runs), the unpooled-holder scan (`ScanUnpooled`: live tickets under `strays/<n>/<K>/` or under `queues/<K>/` while the fence is missing, plus live orphan records) with its charging rule (`ChargedPools`) and cleanup (`CleanStrays`, `lane.RemoveIfIdle`), the kill target finder (`FindKillTarget`) and the old-holder kill (`KillOldHolder`: steps 0 to 5 with a protected window), status warnings and doctor's new reports and PATH version probe. `lane.Acquire` takes an `Unpooled` count per poll, so `run` on a pool waits for unpooled holders and refuses at once when one of them is its own ancestor. The CLI (`run`, `kill`, `force-release`, `status`, `doctor`) and the TUI killer call these.

**Tech Stack:** Go 1.27, `golang.org/x/sys` (unix, windows), standard library. Tests: `go test`, integration tests that build and run the real binary, its `incoda_crashpoints` build, binaries of the v0.2.0 and v0.6.0 tags, and two small test programs (`internal/testprog/tree` from plan 1, `internal/testprog/oldholder` new here).

**Spec:** `docs/superpowers/specs/2026-10-01-system-pools-design.md` (sections 2.3 unpooled runs and stray cleanup, 2.6 self-wait for stray ancestors, 3.2 in full beyond its first paragraph, 5.3 the stray, fence and stopped-holder lines, 5.5 the doctor remainder, 9 the tests listed in the self-review). Plan index: `docs/superpowers/plans/2026-10-01-system-pools-00-index.md`. Plan 2a (`...-02a-layout-migration.md`) has landed (`8a2b510..b82c498`, follow-ups in `ee37fa4`); this plan builds on its `machine` package, `lane.ProbeLane`, `lane.OpenIn` modes and the crash-point build.

## Global Constraints

- Go 1.27.0 or newer; no new module dependencies (standard library, `golang.org/x/sys`, the existing charm libraries only).
- `just ci` must pass at the end of every task: `gofmt` no-op, `go mod tidy` no-op, `go vet ./...` and `go vet -tags incoda_crashpoints ./...`, `go test -race ./...` (plain `go test` on Windows).
- The Windows and Linux builds must keep compiling: before each commit run `GOOS=windows go vet ./...`, `GOOS=windows go vet -tags incoda_crashpoints ./...`, `GOOS=linux go vet ./...` and `GOOS=linux go vet -tags incoda_crashpoints ./...`.
- Plain prose in comments, docs and commit messages: no em dashes or en dashes, no emoji. Beware `gofmt`'s doc-comment rewriting: two single quotes in a doc comment become a typographic quote, so never write them there.
- Commit messages carry no `Co-Authored-By` or other AI attribution lines.
- Exit codes unchanged: 120 usage and refusals, 121 timeout, 122 state, 123 spawn, 124 killed, 125 kill pending, 130 interrupt.
- Work on branch `feat/system-pools`.
- Carried forward from plan 2a: no build from this branch is released, installed on PATH or handed to anyone until Task 7 (kill ends an older incoda with its whole job) and Task 8 (the `force-release --live` refusal) have landed. Until then the M5 stop line prints `kill ... --force`, which ends only the old incoda's pid and leaves its job running while the lane reads free, and a kill of a pre-fence v0.3.0 to v0.6.0 holder ends only its direct child.
- Layout knowledge stays in `internal/lane/statedir.go` (`LanesDir`, `LaneDir`, `QueuesDir`) plus `machine.StraysDir` (`<state>/strays`) and the new `machine.OrphansDir` (`<state>/orphans`). No released binary reads either. This binary never creates `<state>/queues` as a directory.
- Lock order (spec 3.1) is unchanged: `machine.lock`, then lane registry locks (several only in key order), then ticket locks (non-blocking only). The unpooled scan of an acquisition poll probes stray lanes' registry locks while the run holds only its own tickets and no registry lock (`Position` releases its registry lock before the scan runs; ticket locks are never waited on). The old-holder kill takes no registry lock between its SIGSTOP and its last SIGKILL: it re-checks the target by `TryLock` of the ticket file alone.
- Every ticket probe is the create-free probe of spec 2.6 step 2 (`lane.ProbeLane`, `lane.ProbeTicket`). Stray lane directories are deleted only through `lane.RemoveIfIdle` (under their registry lock), only on a migrated layout (a re-fence under `machine.lock`, doctor, acquisition polls), and `<state>/strays` itself is never deleted outside M6.
- Process listing, verified in the module cache at `~/go/pkg/mod/golang.org/x/sys@v0.47.0/unix` and on this machine: darwin `unix.SysctlKinfoProcSlice(name string, args ...int) ([]KinfoProc, error)` (syscall_darwin.go:519) with `"kern.proc.all"` (CTL_KERN, KERN_PROC, KERN_PROC_ALL) and `unix.SysctlKinfoProc(name string, args ...int) (*KinfoProc, error)` (syscall_darwin.go:502) with `"kern.proc.pid"`; `KinfoProc` (ztypes_darwin_arm64.go:814 and ztypes_darwin_amd64.go:814) fields `Proc.P_pid int32` (line 771), `Proc.P_stat int8` (770), `Proc.P_starttime Timeval` (766; `Sec int64`, `Usec int32`), `Eproc.Ppid int32` (748), `Eproc.Pgid int32` (749). x/sys has no names for `SSTOP` (4) and `SZOMB` (5) of `<sys/proc.h>`: the code uses literals with a comment. For a pid with no process `SysctlKinfoProc` answers `EIO` (an empty reply), so `Lookup` confirms with `kill(pid, 0) == ESRCH`. Linux reads `/proc/<pid>/stat` and counts fields after the last `)`: field 3 state, 4 ppid, 5 pgrp, 22 starttime. Signals go through `unix.Kill` (syscall_darwin.go:375, zsyscall_linux.go:1116) with `unix.SIGSTOP`, `unix.SIGCONT`, `unix.SIGKILL`. Run on this Mac: `SysctlKinfoProcSlice("kern.proc.all")` returned every process with its ppid, pgid and start time, and a SIGSTOPped child read `P_stat` 4.
- Windows old-holder kill is termination only (spec 3.2). Verified with `git show <tag>:internal/child/child_windows.go` for every tag v0.1.0 to v0.6.0: `newSupervisor` creates a job object with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` and sets `CREATE_SUSPENDED` (lines 23 to 48 in v0.1.0, v0.1.1, v0.2.0; 24 to 49 from v0.3.0), and `afterStart` (line 52; 53 from v0.3.0) calls `AssignProcessToJobObject` before the child runs, so the kernel ends the whole tree when the old incoda dies.
- Crash injection (plan 2a, `-tags incoda_crashpoints` only) gains two step names inside the old-holder kill: `kill-before-stop` (the target was found live, nothing is stopped yet) and `kill-stopped` (the old incoda and every descendant are stopped, no record written yet). Same variables: `INCODA_TEST_CRASH_AT`, `INCODA_TEST_PAUSE_AT`, `INCODA_TEST_PAUSE_FILE`.
- Old-binary tests build tags `v0.2.0` and `v0.6.0` with plan 2a's `oldBinary(t, tag)` (`git archive <tag> | tar -x` into the temp build directory, then `go build`); never `git worktree`, never anything that writes to the repository's `.git`. A failed build skips the test with a message naming the tag.
- Test safety rules, binding on every test this plan adds or changes:
  - Tests that run old binaries run them by the absolute path `oldBinary` returns (inside the temp build directory), and every invocation of any incoda, old or new, gets `INCODA_DIR` set to a `t.TempDir()` through `laneEnv` (or `doctorEnv`, which is `laneEnv` with PATH replaced).
  - Never invoke an incoda found on PATH. The one exception is the doctor PATH-probe test of Task 10, which puts fake `incoda` shell scripts (absolute `/bin/sh` shebang, `/bin/sleep`) in temp directories and sets PATH explicitly for the doctor child. From Task 10 on, every test that runs `incoda doctor` sets PATH explicitly (`doctorEnv`, an empty temp directory by default), because doctor executes every incoda it finds on PATH.
  - Never read or write `~/Library/Application Support/incoda` (or the platform default state directory). Before the first task and after the last, record that directory's modification time and its number of lanes (`stat -f %m`, `ls .../queues | wc -l` or `.../lanes`) and check they are unchanged.
  - Run every command with bash, not zsh.
  - Kill tests never signal the test runner's own process group or any process outside the test's own tree. Every old holder a test starts runs in a new process group of its own (`startInGroup`, `Setpgid`), cleanup signals only that group (its id is the holder's pid by construction, signalled before `Wait` reaps it), and every kill test calls `runnerSentinel`, which starts a process in the runner's group and fails the test unless it is still alive at the end.
  - Every test that stops a process (SIGSTOP, or a kill that dies inside its window) resumes it with SIGCONT in cleanup before killing it: `startInGroup`'s cleanup sends SIGCONT to the group first, and `procinfo`'s `TestStoppedState` does the same for its child.
- Every string that came from state, a file, argv, another process or the environment is printed through `textsafe.Escape` (keys are validated and print bare).
- Printed stop and rerun lines (`incoda kill --queue K --pid N --reason '...'`) are built by `machine.KillLine`: keys bare, the reason one single-quoted word quoted per spec 2.6 fix lines (POSIX `'\''`, PowerShell doubled quote). Their reasons are constants of this binary (`incoda upgrade`, `resume interrupted kill`), so no line ever needs a placeholder.
- Pre-existing timing-sensitive tests fail on a loaded machine without any change of this plan: `TestDisagreeingSlotsRefusedAtEnrollAfterConfigChange`, `TestFIFOOrder` under a loaded `-race` suite, and `internal/sysinfo` `TestReadCPUDarwin` (it failed once during this plan's validation, on a tree where only `internal/machine` had changed). If one of these alone fails `just ci`, rerun it in isolation before debugging this plan's changes.

## Decisions this plan makes where the spec leaves room

- **Ruling: every old-layout holder is ended by the descendant walk, never by a request alone; v0.3.0 to v0.6.0 holders run their child in their own group and acknowledge a request by ending only that child. Cost: the old job does not print its own kill notice.** (Controller ruling, 2026-10-02. "Their own group" is the old incoda's group: from v0.3.0 the child never gets a group of its own, so ending it leaves its descendants running.) An old-layout holder is a ticket under `queues/` before the fence (or while a careless `rm` left `queues/` a directory), under `strays/`, or under `queues.new/` or `lanes/` while `machine.json` is absent. `incoda kill` on one writes no request file and runs the old-holder kill of spec 3.2 steps 0 to 5 at once; it needs no `--force` (accepted, same meaning). The `add --force` refusal of spec 3.2 for unreachable holders therefore does not exist, and the stop lines of M2, M5 and `upgrade-pending:` carry no `--force` (the tests that expected it on M5 are updated in Task 6). Exit codes are otherwise unchanged; exit 125 remains for a run of this binary that does not acknowledge.

- **Where unpooled holders count before plan 3.** Spec 2.3 charges an unpooled holder on key K to the pools linked from `lanes/K`, to pool K, or to every pool. `machine.ChargedPools` implements exactly that rule against `machine.json` and `lanes/K/config.json` (a link naming no registered pool, or an unreadable config, falls back to every pool, the safe direction). Until plan 3 makes every run take its pools, only an acquisition on a pool lane is charged: a run on a project key is never held off by unpooled holders in this plan. Plan 3 gets the counting for free once runs enroll on their pools.
- **Admission.** On a pool, a run is admitted when its position plus the number of unpooled holders charged to that pool is below the effective slot count (`lane.AcquireOptions.Unpooled`): unpooled holders hold ahead of every waiter, exclusive tickets narrow as before.
- **Which polls clean.** "An acquisition poll that holds no other lock" is every poll on a pool: `Position` has released its registry lock when the scan runs, and a run's own ticket locks are never waited on, so taking a stray's registry lock there cannot invert the lock order. Cost per poll: one `ReadDir` of `strays/`, one of `orphans/`, one `lstat` of `queues`, plus probes of whatever stray lanes exist.
- **`queues/<K>` while the fence is missing.** Counted as unpooled (spec 2.3) and, like every old-layout holder, killed by the walk. Acquisition polls never delete there: the next re-fence moves `queues/` to `strays/`.
- **Kill targets.** `lanes/<K>` on a migrated layout is a run of this binary (today's path, unchanged: request, then `--force` terminates). Every other place `FindKillTarget` looks (`queues/<K>` while `queues/` is a directory, `strays/<n>/<K>`, `lanes/<K>` or `queues.new/<K>` while `machine.json` is absent) holds an older incoda: `TargetOld`, ended by the walk (the ruling above).
- **Orphan records.** An orphan blocker in M2 or M5 is listed but gets no stop line (there is no participant left to kill). A record that does not parse is neither counted nor deleted; doctor names it. A zombie descendant counts as gone; a recorded group counts while `kill(-G, 0)` answers anything but ESRCH (EPERM included).
- **Walk convergence.** A walk pass is stable when it finds no new descendant and the old incoda and every descendant read stopped (`T`); after 5 seconds without that (a process in uninterruptible sleep) the walk records what it found.
- **Abort and failure texts the spec does not give.** A failed record write exits 122 `incoda: kill: cannot write the orphan record for older incoda pid N (<error>); nothing was terminated and its job was resumed`. A tree still running 10 seconds after the SIGKILLs exits 122 `incoda: kill: older incoda pid N was sent SIGKILL but <pids> still run; the orphan record keeps "K" busy until they exit` and keeps the record.
- **The protected window** delivers SIGINT, SIGTERM and SIGHUP to a channel nobody reads (`signal.Notify` then `signal.Stop`), which ignores them for the window without resetting handlers another part of the process (the TUI) installed.
- **`force-release --live` while `machine.json` is absent** is refused only when the lane has a live ticket (with none it behaves like plain force-release). Its stop lines are the same `incoda kill --queue K --pid N --reason 'incoda upgrade'` as M2's and M5's.
- **The stopped-holder line** keeps the spec's `--force` (accepted, same meaning) and is printed as the spec's line, then `  or resume it instead: kill -CONT N` on its own line, so the rerun line stays copyable.
- **Status warnings** go after the machine line, the last line plain status printed before, so every existing line stays a byte-identical prefix. They appear in plain `status` and `watch --plain`; `status --json` and the interactive watch get them in plan 5.
- **doctor and PATH versions.** An incoda on PATH older than 0.7 gets an `attention:` line (doctor still exits 0: it does not make runs fail closed), as do a `dev` or unparseable version and a probe that does not answer within 5 seconds. doctor deletes fully dead stray lanes only on a migrated layout. The fence give-up message of M4 gains `: <last rename error>` after the spec's text.

---

## File structure

| File | Responsibility |
|---|---|
| `internal/machine/layout.go` | `unmigratedRoot`: the read-only root follows the lanes through rows 3 and 6 |
| `internal/machine/exchange_darwin.go`, `exchange_linux.go`, `notidle_windows.go`, `notidle_other.go` | `isNoExchange` (EPERM, EXDEV take the fallback), `notIdleError` (Windows sharing violation and access denied only) |
| `internal/machine/fence.go`, `lock.go`, `migrate.go`, `bootstrap.go` | plan 2a follow-ups: last rename error, escaped lock errors, `SetBlockers` guard, `notIdleWait`, `refenceWaiting`, the commit's re-fence cap, the M7 skip, row 9 without M0 |
| `internal/procinfo/list.go`, `list_darwin.go`, `list_linux.go`, `list_other.go` | `Proc`, `List`, `Lookup`, `Stopped`, `ErrNoProcess`, `parseStat` |
| `internal/machine/orphan.go`, `orphan_unix.go`, `orphan_windows.go` | orphan records: `Orphan`, `OrphansDir`, `writeOrphan`, `ReadOrphans`, `LiveOrphans`, `SweepOrphans`, group membership |
| `internal/machine/idle.go`, `note.go` | orphan blockers in M2 and M5; `KillLine` stop lines |
| `internal/machine/unpooled.go` | `Unpooled`, `ScanUnpooled`, `CleanStrays`, `ChargedPools`, `ChargedTo`, `UpgradeBlocked` |
| `internal/lane/probe.go`, `acquire.go` | `RemoveIfIdle`; `AcquireOptions.Unpooled` |
| `internal/machine/killtarget.go` | `TargetKind` (`TargetNone`, `TargetLane`, `TargetOld`), `KillTarget`, `FindKillTarget`, `KillLine`, `fixWord` |
| `internal/machine/oldkill.go`, `oldkill_unix.go`, `oldkill_windows.go` | `KillOldHolder` (steps 0 to 5, the protected window), `walkTree`, texts |
| `internal/machine/warnings.go` | `StatusWarnings`, `StoppedLines`, `FenceMissingLine`, `Holder` |
| `internal/machine/versions.go`, `versionprobe_unix.go`, `versionprobe_windows.go`, `pathcheck.go` | doctor's PATH probe: `IncodasOnPath`, `ProbeVersion`, `parseVersion`, `PathVersionLines` |
| `internal/machine/doctor.go` | strays, orphan records, unpooled and stopped holders; the lost-registry text |
| `internal/cli/state.go`, `run.go`, `config.go` | `mutatingState` returns the registry; `run` counts unpooled holders on pools |
| `internal/cli/kill.go`, `internal/tui/killer.go` | kill addresses the layout it finds; `killOld` |
| `internal/cli/misc.go` | `force-release` refusal and sweep; doctor flags and new sections |
| `internal/report/report.go`, `internal/cli/status.go` | `Report.Warnings` and their rendering |
| `internal/testprog/oldholder/main.go` | a stand-in old holder that can release its ticket and keep running |
| root `strays_test.go`, `oldkill_test.go`, `forcerelease_test.go`, `statuswarn_test.go`, `doctorpath_test.go` | integration tests |


---

### Task 1: Read-only views follow the lanes through a migration

Plan 2a's first follow-up. `machine.Inspect` returned the old `queues` path as `View.Root` in recovery rows 3 (swapped, not yet renamed: the lanes are in `queues.new/`) and 6 (a fence without `lanes/`), so `status`, `queues` and `kill` read the fence file as a lanes root, listed nothing and reported a lane free while an older run still held it in `queues.new/`. The root now follows the lanes. The unused `layoutState.Registry` field goes in the same edit.

**Files:**
- Modify: `internal/machine/layout.go` (`layoutState`, `scanLayout`, `Inspect`, new `unmigratedRoot`)
- Test: `internal/machine/migrate_test.go` (append `TestInspectRootFollowsTheLanesThroughAMigration`)

**Interfaces:**
- Consumes: `scanLayout`, `Row`, `fenceNewPath`, `writePlan`, `machineSnapshot`, `seedOld` (plan 2a).
- Produces: `unmigratedRoot(stateDir string, st layoutState) string`; `View.Root` is `queues.new/` in row 3 and `lanes/` in every unmigrated row whose `queues` is not a directory. `layoutState` loses `Registry`.

- [ ] **Step 1: Write the failing tests**

In `internal/machine/migrate_test.go`, replace:

```go
		t.Fatalf("machine.log:\n%s", b)
	}
}
```

with:

```go
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
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/machine/ -run TestInspectRootFollowsTheLanesThroughAMigration -count=1`

Expected: FAIL in the subtests `row_3,_swapped` (`Inspect = {Root:.../queues ...}, want root .../queues.new`) and `row_6,_a_fence_without_lanes/` (`want root .../lanes`).

- [ ] **Step 3: Point the read-only root at the lanes**

In `internal/machine/layout.go`, make these 3 replacements, in order (each quoted block occurs exactly once in the file at that point):

(1 of 3) Replace:

```go
	}
}

// layoutState is what lstat finds at the five names the migration uses.
type layoutState struct {
	Queues, QueuesNew     entryKind
	Lanes, Plan, Registry bool
}

func scanLayout(stateDir string) layoutState {
```

with:

```go
	}
}

// layoutState is what lstat finds at the names the migration uses
// (machine.json is read, not stat'ed, by every caller).
type layoutState struct {
	Queues, QueuesNew entryKind
	Lanes, Plan       bool
}

func scanLayout(stateDir string) layoutState {
```

(2 of 3) Replace:

```go
		QueuesNew: kindOf(fenceNewPath(stateDir)),
		Lanes:     kindOf(lane.LanesDir(stateDir)) == aDir,
		Plan:      kindOf(planPath(stateDir)) == aFile,
		Registry:  kindOf(RegistryPath(stateDir)) != absent,
	}
}
```

with:

```go
		QueuesNew: kindOf(fenceNewPath(stateDir)),
		Lanes:     kindOf(lane.LanesDir(stateDir)) == aDir,
		Plan:      kindOf(planPath(stateDir)) == aFile,
	}
}
```

(3 of 3) Replace:

```go
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
```

with:

```go
			}
			return View{}, registryLostError()
		}
		return View{Root: unmigratedRoot(stateDir, st), Banner: banner(stateDir)}, nil
	}
}

// unmigratedRoot is where the lanes of a layout without machine.json are
// right now. Before the fence it is the old queues/. Once the fence is a
// file the lanes are in queues.new/ (row 3: swapped, not yet renamed) or in
// lanes/ (every later row; in row 6 lanes/ does not exist yet and holds
// nothing). Reading the fence file as a lanes root would list nothing and
// report a lane free while an older run still holds it.
func unmigratedRoot(stateDir string, st layoutState) string {
	switch {
	case st.Queues == aFile && st.QueuesNew == aDir:
		return fenceNewPath(stateDir)
	case st.Queues == aDir:
		return lane.QueuesDir(stateDir)
	default:
		return lane.LanesDir(stateDir)
	}
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go test ./internal/machine/ -run 'TestInspect' -count=1`

Expected: `ok  	github.com/deblasis/incoda/internal/machine`.

- [ ] **Step 5: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 6: Commit**

```bash
git add internal/machine/layout.go internal/machine/migrate_test.go
git commit -F - <<'MSG'
fix: read-only views follow the lanes through a migration

In recovery rows 3 and 6 machine.Inspect handed status the fence file as
the lanes root, so it listed nothing and read a lane free while an older
run still held it in queues.new/. Rows 3 and 6 now read queues.new/ and
lanes/. The unused layoutState.Registry field is gone.
MSG
```

---

### Task 2: The plan 2a follow-ups in the migration

The rest of plan 2a's machine-level follow-ups, each small, together in one reviewable change:

- EPERM and EXDEV from the atomic exchange mean "no exchange here" (a sandbox, a container runtime, overlayfs): M4 takes the rename fallback (`isNoExchange`).
- The fence give-up message names the last rename error.
- The three `machine.lock` open errors are escaped.
- `SetBlockers` on a released or nil lock returns an error instead of using a closed handle.
- Windows not-idle waits (a refused directory rename) print one waiting block naming the older runs that hold tickets in `queues/` (and put them in the note), and only `ERROR_SHARING_VIOLATION` and `ERROR_ACCESS_DENIED` count as not idle (`notIdleError` replaces the `notIdleOnRenameFailure` flag).
- The commit's re-fence cap (`maxCommitRefences`) counts fences actually re-placed, not not-idle retries.
- M7 skips a pool config that turns malformed between its check and its write.
- A lost registry (row 9) is no migration: no M0 PATH warning and no `op=migrate` note (`Ensure` sends it to `repair`).
- Tests for row 9 with a stale `queues/` directory and for the empty-directory race of the fence placement.

**Files:**
- Modify: `internal/machine/exchange_darwin.go`, `internal/machine/exchange_linux.go` (full rewrite, shown whole)
- Create: `internal/machine/notidle_windows.go`, `internal/machine/notidle_other.go`
- Modify: `internal/machine/fence.go` (seams, `placeFence`)
- Modify: `internal/machine/lock.go` (`AcquireLock` errors, `SetBlockers`)
- Modify: `internal/machine/migrate.go` (`Ensure`, `runMigration`, `notIdleWait`, `refenceWaiting`, `fenceMigration`, `finishMigration`)
- Modify: `internal/machine/bootstrap.go` (`beforeBootstrapWrite`, `applyBootstrap`)
- Create: `internal/machine/carried_test.go`, `internal/machine/exchange_unix_test.go`
- Modify: `internal/machine/fence_test.go`, `internal/machine/migrate_test.go` (the not-idle seam, the give-up text)

**Interfaces:**
- Consumes: `placeFence`, `refence`, `waitIdle`, `findBlockers`, `upgradeWaitLines`, `upgradeTimeoutLines`, `budgetDeadline`, `checkPath`, `pathChecked`, `holdTicket`, `takeLock`, `ensure`, `seedOld`, `assertMigrated` (plan 2a).
- Produces: `isNoExchange(err error) bool` (darwin, linux); `notIdleError(err error) bool` (per OS) and the seam `isNotIdle`; `type notIdleWait struct{ printed bool }` with `(*notIdleWait).wait(stateDir string, lk *Lock, o Options) error`; `notIdleLine`; `refenceWaiting(stateDir string, lk *Lock, o Options, w *notIdleWait) error`; seam `beforeBootstrapWrite func(key string)`. `waitNotIdle` and `notIdleOnRenameFailure` are removed.

- [ ] **Step 1: Write the failing tests**

`TestPlaceFenceGivesUpAfterAHundredTries` changes because the refusal now names the last rename error; the two seam edits replace the removed `notIdleOnRenameFailure` flag.

Create `internal/machine/carried_test.go`:

```go
package machine

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
)

// TestSetBlockersAfterRelease: a released lock refuses to write a note
// instead of dereferencing a closed handle.
func TestSetBlockersAfterRelease(t *testing.T) {
	lk := takeLock(t, t.TempDir())
	lk.Release()
	if err := lk.SetBlockers([]Blocker{{Key: "k", PID: 1}}); err == nil {
		t.Fatal("SetBlockers after Release must fail")
	}
	var nilLock *Lock
	if err := nilLock.SetBlockers(nil); err == nil {
		t.Fatal("SetBlockers on a nil lock must fail")
	}
}

// TestNotIdleWaitNamesTheOldRunsInQueues: while Windows refuses to move
// queues/, the wait names the older runs that hold tickets there (in the
// note and once on stderr), and falls back to the open-file line when it
// finds none.
func TestNotIdleWaitNamesTheOldRunsInQueues(t *testing.T) {
	state := t.TempDir()
	holdTicket(t, lane.QueuesDir(state), "builds", 4711, "zig", "build")
	lk := takeLock(t, state)
	var errBuf bytes.Buffer
	o := Options{Start: time.Now(), Wait: time.Minute, Poll: time.Millisecond, Stderr: &errBuf}
	var w notIdleWait
	for i := 0; i < 2; i++ {
		if err := w.wait(state, lk, o); err != nil {
			t.Fatal(err)
		}
	}
	want := "incoda: upgrade-wait: state upgrade waits for 1 run(s) by an older incoda:\nincoda:   builds pid 4711: zig build\n"
	if !strings.HasPrefix(errBuf.String(), want) || strings.Count(errBuf.String(), "upgrade-wait:") != 1 {
		t.Fatalf("stderr:\n%s", errBuf.String())
	}
	if n, ok := ReadNote(state); !ok || len(n.Blockers) != 1 || n.Blockers[0].PID != 4711 {
		t.Fatalf("note %+v %v", n, ok)
	}

	empty := t.TempDir()
	errBuf.Reset()
	w = notIdleWait{}
	if err := w.wait(empty, takeLock(t, empty), o); err != nil {
		t.Fatal(err)
	}
	if errBuf.String() != "incoda: "+notIdleLine+"\n" {
		t.Fatalf("stderr without blockers: %q", errBuf.String())
	}

	o.Wait = 0
	var to *Timeout
	if err := w.wait(state, lk, o); !errors.As(err, &to) || !strings.HasPrefix(to.Msg, "upgrade-timeout: state upgrade still waits for 1 run(s)") {
		t.Fatalf("budget spent: %v", err)
	}
}

// TestCommitRefenceCapCountsOnlyRealRefences: the fence vanishes once
// before the commit and Windows refuses to move the recreated queues/ more
// often than the cap. Only fences actually re-placed count, so the
// migration still commits.
func TestCommitRefenceCapCountsOnlyRealRefences(t *testing.T) {
	state := t.TempDir()
	seedOld(t, state)
	checks := 0
	beforeCommitCheck = func() {
		checks++
		if checks == 1 {
			if err := os.Remove(lane.QueuesDir(state)); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(lane.QueuesDir(state), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	var refused atomic.Int32
	renameDir = func(from, to string) error {
		if strings.HasPrefix(to, StraysDir(state)) && refused.Load() < maxCommitRefences+50 {
			refused.Add(1)
			return errors.New("sharing violation")
		}
		return os.Rename(from, to)
	}
	isNotIdle = func(error) bool { return true }
	defer func() { beforeCommitCheck, renameDir, isNotIdle = func() {}, os.Rename, notIdleError }()
	var errBuf bytes.Buffer
	_, err := Ensure(state, Options{Start: time.Now(), Wait: time.Minute, Poll: time.Millisecond, By: "incoda test", Stderr: &errBuf})
	if err != nil {
		t.Fatalf("%v\n%s", err, errBuf.String())
	}
	if refused.Load() != maxCommitRefences+50 || checks != 2 {
		t.Fatalf("refused %d, checks %d", refused.Load(), checks)
	}
	assertMigrated(t, state, true)
}

// TestBootstrapSkipsAConfigThatTurnsMalformed: a pool config that becomes
// unreadable between M7's check and its write is left in place, like any
// malformed config, and the migration still commits.
func TestBootstrapSkipsAConfigThatTurnsMalformed(t *testing.T) {
	state := t.TempDir()
	seedOld(t, state)
	beforeBootstrapWrite = func(key string) {
		if key == "builds" {
			if err := os.WriteFile(filepath.Join(lane.LaneDir(state, key), "config.json"), []byte("nope"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	defer func() { beforeBootstrapWrite = func(string) {} }()
	reg, _, err := ensure(t, state)
	if err != nil || !reg.IsPool("builds") {
		t.Fatalf("Ensure = %+v %v", reg, err)
	}
	if b, _ := os.ReadFile(filepath.Join(lane.LaneDir(state, "builds"), "config.json")); string(b) != "nope" {
		t.Fatalf("the malformed config was rewritten: %q", b)
	}
}

// TestRow9SkipsM0AndTheMigrateNote: a lost registry is no migration. With
// a stale queues/ directory in the fence's place, Ensure re-places the
// fence under a "refence" note (never "migrate"), prints no PATH warning,
// moves the directory to strays/ and fails closed.
func TestRow9SkipsM0AndTheMigrateNote(t *testing.T) {
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
	if err := os.MkdirAll(filepath.Join(lane.QueuesDir(state), "stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	pathDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pathDir, "incoda"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	pathChecked.Store(false)
	defer pathChecked.Store(true)
	var ops []string
	renameDir = func(from, to string) error {
		if n, ok := ReadNote(state); ok {
			ops = append(ops, n.Op)
		}
		return os.Rename(from, to)
	}
	defer func() { renameDir = os.Rename }()
	var errBuf bytes.Buffer
	_, err := Ensure(state, Options{Start: time.Now(), Wait: 10 * time.Second, Poll: 20 * time.Millisecond,
		Stderr: &errBuf, Path: pathDir, Exe: os.Args[0]})
	var se *StateError
	if !errors.As(err, &se) || se.Msg != "machine-state: machine.json: missing while lanes/ exists; run incoda doctor" {
		t.Fatalf("want the lost-registry refusal, got %v", err)
	}
	if strings.Contains(errBuf.String(), "upgrade-warning") {
		t.Fatalf("a lost registry must not run M0:\n%s", errBuf.String())
	}
	if len(ops) != 1 || ops[0] != "refence" {
		t.Fatalf("machine.lock note ops during the re-fence: %v", ops)
	}
	if !FencePlaced(state) {
		t.Fatal("the fence must be back")
	}
	batches, _ := os.ReadDir(StraysDir(state))
	if len(batches) != 1 {
		t.Fatalf("strays: %v", batches)
	}
	if _, err := os.Stat(filepath.Join(StraysDir(state), batches[0].Name(), "stale")); err != nil {
		t.Fatal(err)
	}
}

// TestEmptyDirRaceSendsTheFirstOldRunToStrays: on an empty state directory
// an older incoda's first run creates queues/ between lanes/ and the
// fence. The race rule moves it to strays/, M5 waits for its live ticket,
// and M6 merges the lane into lanes/.
func TestEmptyDirRaceSendsTheFirstOldRunToStrays(t *testing.T) {
	state := t.TempDir()
	var released atomic.Bool
	first := true
	beforePlace = func() {
		if !first {
			return
		}
		first = false
		release := holdTicket(t, lane.QueuesDir(state), "early", 999998, "make")
		go func() {
			time.Sleep(300 * time.Millisecond)
			released.Store(true)
			release()
		}()
	}
	defer func() { beforePlace = func() {} }()
	_, out, err := ensure(t, state)
	if err != nil {
		t.Fatal(err)
	}
	if !released.Load() {
		t.Fatal("the migration committed while the early run was live")
	}
	if !strings.Contains(out, "early pid 999998: make") || !strings.Contains(out, "incoda kill --queue early --pid 999998 --reason 'incoda upgrade' --force") {
		t.Fatalf("M5 must name the early run with the --force stop line:\n%s", out)
	}
	assertMigrated(t, state, false)
	if !lane.Exists(state, "early") {
		t.Fatal("the early lane was not merged into lanes/")
	}
}

// TestAcquireLockEscapesItsErrors: a state directory path with a control
// character reaches the terminal escaped, never raw.
func TestAcquireLockEscapesItsErrors(t *testing.T) {
	state := filepath.Join(t.TempDir(), "a\x1bb", "missing")
	_, err := AcquireLock(state, LockOptions{Op: "migrate", Start: time.Now(), Wait: time.Second})
	var se *StateError
	if !errors.As(err, &se) || strings.ContainsRune(se.Msg, '\x1b') || !strings.Contains(se.Msg, `a\x1bb`) {
		t.Fatalf("want an escaped open error, got %v", err)
	}
}
```

Create `internal/machine/exchange_unix_test.go`:

```go
//go:build darwin || linux

package machine

import (
	"fmt"
	"testing"

	"golang.org/x/sys/unix"
)

// TestIsNoExchange: errors that mean "this filesystem or sandbox has no
// atomic exchange" send M4 to the rename fallback; any other error is a
// real failure of the rename.
func TestIsNoExchange(t *testing.T) {
	for _, e := range []error{unix.EINVAL, unix.ENOTSUP, unix.EPERM, unix.EXDEV, fmt.Errorf("renamex: %w", unix.EXDEV)} {
		if !isNoExchange(e) {
			t.Fatalf("%v must take the fallback", e)
		}
	}
	for _, e := range []error{nil, unix.EACCES, unix.ENOENT, unix.EBUSY} {
		if isNoExchange(e) {
			t.Fatalf("%v is not a missing exchange", e)
		}
	}
}
```

In `internal/machine/fence_test.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file at that point):

(1 of 2) Replace:

```go
	defer func() { beforePlace = func() {} }()
	_, err := placeFence(state)
	var se *StateError
	if !errors.As(err, &se) || se.Msg != "machine-state: cannot place the queues fence" {
		t.Fatalf("want the placement refusal, got %v", err)
	}
	// The first try finds nothing to move; every later one moves the
	// directory the previous try's hook recreated.
```

with:

```go
	defer func() { beforePlace = func() {} }()
	_, err := placeFence(state)
	var se *StateError
	// The refusal names the last rename error, so a human can tell a race
	// from a permission problem.
	if !errors.As(err, &se) || !strings.HasPrefix(se.Msg, "machine-state: cannot place the queues fence: rename ") ||
		!strings.Contains(se.Msg, "queues.new") {
		t.Fatalf("want the placement refusal with the last rename error, got %v", err)
	}
	// The first try finds nothing to move; every later one moves the
	// directory the previous try's hook recreated.
```

(2 of 2) Replace:

```go
		t.Fatal(err)
	}
	renameDir = func(string, string) error { return errors.New("sharing violation") }
	notIdleOnRenameFailure = true
	defer func() { renameDir = os.Rename; notIdleOnRenameFailure = runtime.GOOS == "windows" }()
	if _, err := placeFence(state); !errors.Is(err, errNotIdle) {
		t.Fatalf("want errNotIdle, got %v", err)
	}
```

with:

```go
		t.Fatal(err)
	}
	renameDir = func(string, string) error { return errors.New("sharing violation") }
	isNotIdle = func(error) bool { return true }
	defer func() { renameDir = os.Rename; isNotIdle = notIdleError }()
	if _, err := placeFence(state); !errors.Is(err, errNotIdle) {
		t.Fatalf("want errNotIdle, got %v", err)
	}
```

In `internal/machine/migrate_test.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file at that point):

(1 of 2) Replace:

```go
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
```

with:

```go
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
```

(2 of 2) Replace:

```go
		}
		return os.Rename(from, to)
	}
	notIdleOnRenameFailure = true
	defer func() {
		exchangeFn, renameDir, notIdleOnRenameFailure = exchange, os.Rename, runtime.GOOS == "windows"
	}()
	if _, _, err := ensure(t, state); err != nil {
		t.Fatal(err)
```

with:

```go
		}
		return os.Rename(from, to)
	}
	isNotIdle = func(error) bool { return true }
	defer func() {
		exchangeFn, renameDir, isNotIdle = exchange, os.Rename, notIdleError
	}()
	if _, _, err := ensure(t, state); err != nil {
		t.Fatal(err)
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go vet ./internal/machine/`

Expected: the test build fails: `undefined: notIdleWait` (and `isNotIdle`, `notIdleError`, `beforeBootstrapWrite`, `isNoExchange`).

- [ ] **Step 3: Classify the exchange and not-idle errors**

Replace the whole content of `internal/machine/exchange_darwin.go` with (shown whole: the changes are spread across the file):

```go
//go:build darwin

package machine

import (
	"errors"

	"golang.org/x/sys/unix"
)

// exchange swaps a and b atomically with renamex_np(RENAME_SWAP)
// (golang.org/x/sys/unix.RenamexNp). APFS supports it for a file and a
// directory; anything that refuses the swap itself is reported as
// errNoExchange so M4 takes the rename fallback (isNoExchange).
func exchange(a, b string) error {
	if noExchange() {
		return errNoExchange
	}
	err := unix.RenamexNp(a, b, unix.RENAME_SWAP)
	if isNoExchange(err) {
		return errNoExchange
	}
	return err
}

// isNoExchange reports an error that means "no atomic exchange here", as
// opposed to a failed rename: EINVAL and ENOTSUP (a filesystem without
// RENAME_SWAP), EPERM (a sandbox or a mount that forbids it) and EXDEV (the
// two names on different filesystems, which only an overlay can produce).
// The fallback's plain renames report their own errors.
func isNoExchange(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) ||
		errors.Is(err, unix.EPERM) || errors.Is(err, unix.EXDEV)
}
```

Replace the whole content of `internal/machine/exchange_linux.go` with (shown whole: the changes are spread across the file):

```go
//go:build linux

package machine

import (
	"errors"

	"golang.org/x/sys/unix"
)

// exchange swaps a and b atomically with renameat2(RENAME_EXCHANGE)
// (golang.org/x/sys/unix.Renameat2). Anything that refuses the exchange
// itself is reported as errNoExchange so M4 takes the rename fallback
// (isNoExchange).
func exchange(a, b string) error {
	if noExchange() {
		return errNoExchange
	}
	err := unix.Renameat2(unix.AT_FDCWD, a, unix.AT_FDCWD, b, unix.RENAME_EXCHANGE)
	if isNoExchange(err) {
		return errNoExchange
	}
	return err
}

// isNoExchange reports an error that means "no atomic exchange here", as
// opposed to a failed rename: EINVAL and ENOTSUP (a filesystem without
// RENAME_EXCHANGE), ENOSYS (a kernel older than 3.15), EPERM (a seccomp
// profile or container runtime that forbids renameat2) and EXDEV (overlayfs
// answers it for a directory that lives in a lower layer).
func isNoExchange(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.ENOSYS) ||
		errors.Is(err, unix.EPERM) || errors.Is(err, unix.EXDEV)
}
```

Create `internal/machine/notidle_windows.go`:

```go
//go:build windows

package machine

import (
	"errors"

	"golang.org/x/sys/windows"
)

// notIdleError reports a directory rename that Windows refused because a
// file inside is still open (ERROR_SHARING_VIOLATION) or because a handle
// without delete sharing is open on it (ERROR_ACCESS_DENIED): an older run
// is still in there, so the caller waits. Any other error is a real
// failure.
func notIdleError(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
```

Create `internal/machine/notidle_other.go`:

```go
//go:build !windows

package machine

// notIdleError is false off Windows: a POSIX rename does not care about
// open files, so a refused rename is a real failure.
func notIdleError(error) bool { return false }
```

- [ ] **Step 4: Fence and lock**

Replace the whole content of `internal/machine/fence.go` with (shown whole: the changes are spread across the file):

```go
package machine

import (
	"errors"
	"os"
	"path/filepath"
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
	exchangeFn  = exchange
	beforePlace = func() {}
	renameDir   = os.Rename
	// isNotIdle classifies a refused directory rename (notIdleError).
	isNotIdle = notIdleError
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
// EISDIR). It gives up after maxPlaceTries, naming the last rename error.
// It returns the strays it made.
func placeFence(stateDir string) ([]string, error) {
	if err := writeFenceNew(stateDir); err != nil {
		return nil, err
	}
	q := lane.QueuesDir(stateDir)
	var moved []string
	var last error
	for i := 0; i < maxPlaceTries; i++ {
		if fi, err := os.Lstat(q); err == nil && !fi.Mode().IsRegular() {
			dst, err := moveToStrays(stateDir, q)
			if err != nil {
				if isNotIdle(err) {
					return moved, errNotIdle
				}
				return moved, stateErrorf("cannot move %s to strays/: %s", textsafe.Escape(q), textsafe.Escape(err.Error()))
			}
			moved = append(moved, dst)
		}
		beforePlace()
		if last = os.Rename(fenceNewPath(stateDir), q); last == nil {
			return moved, nil
		}
	}
	return moved, stateErrorf("cannot place the queues fence: %s", textsafe.Escape(last.Error()))
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

Replace the whole content of `internal/machine/lock.go` with (shown whole: the changes are spread across the file):

```go
package machine

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/deblasis/incoda/internal/lockfile"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/textsafe"
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
		return nil, stateErrorf("cannot open %s: %s", textsafe.Escape(LockPath(stateDir)), esc(err))
	}
	deadline := lockDeadline(o.Start, o.Wait, time.Now())
	var printedAt time.Time
	for attempt := 0; ; attempt++ {
		ok, err := f.TryLock()
		if err != nil {
			f.Close()
			return nil, stateErrorf("machine.lock: %s", esc(err))
		}
		if ok {
			l := &Lock{f: f, op: o.Op, since: time.Now()}
			if err := l.SetBlockers(nil); err != nil {
				l.Release()
				return nil, stateErrorf("cannot write the machine.lock note: %s", esc(err))
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
	if l == nil || l.f == nil {
		return errors.New("machine.lock is not held")
	}
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

- [ ] **Step 5: The migration: not-idle waits, the re-fence cap, row 9 and M7**

In `internal/machine/migrate.go`, make these 10 replacements, in order (each quoted block occurs exactly once in the file at that point):

(1 of 10) Replace:

```go
	if !errors.Is(err, ErrNoRegistry) {
		return nil, err
	}
	return migrate(stateDir, o)
}
```

with:

```go
	if !errors.Is(err, ErrNoRegistry) {
		return nil, err
	}
	if scanLayout(stateDir).row() == RowRegistryLost {
		// A lost registry is no migration: no M0 PATH warning and no
		// op=migrate note. repair re-places a missing fence and fails
		// closed (or finds machine.json after all: a commit can land
		// between ReadRegistry and scanLayout).
		return repair(stateDir, o)
	}
	return migrate(stateDir, o)
}
```

(2 of 10) Replace:

```go
// runMigration classifies the layout and resumes at the step the recovery
// table names. The caller holds machine.lock.
func runMigration(stateDir string, lk *Lock, o Options) (*Registry, error) {
	for {
		reg, err := ReadRegistry(stateDir)
		if err == nil {
```

with:

```go
// runMigration classifies the layout and resumes at the step the recovery
// table names. The caller holds machine.lock.
func runMigration(stateDir string, lk *Lock, o Options) (*Registry, error) {
	var w notIdleWait
	for {
		reg, err := ReadRegistry(stateDir)
		if err == nil {
```

(3 of 10) Replace:

```go
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
```

with:

```go
				return nil, stateErrorf("cannot remove migration.json: %s", esc(err))
			}
			if !FencePlaced(stateDir) {
				if err := refenceWaiting(stateDir, lk, o, &w); err != nil {
					return nil, err
				}
			}
```

(4 of 10) Replace:

```go
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
```

with:

```go
		switch st.row() {
		case RowRegistryLost:
			if !FencePlaced(stateDir) {
				if err := refenceWaiting(stateDir, lk, o, &w); err != nil {
					return nil, err
				}
			}
			return nil, registryLostError()
		case RowSwapped:
			if err := renameDir(fenceNewPath(stateDir), lane.LanesDir(stateDir)); err != nil {
				return nil, stateErrorf("cannot rename queues.new to lanes: %s", esc(err))
```

(5 of 10) Replace:

```go
			stepErr = beginMigration(stateDir, lk, o, st)
		}
		if errors.Is(stepErr, errNotIdle) {
			if err := waitNotIdle(o); err != nil {
				return nil, err
			}
			continue
```

with:

```go
			stepErr = beginMigration(stateDir, lk, o, st)
		}
		if errors.Is(stepErr, errNotIdle) {
			if err := w.wait(stateDir, lk, o); err != nil {
				return nil, err
			}
			continue
```

(6 of 10) Replace:

```go
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
```

with:

```go
	}
}

// notIdleWait is the wait after a directory rename was refused because
// something still has a file open inside (Windows: a sharing violation or
// access denied). It remembers whether it has printed its line.
type notIdleWait struct{ printed bool }

// wait probes the old queues/ (when it is a directory) for the older runs
// that keep files open there, writes them into the machine.lock note, prints
// one upgrade-wait block (the blockers, or the open-file line when none is
// found), then sleeps one poll, or gives up when the --wait budget is
// spent.
func (w *notIdleWait) wait(stateDir string, lk *Lock, o Options) error {
	var bs []Blocker
	if kindOf(lane.QueuesDir(stateDir)) == aDir {
		bs, _ = findBlockers(stateDir, phaseM2)
		_ = lk.SetBlockers(bs)
	}
	if !w.printed {
		w.printed = true
		if len(bs) > 0 {
			fmt.Fprintf(o.stderr(), "incoda: %s\n", joinLines(upgradeWaitLines(bs, phaseM2)))
		} else {
			fmt.Fprintf(o.stderr(), "incoda: %s\n", notIdleLine)
		}
	}
	if dl := budgetDeadline(o.Start, o.Wait); !dl.IsZero() && !time.Now().Before(dl) {
		if len(bs) > 0 {
			return &Timeout{Msg: joinLines(upgradeTimeoutLines(bs, phaseM2, o.Wait))}
		}
		return &Timeout{Msg: joinLines([]string{
			"upgrade-timeout: a directory the upgrade must move still has a file open inside it (an older incoda or another program)",
			"upgrade the older incoda on PATH; see incoda doctor",
```

(7 of 10) Replace:

```go
	return nil
}

// beginMigration is M1 (leftover queues.new), M2, M3 and M4.
func beginMigration(stateDir string, lk *Lock, o Options, st layoutState) error {
	if st.Queues == aDir && st.QueuesNew == aFile {
```

with:

```go
	return nil
}

// notIdleLine is printed once when a rename is refused and no older run is
// found holding a ticket: something else has a file open in the directory.
const notIdleLine = "upgrade-wait: a directory the upgrade must move still has a file open inside it (an older incoda or another program); waiting"

// refenceWaiting re-places the fence, waiting (within the --wait budget)
// while Windows refuses to move a queues/ directory that still has an open
// file inside.
func refenceWaiting(stateDir string, lk *Lock, o Options, w *notIdleWait) error {
	for {
		err := refence(stateDir)
		if !errors.Is(err, errNotIdle) {
			return err
		}
		if err := w.wait(stateDir, lk, o); err != nil {
			return err
		}
	}
}

// beginMigration is M1 (leftover queues.new), M2, M3 and M4.
func beginMigration(stateDir string, lk *Lock, o Options, st layoutState) error {
	if st.Queues == aDir && st.QueuesNew == aFile {
```

(8 of 10) Replace:

```go
			return stateErrorf("cannot swap the queues fence in: %s", esc(err))
		}
		if err := renameDir(q, lanes); err != nil {
			if notIdleOnRenameFailure {
				return errNotIdle
			}
			return stateErrorf("cannot rename queues to lanes: %s", esc(err))
```

with:

```go
			return stateErrorf("cannot swap the queues fence in: %s", esc(err))
		}
		if err := renameDir(q, lanes); err != nil {
			if isNotIdle(err) {
				return errNotIdle
			}
			return stateErrorf("cannot rename queues to lanes: %s", esc(err))
```

(9 of 10) Replace:

```go
// queues/ goes to strays/) and the migration goes back to M5, which probes
// strays/ and waits for the run, all within the one --wait budget.
func finishMigration(stateDir string, lk *Lock, o Options) (*Registry, error) {
	for refences := 0; ; refences++ {
		if err := waitIdle(stateDir, lk, o, phaseM5); err != nil {
			return nil, err
		}
```

with:

```go
// queues/ goes to strays/) and the migration goes back to M5, which probes
// strays/ and waits for the run, all within the one --wait budget.
func finishMigration(stateDir string, lk *Lock, o Options) (*Registry, error) {
	var w notIdleWait
	// refences counts fences actually re-placed; waits for a directory
	// Windows will not move yet do not count against the cap.
	refences := 0
	for {
		if err := waitIdle(stateDir, lk, o, phaseM5); err != nil {
			return nil, err
		}
```

(10 of 10) Replace:

```go
		if refences >= maxCommitRefences {
			return nil, stateErrorf("the queues fence keeps disappearing; machine.json was not written: see incoda doctor")
		}
		if err := refence(stateDir); errors.Is(err, errNotIdle) {
			if err := waitNotIdle(o); err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		}
	}
	reg := &Registry{Schema: RegistrySchema, Layout: Layout, Generation: 1, Pools: BootstrapPools(),
		MigratedBy: o.By, MigratedAt: time.Now().UTC().Format(time.RFC3339)}
```

with:

```go
		if refences >= maxCommitRefences {
			return nil, stateErrorf("the queues fence keeps disappearing; machine.json was not written: see incoda doctor")
		}
		if err := refenceWaiting(stateDir, lk, o, &w); err != nil {
			return nil, err
		}
		refences++
	}
	reg := &Registry{Schema: RegistrySchema, Layout: Layout, Generation: 1, Pools: BootstrapPools(),
		MigratedBy: o.By, MigratedAt: time.Now().UTC().Format(time.RFC3339)}
```

In `internal/machine/bootstrap.go`, make these 3 replacements, in order (each quoted block occurs exactly once in the file at that point):

(1 of 3) Replace:

```go
func esc(err error) string { return textsafe.Escape(err.Error()) }

// mergeStrays is M6. Every ticket under strays/ is dead by now (M5 waited
// for the live ones). A strays/<n>/<K> whose lanes/<K> does not exist is
// renamed into lanes/; the rest hold only dead tickets and a log fragment,
```

with:

```go
func esc(err error) string { return textsafe.Escape(err.Error()) }

// Seam for tests; production never changes it.
var beforeBootstrapWrite = func(key string) {}

// mergeStrays is M6. Every ticket under strays/ is dead by now (M5 waited
// for the live ones). A strays/<n>/<K> whose lanes/<K> does not exist is
// renamed into lanes/; the rest hold only dead tickets and a log fragment,
```

(2 of 3) Replace:

```go
			q.Close()
			continue
		}
		_, err = q.UpdateConfig(func(c *lane.Config) error {
			if c.Slots < 1 {
				c.Slots = 1
```

with:

```go
			q.Close()
			continue
		}
		beforeBootstrapWrite(key)
		_, err = q.UpdateConfig(func(c *lane.Config) error {
			if c.Slots < 1 {
				c.Slots = 1
```

(3 of 3) Replace:

```go
		})
		q.Close()
		if err != nil {
			return stateErrorf("cannot write the config of pool %q: %s", key, esc(err))
		}
	}
```

with:

```go
		})
		q.Close()
		if err != nil {
			if _, rerr := lane.ReadConfig(q.Dir); rerr != nil {
				// The config turned unreadable between the check above
				// and the write (a human editing it): leave it in place,
				// as M7 leaves every malformed config.
				continue
			}
			return stateErrorf("cannot write the config of pool %q: %s", key, esc(err))
		}
	}
```

- [ ] **Step 6: Run the tests to see them pass**

Run: `go test -race ./internal/machine/ -count=1`

Expected: `ok  	github.com/deblasis/incoda/internal/machine`.

- [ ] **Step 7: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 8: Commit**

```bash
git add internal/machine/bootstrap.go internal/machine/carried_test.go internal/machine/exchange_darwin.go internal/machine/exchange_linux.go internal/machine/exchange_unix_test.go internal/machine/fence.go internal/machine/fence_test.go internal/machine/lock.go internal/machine/migrate.go internal/machine/migrate_test.go internal/machine/notidle_other.go internal/machine/notidle_windows.go
git commit -F - <<'MSG'
fix: plan 2a follow-ups in the migration and the fence

EPERM and EXDEV from the atomic exchange take the rename fallback, the
fence refusal names the last rename error, machine.lock errors are
escaped and SetBlockers refuses on a released lock. On Windows a refused
directory rename waits naming the older runs in queues/, and only a
sharing violation or access denied counts as not idle. The commit's
re-fence cap counts real re-fences only, M7 skips a config that turns
malformed mid-step, and a lost registry runs no PATH check and writes no
migrate note.
MSG
```

---

### Task 3: List processes: parent, group, start time and state

The old-holder kill (Task 6), orphan records (Task 4) and the stopped-holder lines (Tasks 9 and 10) need to list every process and read one: parent pid, process group, a start time that identifies the incarnation of a pid, and whether it is stopped. `procinfo` already reads parents for `INCODA_HELD` verification; it gains `List`, `Lookup` and `Stopped`. The `/proc` parser is plain text parsing, kept in a file every platform compiles so its test runs on macOS too.

**Files:**
- Create: `internal/procinfo/list.go`, `internal/procinfo/list_darwin.go`, `internal/procinfo/list_linux.go`, `internal/procinfo/list_other.go`
- Create: `internal/procinfo/stat_test.go`, `internal/procinfo/list_test.go`

**Interfaces:**
- Consumes: `procinfo.ErrUnsupported` (plan 1).
- Produces: `type procinfo.Proc struct{ PID, PPID, PGID int; Start uint64; State byte }` (`State` is `'T'` stopped, `'Z'` zombie, `'R'` otherwise); `procinfo.ErrNoProcess`; `procinfo.List() ([]Proc, error)`; `procinfo.Lookup(pid int) (Proc, error)` (`ErrNoProcess` for no process); `procinfo.Stopped(pid int) (bool, error)`; `parseStat(s string) (Proc, error)`. Windows and the BSDs return `ErrUnsupported` from `List` and `Lookup`.

- [ ] **Step 1: Write the failing tests**

Create `internal/procinfo/stat_test.go`:

```go
package procinfo

import "testing"

func TestParseStat(t *testing.T) {
	line := "4711 (we (ird) name) T 4700 4690 4690 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 987654 1000 10 18446744073709551615\n"
	p, err := parseStat(line)
	if err != nil {
		t.Fatal(err)
	}
	if p != (Proc{PID: 4711, PPID: 4700, PGID: 4690, Start: 987654, State: 'T'}) {
		t.Fatalf("parseStat = %+v", p)
	}
	for _, bad := range []string{"", "4711 (x", "4711 (x) R 1", "x (y) R 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19"} {
		if _, err := parseStat(bad); err == nil {
			t.Fatalf("parseStat(%q) must fail", bad)
		}
	}
	if p, _ := parseStat("9 (z) Z 1 9 9 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 5 0 0 0"); p.State != 'Z' {
		t.Fatalf("zombie state %q", p.State)
	}
}
```

Create `internal/procinfo/list_test.go`:

```go
//go:build darwin || linux

package procinfo

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func skipNoList(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no process listing on " + runtime.GOOS)
	}
}

func TestListAndLookupSeeThisProcess(t *testing.T) {
	skipNoList(t)
	ps, err := List()
	if err != nil {
		t.Fatal(err)
	}
	var self Proc
	for _, p := range ps {
		if p.PID == os.Getpid() {
			self = p
		}
	}
	if self.PID == 0 || self.PPID != os.Getppid() || self.PGID != syscall.Getpgrp() || self.Start == 0 || self.State == 'T' {
		t.Fatalf("this process in the listing: %+v", self)
	}
	got, err := Lookup(os.Getpid())
	if err != nil || got.Start != self.Start || got.PGID != self.PGID {
		t.Fatalf("Lookup(self) = %+v %v, listing has %+v", got, err, self)
	}
	if _, err := Lookup(1 << 30); !errors.Is(err, ErrNoProcess) {
		t.Fatalf("a missing pid: %v", err)
	}
}

// TestStoppedState: a child stopped with SIGSTOP reads as 'T' until
// SIGCONT. The child is always resumed and killed in cleanup.
func TestStoppedState(t *testing.T) {
	skipNoList(t)
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	pid := c.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGCONT)
		_ = c.Process.Kill()
		_ = c.Wait()
	})
	waitState := func(want bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			got, err := Stopped(pid)
			if err != nil {
				t.Fatal(err)
			}
			if got == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("pid %d stopped=%v, want %v", pid, got, want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitState(false)
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	waitState(true)
	if err := syscall.Kill(pid, syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	waitState(false)
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go vet ./internal/procinfo/`

Expected: the test build fails: `undefined: List` (and `parseStat`, `Lookup`, `Stopped`, `ErrNoProcess`).

- [ ] **Step 3: Implement the listing**

Create `internal/procinfo/list.go`:

```go
package procinfo

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Proc is one process as the process listing sees it.
type Proc struct {
	PID, PPID, PGID int
	// Start identifies this incarnation of PID, so a recorded pid is never
	// confused with a later process that reused the number: microseconds
	// since the epoch on macOS (p_starttime), clock ticks since boot on
	// Linux (field 22 of /proc/<pid>/stat). Only equality is meaningful.
	Start uint64
	// State is 'T' for a stopped process, 'Z' for a zombie and 'R' for
	// anything else.
	State byte
}

// ErrNoProcess means no process has the pid.
var ErrNoProcess = errors.New("no such process")

// Stopped reports whether pid is in the stopped state (T). A missing
// process is not stopped.
func Stopped(pid int) (bool, error) {
	p, err := Lookup(pid)
	if errors.Is(err, ErrNoProcess) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return p.State == 'T', nil
}

// parseStat parses the content of /proc/<pid>/stat: field 3 (state), 4
// (ppid), 5 (pgrp) and 22 (starttime). The command name in field 2 may hold
// spaces and parentheses, so fields are counted after the last ')'. It is
// plain parsing, kept apart from the Linux reader so every platform tests
// it.
func parseStat(s string) (Proc, error) {
	open := strings.IndexByte(s, '(')
	i := strings.LastIndexByte(s, ')')
	if open <= 0 || i < open {
		return Proc{}, fmt.Errorf("unparseable stat line")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(s[:open]))
	if err != nil {
		return Proc{}, fmt.Errorf("unparseable pid in stat line: %w", err)
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 || len(f[0]) != 1 {
		return Proc{}, fmt.Errorf("short stat line for pid %d", pid)
	}
	ppid, err1 := strconv.Atoi(f[1])
	pgid, err2 := strconv.Atoi(f[2])
	start, err3 := strconv.ParseUint(f[19], 10, 64)
	if err := errors.Join(err1, err2, err3); err != nil {
		return Proc{}, fmt.Errorf("stat line for pid %d: %w", pid, err)
	}
	st := byte('R')
	switch f[0][0] {
	case 'T', 't':
		st = 'T'
	case 'Z', 'X':
		st = 'Z'
	}
	return Proc{PID: pid, PPID: ppid, PGID: pgid, Start: start, State: st}, nil
}
```

Create `internal/procinfo/list_darwin.go`:

```go
//go:build darwin

package procinfo

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// p_stat values from <sys/proc.h>; golang.org/x/sys/unix has no names for
// them.
const (
	sstop = 4 // SSTOP
	szomb = 5 // SZOMB
)

func fromKinfo(k *unix.KinfoProc) Proc {
	st := byte('R')
	switch k.Proc.P_stat {
	case sstop:
		st = 'T'
	case szomb:
		st = 'Z'
	}
	return Proc{
		PID:   int(k.Proc.P_pid),
		PPID:  int(k.Eproc.Ppid),
		PGID:  int(k.Eproc.Pgid),
		Start: uint64(k.Proc.P_starttime.Sec)*1_000_000 + uint64(k.Proc.P_starttime.Usec),
		State: st,
	}
}

// List returns every process: sysctl(CTL_KERN, KERN_PROC, KERN_PROC_ALL).
func List() ([]Proc, error) {
	ks, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, fmt.Errorf("sysctl kern.proc.all: %w", err)
	}
	out := make([]Proc, 0, len(ks))
	for i := range ks {
		out = append(out, fromKinfo(&ks[i]))
	}
	return out, nil
}

// Lookup reads one process: sysctl(CTL_KERN, KERN_PROC, KERN_PROC_PID).
// A pid with no process is ErrNoProcess.
func Lookup(pid int) (Proc, error) {
	if pid <= 0 {
		return Proc{}, ErrNoProcess
	}
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err == nil && int(k.Proc.P_pid) == pid {
		return fromKinfo(k), nil
	}
	// sysctl answers EIO (an empty reply) for a pid with no process.
	if kerr := unix.Kill(pid, 0); errors.Is(kerr, unix.ESRCH) {
		return Proc{}, ErrNoProcess
	}
	if err == nil {
		err = fmt.Errorf("sysctl kern.proc.pid.%d answered for pid %d", pid, k.Proc.P_pid)
	}
	return Proc{}, err
}
```

Create `internal/procinfo/list_linux.go`:

```go
//go:build linux

package procinfo

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"
)

// List returns every process: every numeric entry of /proc. A process that
// exits between the directory read and its stat read is skipped.
func List() ([]Proc, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []Proc
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		p, err := Lookup(pid)
		if errors.Is(err, ErrNoProcess) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Lookup reads /proc/<pid>/stat. A pid with no process is ErrNoProcess;
// a read that races the process's exit answers ESRCH, which is the same.
func Lookup(pid int) (Proc, error) {
	if pid <= 0 {
		return Proc{}, ErrNoProcess
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return Proc{}, ErrNoProcess
	}
	if err != nil {
		return Proc{}, err
	}
	return parseStat(string(b))
}
```

Create `internal/procinfo/list_other.go`:

```go
//go:build !darwin && !linux

package procinfo

// List is not available here (Windows, the BSDs).
func List() ([]Proc, error) { return nil, ErrUnsupported }

// Lookup is not available here.
func Lookup(int) (Proc, error) { return Proc{}, ErrUnsupported }
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go test -race ./internal/procinfo/ -count=1`

Expected: `ok  	github.com/deblasis/incoda/internal/procinfo` (`TestStoppedState` stops a `sleep 30` child, sees `T`, resumes it and always SIGCONTs and kills it in cleanup).

- [ ] **Step 5: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 6: Commit**

```bash
git add internal/procinfo/list.go internal/procinfo/list_darwin.go internal/procinfo/list_linux.go internal/procinfo/list_other.go internal/procinfo/list_test.go internal/procinfo/stat_test.go
git commit -F - <<'MSG'
feat: procinfo lists processes with parent, group, start time and state

sysctl kern.proc.all on macOS and /proc/<pid>/stat on Linux. The start
time tells a recorded pid from a later process that reused the number,
and the state tells a stopped holder from a running one.
MSG
```

---

### Task 4: Orphan records, counted by the idle checks

Before the old-holder kill sends any terminating signal it records the job it is about to end in `<state>/orphans/<old pid>-<unix-nanos>.orphan` (spec 3.2, step 4): the key, the old pid and command, every descendant's pid and start time, and every eligible process group. While anything recorded still runs the record is a live holder on its key, so the lane stays busy until the tree is empty. This task adds the record format and its liveness test and makes the idle checks M2 and M5 count live records (deleting stale ones), with no stop line for them because there is no participant left to kill. The kill that writes records comes in Task 6.

**Files:**
- Create: `internal/machine/orphan.go`, `internal/machine/orphan_unix.go`, `internal/machine/orphan_windows.go`
- Modify: `internal/machine/note.go` (`Blocker.Orphan`), `internal/machine/idle.go` (`findBlockers`, `blockerLines`)
- Create: `internal/machine/orphan_test.go`

**Interfaces:**
- Consumes: `procinfo.Lookup`, `procinfo.ErrNoProcess`, `procinfo.Proc` (Task 3); `atomicfile.Write`, `lane.ValidateKey`, `lane.Ticket.CommandString`, `findBlockers`, `blockerLines`, `holdTicket` (plan 2a).
- Produces: `machine.OrphansDir(stateDir string) string`; `type machine.OrphanProc struct{ PID int; Start uint64 }`; `type machine.Orphan struct{ Key string; PID int; Command []string; Descendants []OrphanProc; Groups []int; ByPID int; At string; File string }` with `CommandString() string`, `Live() bool`, `pidList() string`; `writeOrphan(stateDir string, o *Orphan) (string, error)`; `type machine.BadOrphan struct{ File string; Err error }`; `machine.ReadOrphans(stateDir string) ([]Orphan, []BadOrphan, error)`; `machine.LiveOrphans(stateDir string, sweep bool) ([]Orphan, error)`; `machine.SweepOrphans(stateDir, key string) (int, error)`; `groupHasMembers(g int) bool`; `Blocker.Orphan bool`.

- [ ] **Step 1: Write the failing tests**

Create `internal/machine/orphan_test.go`:

```go
//go:build !windows

package machine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/procinfo"
)

// startSleeper starts `sleep 30`, in its own process group when ownGroup
// is set, and always kills it (and its group) in cleanup.
func startSleeper(t *testing.T, ownGroup bool) (*exec.Cmd, procinfo.Proc) {
	t.Helper()
	c := exec.Command("sleep", "30")
	if ownGroup {
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = c.Process.Kill()
			_ = c.Wait()
		}
	})
	p, err := procinfo.Lookup(c.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	return c, p
}

func killAndReap(t *testing.T, c *exec.Cmd) {
	t.Helper()
	_ = c.Process.Kill()
	_ = c.Wait()
}

func TestOrphanRecordIsLiveUntilItsTreeIsEmpty(t *testing.T) {
	state := t.TempDir()
	c, p := startSleeper(t, false)
	rec := &Orphan{Key: "builds", PID: 999990, Command: []string{"zig", "build"},
		Descendants: []OrphanProc{{PID: p.PID, Start: p.Start}}}
	path, err := writeOrphan(state, rec)
	if err != nil || !strings.HasPrefix(filepath.Base(path), "999990-") || !strings.HasSuffix(path, ".orphan") {
		t.Fatalf("writeOrphan = %s %v", path, err)
	}
	live, err := LiveOrphans(state, true)
	if err != nil || len(live) != 1 || live[0].Key != "builds" || live[0].File != filepath.Base(path) {
		t.Fatalf("LiveOrphans = %+v %v", live, err)
	}
	bs, err := findBlockers(state, phaseM2)
	if err != nil || len(bs) != 1 || !bs[0].Orphan || bs[0].PID != 999990 ||
		bs[0].Command != "zig build (its job is still exiting after a kill: pids "+strconv.Itoa(p.PID)+")" {
		t.Fatalf("findBlockers = %+v %v", bs, err)
	}
	if lines := blockerLines(bs, phaseM2); strings.Contains(strings.Join(lines, "\n"), "incoda kill") {
		t.Fatalf("an orphan has no participant left to kill:\n%s", strings.Join(lines, "\n"))
	}
	killAndReap(t, c)
	live, err = LiveOrphans(state, false)
	if err != nil || len(live) != 0 {
		t.Fatalf("after the tree ended: %+v %v", live, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("a read without sweep must not delete the record")
	}
	if _, err := LiveOrphans(state, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a sweep deletes a stale record")
	}
}

func TestOrphanRecordIgnoresAReusedPid(t *testing.T) {
	state := t.TempDir()
	_, p := startSleeper(t, false)
	if _, err := writeOrphan(state, &Orphan{Key: "k", PID: 999991,
		Descendants: []OrphanProc{{PID: p.PID, Start: p.Start + 1}}}); err != nil {
		t.Fatal(err)
	}
	if live, _ := LiveOrphans(state, false); len(live) != 0 {
		t.Fatal("a pid with another start time is a different process")
	}
}

func TestOrphanRecordCountsAGroupWithMembers(t *testing.T) {
	state := t.TempDir()
	c, p := startSleeper(t, true)
	if p.PGID != p.PID {
		t.Fatalf("the sleeper must lead its own group: %+v", p)
	}
	if _, err := writeOrphan(state, &Orphan{Key: "k", PID: 999992, Groups: []int{p.PGID}}); err != nil {
		t.Fatal(err)
	}
	if live, _ := LiveOrphans(state, false); len(live) != 1 {
		t.Fatal("a recorded group with a member is live")
	}
	killAndReap(t, c)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if live, _ := LiveOrphans(state, false); len(live) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("an empty group is not live")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSweepOrphansIsPerKeyAndKeepsLiveOnes(t *testing.T) {
	state := t.TempDir()
	_, p := startSleeper(t, false)
	for _, o := range []*Orphan{
		{Key: "a", PID: 999993},
		{Key: "b", PID: 999994},
		{Key: "a", PID: 999995, Descendants: []OrphanProc{{PID: p.PID, Start: p.Start}}},
	} {
		if _, err := writeOrphan(state, o); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond) // distinct file names
	}
	if err := os.WriteFile(filepath.Join(OrphansDir(state), "junk.orphan"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := SweepOrphans(state, "a")
	if err != nil || n != 1 {
		t.Fatalf("SweepOrphans(a) = %d %v", n, err)
	}
	all, bad, _ := ReadOrphans(state)
	if len(all) != 2 || len(bad) != 1 || bad[0].File != "junk.orphan" {
		t.Fatalf("left: %+v bad %+v", all, bad)
	}
}

func TestOrphanAndTicketOfOnePidAreOneBlocker(t *testing.T) {
	state := t.TempDir()
	_, p := startSleeper(t, false)
	holdTicket(t, lane.QueuesDir(state), "builds", 4711, "zig", "build")
	if _, err := writeOrphan(state, &Orphan{Key: "builds", PID: 4711,
		Descendants: []OrphanProc{{PID: p.PID, Start: p.Start}}}); err != nil {
		t.Fatal(err)
	}
	bs, err := findBlockers(state, phaseM2)
	if err != nil || len(bs) != 1 || bs[0].Orphan {
		t.Fatalf("findBlockers = %+v %v", bs, err)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go vet ./internal/machine/`

Expected: the test build fails: `undefined: Orphan` (and `writeOrphan`, `LiveOrphans`, `SweepOrphans`, `ReadOrphans`, `OrphansDir`).

- [ ] **Step 3: The record**

Create `internal/machine/orphan.go`:

```go
package machine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/atomicfile"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/procinfo"
)

const (
	orphansName = "orphans"
	orphanExt   = ".orphan"
)

// OrphansDir is <state>/orphans: the records an old-holder kill writes
// before it sends any terminating signal (spec 3.2). Older binaries never
// read it.
func OrphansDir(stateDir string) string { return filepath.Join(stateDir, orphansName) }

// OrphanProc is one recorded descendant: its pid and the start time that
// identifies that incarnation of the pid (procinfo.Proc.Start).
type OrphanProc struct {
	PID   int    `json:"pid"`
	Start uint64 `json:"start"`
}

// Orphan is an orphan record: the job of an older incoda that a kill is
// ending. While any recorded descendant still runs with its recorded start
// time, or any recorded process group still has members, the record is a
// live holder on Key: idle checks wait for it and acquisitions count it
// (spec 2.3, 3.2).
type Orphan struct {
	Key         string       `json:"key"`
	PID         int          `json:"pid"`
	Command     []string     `json:"command,omitempty"`
	Descendants []OrphanProc `json:"descendants"`
	Groups      []int        `json:"groups"`
	ByPID       int          `json:"by_pid"`
	At          string       `json:"at"`

	// File is the record's file name inside orphans/.
	File string `json:"-"`
}

// CommandString renders the recorded command the way tickets do.
func (o Orphan) CommandString() string { return lane.Ticket{Command: o.Command}.CommandString() }

// orphanName is <old pid>-<unix-nanos>.orphan.
func orphanName(pid int, at time.Time) string {
	return fmt.Sprintf("%d-%d%s", pid, at.UnixNano(), orphanExt)
}

// writeOrphan writes the record by temp file plus rename, so a reader sees
// all of it or none of it, and returns its path.
func writeOrphan(stateDir string, o *Orphan) (string, error) {
	now := time.Now()
	if o.At == "" {
		o.At = now.UTC().Format(time.RFC3339Nano)
	}
	if o.Descendants == nil {
		o.Descendants = []OrphanProc{}
	}
	if o.Groups == nil {
		o.Groups = []int{}
	}
	if err := os.MkdirAll(OrphansDir(stateDir), 0o755); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return "", err
	}
	o.File = orphanName(o.PID, now)
	path := filepath.Join(OrphansDir(stateDir), o.File)
	if err := atomicfile.Write(path, append(b, '\n'), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// BadOrphan is a file in orphans/ that does not parse as a record. It is
// never counted (nothing says which key or tree it meant) and never
// deleted; doctor names it.
type BadOrphan struct {
	File string
	Err  error
}

// ReadOrphans reads every record in orphans/, sorted by file name. A
// missing directory holds none.
func ReadOrphans(stateDir string) ([]Orphan, []BadOrphan, error) {
	entries, err := os.ReadDir(OrphansDir(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var out []Orphan
	var bad []BadOrphan
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), orphanExt) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(OrphansDir(stateDir), e.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue // deleted by another reader since the listing
		}
		var o Orphan
		if err == nil {
			err = json.Unmarshal(b, &o)
		}
		if err == nil && (lane.ValidateKey(o.Key) != nil || o.PID <= 0) {
			err = fmt.Errorf("no valid key and pid")
		}
		if err != nil {
			bad = append(bad, BadOrphan{File: e.Name(), Err: err})
			continue
		}
		o.File = e.Name()
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out, bad, nil
}

// Live reports whether the recorded tree still has a process: a recorded
// descendant that exists with its recorded start time and is not a zombie,
// or a recorded group for which kill(-G, 0) answers anything but ESRCH
// (EPERM included: macOS answers it for a group of zombies). A lookup that
// fails for another reason counts as live: an unreadable process table
// must never read as "tree empty".
func (o Orphan) Live() bool {
	for _, d := range o.Descendants {
		p, err := procinfo.Lookup(d.PID)
		if errors.Is(err, procinfo.ErrNoProcess) {
			continue
		}
		if err != nil {
			return true
		}
		if p.Start == d.Start && p.State != 'Z' {
			return true
		}
	}
	for _, g := range o.Groups {
		if groupHasMembers(g) {
			return true
		}
	}
	return false
}

// LiveOrphans returns the records whose tree still runs. With sweep set it
// deletes the others, as idle checks, acquisitions and plain force-release
// do; status and doctor only read.
func LiveOrphans(stateDir string, sweep bool) ([]Orphan, error) {
	all, _, err := ReadOrphans(stateDir)
	if err != nil {
		return nil, err
	}
	var live []Orphan
	for _, o := range all {
		if o.Live() {
			live = append(live, o)
			continue
		}
		if sweep {
			_ = os.Remove(filepath.Join(OrphansDir(stateDir), o.File))
		}
	}
	return live, nil
}

// SweepOrphans deletes the records of key whose tree is empty ("" for
// every key) and returns how many it deleted. Plain force-release uses it.
func SweepOrphans(stateDir, key string) (int, error) {
	all, _, err := ReadOrphans(stateDir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, o := range all {
		if (key != "" && o.Key != key) || o.Live() {
			continue
		}
		if os.Remove(filepath.Join(OrphansDir(stateDir), o.File)) == nil {
			n++
		}
	}
	return n, nil
}

// pidList renders the recorded descendant pids, for display.
func (o Orphan) pidList() string {
	parts := make([]string, len(o.Descendants))
	for i, d := range o.Descendants {
		parts[i] = strconv.Itoa(d.PID)
	}
	return strings.Join(parts, ", ")
}
```

Create `internal/machine/orphan_unix.go`:

```go
//go:build !windows

package machine

import (
	"errors"

	"golang.org/x/sys/unix"
)

// groupHasMembers reports whether process group g has any member:
// kill(-g, 0) answering anything but ESRCH. EPERM counts as members (macOS
// answers it for a group whose members are all zombies).
func groupHasMembers(g int) bool {
	if g <= 1 {
		return false
	}
	return !errors.Is(unix.Kill(-g, 0), unix.ESRCH)
}
```

Create `internal/machine/orphan_windows.go`:

```go
//go:build windows

package machine

// groupHasMembers is never asked on Windows: an old-holder kill there only
// terminates the old incoda (its job object ends the tree) and writes no
// record.
func groupHasMembers(int) bool { return false }
```

- [ ] **Step 4: M2 and M5 wait for live records**

In `internal/machine/note.go`, replace:

```go
	Key     string
	PID     int
	Command string
}

// Note is the line the machine.lock holder writes into the lock file, so
```

with:

```go
	Key     string
	PID     int
	Command string
	// Orphan marks the job of an older incoda that a kill already ended
	// (an orphan record, spec 3.2): it is waited for, but there is no
	// participant left to kill.
	Orphan bool
}

// Note is the line the machine.lock holder writes into the lock file, so
```

In `internal/machine/idle.go`, make these 3 replacements, in order (each quoted block occurs exactly once in the file at that point):

(1 of 3) Replace:

```go
}

// findBlockers probes every ticket the phase covers and returns the live
// ones, sorted by key then pid. Plan 2b adds orphan records here (a record
// is live until its recorded tree is empty).
func findBlockers(stateDir string, ph phase) ([]Blocker, error) {
	dirs, err := laneDirsFor(stateDir, ph)
	if err != nil {
```

with:

```go
}

// findBlockers probes every ticket the phase covers and returns the live
// ones, plus every orphan record whose tree still runs (deleting the
// others), sorted by key then pid, one entry per key and pid.
func findBlockers(stateDir string, ph phase) ([]Blocker, error) {
	dirs, err := laneDirsFor(stateDir, ph)
	if err != nil {
```

(2 of 3) Replace:

```go
			bs = append(bs, Blocker{Key: filepath.Base(d), PID: p.PID(), Command: textsafe.Escape(cmd)})
		}
	}
	sort.Slice(bs, func(i, j int) bool {
		if bs[i].Key != bs[j].Key {
			return bs[i].Key < bs[j].Key
```

with:

```go
			bs = append(bs, Blocker{Key: filepath.Base(d), PID: p.PID(), Command: textsafe.Escape(cmd)})
		}
	}
	orphans, err := LiveOrphans(stateDir, true)
	if err != nil {
		return nil, fmt.Errorf("orphans/: %w", err)
	}
	seen := map[string]bool{}
	for _, b := range bs {
		seen[fmt.Sprintf("%s/%d", b.Key, b.PID)] = true
	}
	for _, o := range orphans {
		if seen[fmt.Sprintf("%s/%d", o.Key, o.PID)] {
			continue
		}
		bs = append(bs, Blocker{Key: o.Key, PID: o.PID, Orphan: true,
			Command: textsafe.Escape(o.CommandString()) + " (its job is still exiting after a kill: pids " + o.pidList() + ")"})
	}
	sort.Slice(bs, func(i, j int) bool {
		if bs[i].Key != bs[j].Key {
			return bs[i].Key < bs[j].Key
```

(3 of 3) Replace:

```go
	for _, b := range bs {
		lines = append(lines, fmt.Sprintf("  %s pid %d: %s", b.Key, b.PID, b.Command))
	}
	lines = append(lines, "ask the user before stopping another session's job; they can run:")
	for _, b := range bs {
		lines = append(lines, "  "+killLine(b, ph))
	}
	return append(lines, "do not force-release them: the job keeps running and the upgrade would overlap it.")
}
```

with:

```go
	for _, b := range bs {
		lines = append(lines, fmt.Sprintf("  %s pid %d: %s", b.Key, b.PID, b.Command))
	}
	var stops []string
	for _, b := range bs {
		if !b.Orphan {
			stops = append(stops, "  "+killLine(b, ph))
		}
	}
	if len(stops) > 0 {
		lines = append(lines, "ask the user before stopping another session's job; they can run:")
		lines = append(lines, stops...)
	}
	return append(lines, "do not force-release them: the job keeps running and the upgrade would overlap it.")
}
```

- [ ] **Step 5: Run the tests to see them pass**

Run: `go test -race ./internal/machine/ -count=1`

Expected: `ok  	github.com/deblasis/incoda/internal/machine`.

- [ ] **Step 6: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 7: Commit**

```bash
git add internal/machine/idle.go internal/machine/note.go internal/machine/orphan.go internal/machine/orphan_test.go internal/machine/orphan_unix.go internal/machine/orphan_windows.go
git commit -F - <<'MSG'
feat: orphan records keep a killed older run's job counted until it is gone

A record names the descendants (pid and start time) and process groups
of an older incoda's job. The idle checks M2 and M5 wait while any of
them still runs, delete stale records, and print no stop line for them:
there is no participant left to kill.
MSG
```

---

### Task 5: Unpooled runs of an older incoda are counted

Spec 2.3: after a careless `rm` of the fence an older incoda can run again, outside every pool. A live ticket under `strays/<n>/<K>/`, or under `queues/<K>/` while the fence is missing, and a live orphan record, are unpooled holders. Every new-binary acquisition counts each one as a held slot on the pools it is charged to (`ChargedPools`): the pools linked from `lanes/K`, pool K itself, or every pool. New runs wait instead of starting beside it, name it on their busy line (`unpooled run by an older incoda: pid N, key K`), and refuse at once with `upgrade-blocked:` when it is their own ancestor. A re-fence and every acquisition poll delete stray lanes whose tickets all died, appending their log to `lanes/<K>/lane.log`.

Until plan 3 links project lanes, only acquisitions on pool lanes are charged (see Decisions). The registry the run uses comes back from `mutatingState`.

**Files:**
- Create: `internal/machine/unpooled.go`
- Modify: `internal/lane/probe.go` (append `RemoveIfIdle`), `internal/lane/acquire.go` (`AcquireOptions.Unpooled`, admission)
- Modify: `internal/machine/migrate.go` (re-fence runs `CleanStrays`)
- Modify: `internal/cli/state.go` (`mutatingState` returns the registry), `internal/cli/config.go`, `internal/cli/run.go`
- Create: `internal/machine/unpooled_test.go`, `internal/lane/unpooled_test.go`, root `strays_test.go`
- Modify: `internal/machine/migrate_test.go` (`TestEnsureRefencesAMigratedLayout`), root `migrate_test.go` (`TestRunReplacesAMissingFence`)

**Interfaces:**
- Consumes: `machine.LiveOrphans`, `Orphan.CommandString` (Task 4); `lane.ProbeLane`, `lane.ListIn`, `lane.Exists`, `lane.ReadConfig`, `appendFragment`, `kindOf`, `StraysDir`, `upgradeBlocked`, `Registry.IsPool`, `refenceWaiting` (plans 2a and Task 2); `oldBinary`, `holdOldTicket`, `readInterval`, `waitForTicket` (root test helpers).
- Produces: `type machine.Unpooled struct{ Key string; PID int; Command, Where string }` with `Line() string`; `machine.ScanUnpooled(stateDir string, clean bool) ([]Unpooled, error)`; `machine.CleanStrays(stateDir string) error`; `machine.ChargedPools(stateDir string, reg *Registry, key string) []string`; `machine.ChargedTo(stateDir string, reg *Registry, pool string, us []Unpooled) []Unpooled`; `machine.UpgradeBlocked(pid int, key string) *Refusal`; `lane.RemoveIfIdle(laneDir string, keepLog func(logPath string)) (bool, error)`; `lane.AcquireOptions.Unpooled func() (int, error)`; `cli.mutatingState(...) (string, *machine.Registry, error)`; test helper `migrated(t) (string, *Registry)` (machine tests) and `startOldRunAfterFenceDeletion` (root).

- [ ] **Step 1: Write the failing tests**

The two modified tests change because a re-fence now deletes stray lanes whose tickets are all dead: the unit test gets one dead and one live stray lane and checks that only the live one stays; the integration test's empty stale lane no longer lingers.

Create `internal/machine/unpooled_test.go`:

```go
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
```

Create `internal/lane/unpooled_test.go`:

```go
package lane

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestAcquireCountsUnpooledHolders: holders without a ticket here (an
// older incoda's unpooled run counted on a pool) hold slots ahead of every
// waiter. A free lane with one such holder admits nobody until it goes;
// an error from the count ends the wait as is.
func TestAcquireCountsUnpooledHolders(t *testing.T) {
	q, err := Open(t.TempDir(), "builds")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	en, err := q.Enroll(Ticket{Slots: 1, Command: []string{"new"}})
	if err != nil {
		t.Fatal(err)
	}
	defer en.Release(0)

	var unpooled atomic.Int32
	unpooled.Store(1)
	waits := 0
	opt := AcquireOptions{Wait: 200 * time.Millisecond, Poll: 20 * time.Millisecond,
		Unpooled: func() (int, error) { return int(unpooled.Load()), nil },
		OnWait:   func(int, int, []Entry, time.Duration) { waits++ }}
	if err := en.Acquire(context.Background(), opt); err != ErrTimeout {
		t.Fatalf("one unpooled holder on a one-slot lane: want ErrTimeout, got %v", err)
	}
	if waits == 0 {
		t.Fatal("a run held off by an unpooled holder is waiting and must say so")
	}

	go func() { time.Sleep(100 * time.Millisecond); unpooled.Store(0) }()
	opt.Wait = 5 * time.Second
	if err := en.Acquire(context.Background(), opt); err != nil {
		t.Fatalf("once the unpooled holder is gone: %v", err)
	}

	boom := errors.New("upgrade-blocked")
	en2, err := q.Enroll(Ticket{Slots: 1, Command: []string{"other"}})
	if err != nil {
		t.Fatal(err)
	}
	defer en2.Release(0)
	opt.Unpooled = func() (int, error) { return 0, boom }
	if err := en2.Acquire(context.Background(), opt); !errors.Is(err, boom) {
		t.Fatalf("the count's error ends the wait: %v", err)
	}
}

// TestRemoveIfIdle: a stray lane with only dead tickets is deleted under
// its registry lock after its log is handed on; one with a live ticket
// is kept; a missing directory is no error.
func TestRemoveIfIdle(t *testing.T) {
	root := t.TempDir()
	q, err := OpenIn(root, "dead", Create)
	if err != nil {
		t.Fatal(err)
	}
	en, err := q.Enroll(Ticket{Slots: 1, Command: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	en.lock.Close() // dies without Release: a dead ticket stays behind
	q.Close()
	var kept []string
	keep := func(p string) { kept = append(kept, p) }
	if ok, err := RemoveIfIdle(q.Dir, keep); !ok || err != nil {
		t.Fatalf("dead lane: %v %v", ok, err)
	}
	if ExistsIn(root, "dead") || len(kept) != 1 || kept[0] != LogPath(q.Dir) {
		t.Fatalf("deleted=%v kept=%v", !ExistsIn(root, "dead"), kept)
	}

	live, err := OpenIn(root, "live", Create)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	held, err := live.Enroll(Ticket{Slots: 1, Command: []string{"y"}})
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release(0)
	if ok, err := RemoveIfIdle(live.Dir, keep); ok || err != nil || !ExistsIn(root, "live") {
		t.Fatalf("live lane: %v %v", ok, err)
	}
	if ok, err := RemoveIfIdle(filepath.Join(root, "missing"), keep); ok || err != nil {
		t.Fatalf("missing lane: %v %v", ok, err)
	}
}
```

In `internal/machine/migrate_test.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file at that point):

(1 of 2) Replace:

```go
	if err := os.Remove(lane.QueuesDir(state)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(lane.QueuesDir(state), "stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Inspect reports the missing fence and changes nothing.
	v, err := Inspect(state)
	if err != nil || !v.Migrated || !v.FenceMissing {
```

with:

```go
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
```

(2 of 2) Replace:

```go
	if len(batches) != 1 {
		t.Fatalf("want the queues/ dir in one strays batch, got %v", batches)
	}
	if _, err := os.Stat(filepath.Join(StraysDir(state), batches[0].Name(), "stale")); err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(MachineLogPath(state))
	if !strings.Contains(string(log), "event=refence pid=") || !strings.Contains(string(log), "strays="+batches[0].Name()) {
		t.Fatalf("machine.log:\n%s", log)
```

with:

```go
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
```

Create `strays_test.go`:

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
	"time"

	"github.com/deblasis/incoda/internal/machine"
)

// startOldRunAfterFenceDeletion migrates a state directory, deletes the
// fence (the careless rm of spec 2.3), and starts an older incoda run on
// key, which recreates queues/<key> and holds it. It returns the run, which
// the caller waits for, and the state directory.
func startOldRunAfterFenceDeletion(t *testing.T, tag, key string, args ...string) (*exec.Cmd, string) {
	t.Helper()
	incoda, _ := binaries(t)
	old := oldBinary(t, tag)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "seed"); code != 0 {
		t.Fatalf("migrate: %d\n%s", code, out)
	}
	if err := os.Remove(filepath.Join(state, "queues")); err != nil {
		t.Fatal(err)
	}
	o := exec.Command(old, append([]string{"run", "--queue", key, "--poll", "50ms", "--quiet", "--"}, args...)...)
	o.Env = laneEnv(state)
	if err := o.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Process.Kill(); _ = o.Wait() })
	waitForTicket(t, filepath.Join(state, "queues", key))
	return o, state
}

// TestFenceDeletionWithALiveStrayIsCounted: after a careless rm of the
// fence an older incoda runs on the builds pool. The next new run re-places
// the fence (its queues/ goes to strays/), counts that run as a held slot
// on builds, names it on its busy line, and starts only after it ended;
// the dead stray lane is then deleted and its log kept.
func TestFenceDeletionWithALiveStrayIsCounted(t *testing.T) {
	incoda, stamp := binaries(t)
	stamps := t.TempDir()
	o, state := startOldRunAfterFenceDeletion(t, "v0.6.0", "builds", stamp, filepath.Join(stamps, "old.txt"), "old", "2500")

	out, code := runIncoda(t, incoda, state, "run", "--queue", "builds", "--wait", "60s", "--poll", "50ms", "--",
		stamp, filepath.Join(stamps, "new.txt"), "new", "10")
	if code != 0 {
		t.Fatalf("new run: exit %d\n%s", code, out)
	}
	if want := fmt.Sprintf("incoda:   unpooled run by an older incoda: pid %d, key builds\n", o.Process.Pid); !strings.Contains(out, want) {
		t.Fatalf("missing %q in:\n%s", want, out)
	}
	if err := o.Wait(); err != nil {
		t.Fatalf("the old run must finish normally: %v", err)
	}
	oldIv, ok1 := readInterval(t, filepath.Join(stamps, "old.txt"))
	newIv, ok2 := readInterval(t, filepath.Join(stamps, "new.txt"))
	if !ok1 || !ok2 || newIv.enter < oldIv.exit {
		t.Fatalf("the new job overlapped the unpooled old one: old %+v new %+v", oldIv, newIv)
	}
	if !machine.FencePlaced(state) {
		t.Fatal("the new run re-places the fence")
	}
	batches, _ := os.ReadDir(machine.StraysDir(state))
	for _, b := range batches {
		if _, err := os.Stat(filepath.Join(machine.StraysDir(state), b.Name(), "builds")); err == nil {
			t.Fatal("the dead stray lane must be deleted by an acquisition poll")
		}
	}
	log, _ := os.ReadFile(filepath.Join(laneDir(state, "builds"), "lane.log"))
	if !strings.Contains(string(log), fmt.Sprintf("event=acquire pid=%d", o.Process.Pid)) {
		t.Fatalf("the stray's log fragment must be appended to lanes/builds/lane.log:\n%s", log)
	}
}

// TestUnknownStrayKeyCountsOnEveryPool: an unpooled run on a key that is
// no lane of the new layout counts on every pool, and on nothing else: a
// project key does not wait for it.
func TestUnknownStrayKeyCountsOnEveryPool(t *testing.T) {
	incoda, stamp := binaries(t)
	o, state := startOldRunAfterFenceDeletion(t, "v0.6.0", "oldjob", stamp, filepath.Join(t.TempDir(), "old.txt"), "old", "30000")
	// config re-places the fence, which moves queues/ to strays/.
	if out, code := runIncoda(t, incoda, state, "config", "proj"); code != 0 {
		t.Fatalf("config: %d\n%s", code, out)
	}
	for _, pool := range []string{"builds", "computer-use", "tests", "vm"} {
		out, code := runIncoda(t, incoda, state, "run", "--queue", pool, "--wait", "300ms", "--poll", "50ms", "--", stamp, filepath.Join(t.TempDir(), "p.txt"), "p", "1")
		if code != 121 || !strings.Contains(out, fmt.Sprintf("unpooled run by an older incoda: pid %d, key oldjob", o.Process.Pid)) {
			t.Fatalf("pool %s: want exit 121 naming the unpooled run, got %d:\n%s", pool, code, out)
		}
	}
	if out, code := runIncoda(t, incoda, state, "run", "--queue", "proj", "--wait", "0", "--", stamp, filepath.Join(t.TempDir(), "q.txt"), "q", "1"); code != 0 {
		t.Fatalf("a project key is not charged: %d\n%s", code, out)
	}
}

// TestUnpooledAncestorIsUpgradeBlocked: a new run inside the job of an
// unpooled older run would wait for its own ancestor; it refuses at once.
func TestUnpooledAncestorIsUpgradeBlocked(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no ancestry walk on Windows")
	}
	incoda, stamp := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "seed"); code != 0 {
		t.Fatalf("migrate: %d\n%s", code, out)
	}
	// This test process is the new run's parent: a stray ticket it holds is
	// an unpooled older incoda that is the run's ancestor.
	holdOldTicket(t, filepath.Join(machine.StraysDir(state), "1"), "builds", os.Getpid(), "outer", "job")
	start := time.Now()
	out, code := runIncoda(t, incoda, state, "run", "--queue", "builds", "--wait", "30s", "--", stamp, filepath.Join(t.TempDir(), "s"), "s", "1")
	want := fmt.Sprintf(`incoda: upgrade-blocked: an older incoda (pid %d, an ancestor of this process) holds "builds"; rerun the outer command after it exits`, os.Getpid())
	if code != 120 || !strings.Contains(out, want) {
		t.Fatalf("want exit 120 and %q, got %d:\n%s", want, code, out)
	}
	if el := time.Since(start); el > 10*time.Second {
		t.Fatalf("refused after %s; it must refuse at once", el)
	}
}
```

In `migrate_test.go`, replace:

```go
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
```

with:

```go
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
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go vet ./internal/... . 2>&1 | head`

Expected: the test build fails: `unknown field Unpooled in struct literal of type AcquireOptions` and `undefined: ChargedPools` (and `ScanUnpooled`, `CleanStrays`, `RemoveIfIdle`).

- [ ] **Step 3: Delete a stray lane only when every ticket in it is dead**

In `internal/lane/probe.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file at that point):

(1 of 2) Replace:

```go
	"encoding/json"
	"errors"
	"os"

	"github.com/deblasis/incoda/internal/lockfile"
)
```

with:

```go
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/deblasis/incoda/internal/lockfile"
)
```

(2 of 2) Replace:

```go
	}
	return p
}
```

with:

```go
	}
	return p
}

// RemoveIfIdle deletes laneDir when no ticket in it is live, deciding
// under one hold of its registry lock with the same probe as ProbeLane
// (a ticket whose probe fails counts as live). Before deleting it hands
// the directory's lane.log path to keepLog. It reports whether it deleted
// the directory; a directory that is already gone is not an error.
//
// It is meant for stray lane directories (spec 2.3): nobody enrolls there,
// because older binaries address queues/<K>, which the fence makes
// ENOTDIR, so no ticket can appear between the probe and the delete. The
// registry lock file goes last, after the hold ends, because Windows
// cannot delete a directory while a handle inside it is open.
func RemoveIfIdle(laneDir string, keepLog func(logPath string)) (bool, error) {
	reg, err := lockfile.OpenExisting(RegistryLockPath(laneDir))
	if errors.Is(err, os.ErrNotExist) {
		// No registry lock: older binaries create it before any ticket,
		// so no ticket here was ever live.
		if fi, serr := os.Stat(laneDir); serr != nil || !fi.IsDir() {
			return false, nil
		}
		keepLog(LogPath(laneDir))
		return true, os.RemoveAll(laneDir)
	}
	if err != nil {
		return false, err
	}
	if err := reg.Lock(); err != nil {
		reg.Close()
		return false, err
	}
	entries, err := os.ReadDir(laneDir)
	if err != nil {
		reg.Close()
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	for _, de := range entries {
		if de.IsDir() {
			continue
		}
		if _, ok := parseTicketName(de.Name()); !ok {
			continue
		}
		if p := probeLocked(laneDir, de.Name()); p.Live || p.ProbeErr != nil {
			reg.Close()
			return false, nil
		}
	}
	keepLog(LogPath(laneDir))
	for _, de := range entries {
		if de.Name() != registryLockName {
			_ = os.RemoveAll(filepath.Join(laneDir, de.Name()))
		}
	}
	reg.Close()
	return true, os.RemoveAll(laneDir)
}
```

- [ ] **Step 4: Scan, charge and clean unpooled holders**

Create `internal/machine/unpooled.go`:

```go
package machine

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/textsafe"
)

// Unpooled is a holder no pool admitted (spec 2.3): a live ticket of an
// older incoda under strays/<n>/<K>/, or under queues/<K>/ while the fence
// is missing, or an orphan record whose tree still runs. New-binary
// acquisitions count each one as a held slot (ChargedPools).
type Unpooled struct {
	Key string
	PID int
	// Command is escaped for display.
	Command string
	// Where is "strays/<n>", "queues" or "orphans".
	Where string
}

// Line is how busy lines and status name it.
func (u Unpooled) Line() string {
	return fmt.Sprintf("unpooled run by an older incoda: pid %d, key %s", u.PID, u.Key)
}

// UpgradeBlocked is the refusal for a run whose own ancestor is an older
// incoda it would wait for: an idle-check blocker (spec 3.3 M2) or a
// counted unpooled holder (spec 2.3, 2.6 self-wait).
func UpgradeBlocked(pid int, key string) *Refusal { return upgradeBlocked(pid, key) }

// ScanUnpooled lists the live unpooled holders, sorted by key then pid, one
// per key and pid. Every ticket probe is the create-free probe of spec 2.6
// step 2 under that directory's registry.lock. Its cost is one ReadDir of
// strays/ and one of orphans/ (plus an lstat of queues and the probes of
// whatever stray lanes exist).
//
// With clean set (an acquisition poll, a re-fence, doctor) it also deletes
// every strays/<n>/<K>/ whose tickets are all dead, appending its lane.log
// to lanes/<K>/lane.log when that lane exists, deletes stray batches left
// empty, and deletes orphan records whose tree is empty. It never deletes
// strays/ itself (a re-fence may be moving a directory into it).
//
// Call it only on a migrated layout: during a migration strays/ belongs to
// M5 and M6.
func ScanUnpooled(stateDir string, clean bool) ([]Unpooled, error) {
	var out []Unpooled
	batches, err := os.ReadDir(StraysDir(stateDir))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("strays/: %w", err)
	}
	for _, b := range batches {
		if !b.IsDir() {
			continue
		}
		batch := filepath.Join(StraysDir(stateDir), b.Name())
		found, err := probeRoot(batch, "strays/"+b.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
		if clean {
			if err := cleanBatch(stateDir, batch); err != nil {
				return nil, err
			}
		}
	}
	if kindOf(lane.QueuesDir(stateDir)) == aDir {
		found, err := probeRoot(lane.QueuesDir(stateDir), "queues")
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	orphans, err := LiveOrphans(stateDir, clean)
	if err != nil {
		return nil, fmt.Errorf("orphans/: %w", err)
	}
	for _, o := range orphans {
		out = append(out, Unpooled{Key: o.Key, PID: o.PID, Command: textsafe.Escape(o.CommandString()), Where: "orphans"})
	}
	return dedupeUnpooled(out), nil
}

// probeRoot probes every lane directory in root. A directory deleted by a
// concurrent cleaner between the listing and the probe holds nothing.
func probeRoot(root, where string) ([]Unpooled, error) {
	keys, err := lane.ListIn(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	var out []Unpooled
	for _, k := range keys {
		live, err := lane.ProbeLane(filepath.Join(root, k))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", where, k, err)
		}
		for _, p := range live {
			cmd := "(unreadable ticket)"
			if p.PayloadErr == nil && p.ProbeErr == nil {
				cmd = p.Ticket.CommandString()
			}
			out = append(out, Unpooled{Key: k, PID: p.PID(), Command: textsafe.Escape(cmd), Where: where})
		}
	}
	return out, nil
}

// CleanStrays deletes every fully dead lane directory under strays/ (see
// ScanUnpooled), without listing anything. A re-fence and doctor use it on
// a migrated layout.
func CleanStrays(stateDir string) error {
	batches, err := os.ReadDir(StraysDir(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, b := range batches {
		if b.IsDir() {
			if err := cleanBatch(stateDir, filepath.Join(StraysDir(stateDir), b.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// cleanBatch deletes every fully dead lane directory of one strays batch,
// passing its log fragment to lanes/<K>/lane.log when that lane exists,
// and then the batch itself if nothing is left in it.
func cleanBatch(stateDir, batch string) error {
	keys, err := lane.ListIn(batch)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("%s: %w", batch, err)
	}
	for _, k := range keys {
		_, err := lane.RemoveIfIdle(filepath.Join(batch, k), func(logPath string) {
			if lane.Exists(stateDir, k) {
				appendFragment(logPath, lane.LogPath(lane.LaneDir(stateDir, k)))
			}
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s/%s: %w", batch, k, err)
		}
	}
	if left, err := os.ReadDir(batch); err == nil && len(left) == 0 {
		_ = os.Remove(batch)
	}
	return nil
}

func dedupeUnpooled(us []Unpooled) []Unpooled {
	sort.SliceStable(us, func(i, j int) bool {
		if us[i].Key != us[j].Key {
			return us[i].Key < us[j].Key
		}
		return us[i].PID < us[j].PID
	})
	var out []Unpooled
	for i, u := range us {
		if i > 0 && u.Key == us[i-1].Key && u.PID == us[i-1].PID {
			continue
		}
		out = append(out, u)
	}
	return out
}

// ChargedPools is where an unpooled holder on key counts as one held slot
// (spec 2.3): the pools linked from lanes/<key> if key is a linked project
// lane, key itself if it is a pool, and every pool otherwise (an unknown or
// unlinked key, a config that cannot be read, or a link naming no
// registered pool: the safe direction).
func ChargedPools(stateDir string, reg *Registry, key string) []string {
	if reg.IsPool(key) {
		return []string{key}
	}
	if cfg, err := lane.ReadConfig(lane.LaneDir(stateDir, key)); err == nil {
		var linked []string
		for _, p := range cfg.Pools {
			if reg.IsPool(p) {
				linked = append(linked, p)
			}
		}
		if len(linked) > 0 {
			sort.Strings(linked)
			return linked
		}
	}
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
		}
	}
	return out
}
```

In `internal/machine/migrate.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file at that point):

(1 of 2) Replace:

```go
				if err := refenceWaiting(stateDir, lk, o, &w); err != nil {
					return nil, err
				}
			}
			return reg, nil
		}
```

with:

```go
				if err := refenceWaiting(stateDir, lk, o, &w); err != nil {
					return nil, err
				}
				// A re-fence on a migrated layout deletes every stray
				// lane whose tickets are all dead (spec 2.3); live ones
				// stay and are counted by acquisitions.
				if err := CleanStrays(stateDir); err != nil {
					return nil, stateErrorf("cannot clean strays/: %s", esc(err))
				}
			}
			return reg, nil
		}
```

(2 of 2) Replace:

```go
}

// refence re-places the fence with the race rule and logs event=refence to
// machine.log, naming the strays it made. Counting and cleaning strays is
// plan 2b.
func refence(stateDir string) error {
	moved, err := placeFence(stateDir)
	if err != nil {
```

with:

```go
}

// refence re-places the fence with the race rule and logs event=refence to
// machine.log, naming the strays it made. Callers on a migrated layout then
// run CleanStrays; during a migration M5 and M6 own strays/.
func refence(stateDir string) error {
	moved, err := placeFence(stateDir)
	if err != nil {
```

- [ ] **Step 5: Count them in the acquisition**

In `run.go` the busy line now skips the run's own ticket when it is inside the slot count: a run held off only by unpooled holders would otherwise list itself as a holder.

In `internal/lane/acquire.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file at that point):

(1 of 2) Replace:

```go
	// it already holds: a kill addressed to one of those must end the wait
	// on the next one, not sit unread until every key is held.
	Killed func() (KillRequest, bool)
}

// Acquire blocks until this enrollment holds a slot, the wait budget runs out,
```

with:

```go
	// it already holds: a kill addressed to one of those must end the wait
	// on the next one, not sit unread until every key is held.
	Killed func() (KillRequest, bool)
	// Unpooled, when set, is called on every poll after the position. It
	// returns how many of this lane's slots are held by holders that have
	// no ticket here: unpooled runs of an older incoda counted on a pool
	// (spec 2.3). They hold ahead of every waiter, so this run is admitted
	// only when its position plus that count is below the slot count. An
	// error ends the wait and is returned as is.
	Unpooled func() (int, error)
}

// Acquire blocks until this enrollment holds a slot, the wait budget runs out,
```

(2 of 2) Replace:

```go
		if err != nil {
			return err
		}
		if idx >= 0 && idx < slots {
			e.MarkAcquired()
			return nil
		}
```

with:

```go
		if err != nil {
			return err
		}
		extra := 0
		if opt.Unpooled != nil {
			if extra, err = opt.Unpooled(); err != nil {
				return err
			}
		}
		if idx >= 0 && idx+extra < slots {
			e.MarkAcquired()
			return nil
		}
```

In `internal/cli/state.go`, replace:

```go
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
```

with:

```go
// that takes a ticket or writes config, then migrates it or re-places a
// missing fence (machine.Ensure), before the caller holds any ticket.
// start and wait are the command's --wait budget, which this spends first.
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

// machineExit maps the machine package's errors to exit codes: 122 for
```

In `internal/cli/config.go`, replace:

```go
	if err != nil {
		return err
	}
	dir, err := mutatingState(start, wait.d, 200*time.Millisecond, procinfo.ParentChain(), stderr)
	if err != nil {
		return err
	}
```

with:

```go
	if err != nil {
		return err
	}
	dir, _, err := mutatingState(start, wait.d, 200*time.Millisecond, procinfo.ParentChain(), stderr)
	if err != nil {
		return err
	}
```

In `internal/cli/run.go`, make these 6 replacements, in order (each quoted block occurs exactly once in the file at that point):

(1 of 6) Replace:

```go
	"github.com/deblasis/incoda/internal/colorize"
	"github.com/deblasis/incoda/internal/held"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/textsafe"
)
```

with:

```go
	"github.com/deblasis/incoda/internal/colorize"
	"github.com/deblasis/incoda/internal/held"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/textsafe"
)
```

(2 of 6) Replace:

```go
		return err
	}
	chain := procinfo.ParentChain()
	dir, err := mutatingState(start, wait.d, *poll, chain, stderr)
	if err != nil {
		return err
	}
```

with:

```go
		return err
	}
	chain := procinfo.ParentChain()
	dir, reg, err := mutatingState(start, wait.d, *poll, chain, stderr)
	if err != nil {
		return err
	}
```

(3 of 6) Replace:

```go
			}
		}
		key := pt.key
		acqErr := en.Acquire(ctx, lane.AcquireOptions{
			Wait:   budget,
			Poll:   *poll,
```

with:

```go
			}
		}
		key := pt.key
		// On a pool, unpooled runs of an older incoda (strays and orphan
		// records, spec 2.3) hold slots too. Each poll rescans them,
		// deletes stray lanes that have fully died, and refuses at once
		// when one of them is this run's own ancestor: waiting for it
		// would never end.
		var unpooled []machine.Unpooled
		var countUnpooled func() (int, error)
		if reg.IsPool(key) {
			countUnpooled = func() (int, error) {
				all, err := machine.ScanUnpooled(dir, true)
				if err != nil {
					return 0, &machine.StateError{Msg: "machine-state: cannot scan for unpooled runs: " + textsafe.Escape(err.Error())}
				}
				mine := machine.ChargedTo(dir, reg, key, all)
				if !chain.Skip {
					for _, u := range mine {
						if chain.Contains(u.PID) {
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
			Poll:   *poll,
```

(4 of 6) Replace:

```go
				}
				return lane.KillRequest{}, false
			},
			OnWait: func(pos, effSlots int, live []lane.Entry, waited time.Duration) {
				if *quiet {
					return
```

with:

```go
				}
				return lane.KillRequest{}, false
			},
			Unpooled: countUnpooled,
			OnWait: func(pos, effSlots int, live []lane.Entry, waited time.Duration) {
				if *quiet {
					return
```

(5 of 6) Replace:

```go
					if i >= effSlots {
						break
					}
					fmt.Fprintf(stderr, "%s   %s\n", p.Dim("incoda:"),
						p.Dim(fmt.Sprintf("holder pid %d in %s: %s", e.Ticket.PID, textsafe.Escape(e.Ticket.Dir), textsafe.Escape(e.Ticket.CommandString()))))
				}
			},
		})
		if acqErr != nil {
```

with:

```go
					if i >= effSlots {
						break
					}
					if e.File == en.Name() {
						// Held off only by unpooled runs: this run's own
						// ticket is inside the slot count.
						continue
					}
					fmt.Fprintf(stderr, "%s   %s\n", p.Dim("incoda:"),
						p.Dim(fmt.Sprintf("holder pid %d in %s: %s", e.Ticket.PID, textsafe.Escape(e.Ticket.Dir), textsafe.Escape(e.Ticket.CommandString()))))
				}
				for _, u := range unpooled {
					fmt.Fprintf(stderr, "%s   %s\n", p.Dim("incoda:"), p.Dim(u.Line()))
				}
			},
		})
		if acqErr != nil {
```

(6 of 6) Replace:

```go
				return exitWith(ExitKilled, "%s", p.Red(fmt.Sprintf("cancelled while queued on %q by %s: %s",
					key, textsafe.Escape(killed.Request.By), textsafe.Escape(killed.Request.Reason))))
			}
			if errors.Is(acqErr, lane.ErrTimeout) {
				rc = ExitTimeout
				pt.q.Logf("queue=%s event=giveup pid=%d waited=%s", key, os.Getpid(), wait.d)
```

with:

```go
				return exitWith(ExitKilled, "%s", p.Red(fmt.Sprintf("cancelled while queued on %q by %s: %s",
					key, textsafe.Escape(killed.Request.By), textsafe.Escape(killed.Request.Reason))))
			}
			var rf *machine.Refusal
			var se *machine.StateError
			if errors.As(acqErr, &rf) || errors.As(acqErr, &se) {
				rc = ExitUsage
				if se != nil {
					rc = ExitState
				}
				return machineExit(acqErr)
			}
			if errors.Is(acqErr, lane.ErrTimeout) {
				rc = ExitTimeout
				pt.q.Logf("queue=%s event=giveup pid=%d waited=%s", key, os.Getpid(), wait.d)
```

- [ ] **Step 6: Run the tests to see them pass**

Run: `go test -race ./internal/... -count=1 && go test . -run 'TestFenceDeletionWithALiveStrayIsCounted|TestUnknownStrayKeyCountsOnEveryPool|TestUnpooledAncestorIsUpgradeBlocked|TestRunReplacesAMissingFence' -count=1`

Expected: every package `ok`; the root run `ok  	github.com/deblasis/incoda` (about 25s: two tests run a v0.6.0 binary).

- [ ] **Step 7: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 8: Commit**

```bash
git add internal/cli/config.go internal/cli/run.go internal/cli/state.go internal/lane/acquire.go internal/lane/probe.go internal/lane/unpooled_test.go internal/machine/migrate.go internal/machine/migrate_test.go internal/machine/unpooled.go internal/machine/unpooled_test.go migrate_test.go strays_test.go
git commit -F - <<'MSG'
feat: acquisitions on a pool count unpooled runs of an older incoda

A live ticket under strays/, or under queues/ while the fence is
missing, and a live orphan record each hold a slot on the pools their
key is charged to. A run waits for them, names them on its busy line,
and refuses at once with upgrade-blocked when one is its own ancestor.
A re-fence and every acquisition poll delete stray lanes whose tickets
all died, appending their log to lanes/<K>/lane.log.
MSG
```

---

### Task 6: The old-holder kill

Spec 3.2. Today's `kill --force` sends SIGKILL to the incoda pid only; from v0.3.0 on an old incoda's child shares its group, so the job survives with ppid 1 while the lane reads free. `KillOldHolder` ends the whole job (Unix): refuse when the old incoda is kill's ancestor (step 0); SIGSTOP it (1); re-check that it still holds its ticket by `TryLock` of the ticket file alone (2; released means SIGCONT and abort); walk and SIGSTOP its descendants until a pass is stable (3), excluding kill and its ancestors; write the orphan record (4); SIGKILL every recorded group, every recorded descendant whose start time still matches, then the old incoda (5); wait until the tree is empty and the ticket free; delete the record. From the first SIGSTOP to the last SIGKILL SIGINT, SIGTERM and SIGHUP are ignored, no registry lock is taken, and every abort path resumes everything it stopped. Windows only terminates (its job object ends the tree).

`FindKillTarget` says where kill's participant is and whether it is a run of this binary or an old-layout holder; `KillLine` builds the printed stop lines. Per the ruling (Decisions), every old-layout holder is ended by this kill without `--force`, so the M2 and M5 stop lines drop `--force`; the tests that expected it (`idle_test.go`, `migrate_test.go`, `carried_test.go`, root `oldbin_test.go` and `migrate_crash_test.go`) are updated here. Nothing calls `KillOldHolder` until Task 7.

**Files:**
- Create: `internal/machine/killtarget.go`, `internal/machine/oldkill.go`, `internal/machine/oldkill_unix.go`, `internal/machine/oldkill_windows.go`
- Modify: `internal/machine/idle.go` (`killLine` uses `KillLine` and prints no `--force`; `UpgradeReason`)
- Create: `internal/machine/killtarget_test.go`, `internal/machine/oldkill_unix_test.go`
- Modify (stop lines without `--force`): `internal/machine/idle_test.go` (`TestWaitIdleM5ProbesLanesAndStraysWithForce` becomes `TestWaitIdleM5ProbesLanesAndStrays`), `internal/machine/migrate_test.go` (`TestCommitRefencesWhenTheFenceVanishedDuringM5`), `internal/machine/carried_test.go` (`TestEmptyDirRaceSendsTheFirstOldRunToStrays`), root `oldbin_test.go` (`TestMigrationWaitsForAnOldRunThatSlipsIn`), root `migrate_crash_test.go` (`TestFenceRacesSendANewQueuesDirToStrays`)

**Interfaces:**
- Consumes: `procinfo.List`, `procinfo.Lookup`, `procinfo.Proc`, `procinfo.Chain` (Task 3, plan 1); `writeOrphan`, `Orphan`, `Orphan.Live`, `Orphan.pidList` (Task 4); `lane.ProbeLane`, `lockfile.OpenExisting`, `crashpoint`, `Inspect`, `View`, `StraysDir`, `kindOf`, `stateErrorf`, `esc` (plan 2a); `proc.Terminate`; `migrated` (Task 5 test helper).
- Produces: `type machine.TargetKind int` with `TargetNone`, `TargetLane`, `TargetOld`; `type machine.KillTarget struct{ Kind TargetKind; Key, Root, Dir, Ticket string; PID int; Command []string }` with `Old() bool`; `machine.FindKillTarget(stateDir string, v View, key string, pid int) (KillTarget, error)`; `killLine(b Blocker) string` (no `--force`); `machine.KillLine(key string, pid int, reason string, force bool) string`; `machine.UpgradeReason`; `type machine.OldKillResult struct{ Processes int }`; `machine.KillOldHolder(stateDir string, t KillTarget, chain procinfo.Chain) (OldKillResult, error)`; Unix seams `signalFn`, `listFn`, `lookupFn`, `walkSettle`; seams `writeOrphanFn`, `treeGoneWait`; `walkTree(root int, exclude map[int]bool, stopped *[]int) ([]OrphanProc, []int, error)`; crash points `kill-before-stop`, `kill-stopped`.

- [ ] **Step 1: Write the failing tests**

Create `internal/machine/killtarget_test.go`:

```go
package machine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/deblasis/incoda/internal/lane"
)

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
		tg, err := FindKillTarget(state, v, key, pid)
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
```

Create `internal/machine/oldkill_unix_test.go`:

```go
//go:build !windows

package machine

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/deblasis/incoda/internal/procinfo"
)

// fakeProcs is a process table for walkTree: SIGSTOP marks a process
// stopped, and a process can appear on a later listing (a fork that raced
// the stop).
type fakeProcs struct {
	mu      sync.Mutex
	procs   map[int]procinfo.Proc
	later   []procinfo.Proc // added on the second listing
	lists   int
	signals []string
}

func (f *fakeProcs) list() ([]procinfo.Proc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	if f.lists == 2 {
		for _, p := range f.later {
			f.procs[p.PID] = p
		}
	}
	var out []procinfo.Proc
	for _, p := range f.procs {
		out = append(out, p)
	}
	return out, nil
}

func (f *fakeProcs) signal(pid int, sig unix.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signals = append(f.signals, fmt.Sprintf("%d:%d", pid, sig))
	if p, ok := f.procs[pid]; ok && sig == unix.SIGSTOP {
		p.State = 'T'
		f.procs[pid] = p
	}
	return nil
}

func TestWalkTreeStopsEveryDescendantUntilStable(t *testing.T) {
	proc := func(pid, ppid, pgid int, st byte) procinfo.Proc {
		return procinfo.Proc{PID: pid, PPID: ppid, PGID: pgid, Start: uint64(pid) * 10, State: st}
	}
	f := &fakeProcs{procs: map[int]procinfo.Proc{
		1:   proc(1, 0, 1, 'R'),
		40:  proc(40, 1, 40, 'R'),     // kill's ancestor
		100: proc(100, 40, 40, 'T'),   // the old incoda, already stopped
		101: proc(101, 100, 101, 'R'), // its child, leading its own group
		60:  proc(60, 100, 60, 'R'),   // a child leading a group kill sits in
		102: proc(102, 101, 101, 'R'),
		50:  proc(50, 101, 60, 'R'),  // kill itself, inside the job's tree
		51:  proc(51, 50, 60, 'R'),   // kill's own child: never walked into
		70:  proc(70, 101, 101, 'Z'), // a zombie: nothing to stop or record
	}, later: []procinfo.Proc{proc(103, 102, 101, 'R')}}
	listFn, signalFn = f.list, f.signal
	defer func() { listFn, signalFn = procinfo.List, unix.Kill }()

	stopped := []int{100}
	desc, groups, err := walkTree(100, map[int]bool{50: true, 40: true}, &stopped)
	if err != nil {
		t.Fatal(err)
	}
	var pids []string
	for _, d := range desc {
		pids = append(pids, fmt.Sprintf("%d@%d", d.PID, d.Start))
	}
	got := strings.Join(pids, " ")
	if got != "101@1010 60@600 102@1020 103@1030" && got != "60@600 101@1010 102@1020 103@1030" {
		t.Fatalf("descendants %s", got)
	}
	if fmt.Sprint(groups) != "[101]" {
		t.Fatalf("groups %v: 101 leads a group of the job; 60 holds kill and must never be signalled as a group", groups)
	}
	for _, s := range f.signals {
		pid := strings.Split(s, ":")[0]
		if pid == "50" || pid == "51" || pid == "40" || pid == "70" {
			t.Fatalf("signalled an excluded process or a zombie: %v", f.signals)
		}
	}
	if len(stopped) != 5 || f.lists < 3 {
		t.Fatalf("stopped %v after %d listings; the walk repeats until a pass finds nothing new", stopped, f.lists)
	}
}

func TestWalkTreeListingFailure(t *testing.T) {
	boom := errors.New("sysctl refused")
	listFn = func() ([]procinfo.Proc, error) { return nil, boom }
	defer func() { listFn = procinfo.List }()
	if _, _, err := walkTree(100, nil, &[]int{}); !errors.Is(err, boom) {
		t.Fatalf("want the listing error, got %v", err)
	}
	if got := listingRefusal(4711, boom).Msg; got != "kill: cannot list the job of older incoda pid 4711 (sysctl refused); stop its job by hand, then rerun" {
		t.Fatalf("refusal text %q", got)
	}
}

func TestKillLineQuotesTheReason(t *testing.T) {
	if got := KillLine("builds", 4711, "incoda upgrade", false); got != "incoda kill --queue builds --pid 4711 --reason 'incoda upgrade'" {
		t.Fatal(got)
	}
	if got := KillLine("k", 1, "it's", true); got != `incoda kill --queue k --pid 1 --reason 'it'\''s' --force` {
		t.Fatal(got)
	}
}
```

In `internal/machine/idle_test.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file at that point):

(1 of 2) Replace:

```go
	}
}

func TestWaitIdleM5ProbesLanesAndStraysWithForce(t *testing.T) {
	state := t.TempDir()
	holdTicket(t, lane.LanesDir(state), "slip", 6001, "just", "gate")
	holdTicket(t, filepath.Join(StraysDir(state), "1727853243000000000"), "late", 6002, "make")
```

with:

```go
	}
}

func TestWaitIdleM5ProbesLanesAndStrays(t *testing.T) {
	state := t.TempDir()
	holdTicket(t, lane.LanesDir(state), "slip", 6001, "just", "gate")
	holdTicket(t, filepath.Join(StraysDir(state), "1727853243000000000"), "late", 6002, "make")
```

(2 of 2) Replace:

```go
		"incoda: upgrade-wait: state upgrade waits for 2 run(s) by an older incoda:\n",
		"incoda:   late pid 6002: make\n",
		"incoda:   slip pid 6001: just gate\n",
		"incoda:   incoda kill --queue late --pid 6002 --reason 'incoda upgrade' --force\n",
		"incoda:   incoda kill --queue slip --pid 6001 --reason 'incoda upgrade' --force\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
```

with:

```go
		"incoda: upgrade-wait: state upgrade waits for 2 run(s) by an older incoda:\n",
		"incoda:   late pid 6002: make\n",
		"incoda:   slip pid 6001: just gate\n",
		"incoda:   incoda kill --queue late --pid 6002 --reason 'incoda upgrade'\n",
		"incoda:   incoda kill --queue slip --pid 6001 --reason 'incoda upgrade'\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
```

In `internal/machine/migrate_test.go`, replace:

```go
		t.Fatalf("fence checks before the commit: %d, want 2", checks)
	}
	if !strings.Contains(out, "incoda: upgrade-wait: state upgrade waits for 1 run(s) by an older incoda:\n") ||
		!strings.Contains(out, "slipped pid 999999: zig build") || !strings.Contains(out, "--force") {
		t.Fatalf("output:\n%s", out)
	}
	assertMigrated(t, state, true)
```

with:

```go
		t.Fatalf("fence checks before the commit: %d, want 2", checks)
	}
	if !strings.Contains(out, "incoda: upgrade-wait: state upgrade waits for 1 run(s) by an older incoda:\n") ||
		!strings.Contains(out, "slipped pid 999999: zig build") ||
		!strings.Contains(out, "incoda kill --queue slipped --pid 999999 --reason 'incoda upgrade'\n") {
		t.Fatalf("output:\n%s", out)
	}
	assertMigrated(t, state, true)
```

In `internal/machine/carried_test.go`, replace:

```go
	if !released.Load() {
		t.Fatal("the migration committed while the early run was live")
	}
	if !strings.Contains(out, "early pid 999998: make") || !strings.Contains(out, "incoda kill --queue early --pid 999998 --reason 'incoda upgrade' --force") {
		t.Fatalf("M5 must name the early run with the --force stop line:\n%s", out)
	}
	assertMigrated(t, state, false)
	if !lane.Exists(state, "early") {
```

with:

```go
	if !released.Load() {
		t.Fatal("the migration committed while the early run was live")
	}
	if !strings.Contains(out, "early pid 999998: make") || !strings.Contains(out, "incoda kill --queue early --pid 999998 --reason 'incoda upgrade'\n") {
		t.Fatalf("M5 must name the early run with its stop line:\n%s", out)
	}
	assertMigrated(t, state, false)
	if !lane.Exists(state, "early") {
```

In `oldbin_test.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file at that point):

(1 of 2) Replace:

```go
// TestMigrationWaitsForAnOldRunThatSlipsIn (M5): an older incoda starts a
// run after the idle check and before the fence; the swap carries its
// ticket into lanes/, and the migration waits for it with the --force
// stop line before anything new runs.
func TestMigrationWaitsForAnOldRunThatSlipsIn(t *testing.T) {
	if runtime.GOOS == "windows" {
```

with:

```go
// TestMigrationWaitsForAnOldRunThatSlipsIn (M5): an older incoda starts a
// run after the idle check and before the fence; the swap carries its
// ticket into lanes/, and the migration waits for it with its
// stop line before anything new runs.
func TestMigrationWaitsForAnOldRunThatSlipsIn(t *testing.T) {
	if runtime.GOOS == "windows" {
```

(2 of 2) Replace:

```go
		t.Fatal(err)
	}

	waitForText(t, &mErr, fmt.Sprintf("incoda:   incoda kill --queue slip --pid %d --reason 'incoda upgrade' --force\n", o.Process.Pid))
	if !machine.FencePlaced(state) {
		t.Fatal("M5 waits behind the fence")
	}
```

with:

```go
		t.Fatal(err)
	}

	waitForText(t, &mErr, fmt.Sprintf("incoda:   incoda kill --queue slip --pid %d --reason 'incoda upgrade'\n", o.Process.Pid))
	if !machine.FencePlaced(state) {
		t.Fatal("M5 waits behind the fence")
	}
```

In `migrate_crash_test.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file at that point):

(1 of 2) Replace:

```go
// creates queues/ and takes a ticket there inside the window before the
// fence is placed (the rename fallback, and the empty-dir path). The race
// rule moves that queues/ to strays/, the fence goes in, M5 waits for the
// live stray ticket with the --force stop line, and M6 merges it into lanes/.
func TestFenceRacesSendANewQueuesDirToStrays(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot rename a directory with an open file inside; there the migration waits for that run before placing the fence (TestPlaceFenceNotIdleWhereADirectoryCannotMove)")
```

with:

```go
// creates queues/ and takes a ticket there inside the window before the
// fence is placed (the rename fallback, and the empty-dir path). The race
// rule moves that queues/ to strays/, the fence goes in, M5 waits for the
// live stray ticket with its stop line, and M6 merges it into lanes/
func TestFenceRacesSendANewQueuesDirToStrays(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot rename a directory with an open file inside; there the migration waits for that run before placing the fence (TestPlaceFenceNotIdleWhereADirectoryCannotMove)")
```

(2 of 2) Replace:

```go
			if err := os.WriteFile(pause, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			waitForText(t, &errBuf, "incoda:   incoda kill --queue late --pid 999998 --reason 'incoda upgrade' --force\n")
			if !machine.FencePlaced(state) {
				t.Fatal("the fence must be in place while M5 waits")
			}
```

with:

```go
			if err := os.WriteFile(pause, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			waitForText(t, &errBuf, "incoda:   incoda kill --queue late --pid 999998 --reason 'incoda upgrade'\n")
			if !machine.FencePlaced(state) {
				t.Fatal("the fence must be in place while M5 waits")
			}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go vet ./internal/machine/`

Expected: the test build fails: `undefined: KillTarget` (and `FindKillTarget`, `TargetOld`, `walkTree`, `listFn`, `signalFn`, `KillLine`, `listingRefusal`). Once it builds, the updated stop-line tests fail until `killLine` drops `--force`.

- [ ] **Step 3: Find the target and build stop lines**

Create `internal/machine/killtarget.go`:

```go
package machine

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/deblasis/incoda/internal/lane"
)

// TargetKind says how kill ends a participant (spec 3.2).
type TargetKind int

const (
	// TargetNone: no live participant with that pid on that key.
	TargetNone TargetKind = iota
	// TargetLane: a ticket of this binary under lanes/ on a migrated
	// layout; it watches its .kill file and ends its own tree.
	TargetLane
	// TargetOld: an old-layout holder, a ticket of an older incoda under
	// queues/<K> (before the fence, or after a careless rm of it), under
	// strays/, or under lanes/ or queues.new/ while machine.json is absent.
	// It is always ended by the old-holder kill, never by a request: v0.3.0
	// to v0.6.0 acknowledge a request by ending only their direct child,
	// which shares their group, so its descendants would keep running
	// while the lane reads free; behind the fence no request reaches it.
	TargetOld
)

// KillTarget is the participant kill found: where its ticket is and who
// holds it.
type KillTarget struct {
	Kind TargetKind
	Key  string
	// Root holds the lane directory Dir; Ticket is the ticket file name.
	Root, Dir, Ticket string
	PID               int
	Command           []string
}

// Old reports whether the target is an older incoda (an old-layout holder).
func (t KillTarget) Old() bool { return t.Kind == TargetOld }

// FindKillTarget looks for the live participant pid on key wherever the
// layout v allows one: lanes/<key> on a migrated layout, queues/<key> while
// queues/ is a directory, the root a migration has moved the lanes to, and
// every strays/<n>/<key>. Every probe is create-free (spec 2.6 step 2); it
// never writes, migrates or takes machine.lock.
func FindKillTarget(stateDir string, v View, key string, pid int) (KillTarget, error) {
	type place struct {
		root string
		kind TargetKind
	}
	var places []place
	switch {
	case v.Migrated:
		places = append(places, place{lane.LanesDir(stateDir), TargetLane})
		if kindOf(lane.QueuesDir(stateDir)) == aDir {
			places = append(places, place{lane.QueuesDir(stateDir), TargetOld})
		}
	default:
		places = append(places, place{v.Root, TargetOld})
	}
	batches, err := os.ReadDir(StraysDir(stateDir))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return KillTarget{}, fmt.Errorf("strays/: %w", err)
	}
	for _, b := range batches {
		if b.IsDir() {
			places = append(places, place{filepath.Join(StraysDir(stateDir), b.Name()), TargetOld})
		}
	}
	for _, pl := range places {
		dir := filepath.Join(pl.root, key)
		live, err := lane.ProbeLane(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return KillTarget{}, fmt.Errorf("%s: %w", dir, err)
		}
		for _, p := range live {
			if p.PID() == pid {
				return KillTarget{Kind: pl.kind, Key: key, Root: pl.root, Dir: dir, Ticket: p.Name, PID: pid, Command: p.Ticket.Command}, nil
			}
		}
	}
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
	return s
}
```

In `internal/machine/idle.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file at that point):

(1 of 2) Replace:

```go
	var stops []string
	for _, b := range bs {
		if !b.Orphan {
			stops = append(stops, "  "+killLine(b, ph))
		}
	}
	if len(stops) > 0 {
```

with:

```go
	var stops []string
	for _, b := range bs {
		if !b.Orphan {
			stops = append(stops, "  "+killLine(b))
		}
	}
	if len(stops) > 0 {
```

(2 of 2) Replace:

```go
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

with:

```go
	return append(lines, "do not force-release them: the job keeps running and the upgrade would overlap it.")
}

// killLine is the stop line for one older run, before the fence (M2) and
// after it (M5) alike: kill ends an older incoda with its whole job by the
// old-holder kill of spec 3.2, with or without --force.
func killLine(b Blocker) string {
	return KillLine(b.Key, b.PID, UpgradeReason, false)
}

// UpgradeReason is the --reason of every stop line the upgrade prints
// (spec 3.2, 3.3).
const UpgradeReason = "incoda upgrade"
```

- [ ] **Step 4: The kill itself**

Create `internal/machine/oldkill.go`:

```go
package machine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/lockfile"
	"github.com/deblasis/incoda/internal/textsafe"
)

// OldKillResult is what an old-holder kill ended.
type OldKillResult struct {
	// Processes is how many descendants of the old incoda were recorded
	// and ended (Unix; zero on Windows, where the job object ends them).
	Processes int
}

// Seams for tests; production never changes them.
var (
	writeOrphanFn = writeOrphan
	// treeGoneWait bounds the wait, after the SIGKILLs, for the recorded
	// tree to empty and the ticket to free.
	treeGoneWait = 10 * time.Second
)

// ticketHeld is step 2 of the old-holder kill: TryLock of the ticket file
// alone, no registry lock. Still locked means the old incoda still holds
// it (ticket descriptors are close-on-exec, so no child inherited it); a
// missing or lockable ticket means it does not.
func ticketHeld(t KillTarget) (bool, error) {
	f, err := lockfile.OpenExisting(filepath.Join(t.Dir, t.Ticket))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	ok, err := f.TryLock()
	if err != nil {
		return false, err
	}
	return !ok, nil
}

// waitTreeGone waits, up to treeGoneWait, until the recorded tree is empty
// and the old incoda's ticket is free. It reports the pids still running
// when it gives up.
func waitTreeGone(t KillTarget, rec *Orphan) (bool, string) {
	deadline := time.Now().Add(treeGoneWait)
	for {
		held, err := ticketHeld(t)
		if err == nil && !held && !rec.Live() {
			return true, ""
		}
		if time.Now().After(deadline) {
			var left []string
			if held || err != nil {
				left = append(left, fmt.Sprintf("%d (the older incoda)", t.PID))
			}
			if rec.Live() {
				left = append(left, rec.pidList())
			}
			return false, strings.Join(left, ", ")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func ancestorRefusal(pid int) *Refusal {
	return &Refusal{Msg: fmt.Sprintf("kill: pid %d is an ancestor of this process; run kill from outside its job", pid)}
}

func noLongerHolds(pid int, key string) *Refusal {
	return &Refusal{Msg: fmt.Sprintf("kill: pid %d no longer holds %q", pid, key)}
}

func listingRefusal(pid int, err error) *Refusal {
	return &Refusal{Msg: fmt.Sprintf("kill: cannot list the job of older incoda pid %d (%s); stop its job by hand, then rerun", pid, textsafe.Escape(err.Error()))}
}

func recordFailed(pid int, err error) *StateError {
	return &StateError{Msg: fmt.Sprintf("kill: cannot write the orphan record for older incoda pid %d (%s); nothing was terminated and its job was resumed", pid, textsafe.Escape(err.Error()))}
}

func treeNotGone(t KillTarget, left string) *StateError {
	return &StateError{Msg: fmt.Sprintf("kill: older incoda pid %d was sent SIGKILL but %s still run; the orphan record keeps %q busy until they exit", t.PID, left, t.Key)}
}
```

Create `internal/machine/oldkill_unix.go`:

```go
//go:build !windows

package machine

import (
	"os"
	"os/signal"
	"time"

	"golang.org/x/sys/unix"

	"github.com/deblasis/incoda/internal/procinfo"
)

// Seams for tests; production never changes them.
var (
	signalFn = unix.Kill
	listFn   = procinfo.List
	lookupFn = procinfo.Lookup
	// walkSettle bounds how long the descendant walk waits for every
	// stopped process to show the stopped state.
	walkSettle = 5 * time.Second
)

// KillOldHolder force-ends an old-layout holder and its whole job (spec
// 3.2, steps 0 to 5): refuse an ancestor; SIGSTOP the old incoda; re-check
// that it still holds its ticket; walk and SIGSTOP its descendants until
// the walk is stable; write the orphan record; SIGKILL the recorded groups,
// the recorded descendants (start time re-checked) and the old incoda;
// then wait until the tree is empty and the ticket free, and delete the
// record. From the first SIGSTOP to the last SIGKILL SIGINT, SIGTERM and
// SIGHUP are ignored, no registry lock is taken, and every abort path
// resumes everything it stopped.
func KillOldHolder(stateDir string, t KillTarget, chain procinfo.Chain) (OldKillResult, error) {
	pid := t.PID
	if !chain.Skip && chain.Contains(pid) {
		return OldKillResult{}, ancestorRefusal(pid)
	}
	self := os.Getpid()
	exclude := map[int]bool{self: true}
	for _, a := range chain.PIDs {
		exclude[a] = true
	}
	crashpoint("kill-before-stop")

	// The protected window. Signals that would end kill are taken off the
	// default action (delivered to a channel nobody reads) until the old
	// incoda has been sent SIGKILL.
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, unix.SIGINT, unix.SIGTERM, unix.SIGHUP)
	windowOpen := true
	closeWindow := func() {
		if windowOpen {
			signal.Stop(sigs)
			windowOpen = false
		}
	}
	defer closeWindow()
	stopped := []int{pid}
	resume := func() {
		for _, p := range stopped {
			_ = signalFn(p, unix.SIGCONT)
		}
	}

	// Step 1. A stopped old incoda cannot start or reap a child.
	_ = signalFn(pid, unix.SIGSTOP)
	// Step 2.
	held, err := ticketHeld(t)
	if err != nil {
		resume()
		return OldKillResult{}, stateErrorf("kill: cannot re-check the ticket of pid %d: %s", pid, esc(err))
	}
	if !held {
		resume()
		return OldKillResult{}, noLongerHolds(pid, t.Key)
	}
	// Step 3.
	desc, groups, err := walkTree(pid, exclude, &stopped)
	if err != nil {
		resume()
		return OldKillResult{}, listingRefusal(pid, err)
	}
	crashpoint("kill-stopped")
	// Step 4. Older binaries never read orphans/.
	rec := &Orphan{Key: t.Key, PID: pid, Command: t.Command, Descendants: desc, Groups: groups, ByPID: self}
	path, err := writeOrphanFn(stateDir, rec)
	if err != nil {
		resume()
		return OldKillResult{}, recordFailed(pid, err)
	}
	// Step 5. The job is frozen: a graceful SIGTERM would buy nothing, and
	// a resumed job could fork outside the walk.
	for _, g := range groups {
		_ = signalFn(-g, unix.SIGKILL)
	}
	for _, d := range desc {
		if p, err := lookupFn(d.PID); err == nil && p.Start == d.Start {
			_ = signalFn(d.PID, unix.SIGKILL)
		}
	}
	_ = signalFn(pid, unix.SIGKILL)
	closeWindow()

	if ok, left := waitTreeGone(t, rec); !ok {
		return OldKillResult{}, treeNotGone(t, left)
	}
	_ = os.Remove(path)
	return OldKillResult{Processes: len(desc)}, nil
}

// walkTree is step 3: list every process, collect the transitive
// descendants of root by parent pid, SIGSTOP each newly found one (adding
// it to stopped), and list again until a pass finds no new descendant and
// root and every descendant show the stopped state (a stopped process
// cannot fork, so the walk converges). Processes in exclude (kill itself
// and its ancestors) are never walked into and never signalled. A
// descendant that never shows the stopped state (uninterruptible sleep)
// ends the wait after walkSettle; what was found is recorded.
//
// It returns the descendants with their start times, and every process
// group whose leader is a descendant and which contains no excluded
// process.
func walkTree(root int, exclude map[int]bool, stopped *[]int) ([]OrphanProc, []int, error) {
	known := map[int]procinfo.Proc{}
	var order []int
	var last map[int]procinfo.Proc
	deadline := time.Now().Add(walkSettle)
	for {
		ps, err := listFn()
		if err != nil {
			return nil, nil, err
		}
		byPID := make(map[int]procinfo.Proc, len(ps))
		children := map[int][]procinfo.Proc{}
		for _, p := range ps {
			byPID[p.PID] = p
			children[p.PPID] = append(children[p.PPID], p)
		}
		last = byPID
		settled := byPID[root].State == 'T'
		fresh := 0
		queue := []int{root}
		seen := map[int]bool{root: true}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, c := range children[cur] {
				if seen[c.PID] || exclude[c.PID] || c.State == 'Z' {
					continue
				}
				seen[c.PID] = true
				queue = append(queue, c.PID)
				if _, ok := known[c.PID]; !ok {
					known[c.PID] = c
					order = append(order, c.PID)
					_ = signalFn(c.PID, unix.SIGSTOP)
					*stopped = append(*stopped, c.PID)
					fresh++
				}
				if c.State != 'T' {
					settled = false
				}
			}
		}
		if fresh == 0 && (settled || time.Now().After(deadline)) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	// A group that holds kill or one of its ancestors is never signalled
	// as a group: its members that descend from root are killed one by one.
	forbidden := map[int]bool{}
	for p := range exclude {
		if pr, ok := last[p]; ok {
			forbidden[pr.PGID] = true
		}
	}
	var desc []OrphanProc
	var groups []int
	for _, p := range order {
		d := known[p]
		desc = append(desc, OrphanProc{PID: d.PID, Start: d.Start})
		if d.PGID == d.PID && !forbidden[d.PGID] {
			groups = append(groups, d.PGID)
		}
	}
	return desc, groups, nil
}
```

Create `internal/machine/oldkill_windows.go`:

```go
//go:build windows

package machine

import (
	"github.com/deblasis/incoda/internal/proc"
	"github.com/deblasis/incoda/internal/procinfo"
)

// killedExit is the exit code a forced kill hands the old incoda, the 124
// of a run killed through the lane.
const killedExit = 124

// KillOldHolder on Windows needs only the termination (spec 3.2): every
// release from v0.1.0 to v0.6.0 starts its child suspended and assigns it
// to a job object with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE before it runs
// (internal/child/child_windows.go, newSupervisor and afterStart), so the
// kernel ends the whole tree when the old incoda dies. No record is
// written.
func KillOldHolder(stateDir string, t KillTarget, chain procinfo.Chain) (OldKillResult, error) {
	if err := proc.Terminate(t.PID, killedExit); err != nil {
		return OldKillResult{}, stateErrorf("kill: cannot terminate older incoda pid %d: %s", t.PID, esc(err))
	}
	rec := &Orphan{Key: t.Key, PID: t.PID}
	if ok, left := waitTreeGone(t, rec); !ok {
		return OldKillResult{}, treeNotGone(t, left)
	}
	return OldKillResult{}, nil
}
```

- [ ] **Step 5: Run the tests to see them pass**

Run: `go test -race ./internal/machine/ -count=1 && go test . -run 'TestMigrationWaitsForAnOldRunThatSlipsIn|TestFenceRacesSendANewQueuesDirToStrays' -count=1`

Expected: `ok` for both.

- [ ] **Step 6: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 7: Commit**

```bash
git add internal/machine/carried_test.go internal/machine/idle.go internal/machine/idle_test.go internal/machine/killtarget.go internal/machine/killtarget_test.go internal/machine/migrate_test.go internal/machine/oldkill.go internal/machine/oldkill_unix.go internal/machine/oldkill_unix_test.go internal/machine/oldkill_windows.go migrate_crash_test.go oldbin_test.go
git commit -F - <<'MSG'
feat: the old-holder kill stops, records and ends an older incoda's whole job

SIGSTOP the old incoda, re-check its ticket, walk and stop its
descendants until the walk is stable, write the orphan record, then
SIGKILL the recorded groups, descendants and the old incoda, and wait
for the tree to empty. Signals that would end kill are ignored inside
that window and every abort resumes what it stopped. Windows only
terminates: the job object ends the tree. FindKillTarget says where a
participant is and whether it is an older incoda. Since every older
incoda is ended this way, the upgrade's stop lines need no --force.
MSG
```

---

### Task 7: kill addresses the layout it finds

Spec 3.2 wired, with the ruling of the Decisions section. `kill` finds its participant with `FindKillTarget`. A run of this binary is killed as before (request, then `--force` terminates). An old-layout holder, before the fence or after it, gets no request file: `kill` runs the old-holder kill at once, with or without `--force`, so a v0.3.0 to v0.6.0 holder can no longer acknowledge a request by ending only its direct child while its grandchildren keep running. The TUI killer goes through the same paths (its request on an older incoda is the walk).

The tests run the real v0.6.0 and v0.2.0 binaries and a new stand-in, `internal/testprog/oldholder`, which can let go of its ticket on cue while it keeps running. Every one of them follows the kill-test safety rules of the Global Constraints.

**Files:**
- Modify: `internal/cli/kill.go` (`cmdKill`, new `killOld`)
- Modify: `internal/tui/killer.go` (`target`, `Request`, `Gone`, `Force`)
- Create: `internal/testprog/oldholder/main.go`
- Create: root `oldkill_test.go`

**Interfaces:**
- Consumes: `machine.FindKillTarget`, `machine.KillTarget`, `machine.TargetOld`, `machine.KillOldHolder` (Task 6); `machine.ReadOrphans`, `machine.OrphansDir` (Task 4); `procinfo.Lookup`, `procinfo.Stopped` (Task 3); `readState`, `machineExit`, `lane.AppendLog`, `lane.OpenIn` (plan 2a); root helpers `binaries`, `oldBinary`, `crashBinary`, `treeBinary`, `readTree`, `laneEnv`, `runIncoda`, `runWithEnv`, `waitForFile`, `waitForText`, `waitForTicket`, `syncBuffer`, `exitCodeOf`.
- Produces: `cli.killOld(dir string, t machine.KillTarget, req lane.KillRequest, stdout io.Writer, p colorize.Palette) error`; `(tui.LaneKiller).target(key string, pid int) (machine.KillTarget, error)`, `(tui.LaneKiller).killOld(t machine.KillTarget, reason string) error`; root helpers `noKillRequests`, `oldHolderBinary`, `runnerSentinel`, `alive`, `stopped`, `waitGone`, `startInGroup`, `shJob`, `readJob`, `strayOldRun`, `oldHolder`; lane.log line `event=kill ... forced=true old=true processes=N`.

- [ ] **Step 1: Write the failing tests**

The stand-in old holder is test infrastructure and is written with the tests.

Create `oldkill_test.go`:

```go
//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/procinfo"
)

var (
	holderOnce sync.Once
	holderBin  string
	holderErr  error
)

// oldHolderBinary builds internal/testprog/oldholder next to the incoda
// test binary.
func oldHolderBinary(t *testing.T) string {
	t.Helper()
	incoda, _ := binaries(t)
	holderOnce.Do(func() {
		holderBin = filepath.Join(filepath.Dir(incoda), "oldholder")
		cmd := exec.Command("go", "build", "-o", holderBin, "./internal/testprog/oldholder")
		cmd.Env = append(os.Environ(), "GOTOOLCHAIN=auto")
		if out, err := cmd.CombinedOutput(); err != nil {
			holderErr = fmt.Errorf("build oldholder: %v\n%s", err, out)
		}
	})
	if holderErr != nil {
		t.Fatal(holderErr)
	}
	return holderBin
}

// runnerSentinel starts a process in the test runner's own process group
// and, at the end of the test, fails unless it is still alive: a kill test
// must never signal the runner's group or anything outside its own tree.
func runnerSentinel(t *testing.T) {
	t.Helper()
	s := exec.Command("sleep", "120")
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !alive(s.Process.Pid) {
			t.Errorf("the sentinel in the test runner's process group (pid %d) was signalled", s.Process.Pid)
		}
		_ = s.Process.Kill()
		_ = s.Wait()
	})
}

// alive reports whether pid exists and is not a zombie.
func alive(pid int) bool {
	p, err := procinfo.Lookup(pid)
	return err == nil && p.State != 'Z'
}

// stopped reports whether pid is in the stopped state.
func stopped(pid int) bool {
	s, _ := procinfo.Stopped(pid)
	return s
}

func waitGone(t *testing.T, what string, pids ...int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for _, pid := range pids {
		for alive(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: pid %d is still running", what, pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// startInGroup starts bin with args in a new process group of its own
// (never the runner's), with INCODA_DIR set to state. Cleanup resumes and
// kills that whole group, which is safe because the group's id is the
// started process's pid by construction and the process is not reaped
// before the signal.
func startInGroup(t *testing.T, state string, bin string, args ...string) *exec.Cmd {
	t.Helper()
	c := exec.Command(bin, args...)
	c.Env = laneEnv(state)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-c.Process.Pid, syscall.SIGCONT)
		_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		_ = c.Wait()
	})
	return c
}

// shJob is a command whose shell starts a grandchild (sleep 60), writes
// "<shell pid> <sleep pid>" to file, and waits for it.
func shJob(file string) []string {
	return []string{"sh", "-c", `sleep 60 & echo "$$ $!" > "$0.tmp" && mv "$0.tmp" "$0"; wait`, file}
}

// readJob waits for shJob's file and returns the shell and sleep pids.
func readJob(t *testing.T, file string) (int, int) {
	t.Helper()
	waitForFile(t, file)
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(b))
	if len(f) != 2 {
		t.Fatalf("job file %q", b)
	}
	sh, _ := strconv.Atoi(f[0])
	sl, _ := strconv.Atoi(f[1])
	return sh, sl
}

// strayOldRun migrates a state directory, deletes the fence, starts an
// older incoda run of shJob on key (in its own group), then re-places the
// fence with a config, which moves the run's lane to strays/: an unpooled
// holder no kill request can reach.
func strayOldRun(t *testing.T, tag, key string) (state string, old *exec.Cmd, sh, sl int) {
	t.Helper()
	incoda, _ := binaries(t)
	bin := oldBinary(t, tag)
	state = t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "seed"); code != 0 {
		t.Fatalf("migrate: %d\n%s", code, out)
	}
	if err := os.Remove(filepath.Join(state, "queues")); err != nil {
		t.Fatal(err)
	}
	job := filepath.Join(t.TempDir(), "job")
	old = startInGroup(t, state, bin, append([]string{"run", "--queue", key, "--poll", "50ms", "--quiet", "--"}, shJob(job)...)...)
	sh, sl = readJob(t, job)
	if out, code := runIncoda(t, incoda, state, "config", "other"); code != 0 {
		t.Fatalf("re-fence: %d\n%s", code, out)
	}
	if !machine.FencePlaced(state) {
		t.Fatal("config must re-place the fence")
	}
	return state, old, sh, sl
}

// TestKillAV060StrayEndsItsWholeJob: a v0.6.0 run outside the pools,
// whose child spawned a grandchild, cannot see kill requests. A plain kill
// (no --force) ends the old incoda, its child and the grandchild before it
// exits 0, deletes the orphan record, and signals nothing outside the job.
func TestKillAV060StrayEndsItsWholeJob(t *testing.T) {
	runnerSentinel(t)
	incoda, _ := binaries(t)
	state, old, sh, sl := strayOldRun(t, "v0.6.0", "builds")
	pid := strconv.Itoa(old.Process.Pid)

	out, code := runIncoda(t, incoda, state, "kill", "--queue", "builds", "--pid", pid, "--reason", "test")
	if code != 0 {
		t.Fatalf("kill: exit %d\n%s", code, out)
	}
	// Every descendant is gone before kill exits 0.
	for _, p := range []int{old.Process.Pid, sh, sl} {
		if alive(p) {
			t.Fatalf("pid %d of the old job survived the kill:\n%s", p, out)
		}
	}
	_ = old.Wait()
	if all, _, _ := machine.ReadOrphans(state); len(all) != 0 {
		t.Fatalf("the orphan record must be deleted once the tree is empty: %+v", all)
	}
}

// noKillRequests fails if kill left a request file in dir: an older
// incoda is ended by the walk, never by a request.
func noKillRequests(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".kill") || strings.HasSuffix(e.Name(), ".kill.tmp") {
			t.Fatalf("kill wrote a request %s for an older incoda", e.Name())
		}
	}
}

// TestKillBeforeTheFence: on a layout not upgraded yet (queues/ still a
// directory) a plain kill writes no request and ends the older incoda with
// its whole job. A v0.6.0 holder whose child spawned a grandchild loses
// both before kill exits 0, and its lane reads free only then; a v0.2.0
// holder, which predates kill, loses its job too, its child's own process
// group included.
func TestKillBeforeTheFence(t *testing.T) {
	runnerSentinel(t)
	incoda, _ := binaries(t)
	for _, tag := range []string{"v0.6.0", "v0.2.0"} {
		t.Run(tag, func(t *testing.T) {
			state := t.TempDir()
			job := filepath.Join(t.TempDir(), "job")
			old := startInGroup(t, state, oldBinary(t, tag), append([]string{"run", "--queue", "pre", "--poll", "50ms", "--quiet", "--"}, shJob(job)...)...)
			sh, sl := readJob(t, job)
			pid := strconv.Itoa(old.Process.Pid)
			if rep := statusJSON(t, incoda, state, "pre"); len(rep.Queues[0].Holders) != 1 {
				t.Fatalf("the old run must hold the lane: %+v", rep.Queues)
			}
			out, code := runIncoda(t, incoda, state, "kill", "--queue", "pre", "--pid", pid, "--reason", "test")
			if code != 0 {
				t.Fatalf("kill: exit %d\n%s", code, out)
			}
			// When kill returns, the child and the grandchild are gone and
			// only then does the lane read free.
			for _, p := range []int{old.Process.Pid, sh, sl} {
				if alive(p) {
					t.Fatalf("pid %d survived the kill", p)
				}
			}
			if rep := statusJSON(t, incoda, state, "pre"); len(rep.Queues[0].Holders) != 0 {
				t.Fatalf("the lane must read free once the job is gone: %+v", rep.Queues)
			}
			noKillRequests(t, filepath.Join(state, "queues", "pre"))
			log, _ := os.ReadFile(filepath.Join(state, "queues", "pre", "lane.log"))
			if !strings.Contains(string(log), "event=kill pid="+pid) || !strings.Contains(string(log), "old=true processes=2") {
				t.Fatalf("lane.log must record the old-holder kill:\n%s", log)
			}
			if _, err := os.Lstat(machine.RegistryPath(state)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("kill must never migrate")
			}
		})
	}
}

// TestKillAfterTheFenceDuringTheUpgrade: an older run slipped in between
// M2 and the fence and sits under lanes/ while machine.json is absent. The
// M5 stop line needs no --force: running it ends the job and the waiting
// migration completes.
func TestKillAfterTheFenceDuringTheUpgrade(t *testing.T) {
	runnerSentinel(t)
	incoda, stamp := binaries(t)
	bin := crashBinary(t)
	old := oldBinary(t, "v0.6.0")
	state := t.TempDir()
	if out, code := runIncoda(t, old, state, "run", "--queue", "seed", "--quiet", "--", stamp, filepath.Join(t.TempDir(), "seed.txt"), "seed", "1"); code != 0 {
		t.Fatalf("seed the old layout: %d\n%s", code, out)
	}
	pause := filepath.Join(t.TempDir(), "go")
	m := exec.Command(bin, "run", "--queue", "newq", "--wait", "60s", "--poll", "50ms", "--", stamp, filepath.Join(t.TempDir(), "new.txt"), "new", "10")
	m.Env = append(laneEnv(state), "INCODA_TEST_PAUSE_AT=M3", "INCODA_TEST_PAUSE_FILE="+pause)
	var mErr syncBuffer
	m.Stderr = &mErr
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Process.Kill(); _ = m.Wait() }()
	waitForFile(t, pause+".reached")
	job := filepath.Join(t.TempDir(), "job")
	o := startInGroup(t, state, old, append([]string{"run", "--queue", "slip", "--poll", "50ms", "--quiet", "--"}, shJob(job)...)...)
	sh, sl := readJob(t, job)
	if err := os.WriteFile(pause, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	pid := strconv.Itoa(o.Process.Pid)
	waitForText(t, &mErr, "incoda kill --queue slip --pid "+pid+" --reason 'incoda upgrade'\n")

	out, code := runIncoda(t, incoda, state, "kill", "--queue", "slip", "--pid", pid, "--reason", "incoda upgrade")
	if code != 0 {
		t.Fatalf("kill: exit %d\n%s", code, out)
	}
	for _, p := range []int{sh, sl} {
		if alive(p) {
			t.Fatalf("pid %d of the slipped-in job survived", p)
		}
	}
	if err := m.Wait(); err != nil {
		t.Fatalf("the migration must complete once the old job is gone: %v\n%s", err, mErr.String())
	}
	if _, err := machine.ReadRegistry(state); err != nil {
		t.Fatal(err)
	}
}

// TestKillANestedV060HolderLeavesTheOuterJob: an inner v0.6.0 run nested
// in an outer one shares the outer group (the shipped Setenv bug). Killing
// the inner holder ends the inner job and nothing else: the outer incoda,
// the outer job's shell and their group are not signalled.
func TestKillANestedV060HolderLeavesTheOuterJob(t *testing.T) {
	runnerSentinel(t)
	incoda, _ := binaries(t)
	old := oldBinary(t, "v0.6.0")
	state := t.TempDir()
	job := filepath.Join(t.TempDir(), "job")
	// The outer job is a shell that runs the inner run, then lingers.
	outer := startInGroup(t, state, old, append([]string{"run", "--queue", "outer", "--poll", "50ms", "--quiet", "--",
		"sh", "-c", `"$@"; sleep 60`, "sh", old, "run", "--queue", "inner", "--poll", "10s", "--quiet", "--"}, shJob(job)...)...)
	sh, sl := readJob(t, job)
	jobShell, err := procinfo.Lookup(sh)
	if err != nil {
		t.Fatal(err)
	}
	inner, err := procinfo.Lookup(jobShell.PPID)
	if err != nil {
		t.Fatal(err)
	}
	if jobShell.PGID != outer.Process.Pid || inner.PGID != outer.Process.Pid {
		t.Fatalf("the inner run and its job must share the outer group %d: %+v %+v", outer.Process.Pid, inner, jobShell)
	}
	outerShell := inner.PPID
	out, code := runIncoda(t, incoda, state, "kill", "--queue", "inner", "--pid", strconv.Itoa(inner.PID), "--reason", "test", "--wait", "0", "--force")
	if code != 0 {
		t.Fatalf("kill --force: exit %d\n%s", code, out)
	}
	for _, p := range []int{inner.PID, sh, sl} {
		if alive(p) {
			t.Fatalf("pid %d of the inner job survived", p)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if !alive(outer.Process.Pid) || !alive(outerShell) || stopped(outer.Process.Pid) || stopped(outerShell) {
		t.Fatal("the outer incoda and the outer job's shell must not be signalled")
	}
}

// TestKillOfAnAncestorIsRefused: kill run from inside an older incoda's
// job, naming that incoda, refuses before stopping anything.
func TestKillOfAnAncestorIsRefused(t *testing.T) {
	runnerSentinel(t)
	incoda, _ := binaries(t)
	old := oldBinary(t, "v0.6.0")
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "seed"); code != 0 {
		t.Fatalf("migrate: %d\n%s", code, out)
	}
	if err := os.Remove(filepath.Join(state, "queues")); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	goFile, result := filepath.Join(dir, "go"), filepath.Join(dir, "result")
	script := `while [ ! -e "$1" ]; do sleep 0.05; done; "$0" kill --queue anc --pid $PPID --reason test --force > "$2.tmp" 2>&1; echo "rc=$?" >> "$2.tmp"; mv "$2.tmp" "$2"`
	o := startInGroup(t, state, old, "run", "--queue", "anc", "--poll", "50ms", "--quiet", "--", "sh", "-c", script, incoda, goFile, result)
	waitForTicket(t, filepath.Join(state, "queues", "anc"))
	if out, code := runIncoda(t, incoda, state, "config", "other"); code != 0 {
		t.Fatalf("re-fence: %d\n%s", code, out)
	}
	if err := os.WriteFile(goFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, result)
	b, _ := os.ReadFile(result)
	want := fmt.Sprintf("incoda: kill: pid %d is an ancestor of this process; run kill from outside its job\nrc=120\n", o.Process.Pid)
	if !strings.HasSuffix(string(b), want) {
		t.Fatalf("want %q, got %q", want, b)
	}
	if err := o.Wait(); err != nil {
		t.Fatalf("the old run was signalled: %v", err)
	}
}

// TestKillInsideAGroupItMustNotSignal: kill runs inside the process group
// that a v0.2.0 run's child leads. That group holds kill, so it is never
// signalled as a group: the job's processes are ended one by one, and a
// sentinel in the same group survives.
func TestKillInsideAGroupItMustNotSignal(t *testing.T) {
	runnerSentinel(t)
	incoda, _ := binaries(t)
	tree := treeBinary(t)
	state := t.TempDir()
	out := filepath.Join(t.TempDir(), "tree.txt")
	old := startInGroup(t, state, oldBinary(t, "v0.2.0"), "run", "--queue", "grp", "--poll", "50ms", "--quiet", "--", tree, out)
	ti := readTree(t, out)
	if ti.pgid != ti.pid {
		t.Fatalf("v0.2.0 puts its child in its own group: %+v", ti)
	}
	inGroup := func(args ...string) *exec.Cmd {
		c := exec.Command(args[0], args[1:]...)
		c.Env = laneEnv(state)
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: ti.pgid}
		return c
	}
	sentinel := inGroup("sleep", "60")
	if err := sentinel.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sentinel.Process.Kill(); _ = sentinel.Wait() }()
	k := inGroup(incoda, "kill", "--queue", "grp", "--pid", strconv.Itoa(old.Process.Pid), "--reason", "test", "--wait", "0", "--force")
	b, err := k.CombinedOutput()
	if err != nil {
		t.Fatalf("kill: %v\n%s", err, b)
	}
	waitGone(t, "the job", ti.pid, ti.grandchild, old.Process.Pid)
	if !alive(sentinel.Process.Pid) {
		t.Fatal("the group that holds kill was signalled")
	}
}

// oldHolder starts the oldholder test program on key under root and
// returns it with its pid and its child's pid.
func oldHolder(t *testing.T, state, root, key string) (c *exec.Cmd, pid, child int, release string) {
	t.Helper()
	dir := t.TempDir()
	ready, release := filepath.Join(dir, "ready"), filepath.Join(dir, "release")
	c = startInGroup(t, state, oldHolderBinary(t), filepath.Join(root, key), key, ready, release)
	waitForFile(t, ready)
	b, _ := os.ReadFile(ready)
	var p, ch int
	if _, err := fmt.Sscanf(string(b), "pid %d\nchild %d\n", &p, &ch); err != nil {
		t.Fatalf("ready file %q: %v", b, err)
	}
	return c, p, ch, release
}

// TestKillAbortsWhenTheTargetReleasedBeforeTheStop: the holder lets go of
// its ticket between kill finding it and kill's SIGSTOP. The re-check sees
// the ticket free: kill resumes it and refuses; nothing is terminated.
func TestKillAbortsWhenTheTargetReleasedBeforeTheStop(t *testing.T) {
	runnerSentinel(t)
	bin := crashBinary(t)
	state := t.TempDir()
	h, pid, child, release := oldHolder(t, state, filepath.Join(state, "queues"), "rel")
	pause := filepath.Join(t.TempDir(), "go")
	k := exec.Command(bin, "kill", "--queue", "rel", "--pid", strconv.Itoa(pid), "--reason", "test", "--wait", "0", "--force")
	k.Env = append(laneEnv(state), "INCODA_TEST_PAUSE_AT=kill-before-stop", "INCODA_TEST_PAUSE_FILE="+pause)
	var kOut syncBuffer
	k.Stdout, k.Stderr = &kOut, &kOut
	if err := k.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = k.Process.Kill(); _ = k.Wait() }()
	waitForFile(t, pause+".reached")
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, release+".done")
	if err := os.WriteFile(pause, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	err := k.Wait()
	want := fmt.Sprintf("incoda: kill: pid %d no longer holds \"rel\"\n", pid)
	if exitCodeOf(err) != 120 || !strings.Contains(kOut.String(), want) {
		t.Fatalf("want exit 120 and %q, got %v:\n%s", want, err, kOut.String())
	}
	time.Sleep(100 * time.Millisecond)
	if !alive(pid) || !alive(child) || stopped(pid) || stopped(child) {
		t.Fatalf("the holder and its child must be running and resumed: pid %v/%v child %v/%v", alive(pid), stopped(pid), alive(child), stopped(child))
	}
	_ = h
}

// TestKillWindowIgnoresSIGINTAndSIGTERM: SIGINT and SIGTERM sent to kill
// while the job is frozen do not stop it halfway: kill completes and the
// job is gone.
func TestKillWindowIgnoresSIGINTAndSIGTERM(t *testing.T) {
	runnerSentinel(t)
	bin := crashBinary(t)
	state := t.TempDir()
	_, pid, child, _ := oldHolder(t, state, filepath.Join(state, "queues"), "sig")
	pause := filepath.Join(t.TempDir(), "go")
	k := exec.Command(bin, "kill", "--queue", "sig", "--pid", strconv.Itoa(pid), "--reason", "test", "--wait", "0", "--force")
	k.Env = append(laneEnv(state), "INCODA_TEST_PAUSE_AT=kill-stopped", "INCODA_TEST_PAUSE_FILE="+pause)
	var kOut syncBuffer
	k.Stdout, k.Stderr = &kOut, &kOut
	if err := k.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = k.Process.Kill(); _ = k.Wait() }()
	waitForFile(t, pause+".reached")
	if !stopped(pid) || !stopped(child) {
		t.Fatal("inside the window the old incoda and its child are stopped")
	}
	for _, s := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		if err := k.Process.Signal(s); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if err := os.WriteFile(pause, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := k.Wait(); err != nil {
		t.Fatalf("kill must complete despite SIGINT and SIGTERM: %v\n%s", err, kOut.String())
	}
	waitGone(t, "the job", pid, child)
}

// TestKillFailedRecordWriteResumesEverything: when the orphan record
// cannot be written, nothing is terminated and every process kill stopped
// is resumed.
func TestKillFailedRecordWriteResumesEverything(t *testing.T) {
	runnerSentinel(t)
	incoda, _ := binaries(t)
	state := t.TempDir()
	_, pid, child, _ := oldHolder(t, state, filepath.Join(state, "queues"), "rec")
	if err := os.WriteFile(machine.OrphansDir(state), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runIncoda(t, incoda, state, "kill", "--queue", "rec", "--pid", strconv.Itoa(pid), "--reason", "test", "--wait", "0", "--force")
	want := fmt.Sprintf("incoda: kill: cannot write the orphan record for older incoda pid %d (", pid)
	if code != 122 || !strings.Contains(out, want) || !strings.Contains(out, "); nothing was terminated and its job was resumed\n") {
		t.Fatalf("want exit 122 and %q, got %d:\n%s", want, code, out)
	}
	time.Sleep(100 * time.Millisecond)
	if !alive(pid) || !alive(child) || stopped(pid) || stopped(child) {
		t.Fatalf("everything kill stopped must be resumed: pid %v/%v child %v/%v", alive(pid), stopped(pid), alive(child), stopped(child))
	}
}
```

Create `internal/testprog/oldholder/main.go`:

```go
//go:build !windows

// oldholder stands in for an older incoda holding a lane: it creates
// LANEDIR with its registry.lock and a ticket whose lock it holds (its pid
// in the name and the payload, as every release writes them), starts a
// child `sh -c 'sleep 60 & wait'`, and writes "pid N\nchild C\n" to
// READYFILE. When RELEASEFILE appears it lets go of the ticket but keeps
// running, which no real release can be made to do on cue: kill tests use
// it for "the target released its ticket before the SIGSTOP". It exits
// after 60 seconds whatever happens.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: oldholder LANEDIR KEY READYFILE RELEASEFILE")
		os.Exit(2)
	}
	dir, key, ready, release := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(err)
	}
	reg, err := os.OpenFile(filepath.Join(dir, "registry.lock"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		fail(err)
	}
	defer reg.Close()
	now := time.Now()
	name := fmt.Sprintf("%020d-%d.ticket", now.UnixNano(), os.Getpid())
	tf, err := os.OpenFile(filepath.Join(dir, name), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		fail(err)
	}
	if err := unix.Flock(int(tf.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		fail(err)
	}
	b, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "queue": key, "slots": 1,
		"arrival_nano": now.UnixNano(), "command": []string{"oldholder", key}})
	if _, err := tf.Write(b); err != nil {
		fail(err)
	}
	child := exec.Command("sh", "-c", "sleep 60 & wait")
	if err := child.Start(); err != nil {
		fail(err)
	}
	body := fmt.Sprintf("pid %d\nchild %d\n", os.Getpid(), child.Process.Pid)
	if err := os.WriteFile(ready+".tmp", []byte(body), 0o644); err != nil {
		fail(err)
	}
	if err := os.Rename(ready+".tmp", ready); err != nil {
		fail(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(release); err == nil && tf != nil {
			_ = tf.Close() // the kernel drops the flock with the descriptor
			tf = nil
			_ = os.WriteFile(release+".done", nil, 0o644)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "oldholder:", err)
	os.Exit(1)
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test . -run 'TestKillAV060StrayEndsItsWholeJob|TestKillFailedRecordWriteResumesEverything' -count=1`

Expected: FAIL: `TestKillAV060StrayEndsItsWholeJob` (`kill: exit 120`: today's kill looks only in `lanes/` and refuses `has no live participant`) and `TestKillFailedRecordWriteResumesEverything` (`want exit 122`, got 0: today's `--force` terminates the pid without any record).

- [ ] **Step 3: Wire kill and the TUI killer**

Replace the whole content of `internal/cli/kill.go` with (shown whole: the changes are spread across the file):

```go
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/colorize"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/proc"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/textsafe"
)

// cmdKill addresses a kill request to one participant and reports whether it
// was honoured. The request is cooperative first: the participant's own
// incoda notices within a poll interval, tells its owner who killed it and
// why, takes its job tree down and exits 124. --force is for the participant
// that never answers.
func cmdKill(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("kill", stderr)
	queue := fs.String("queue", "", "queue key (defaults to $INCODA_QUEUE)")
	pid := fs.Int("pid", 0, "pid of the holder or waiter, as status shows it")
	reason := fs.String("reason", "", "why; the killed job's owner reads this on their stderr and the log keeps it")
	wait := fs.Duration("wait", 5*time.Second, "how long to give the participant to acknowledge before giving up, or terminating it with --force")
	force := fs.Bool("force", false, "terminate the participant's process if it does not acknowledge within --wait (an older incoda is always ended with its whole job, with or without it)")
	noColor := fs.Bool("no-color", false, "never emit ANSI color, even on a terminal (the NO_COLOR environment variable does the same)")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: incoda kill --queue KEY --pid N --reason TEXT [--wait 5s] [--force]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for kill"}
	}
	if *pid <= 0 {
		return usagef("kill needs --pid, the participant's pid as `incoda status` shows it")
	}
	if strings.TrimSpace(*reason) == "" {
		return usagef("kill needs --reason: the killed job's owner is told why on their own stderr, and the log keeps it")
	}
	p := paletteFor(stdout, *noColor)
	key, err := resolveKey(*queue)
	if err != nil {
		return err
	}
	// kill addresses the layout it finds and never creates a lane, never
	// migrates and never takes machine.lock (spec 3.2). An older incoda
	// (a ticket under queues/, strays/, or lanes/ or queues.new/ while
	// machine.json is absent) gets no request: it is always ended with its
	// whole job by the old-holder kill, with or without --force. v0.3.0 to
	// v0.6.0 would acknowledge a request by ending only their direct child,
	// and the lane would read free while its descendants run.
	dir, v, err := readState()
	if err != nil {
		return err
	}
	t, err := machine.FindKillTarget(dir, v, key, *pid)
	if err != nil {
		return exitWith(ExitState, "cannot look for pid %d on queue %q: %s", *pid, key, textsafe.Escape(err.Error()))
	}
	req := lane.KillRequest{By: whoami(), ByPID: os.Getpid(), Reason: *reason}
	switch t.Kind {
	case machine.TargetNone:
		return usagef("queue %q has no live participant with pid %d: %v", key, *pid, lane.ErrNoParticipant)
	case machine.TargetOld:
		fmt.Fprintf(stdout, "%s pid %d (an older incoda) on queue %q: %s\n",
			p.Yellow("kill:"), *pid, key, textsafe.Escape(lane.Ticket{Command: t.Command}.CommandString()))
		return killOld(dir, t, req, stdout, p)
	}

	q, err := lane.OpenIn(t.Root, key, lane.Existing)
	if errors.Is(err, os.ErrNotExist) {
		return usagef("queue %q has no live participant with pid %d: %v", key, *pid, lane.ErrNoParticipant)
	}
	if err != nil {
		return exitWith(ExitState, "%v", err)
	}
	defer q.Close()

	entry, err := q.RequestKill(*pid, req)
	if errors.Is(err, lane.ErrNoParticipant) {
		return usagef("%v", err)
	}
	if err != nil {
		return exitWith(ExitState, "cannot request the kill: %v", err)
	}
	role := "waiter"
	if entry.Holding {
		role = "holder"
	}
	fmt.Fprintf(stdout, "%s pid %d (%s) on queue %q: %s\n", p.Yellow("kill requested:"), *pid, role, key, entry.Ticket.CommandString())

	gone, err := q.WaitGone(*pid, *wait, 100*time.Millisecond)
	if err != nil {
		return exitWith(ExitState, "%v", err)
	}
	if gone {
		fmt.Fprintf(stdout, "%s\n", p.Green(fmt.Sprintf("pid %d released the lane", *pid)))
		return nil
	}
	if !*force {
		return exitWith(ExitKillPending,
			"pid %d has not acknowledged after %s. It may be an incoda from before kill existed, or wedged. "+
				"Rerun with --force to terminate it; the kernel frees the lane when it dies", *pid, *wait)
	}
	if err := proc.Terminate(*pid, ExitKilled); err != nil {
		return exitWith(ExitState, "cannot terminate pid %d: %v", *pid, err)
	}
	q.Logf("queue=%s event=kill pid=%d by=%s reason=%s forced=true", key, *pid, textsafe.LogValue(req.By), textsafe.LogValue(req.Reason))
	gone, err = q.WaitGone(*pid, 5*time.Second, 100*time.Millisecond)
	if err != nil {
		return exitWith(ExitState, "%v", err)
	}
	if !gone {
		return exitWith(ExitState, "pid %d was terminated but its ticket is still held; check `incoda status --queue %s`", *pid, key)
	}
	fmt.Fprintf(stdout, "%s\n", p.Green(fmt.Sprintf("pid %d terminated; the kernel released the lane", *pid)))
	return nil
}

// killOld force-ends an older incoda and its whole job (spec 3.2) and
// records the kill in the lane.log of the directory its ticket is in.
func killOld(dir string, t machine.KillTarget, req lane.KillRequest, stdout io.Writer, p colorize.Palette) error {
	res, err := machine.KillOldHolder(dir, t, procinfo.ParentChain())
	if err != nil {
		return machineExit(err)
	}
	lane.AppendLog(t.Dir, "queue=%s event=kill pid=%d by=%s reason=%s forced=true old=true processes=%d",
		t.Key, t.PID, textsafe.LogValue(req.By), textsafe.LogValue(req.Reason), res.Processes)
	fmt.Fprintf(stdout, "%s\n", p.Green(fmt.Sprintf("pid %d and its job (%d more process(es)) terminated; the kernel released the lane", t.PID, res.Processes)))
	return nil
}

// whoami names the killer as user@host, which is what the killed job's owner
// wants to know first.
func whoami() string {
	name := "unknown"
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	host, _ := os.Hostname()
	if host == "" {
		return name
	}
	return name + "@" + host
}
```

Replace the whole content of `internal/tui/killer.go` with (shown whole: the changes are spread across the file):

```go
package tui

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/proc"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/textsafe"
)

// killedExit is the exit code a forced kill hands the participant, the same
// 124 that `incoda run` uses when it stops itself on a request. It is
// repeated here rather than imported because the cli package imports this
// one.
const killedExit = 124

// Killer is what the kill prompt talks to. It is an interface so the model
// can be driven in tests without a state directory or a process to end.
type Killer interface {
	// Request leaves the kill request beside pid's ticket on key.
	Request(key string, pid int, reason string) error
	// Gone reports whether pid has left key, waiting up to wait for it.
	Gone(key string, pid int, wait time.Duration) (bool, error)
	// Force terminates pid outright and records the forced kill on key.
	Force(key string, pid int, reason string) error
}

// LaneKiller is the real Killer: the same request file and termination path
// as `incoda kill`, so the TUI and the command cannot drift apart.
type LaneKiller struct {
	Dir   string
	By    string
	ByPID int
}

// NewLaneKiller names the killer as user@host the way `incoda kill` does.
func NewLaneKiller(dir string) LaneKiller {
	name := "unknown"
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	if host, _ := os.Hostname(); host != "" {
		name += "@" + host
	}
	return LaneKiller{Dir: dir, By: name, ByPID: os.Getpid()}
}

// target finds pid on key in the layout the state directory has now
// (lanes/ once migrated, queues/ before, strays/ for an older incoda that
// runs outside the pools) without creating anything: a kill addresses the
// layout it finds (spec 3.2).
func (k LaneKiller) target(key string, pid int) (machine.KillTarget, error) {
	v, err := machine.Inspect(k.Dir)
	if err != nil {
		return machine.KillTarget{}, err
	}
	t, err := machine.FindKillTarget(k.Dir, v, key, pid)
	if err != nil {
		return machine.KillTarget{}, err
	}
	if t.Kind == machine.TargetNone {
		return t, fmt.Errorf("queue %q has no live participant with pid %d: %w", key, pid, lane.ErrNoParticipant)
	}
	return t, nil
}

func (k LaneKiller) Request(key string, pid int, reason string) error {
	t, err := k.target(key, pid)
	if err != nil {
		return err
	}
	if t.Old() {
		// An older incoda gets no request: it is ended with its whole job
		// at once (spec 3.2, old-holder kill).
		return k.killOld(t, reason)
	}
	q, err := lane.OpenIn(t.Root, key, lane.Existing)
	if err != nil {
		return err
	}
	defer q.Close()
	_, err = q.RequestKill(pid, lane.KillRequest{By: k.By, ByPID: k.ByPID, Reason: reason})
	return err
}

func (k LaneKiller) Gone(key string, pid int, wait time.Duration) (bool, error) {
	t, err := k.target(key, pid)
	if errors.Is(err, lane.ErrNoParticipant) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	q, err := lane.OpenIn(t.Root, key, lane.Existing)
	if err != nil {
		return false, err
	}
	defer q.Close()
	return q.WaitGone(pid, wait, 100*time.Millisecond)
}

// Force ends pid. An older incoda is ended with its whole job (the
// old-holder kill of spec 3.2); a holder of this binary is terminated as
// before.
func (k LaneKiller) Force(key string, pid int, reason string) error {
	t, err := k.target(key, pid)
	if err != nil {
		return err
	}
	if t.Old() {
		return k.killOld(t, reason)
	}
	if err := proc.Terminate(pid, killedExit); err != nil {
		return err
	}
	q, err := lane.OpenIn(t.Root, key, lane.Existing)
	if err != nil {
		return err
	}
	defer q.Close()
	q.Logf("queue=%s event=kill pid=%d by=%s reason=%s forced=true", key, pid, textsafe.LogValue(k.By), textsafe.LogValue(reason))
	return nil
}

// killOld ends an older incoda with its whole job and logs it where its
// ticket is.
func (k LaneKiller) killOld(t machine.KillTarget, reason string) error {
	res, err := machine.KillOldHolder(k.Dir, t, procinfo.ParentChain())
	if err != nil {
		return err
	}
	lane.AppendLog(t.Dir, "queue=%s event=kill pid=%d by=%s reason=%s forced=true old=true processes=%d",
		t.Key, t.PID, textsafe.LogValue(k.By), textsafe.LogValue(reason), res.Processes)
	return nil
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go test -race ./internal/tui/ -count=1 && go test . -run 'Kill' -count=1`

Expected: `ok` for both (the root run takes about 80s: it builds two old tags on first use and each test waits for real processes).

- [ ] **Step 5: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/kill.go internal/testprog/oldholder/main.go internal/tui/killer.go oldkill_test.go
git commit -F - <<'MSG'
feat: kill ends an older incoda with its whole job

kill finds its participant wherever the layout puts it. An older incoda,
before the fence or after it, gets no request: v0.3.0 to v0.6.0 would
acknowledge it by ending only their direct child. kill runs the
old-holder kill at once instead, with or without --force. The TUI killer
takes the same paths.
MSG
```

---

### Task 8: force-release --live waits for the upgrade

Spec 3.2: deleting a live ticket of an older incoda empties the upgrade's idle check while its job keeps running, so the upgrade would overlap it. While `machine.json` is absent `force-release --live` on a lane with live tickets exits 120 `upgrade-pending:` with one `incoda kill --queue K --pid N --reason 'incoda upgrade'` line per live ticket (no `--force`: kill ends an older incoda with its whole job either way). Plain force-release still works and now also deletes the key's stale orphan records.

**Files:**
- Modify: `internal/cli/misc.go` (`cmdForceRelease`, new `upgradePending`)
- Create: root `forcerelease_test.go`

**Interfaces:**
- Consumes: `machine.KillLine`, `machine.UpgradeReason` (Task 6); `machine.SweepOrphans`, `machine.ReadOrphans`, `machine.OrphansDir` (Task 4); `lane.ProbeLane`, `readState`; root helpers `holdOldTicket`, `countTicketsIn`.
- Produces: `cli.upgradePending(v machine.View, key string) error`; output line `queue "K": removed N stale orphan record(s)`.

- [ ] **Step 1: Write the failing tests**

Create `forcerelease_test.go`:

```go
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
// it refuses with one stop line per live ticket and deletes nothing. Plain force-release
// still works.
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
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test . -run 'TestForceReleaseLiveRefusedDuringTheUpgrade|TestPlainForceReleaseDeletesStaleOrphanRecords' -count=1`

Expected: FAIL: `want exit 120 and incoda: upgrade-pending: ...` (today `--live` deletes the ticket and exits 0), and the stale record is not removed.

- [ ] **Step 3: Refuse and sweep**

In `internal/cli/misc.go`, make these 3 replacements, in order (each quoted block occurs exactly once in the file at that point):

(1 of 3) Replace:

```go
	if err != nil {
		return err
	}
	_, v, err := readState()
	if err != nil {
		return err
	}
```

with:

```go
	if err != nil {
		return err
	}
	dir, v, err := readState()
	if err != nil {
		return err
	}
```

(2 of 3) Replace:

```go
		fmt.Fprintf(stdout, "queue %q has no state on this machine; nothing to release\n", key)
		return nil
	}
	q, err := lane.OpenIn(v.Root, key, lane.Existing)
	if err != nil {
		return exitWith(ExitState, "%v", err)
```

with:

```go
		fmt.Fprintf(stdout, "queue %q has no state on this machine; nothing to release\n", key)
		return nil
	}
	if *live && !v.Migrated {
		if err := upgradePending(v, key); err != nil {
			return err
		}
	}
	q, err := lane.OpenIn(v.Root, key, lane.Existing)
	if err != nil {
		return exitWith(ExitState, "%v", err)
```

(3 of 3) Replace:

```go
	}
	q.Logf("queue=%s event=force-release removed=%d live=%v by_pid=%d", key, removed, *live, os.Getpid())
	fmt.Fprintf(stdout, "queue %q: removed %d ticket(s)\n", key, removed)
	return nil
}

func cmdDoctor(args []string, stdout, stderr io.Writer) error {
	start := time.Now()
	fs := newFlagSet("doctor", stderr)
```

with:

```go
	}
	q.Logf("queue=%s event=force-release removed=%d live=%v by_pid=%d", key, removed, *live, os.Getpid())
	fmt.Fprintf(stdout, "queue %q: removed %d ticket(s)\n", key, removed)
	if !*live {
		// Records of old-holder kills whose job has fully exited (spec
		// 3.2): they hold nothing, so plain force-release clears them.
		if n, err := machine.SweepOrphans(dir, key); err == nil && n > 0 {
			fmt.Fprintf(stdout, "queue %q: removed %d stale orphan record(s)\n", key, n)
		}
	}
	return nil
}

// upgradePending refuses force-release --live while machine.json is absent
// (spec 3.2): deleting a live ticket of an older incoda would empty the
// upgrade's idle check while that job keeps running, so the upgrade would
// overlap it. It prints one stop line per live ticket instead (kill ends
// an older incoda with its whole job). With no live ticket it refuses
// nothing.
func upgradePending(v machine.View, key string) error {
	live, err := lane.ProbeLane(filepath.Join(v.Root, key))
	if err != nil {
		return exitWith(ExitState, "cannot probe queue %q: %s", key, textsafe.Escape(err.Error()))
	}
	if len(live) == 0 {
		return nil
	}
	lines := []string{"upgrade-pending: force-release --live would hide a running job from the upgrade; ask the user before stopping another session's job; they can run:"}
	for _, p := range live {
		lines = append(lines, "  "+machine.KillLine(key, p.PID(), machine.UpgradeReason, false))
	}
	return exitWith(ExitUsage, "%s", strings.Join(lines, "\nincoda: "))
}

func cmdDoctor(args []string, stdout, stderr io.Writer) error {
	start := time.Now()
	fs := newFlagSet("doctor", stderr)
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go test . -run 'ForceRelease' -count=1`

Expected: `ok  	github.com/deblasis/incoda`.

- [ ] **Step 5: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 6: Commit**

```bash
git add forcerelease_test.go internal/cli/misc.go
git commit -F - <<'MSG'
feat: force-release --live is refused while the state upgrade is pending

It would hide a running older job from the upgrade's idle check. The
refusal prints one kill line per live ticket for the user. Plain
force-release still works and deletes stale orphan records of its key.
MSG
```

---

### Task 9: Plain status warns about strays, a missing fence and stopped holders

Spec 5.3 (only these lines; the rest of 5.3 is plan 5) and the status half of the stopped-holder rule of 3.2. After every existing line plain status adds a warning block: each live unpooled holder (`unpooled run by an older incoda: pid N, key K`), `fence missing: the next run re-places it (incoda doctor)` on a migrated layout without the fence, and for each live holder in process state `T` the `stopped holder:` line with its exact rerun command. The stopped-holder test makes a kill die inside its window (crash point `kill-stopped`), sees status flag the holder, and recovers it by rerunning `kill --force`.

**Files:**
- Create: `internal/machine/warnings.go`
- Modify: `internal/report/report.go` (`Report.Warnings`, `Build`), `internal/cli/status.go` (`renderReport`)
- Create: root `statuswarn_test.go`
- Modify: root `oldkill_test.go` (append `TestStoppedHolderShownAndRecoveredByRerun`)

**Interfaces:**
- Consumes: `machine.ScanUnpooled`, `Unpooled.Line` (Task 5); `machine.KillLine` (Task 6); `procinfo.Stopped` (Task 3); `machine.View` (plan 2a); root helpers `oldHolder`, `runnerSentinel`, `crashBinary`, `runWithEnv`, `stopped`, `waitGone`, `holdOldTicket`.
- Produces: `machine.ResumeReason`, `machine.FenceMissingLine`, `machine.StoppedLines(key string, pid int) []string`, `type machine.Holder struct{ Key string; PID int }`, `machine.StatusWarnings(stateDir string, v View, holders []Holder) []string`; `report.Report.Warnings []string` (`json:"-"`).

- [ ] **Step 1: Write the failing tests**

Create `statuswarn_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/machine"
)

// TestStatusWarnsAboutUnpooledRunsAndAMissingFence: after a careless rm
// of the fence, plain status ends with the unpooled run and the missing
// fence, and still never re-fences. Every line before the warnings is
// what status printed before.
func TestStatusWarnsAboutUnpooledRunsAndAMissingFence(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "seed"); code != 0 {
		t.Fatalf("migrate: %d\n%s", code, out)
	}
	clean, code := runIncoda(t, incoda, state, "status", "--queue", "seed", "--no-color")
	if code != 0 || strings.Contains(clean, "unpooled") || strings.Contains(clean, "fence missing") {
		t.Fatalf("a healthy layout has no warnings: %d\n%s", code, clean)
	}
	if err := os.Remove(filepath.Join(state, "queues")); err != nil {
		t.Fatal(err)
	}
	holdOldTicket(t, filepath.Join(state, "queues"), "oldjob", 999999, "zig", "build")
	out, code := runIncoda(t, incoda, state, "status", "--queue", "seed", "--no-color")
	want := "\nunpooled run by an older incoda: pid 999999, key oldjob\nfence missing: the next run re-places it (incoda doctor)\n"
	if code != 0 || !strings.HasSuffix(out, want) {
		t.Fatalf("want the warning block %q at the end, got %d:\n%s", want, code, out)
	}
	if machine.FencePlaced(state) {
		t.Fatal("status must never re-fence")
	}
}
```

In `oldkill_test.go`, replace:

```go
		t.Fatalf("everything kill stopped must be resumed: pid %v/%v child %v/%v", alive(pid), stopped(pid), alive(child), stopped(child))
	}
}
```

with:

```go
		t.Fatalf("everything kill stopped must be resumed: pid %v/%v child %v/%v", alive(pid), stopped(pid), alive(child), stopped(child))
	}
}

// TestStoppedHolderShownAndRecoveredByRerun: a kill that dies inside its
// window (here at the kill-stopped crash point) leaves the old incoda and
// its job stopped, still holding the lane. status flags it with the exact
// rerun line, and rerunning kill --force ends it.
func TestStoppedHolderShownAndRecoveredByRerun(t *testing.T) {
	runnerSentinel(t)
	incoda, _ := binaries(t)
	bin := crashBinary(t)
	state := t.TempDir()
	_, pid, child, _ := oldHolder(t, state, filepath.Join(state, "queues"), "stp")
	p := strconv.Itoa(pid)
	out, code := runWithEnv(t, bin, state, []string{"INCODA_TEST_CRASH_AT=kill-stopped"},
		"kill", "--queue", "stp", "--pid", p, "--reason", "test", "--wait", "0", "--force")
	if code != 97 {
		t.Fatalf("the crash binary must die inside the window: %d\n%s", code, out)
	}
	if !stopped(pid) || !stopped(child) {
		t.Fatal("the interrupted kill leaves the old incoda and its job stopped")
	}
	out, code = runIncoda(t, incoda, state, "status", "--queue", "stp", "--no-color")
	want := "stopped holder: pid " + p + "; a kill was interrupted; rerun: incoda kill --queue stp --pid " + p + " --reason 'resume interrupted kill' --force\n" +
		"  or resume it instead: kill -CONT " + p + "\n"
	if code != 0 || !strings.HasSuffix(out, want) {
		t.Fatalf("want %q at the end of status, got %d:\n%s", want, code, out)
	}
	out, code = runIncoda(t, incoda, state, "kill", "--queue", "stp", "--pid", p, "--reason", "resume interrupted kill", "--wait", "0", "--force")
	if code != 0 {
		t.Fatalf("the rerun must recover: %d\n%s", code, out)
	}
	waitGone(t, "the stopped job", pid, child)
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test . -run 'TestStatusWarnsAboutUnpooledRunsAndAMissingFence|TestStoppedHolderShownAndRecoveredByRerun' -count=1`

Expected: FAIL: `want the warning block "\nunpooled run by an older incoda: pid 999999, key oldjob\nfence missing: ..." at the end` and `want "stopped holder: pid N; ..." at the end of status`.

- [ ] **Step 3: Build and print the warnings**

Create `internal/machine/warnings.go`:

```go
package machine

import (
	"fmt"

	"github.com/deblasis/incoda/internal/procinfo"
)

// ResumeReason is the --reason of the rerun line for a stopped holder.
const ResumeReason = "resume interrupted kill"

// FenceMissingLine is plain status's warning for a migrated layout whose
// fence is gone (spec 5.3).
const FenceMissingLine = "fence missing: the next run re-places it (incoda doctor)"

// StoppedLines are the lines status and doctor print for a live holder in
// the stopped state (spec 3.2): an old-holder kill was interrupted inside
// its window. Rerunning the same kill --force is safe (a SIGSTOP of a
// stopped process does nothing); kill -CONT resumes the holder instead.
func StoppedLines(key string, pid int) []string {
	return []string{
		fmt.Sprintf("stopped holder: pid %d; a kill was interrupted; rerun: %s", pid, KillLine(key, pid, ResumeReason, true)),
		fmt.Sprintf("  or resume it instead: kill -CONT %d", pid),
	}
}

// Holder names one live ticket for the stopped-state check.
type Holder struct {
	Key string
	PID int
}

// StatusWarnings is the warning block at the end of plain status (spec
// 5.3): every live unpooled holder, a missing fence on a migrated layout,
// and every holder among holders (plus the unpooled ones) that is in the
// stopped state. It only reads: no lock, no cleanup.
func StatusWarnings(stateDir string, v View, holders []Holder) []string {
	var lines []string
	if v.Migrated {
		us, err := ScanUnpooled(stateDir, false)
		if err != nil {
			lines = append(lines, fmt.Sprintf("cannot scan for unpooled runs: %s", esc(err)))
		}
		for _, u := range us {
			lines = append(lines, u.Line())
			if u.Where != "orphans" {
				holders = append(holders, Holder{Key: u.Key, PID: u.PID})
			}
		}
		if v.FenceMissing {
			lines = append(lines, FenceMissingLine)
		}
	}
	seen := map[int]bool{}
	for _, h := range holders {
		if seen[h.PID] {
			continue
		}
		seen[h.PID] = true
		if s, err := procinfo.Stopped(h.PID); err == nil && s {
			lines = append(lines, StoppedLines(h.Key, h.PID)...)
		}
	}
	return lines
}
```

In `internal/report/report.go`, make these 2 replacements, in order (each quoted block occurs exactly once in the file at that point):

(1 of 2) Replace:

```go
	// 3.2). It is display text, not part of the JSON report; plan 5 adds
	// the layout fields to status --json.
	Banner string `json:"-"`
}

// Queue is one queue inside a Report.
```

with:

```go
	// 3.2). It is display text, not part of the JSON report; plan 5 adds
	// the layout fields to status --json.
	Banner string `json:"-"`
	// Warnings are the lines plain status adds at the end (spec 5.3):
	// unpooled runs of an older incoda, a missing fence, stopped holders.
	// Display text, not part of the JSON report.
	Warnings []string `json:"-"`
}

// Queue is one queue inside a Report.
```

(2 of 2) Replace:

```go
		qr.Free = len(snap.Holders) == 0
		rep.Queues = append(rep.Queues, qr)
	}
	return rep, nil
}
```

with:

```go
		qr.Free = len(snap.Holders) == 0
		rep.Queues = append(rep.Queues, qr)
	}
	var holders []machine.Holder
	for _, qr := range rep.Queues {
		for _, e := range append(append([]lane.Entry(nil), qr.Holders...), qr.Waiting...) {
			holders = append(holders, machine.Holder{Key: qr.Key, PID: e.Ticket.PID})
		}
	}
	rep.Warnings = machine.StatusWarnings(stateDir, v, holders)
	return rep, nil
}
```

In `internal/cli/status.go`, replace:

```go
		renderQueue(w, p, qr)
	}
	fmt.Fprintf(w, "\n%s\n", p.Dim(sysinfo.MachineLine(rep.Memory, rep.CPU)))
}

func renderQueue(w io.Writer, p colorize.Palette, qr QueueReport) {
```

with:

```go
		renderQueue(w, p, qr)
	}
	fmt.Fprintf(w, "\n%s\n", p.Dim(sysinfo.MachineLine(rep.Memory, rep.CPU)))
	// Warnings go after every existing line, so scrapers of the lines
	// above see them unchanged (spec 5.3).
	if len(rep.Warnings) > 0 {
		fmt.Fprintln(w)
		for _, l := range rep.Warnings {
			fmt.Fprintln(w, p.Yellow(l))
		}
	}
}

func renderQueue(w io.Writer, p colorize.Palette, qr QueueReport) {
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go test . -run 'TestStatusWarns|TestStoppedHolder|TestStatus' -count=1`

Expected: `ok  	github.com/deblasis/incoda`.

- [ ] **Step 5: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/status.go internal/machine/warnings.go internal/report/report.go oldkill_test.go statuswarn_test.go
git commit -F - <<'MSG'
feat: plain status warns about unpooled runs, a missing fence and stopped holders

The block follows every existing line, so scrapers see those unchanged.
A holder left stopped by an interrupted kill gets the exact rerun line;
rerunning kill --force recovers it.
MSG
```

---

### Task 10: doctor completes

Spec 5.5's remainder and plan 2a's two doctor follow-ups. doctor now deletes stray lanes whose tickets all died (migrated layout only) and reports what is left in `strays/`, every orphan record (live, stale, unreadable), the live unpooled holders and every live holder in the stopped state as `attention:` items, and every incoda on PATH with its version: it runs `<path> version` with stdin from `/dev/null` in a process group of its own, kills the group at 5 seconds, sets `cmd.WaitDelay`, reads at most 4 KiB, and flags a version older than 0.7, `dev`, an unparseable answer or no answer. On a lost registry it lists the lanes and prints `POOL,POOL` instead of suggesting the bootstrap set; `--rebuild-registry=` with an empty value and `--wait` without `--rebuild-registry` are refused.

Because doctor now executes what it finds on PATH, every test that runs it sets PATH explicitly (`doctorEnv`); the probe test uses fake scripts on a temp PATH.

**Files:**
- Create: `internal/machine/versions.go`, `internal/machine/versionprobe_unix.go`, `internal/machine/versionprobe_windows.go`
- Modify: `internal/machine/pathcheck.go` (`IncodasOnPath`, `OtherIncodas` on top of it)
- Modify: `internal/machine/doctor.go` (`Health`, `Diagnose`, new `describeHolders`)
- Modify: `internal/cli/misc.go` (`cmdDoctor`)
- Create: `internal/machine/versions_test.go`, root `doctorpath_test.go`
- Modify: root `doctor_test.go` (`doctorEnv`, `doctor`, `doctorWithPath`, the lost-registry text, two new tests), root `integration_test.go` (`TestDoctorAndVersionAndQueues` sets PATH), root `oldkill_test.go` (the stopped-holder test checks doctor)

**Interfaces:**
- Consumes: `machine.ReadOrphans`, `Orphan.Live` (Task 4); `machine.ScanUnpooled`, `machine.CleanStrays` (Task 5); `machine.StoppedLines`, `machine.Holder` (Task 9); `procinfo.Stopped` (Task 3); `unmigratedRoot` (Task 1); `lane.ProbeLane`, `lane.ListIn`, `machine.Inspect`, `machine.Rebuild` (plan 2a); `startGetenv` (plan 1).
- Produces: `type machine.PathIncoda struct{ Path string; Self bool }`; `machine.IncodasOnPath(path, self string) []PathIncoda`; `machine.ProbeVersion(path string) (string, error)`; `type machine.Version struct{ Text string; Major, Minor int; Known bool }`; `parseVersion(out string) (Version, error)`; `machine.PathVersionLines(path, self, selfVersion string) (lines, attention []string)`; seam `probeTimeout`; `Health.Strays`, `Health.Orphans`; `describeHolders(stateDir string, h *Health, root string, migrated bool)`; doctor output lines `strays:`, `orphans:`, `on PATH:`; root helpers `doctorEnv(state, path string) []string`, `doctorWithPath`.

- [ ] **Step 1: Write the failing tests**

Create `internal/machine/versions_test.go`:

```go
package machine

import (
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	for _, tc := range []struct {
		out          string
		text         string
		major, minor int
		known        bool
	}{
		{"incoda v0.6.0\ncommit: abc\nbuilt:  x\n", "v0.6.0", 0, 6, true},
		{"incoda 0.5.1\n", "0.5.1", 0, 5, true},
		{"incoda v0.7.0-rc1\n", "v0.7.0-rc1", 0, 7, true},
		{"incoda v1.2.3+meta\n", "v1.2.3+meta", 1, 2, true},
		{"incoda dev\ncommit: none\n", "dev", 0, 0, false},
		{"incoda v0.0.0-20261001-abcdef\n", "v0.0.0-20261001-abcdef", 0, 0, true},
	} {
		v, err := parseVersion(tc.out)
		if err != nil || v.Text != tc.text || v.Known != tc.known || (tc.known && (v.Major != tc.major || v.Minor != tc.minor)) {
			t.Fatalf("parseVersion(%q) = %+v %v", tc.out, v, err)
		}
	}
	for _, bad := range []string{"", "hello there\n", "incoda\n", "incoda v1 extra\n", "Incoda v0.7.0\n"} {
		if _, err := parseVersion(bad); err == nil {
			t.Fatalf("parseVersion(%q) must fail", bad)
		}
	}
	if _, err := parseVersion("\x1b[31mincoda\n"); err == nil || strings.ContainsRune(err.Error(), '\x1b') {
		t.Fatalf("an unparseable line is reported escaped: %v", err)
	}
}

func TestCappedBufferKeepsFourKiB(t *testing.T) {
	var c cappedBuffer
	chunk := strings.Repeat("x", 3000)
	for i := 0; i < 3; i++ {
		if n, err := c.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("Write must accept everything: %d %v", n, err)
		}
	}
	if len(c.String()) != probeOutputCap {
		t.Fatalf("kept %d bytes, want %d", len(c.String()), probeOutputCap)
	}
}
```

Create `doctorpath_test.go`:

```go
//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestDoctorProbesEveryIncodaOnPath: doctor runs `<path> version` for
// every incoda on PATH (never one from the PATH the tests run with: these
// are fake scripts on a temp PATH). It flags one older than 0.7, gives an
// attention line for a dev, an unparseable and a silent one, kills a probe
// that does not answer within 5s with its whole group, and is not held up
// by a probe whose child keeps its output open.
func TestDoctorProbesEveryIncodaOnPath(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "hang.pid")
	scripts := []struct{ name, body string }{
		{"old", `echo "incoda v0.6.0"; echo "commit: abc"`},
		{"new", `read x; echo "incoda v0.7.1"`},
		{"dev", `echo "incoda dev"`},
		{"holdsout", `( /bin/sleep 30 ) & echo "incoda v0.7.0"`},
		{"hang", `echo $$ > "` + pidFile + `"; exec /bin/sleep 30`},
		{"junk", `echo "hello there"`},
	}
	var dirs []string
	for _, sc := range scripts {
		d := filepath.Join(t.TempDir(), sc.name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "incoda"), []byte("#!/bin/sh\n"+sc.body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, d)
	}
	start := time.Now()
	out, code := doctorWithPath(t, incoda, state, strings.Join(dirs, string(os.PathListSeparator)))
	if code != 0 {
		t.Fatalf("doctor exit %d\n%s", code, out)
	}
	p := func(name string) string {
		return filepath.Join(dirs[map[string]int{"old": 0, "new": 1, "dev": 2, "holdsout": 3, "hang": 4, "junk": 5}[name]], "incoda")
	}
	mustContain(t, out,
		"on PATH:   "+p("old")+" v0.6.0 (older than 0.7)\n",
		"on PATH:   "+p("new")+" v0.7.1\n",
		"on PATH:   "+p("dev")+" dev\n",
		"on PATH:   "+p("holdsout")+" v0.7.0\n",
		"on PATH:   "+p("hang")+" (no answer)\n",
		"on PATH:   "+p("junk")+" (version unknown)\n",
		"attention: "+p("old")+" is incoda v0.6.0, older than 0.7: it stops with \"not a directory\" (exit 122) on this state directory and runs nothing; upgrade it",
		"attention: "+p("dev")+" reports version \"dev\": cannot tell whether it is older than 0.7",
		"attention: "+p("hang")+" did not answer \"version\" within 5s",
		"attention: "+p("junk")+": cannot read its version (unexpected output \"hello there\")")
	if el := time.Since(start); el > 15*time.Second {
		t.Fatalf("doctor took %s: every probe is bounded", el)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	hang, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	waitGone(t, "the probe that did not answer", hang)
}
```

In `doctor_test.go`, make these 4 replacements, in order (each quoted block occurs exactly once in the file at that point):

(1 of 4) Replace:

```go
import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
```

with:

```go
import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
```

(2 of 4) Replace:

```go
	"github.com/deblasis/incoda/internal/machine"
)

func doctor(t *testing.T, incoda, state string, args ...string) (string, int) {
	t.Helper()
	return runIncoda(t, incoda, state, append([]string{"doctor", "--no-color"}, args...)...)
}

func mustContain(t *testing.T, out string, wants ...string) {
```

with:

```go
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
```

(3 of 4) Replace:

```go
		if code != 122 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		mustContain(t, out, "problem:   machine.json: missing while lanes/ exists; nothing re-creates it on its own. A human decides which lanes are pools and runs: incoda doctor --rebuild-registry builds,computer-use,tests,vm\n")
	})
}
```

with:

```go
		if code != 122 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		mustContain(t, out, "problem:   machine.json: missing while lanes/ exists; nothing re-creates it on its own. Lanes: alpha, bad, builds, cap-gate, computer-use, old, tests, vm. A human decides which of them are pools and runs: incoda doctor --rebuild-registry POOL,POOL\n")
	})
}
```

(4 of 4) Replace:

```go
		t.Fatalf("the backslash was escaped twice:\n%s", out)
	}
}
```

with:

```go
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
```

In `integration_test.go`, replace:

```go
	state := t.TempDir()

	cmd := exec.Command(incoda, "doctor")
	cmd.Env = laneEnv(state)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, out)
```

with:

```go
	state := t.TempDir()

	cmd := exec.Command(incoda, "doctor")
	cmd.Env = doctorEnv(state, t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, out)
```

In `oldkill_test.go`, replace:

```go
	if code != 0 || !strings.HasSuffix(out, want) {
		t.Fatalf("want %q at the end of status, got %d:\n%s", want, code, out)
	}
	out, code = runIncoda(t, incoda, state, "kill", "--queue", "stp", "--pid", p, "--reason", "resume interrupted kill", "--wait", "0", "--force")
	if code != 0 {
		t.Fatalf("the rerun must recover: %d\n%s", code, out)
```

with:

```go
	if code != 0 || !strings.HasSuffix(out, want) {
		t.Fatalf("want %q at the end of status, got %d:\n%s", want, code, out)
	}
	out, code = doctor(t, incoda, state)
	if code != 0 || !strings.Contains(out, "attention: "+strings.Split(want, "\n")[0]+"\n") ||
		!strings.Contains(out, "attention:   or resume it instead: kill -CONT "+p+"\n") {
		t.Fatalf("doctor must flag the stopped holder, got %d:\n%s", code, out)
	}
	out, code = runIncoda(t, incoda, state, "kill", "--queue", "stp", "--pid", p, "--reason", "resume interrupted kill", "--wait", "0", "--force")
	if code != 0 {
		t.Fatalf("the rerun must recover: %d\n%s", code, out)
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go vet ./internal/machine/; go test . -run 'TestDoctorRefusesBadFlagCombinations|TestDoctorReportsTheLayout' -count=1`

Expected: the machine vet fails on the test build (`undefined: parseVersion`, `cappedBuffer`, `probeOutputCap`), and the root run (whose test build does not include machine's tests) FAILs: `an empty --rebuild-registry is refused: 0` and the lost-registry subtest misses `Lanes: alpha, bad, builds, ...`.

- [ ] **Step 3: Find and probe every incoda on PATH**

In `internal/machine/pathcheck.go`, make these 3 replacements, in order (each quoted block occurs exactly once in the file at that point):

(1 of 3) Replace:

```go
// empty PATH entry is the current directory, as exec.LookPath reads it.
// Nothing is executed.
func OtherIncodas(path, self string) []string {
	if self == "" {
		self, _ = os.Executable()
	}
```

with:

```go
// empty PATH entry is the current directory, as exec.LookPath reads it.
// Nothing is executed.
func OtherIncodas(path, self string) []string {
	var out []string
	for _, e := range IncodasOnPath(path, self) {
		if !e.Self {
			out = append(out, e.Path)
		}
	}
	return out
}

// PathIncoda is one incoda found on PATH.
type PathIncoda struct {
	Path string
	// Self is set when it is the same file as this binary.
	Self bool
}

// IncodasOnPath lists, in PATH order, every executable regular file named
// incoda (incoda.exe on Windows) in a PATH directory, marking the one that
// is this binary. Nothing is executed.
func IncodasOnPath(path, self string) []PathIncoda {
	if self == "" {
		self, _ = os.Executable()
	}
```

(2 of 3) Replace:

```go
		name = "incoda.exe"
	}
	seen := map[string]bool{}
	var out []string
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
```

with:

```go
		name = "incoda.exe"
	}
	seen := map[string]bool{}
	var out []PathIncoda
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
```

(3 of 3) Replace:

```go
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

with:

```go
		if err != nil || !fi.Mode().IsRegular() || (runtime.GOOS != "windows" && fi.Mode().Perm()&0o111 == 0) {
			continue
		}
		out = append(out, PathIncoda{Path: p, Self: selfInfo != nil && os.SameFile(fi, selfInfo)})
	}
	return out
}
```

Create `internal/machine/versions.go`:

```go
package machine

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/deblasis/incoda/internal/textsafe"
)

const (
	// probeOutputCap is the most output doctor reads from a version probe.
	probeOutputCap = 4096
	// probeWaitDelay bounds the wait for the probe's output pipe once the
	// probe itself has exited (a descendant may hold it open).
	probeWaitDelay = 500 * time.Millisecond
)

// probeTimeout is how long doctor lets `<path> version` run before it
// kills the probe's process group (a seam for tests).
var probeTimeout = 5 * time.Second

// errProbeTimeout means the probe did not finish within probeTimeout.
var errProbeTimeout = errors.New("did not answer within the time limit")

// cappedBuffer keeps the first probeOutputCap bytes written to it and
// accepts (and drops) the rest, so the probe never blocks on a full pipe.
type cappedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := probeOutputCap - c.b.Len(); room > 0 {
		if len(p) > room {
			c.b.Write(p[:room])
		} else {
			c.b.Write(p)
		}
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.b.String()
}

// Version is what a version probe found.
type Version struct {
	// Text is the version as printed ("v0.6.0", "dev"), escaped.
	Text         string
	Major, Minor int
	// Known is set when Text parsed as major.minor.patch.
	Known bool
}

// parseVersion reads the first line of `incoda version` output: "incoda
// <version>", where <version> is v0.6.0, 0.6.0, a pre-release such as
// v0.7.0-rc1, or "dev" for a build without a stamped version.
func parseVersion(out string) (Version, error) {
	line, _, _ := strings.Cut(out, "\n")
	f := strings.Fields(line)
	if len(f) != 2 || f[0] != "incoda" {
		return Version{}, fmt.Errorf("unexpected output %q", textsafe.Escape(truncate(line, 60)))
	}
	v := Version{Text: textsafe.Escape(f[1])}
	core := strings.TrimPrefix(f[1], "v")
	if i := strings.IndexAny(core, "-+"); i >= 0 {
		core = core[:i]
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, nil
	}
	nums := make([]int, 3)
	for i, s := range parts {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return v, nil
		}
		nums[i] = n
	}
	v.Major, v.Minor, v.Known = nums[0], nums[1], true
	return v, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// PathVersionLines are doctor's lines for the incodas on PATH (spec 5.5):
// one "on PATH:" line each, and an attention line for one older than 0.7,
// one whose version is dev or does not parse, and one that does not answer
// within the time limit. This binary is listed, not executed.
func PathVersionLines(path, self, selfVersion string) (lines, attention []string) {
	entries := IncodasOnPath(path, self)
	if len(entries) == 0 {
		return []string{"none"}, nil
	}
	for _, e := range entries {
		p := textsafe.Escape(e.Path)
		if e.Self {
			lines = append(lines, fmt.Sprintf("%s %s (this binary)", p, textsafe.Escape(selfVersion)))
			continue
		}
		out, err := ProbeVersion(e.Path)
		if errors.Is(err, errProbeTimeout) {
			lines = append(lines, p+" (no answer)")
			attention = append(attention, fmt.Sprintf("%s did not answer \"version\" within %s; if it is older than 0.7 it stops with \"not a directory\" on this state directory", p, probeTimeout))
			continue
		}
		v, perr := parseVersion(out)
		switch {
		case perr != nil:
			if err != nil {
				perr = fmt.Errorf("%s; %s", textsafe.Escape(err.Error()), perr)
			}
			lines = append(lines, p+" (version unknown)")
			attention = append(attention, fmt.Sprintf("%s: cannot read its version (%s); if it is older than 0.7 it stops with \"not a directory\" on this state directory", p, perr))
		case !v.Known:
			lines = append(lines, fmt.Sprintf("%s %s", p, v.Text))
			attention = append(attention, fmt.Sprintf("%s reports version %q: cannot tell whether it is older than 0.7; if it is, it stops with \"not a directory\" on this state directory", p, v.Text))
		case v.Major == 0 && v.Minor < 7:
			lines = append(lines, fmt.Sprintf("%s %s (older than 0.7)", p, v.Text))
			attention = append(attention, fmt.Sprintf("%s is incoda %s, older than 0.7: it stops with \"not a directory\" (exit 122) on this state directory and runs nothing; upgrade it (brew upgrade incoda, or the install script)", p, v.Text))
		default:
			lines = append(lines, fmt.Sprintf("%s %s", p, v.Text))
		}
	}
	return lines, attention
}
```

Create `internal/machine/versionprobe_unix.go`:

```go
//go:build !windows

package machine

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// ProbeVersion runs `<path> version` with stdin from /dev/null, in a
// process group of its own, and returns at most 4 KiB of its standard
// output (spec 5.5). After probeTimeout it kills the whole group;
// cmd.WaitDelay bounds the wait for an output pipe that a descendant holds
// open after the probe itself exited, and such a descendant's group is
// killed too.
func ProbeVersion(path string) (string, error) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return "", err
	}
	defer devnull.Close()
	var out cappedBuffer
	cmd := exec.Command(path, "version")
	cmd.Stdin = devnull
	cmd.Stdout = &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = probeWaitDelay
	if err := cmd.Start(); err != nil {
		return "", err
	}
	// The group id is the probe's pid. It stays reserved while the probe
	// is unreaped or any member of its group lives, which is exactly when
	// the signals below are sent.
	pgid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(probeTimeout):
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-done
		return out.String(), errProbeTimeout
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		// The probe exited but something it started still holds its
		// output open: end that group too.
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		err = nil
	}
	return out.String(), err
}
```

Create `internal/machine/versionprobe_windows.go`:

```go
//go:build windows

package machine

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// ProbeVersion runs `<path> version` with stdin from NUL, in a process
// group of its own, and returns at most 4 KiB of its standard output (spec
// 5.5). After probeTimeout it terminates the probe; cmd.WaitDelay bounds
// the wait for an output pipe a descendant holds open.
func ProbeVersion(path string) (string, error) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return "", err
	}
	defer devnull.Close()
	var out cappedBuffer
	cmd := exec.Command(path, "version")
	cmd.Stdin = devnull
	cmd.Stdout = &out
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	cmd.WaitDelay = probeWaitDelay
	if err := cmd.Start(); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(probeTimeout):
		_ = cmd.Process.Kill()
		<-done
		return out.String(), errProbeTimeout
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	return out.String(), err
}
```

- [ ] **Step 4: Diagnose strays, orphans and holders**

Replace the whole content of `internal/machine/doctor.go` with (shown whole: the changes are spread across the file):

```go
package machine

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/textsafe"
)

// Health is doctor's reading of the state directory (spec 5.5): the
// layout, the fence, strays/, orphan records, live unpooled holders and
// stopped holders. PATH versions are read by PathVersionLines.
//
// Every string in Problems and Attention is already escaped for display
// (textsafe.Escape applied once, here, to whatever came from state, a file
// or the environment); a caller that prints them must not escape them
// again, since textsafe.Escape is not idempotent.
type Health struct {
	// Layout is one line describing the layout.
	Layout string
	// Fence is "present" or "missing" on a migrated layout, else empty.
	Fence string
	// Strays describes each lane directory left under strays/; Orphans
	// each record under orphans/. Both are empty when there are none.
	Strays  []string
	Orphans []string
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
		describeHolders(stateDir, &h, lane.LanesDir(stateDir), true)
	case !errors.Is(err, ErrNoRegistry):
		h.Layout = "2 (machine.json unusable)"
		h.Problems = append(h.Problems, strings.TrimPrefix(err.Error(), "machine-state: "))
	default:
		switch row := st.row(); row {
		case RowRegistryLost:
			h.Layout = "2 (machine.json missing)"
			keys, _ := lane.ListQueues(stateDir)
			sort.Strings(keys)
			lanes := "none"
			if len(keys) > 0 {
				lanes = strings.Join(keys, ", ")
			}
			h.Problems = append(h.Problems, fmt.Sprintf("machine.json: missing while lanes/ exists; nothing re-creates it on its own. Lanes: %s. A human decides which of them are pools and runs: incoda doctor --rebuild-registry POOL,POOL", lanes))
		case RowNotStarted:
			if st.Queues == aDir {
				h.Layout = "1 (queues/)"
				h.Attention = append(h.Attention, banner(stateDir))
				describeHolders(stateDir, &h, lane.QueuesDir(stateDir), false)
			} else {
				h.Layout = "none yet (the next mutating incoda command creates layout 2)"
			}
		default:
			describeHolders(stateDir, &h, unmigratedRoot(stateDir, st), false)
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

// describeHolders fills in strays/, orphans/, the live unpooled holders
// (on a migrated layout) and every live ticket holder in the stopped state
// (spec 5.5). root is where the lanes are now. It only reads.
func describeHolders(stateDir string, h *Health, root string, migrated bool) {
	batches, _ := lane.ListIn(StraysDir(stateDir))
	sort.Strings(batches)
	for _, b := range batches {
		keys, _ := lane.ListIn(filepath.Join(StraysDir(stateDir), b))
		for _, k := range keys {
			live, err := lane.ProbeLane(filepath.Join(StraysDir(stateDir), b, k))
			if err != nil {
				h.Strays = append(h.Strays, fmt.Sprintf("%s/%s: cannot probe (%s)", b, k, esc(err)))
				continue
			}
			h.Strays = append(h.Strays, fmt.Sprintf("%s/%s: %d live ticket(s)", b, k, len(live)))
		}
	}
	recs, bad, _ := ReadOrphans(stateDir)
	for _, o := range recs {
		state := "its job has exited; incoda force-release --queue " + o.Key + " deletes the record"
		if o.Live() {
			state = "its job still runs (pids " + o.pidList() + ")"
		}
		h.Orphans = append(h.Orphans, fmt.Sprintf("%s: key %s, older incoda pid %d: %s", textsafe.Escape(o.File), o.Key, o.PID, state))
	}
	for _, b := range bad {
		h.Orphans = append(h.Orphans, fmt.Sprintf("%s: unreadable (%s); delete it once you have checked that its job is gone", textsafe.Escape(b.File), esc(b.Err)))
	}
	var holders []Holder
	if migrated {
		us, err := ScanUnpooled(stateDir, false)
		if err != nil {
			h.Attention = append(h.Attention, fmt.Sprintf("cannot scan for unpooled runs: %s", esc(err)))
		}
		for _, u := range us {
			h.Attention = append(h.Attention, fmt.Sprintf("%s (%s): %s; new runs on its pools wait for it", u.Line(), u.Where, u.Command))
			if u.Where != "orphans" {
				holders = append(holders, Holder{Key: u.Key, PID: u.PID})
			}
		}
	}
	keys, _ := lane.ListIn(root)
	for _, k := range keys {
		live, _ := lane.ProbeLane(filepath.Join(root, k))
		for _, p := range live {
			holders = append(holders, Holder{Key: k, PID: p.PID()})
		}
	}
	seen := map[int]bool{}
	for _, hd := range holders {
		if seen[hd.PID] {
			continue
		}
		seen[hd.PID] = true
		if s, err := procinfo.Stopped(hd.PID); err == nil && s {
			h.Attention = append(h.Attention, StoppedLines(hd.Key, hd.PID)...)
		}
	}
}
```

- [ ] **Step 5: doctor prints them and checks its flags**

In `internal/cli/misc.go`, make these 4 replacements, in order (each quoted block occurs exactly once in the file at that point):

(1 of 4) Replace:

```go
package cli

import (
	"fmt"
	"io"
	"os"
```

with:

```go
package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
```

(2 of 4) Replace:

```go
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for doctor"}
	}
	p := paletteFor(stdout, *noColor)

	v, c, d := versionInfo()
```

with:

```go
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for doctor"}
	}
	rebuildSet, waitSet := false, false
	fs.Visit(func(f *flag.Flag) {
		rebuildSet = rebuildSet || f.Name == "rebuild-registry"
		waitSet = waitSet || f.Name == "wait"
	})
	if waitSet && !rebuildSet {
		return usagef("doctor: --wait applies only to --rebuild-registry")
	}
	p := paletteFor(stdout, *noColor)

	v, c, d := versionInfo()
```

(3 of 4) Replace:

```go
		return exitWith(ExitState, "OS file locking is not usable: %v", err)
	}

	if *rebuild != "" {
		var pools []string
		for _, k := range strings.Split(*rebuild, ",") {
			if k = strings.TrimSpace(k); k != "" {
```

with:

```go
		return exitWith(ExitState, "OS file locking is not usable: %v", err)
	}

	if rebuildSet {
		var pools []string
		for _, k := range strings.Split(*rebuild, ",") {
			if k = strings.TrimSpace(k); k != "" {
```

(4 of 4) Replace:

```go
		fmt.Fprintf(stdout, "%s %s\n", p.Dim("INCODA_QUEUE:"), p.Dim("unset (run needs --queue)"))
	}

	h := machine.Diagnose(dir)
	fmt.Fprintf(stdout, "%s %s\n", p.Dim("layout:   "), textsafe.Escape(h.Layout))
	if h.Fence != "" {
		fmt.Fprintf(stdout, "%s %s\n", p.Dim("fence:    "), h.Fence)
	}
	attention := h.Attention
	if stateDirSource() == "INCODA_DIR" {
		attention = append(attention, "INCODA_DIR is set: it is a MACHINE-level override, not a per-project one; it splits pools across state directories, so a caller without it set uses a different state directory, forms separate lanes, and stops serialising against this one")
	}
```

with:

```go
		fmt.Fprintf(stdout, "%s %s\n", p.Dim("INCODA_QUEUE:"), p.Dim("unset (run needs --queue)"))
	}

	// doctor deletes stray lane directories whose tickets have all died
	// (spec 2.3), but only on a migrated layout: during a migration
	// strays/ belongs to M5 and M6.
	cleanErr := ""
	if view, err := machine.Inspect(dir); err == nil && view.Migrated {
		if err := machine.CleanStrays(dir); err != nil {
			cleanErr = "cannot clean strays/: " + textsafe.Escape(err.Error())
		}
	}
	h := machine.Diagnose(dir)
	fmt.Fprintf(stdout, "%s %s\n", p.Dim("layout:   "), textsafe.Escape(h.Layout))
	if h.Fence != "" {
		fmt.Fprintf(stdout, "%s %s\n", p.Dim("fence:    "), h.Fence)
	}
	for _, list := range []struct {
		label string
		lines []string
	}{{"strays:   ", h.Strays}, {"orphans:  ", h.Orphans}} {
		if len(list.lines) == 0 {
			fmt.Fprintf(stdout, "%s none\n", p.Dim(list.label))
		}
		for _, l := range list.lines {
			fmt.Fprintf(stdout, "%s %s\n", p.Dim(list.label), l)
		}
	}
	pathLines, pathAttention := machine.PathVersionLines(startGetenv("PATH"), "", v)
	for _, l := range pathLines {
		fmt.Fprintf(stdout, "%s %s\n", p.Dim("on PATH:  "), l)
	}
	attention := append(h.Attention, pathAttention...)
	if cleanErr != "" {
		attention = append(attention, cleanErr)
	}
	if stateDirSource() == "INCODA_DIR" {
		attention = append(attention, "INCODA_DIR is set: it is a MACHINE-level override, not a per-project one; it splits pools across state directories, so a caller without it set uses a different state directory, forms separate lanes, and stops serialising against this one")
	}
```

- [ ] **Step 6: Run the tests to see them pass**

Run: `go test -race ./internal/machine/ -count=1 && go test . -run 'Doctor|TestStoppedHolder' -count=1`

Expected: `ok` for both (the root run takes about 20s: one fake probe never answers and is killed at 5s).

- [ ] **Step 7: Run the gates**

Run (bash): `just ci && GOOS=windows go vet ./... && GOOS=windows go vet -tags incoda_crashpoints ./... && GOOS=linux go vet ./... && GOOS=linux go vet -tags incoda_crashpoints ./...`

Expected: every step passes and `just ci` ends with the `ok` lines of every package. If only a test named in the Global Constraints as pre-existing timing-sensitive fails, rerun it alone before debugging this task.

- [ ] **Step 8: Commit**

```bash
git add doctor_test.go doctorpath_test.go integration_test.go internal/cli/misc.go internal/machine/doctor.go internal/machine/pathcheck.go internal/machine/versionprobe_unix.go internal/machine/versionprobe_windows.go internal/machine/versions.go internal/machine/versions_test.go oldkill_test.go
git commit -F - <<'MSG'
feat: doctor reports strays, orphan records, stopped holders and every incoda on PATH

doctor deletes stray lanes whose tickets all died, lists strays/ and
orphans/, names live unpooled and stopped holders, and runs each incoda
on PATH for its version, bounded at 5 seconds with its process group
killed, flagging versions older than 0.7. A lost registry lists its
lanes and asks for POOL,POOL, and the flag combinations that did nothing
are refused.
MSG
```

---

## Self-review against the spec

Every in-scope clause, and the task that implements and tests it:

- **2.3 unpooled runs are counted.** Live tickets under `strays/<n>/<K>/` and under `queues/<K>/` while the fence is missing, probed create-free under that directory's `registry.lock` (`ScanUnpooled`, `probeRoot`, Task 5). Counted as one held slot on the pools linked from `lanes/<K>`, pool K, or every pool (`ChargedPools`, `ChargedTo`; `TestChargedPools` covers linked, pool, unlinked, unknown, dangling and broken keys; `TestUnknownStrayKeyCountsOnEveryPool` against v0.6.0 checks every pool waits and a project key does not). New runs wait and their busy line names `unpooled run by an older incoda: pid N, key K` (`TestFenceDeletionWithALiveStrayIsCounted`, v0.6.0, also proves no overlap with stamps). A run whose parent chain contains a counted stray pid exits 120 `upgrade-blocked:` at once (`TestUnpooledAncestorIsUpgradeBlocked`). Orphan records count the same way (`ScanUnpooled` includes `LiveOrphans`). Cleanup: re-fence (`CleanStrays` after `refenceWaiting` on a migrated layout, `TestEnsureRefencesAMigratedLayout`), every acquisition poll (`ScanUnpooled(dir, true)`, `TestFenceDeletionWithALiveStrayIsCounted` checks the dead lane is gone and its log fragment reached `lanes/builds/lane.log`), doctor (Task 10, `TestDoctorReportsStraysOrphansAndUnpooledHolders`), each under the lane's registry lock (`lane.RemoveIfIdle`, `TestRemoveIfIdle`). Cost: one `ReadDir` of `strays/` and of `orphans/` per poll.
- **2.6 self-wait with a stray ancestor.** Task 5 (`upgrade-blocked`, the M2 text, through `UpgradeBlocked`).
- **3.2 kill of old-layout holders, as ruled.** Every old-layout holder is ended by the walk, never by a request (Decisions). Before migration (`queues/` a directory) a plain `kill` with no `--force` ends a v0.6.0 holder's child and grandchild before it exits 0, the lane reads free only after, and no request file is written (`TestKillBeforeTheFence/v0.6.0`); the same for v0.2.0, whose child leads its own group (`TestKillBeforeTheFence/v0.2.0`). After the fence, a holder under `strays/` (`TestKillAV060StrayEndsItsWholeJob`) or under `lanes/` while `machine.json` is absent (`TestKillAfterTheFenceDuringTheUpgrade`, which also runs the printed M5 line) is ended the same way without `--force`. `TestFindKillTarget` covers every place. The spec's `add --force` refusal and `--force` in the M5 line are superseded by the ruling. Tasks 6 and 7.
- **3.2 force-release.** `--live` refused while `machine.json` is absent with the exact `upgrade-pending:` text and one `incoda kill --queue K --pid N --reason 'incoda upgrade'` line per live ticket (no `--force`, as ruled), quoted per 2.6 (`KillLine`, `fixWord`, `TestKillLineQuotesTheReason`), no placeholder; plain force-release still works (`TestForceReleaseLiveRefusedDuringTheUpgrade`, Task 8).
- **3.2 old-holder kill, steps 0 to 5.** Step 0 ancestor refusal (`TestKillOfAnAncestorIsRefused`); 1 SIGSTOP; 2 ticket `TryLock` re-check with SIGCONT and abort (`TestKillAbortsWhenTheTargetReleasedBeforeTheStop`); 3 the full descendant walk, repeated until stable, stopping each new descendant, over `sysctl kern.proc.all` or `/proc/<pid>/stat` fields 4, 5 and 22, excluding kill and its ancestors (`TestWalkTreeStopsEveryDescendantUntilStable`, `TestWalkTreeListingFailure`); 4 the record by temp plus rename before any terminating signal, with every descendant's pid and start time and each eligible group (`writeOrphan`, `walkTree`'s group rule); 5 SIGKILL to groups, to descendants after the start-time re-check, then the old incoda (`TestKillAV060StrayEndsItsWholeJob` with the `sh -c 'sleep 60 & wait'` grandchild; v0.2.0 with its child's own group). The protected window: SIGINT, SIGTERM, SIGHUP ignored (`TestKillWindowIgnoresSIGINTAndSIGTERM`), no registry lock, SIGCONT on every abort path (`TestKillFailedRecordWriteResumesEverything`). Records count as live holders in M2 and M5 (Task 4, `TestOrphanRecordIsLiveUntilItsTreeIsEmpty`) and in stray counting (Task 5) until the tree is empty (`kill(-G, 0)` not ESRCH, EPERM included, `TestOrphanRecordCountsAGroupWithMembers`; descendant alive with its start time, `TestOrphanRecordIgnoresAReusedPid`), then deleted. Plain force-release deletes stale records (`TestPlainForceReleaseDeletesStaleOrphanRecords`); doctor lists every record (Task 10). Listing failure refuses with the exact text and terminates nothing (`listingRefusal`, SIGCONT before returning). Nested v0.6.0 holder: the outer incoda and outer job are not signalled (`TestKillANestedV060HolderLeavesTheOuterJob`). Kill inside a group it must not signal (`TestKillInsideAGroupItMustNotSignal`). Windows: termination only, citation in Global Constraints (`oldkill_windows.go`).
- **3.2 stopped holders.** `status` and `doctor` flag a live holder in state `T` with the exact rerun line, and the rerun recovers it (`TestStoppedHolderShownAndRecoveredByRerun`, Tasks 9 and 10).
- **5.3 (this plan's part).** `unpooled run by an older incoda: pid N, key K`, `fence missing: the next run re-places it (incoda doctor)` and the `stopped holder:` line, after every existing line (`TestStatusWarnsAboutUnpooledRunsAndAMissingFence`, Task 9).
- **5.5 remainder.** `strays/` content, orphan records, live unpooled holders, stopped holders (`TestDoctorReportsStraysOrphansAndUnpooledHolders`, the stopped-holder test); every incoda on PATH with its version, stdin from `/dev/null`, own process group killed at 5s, `cmd.WaitDelay`, at most 4 KiB, `< 0.7` flagged, `attention:` for dev, unparseable and timeout (`TestDoctorProbesEveryIncodaOnPath`, which also bounds a probe whose child holds stdout open; `TestParseVersion`, `TestCappedBufferKeepsFourKiB`); the queues fence (plan 2a, unchanged). Task 10.
- **9, tests in scope.** Old-holder kill against real v0.6.0 (grandchild; before migration with plain `kill`, and as a stray) and v0.2.0, nested v0.6.0, ancestor, group, released before SIGSTOP, SIGINT and SIGTERM in the window, failed record write, stopped holder shown and recovered (Tasks 7, 9, 10); fence deletion with a live stray counted (Task 5); migration with live old tickets: kill before and after the fence (`TestKillBeforeTheFence`, `TestKillAfterTheFenceDuringTheUpgrade`), force-release `--live` refused (Task 8); doctor's version probe bounded for a probe whose child holds stdout open (Task 10).

Carried items:

- **From plan 1 for plan 2:** no build cut before the fence lands. Plan 2a landed the fence; the carried 2a hold (no build before Tasks 7 and 8) is in the Global Constraints.
- **From plan 2a for plan 2b:** `Inspect` rows 3 and 6 (Task 1, first); the M5 `--force` stop line and the `force-release --live` refusal before any build leaves the branch (Tasks 7 and 8, Global Constraints); EPERM and EXDEV to the fallback (Task 2, `TestIsNoExchange`); the last rename error in the give-up message (Task 2, `TestPlaceFenceGivesUpAfterAHundredTries`); escaped `machine.lock` errors (Task 2, `TestAcquireLockEscapesItsErrors`); Windows waiting line, blocker probe on `queues/` and the two not-idle errors (Task 2, `TestNotIdleWaitNamesTheOldRunsInQueues`); the M8 re-fence cap counting real re-fences (Task 2, `TestCommitRefenceCapCountsOnlyRealRefences`); doctor lists the lanes on a lost registry with a `POOL,POOL` placeholder (Task 10, `TestDoctorReportsTheLayout/registry_lost`); `--rebuild-registry=` and `--wait` alone refused (Task 10, `TestDoctorRefusesBadFlagCombinations`); row 9 skips M0 and the `op=migrate` note (Task 2, `TestRow9SkipsM0AndTheMigrateNote`, which also covers the stale `queues/` directory); the `SetBlockers` nil guard (Task 2, `TestSetBlockersAfterRelease`); M7 skips a config that turns malformed (Task 2, `TestBootstrapSkipsAConfigThatTurnsMalformed`); `layoutState.Registry` dropped (Task 1); the empty-directory race test (Task 2, `TestEmptyDirRaceSendsTheFirstOldRunToStrays`).

Residuals stated, not changed here:

- The ruling's cost: an old job ended by the walk does not print its own `killed by` notice (its incoda is stopped, then SIGKILLed). kill's own output and the `event=kill ... old=true` line in that lane's `lane.log` record who and why. The spec text of 3.2 (request before the fence, `add --force` after it) and 3.3 M5 (`--force` in the stop line) should be amended to match the ruling.
- On Linux the process listing is validated by vet and by the platform-independent `parseStat` test only; the integration tests ran on macOS (no Linux host was reachable during planning). Run `go test -race ./internal/procinfo/ ./internal/machine/` and the root kill tests on a Linux machine before the first release.

Left to plan 3: kinds and links (`unlinked:`, `--pool`, `link`, `init`, `pools add|remove`), so that runs on project lanes enroll on their pools and are charged for unpooled holders through them (the charging rule itself is in place); `Enroll` failing closed on `NewerSchemaError`. Plan 4: the nested-run rules (non-blocking acquisition, fix lines in full, outer trailer, self-wait for live ancestors, `root` and `pgid`). Plan 5: `status --json` fields for strays and stopped holders, the rest of 5.3, the tree and watch, the interactive watch's warnings, release notes (the old-holder kill and the `INCODA_HELD` change), and the plan 2a items routed there.

