# incoda

### One heavy job at a time. A keyed, machine-local job lane for builds, tests and AI-agent fleets.

`incoda` serialises heavy processes through named queues on one machine. Prefix
any command and it either runs immediately or waits its turn, FIFO by arrival.
When a holder dies, the kernel frees the lane: no stale locks, no takeover
logic, no `--force` in the common case. One static Go binary, no dependencies.

![incoda demo: a second job queues behind a running build, `status` shows both, and the lane hands over when the holder exits](docs/img/demo.gif)

*(Recorded with [`docs/img/demo.tape`](docs/img/demo.tape); re-record it with [vhs](https://github.com/charmbracelet/vhs).)*

```bash
incoda run --queue builds --reason "zig build (LLVM)" -- zig build -Denable-llvm
incoda run --queue builds --reason "dotnet test suite" -- dotnet test
incoda status --queue builds
```

## Why

Load a machine with enough heavy jobs and it settles the argument for you. On a
memory-constrained workstation, two concurrent memory-hungry builds can push
the kernel into swap exhaustion and a watchdog panic, and there is no warning:
macOS reports `memoryPressure` as FALSE right up until the machine dies. The
fix is not more RAM. It is making the collision impossible: heavy jobs go
through one lane, so they cannot overlap.

The same shape of problem shows up wherever two jobs fight over a resource:
GUI test runs that need the desktop to itself, builds that share one cache
directory, anything driving a device. `incoda` is for anything that needs the
machine, or some part of it, to be quiet.

## What people use it for

- **AI agent fleets.** You are orchestrating a dozen agent sessions in parallel
  and they all want to build, test and lint at once. Give each session the same
  `--queue` key and the heavy work serialises instead of colliding. Works
  across git worktrees: nothing is keyed to a working directory.
- **GUI and E2E test runs.** Two of them at once fight over focus, the
  foreground window and synthesized input, and both fail in ways that look like
  product bugs. A `gui-tests` queue gives each run the desktop to itself.
- **`--slots N` for resources that are not exclusive.** Two CPU-heavy linters
  at a time, no more. Participants that disagree about N settle on the minimum.
- **Shared workstations and self-hosted runners.** One Mac mini serving several
  people, agents or CI jobs: same key, orderly queue, full visibility of who is
  holding it from where.
- **Agent compliance by convention.** Ships with a rule block
  ([`AGENT-RULE.md`](AGENT-RULE.md)) for `CLAUDE.md` / `AGENTS.md`, so your
  agents route heavy commands through the lane without being told each time.

## How it works

**Keys.** A queue is a name. `--queue builds` and `--queue gui-tests` never
block each other. There is no default key: an unkeyed `run` is refused, because
two unrelated projects silently sharing one lane is exactly the failure this
tool prevents.

**Slots.** Each queue has a slot count, default 1: plain mutual exclusion.
`--slots 2` lets two holders run at once.

**FIFO, really.** Ticket filenames encode arrival order, every participant
derives the order from the same directory listing, and a later arrival cannot
overtake an earlier one. Waiters poll instead of waiting on a signal, so
nothing can go stale; the cost is up to one poll interval (500 ms default) of
handoff latency.

**The kernel holds the lock.** Every participant holds an OS-level exclusive
lock on its ticket file for its whole lifetime: `flock` on Unix,
`LockFileEx` on Windows. The kernel releases it when the process ends for any
reason, including `SIGKILL` and power loss. Stale tickets are reaped by trying
to lock them; there is no pid file to lie.

**Machine-local and per-user, never per-directory.** The real callers are
several agent sessions, each in its own git worktree. `incoda run --queue
builds` contends for the same lane from any folder, worktree or drive letter.
State lives in one place per user (`%LOCALAPPDATA%\incoda` on Windows,
`~/Library/Application Support/incoda` on macOS, `$XDG_STATE_HOME/incoda` on
Linux) and nothing is ever resolved from the working directory.

## Commands

| Command | What it does |
|---|---|
| `incoda run --queue KEY [--slots N] [--wait DUR] [--reason TEXT] [--owner WHO] -- <cmd...>` | Acquire a slot, run the command, release on every exit path. `--wait` takes a Go duration (`30m`) or bare seconds (`1800`); `0` fails fast, negative waits forever, default `30m`. `--owner` (or `INCODA_OWNER`) names the session or worktree that queued the job. |
| `incoda status [--queue KEY] [--all] [--json]` | Holders and waiters in arrival order, with pid, elapsed time, command, working directory and reason. `--json` is a stable, versioned schema for scripts. |
| `incoda watch [--queue KEY] [--interval 2s] [--once]` | Repaint `status` on an interval. |
| `incoda queues` | Every queue with state on this machine, and whether it is busy. |
| `incoda force-release --queue KEY [--live]` | Delete a queue's tickets. Refuses while live participants exist unless `--live`. You almost never need this. |
| `incoda doctor` | State directory, `INCODA_DIR` warning, writability, and a real locking probe that fails loudly on filesystems that do not enforce locks. |

`run` passes the child's own exit code through unchanged. Lane-level failures
use a separate band, documented and stable: `120` usage, `121` wait elapsed,
`122` state unusable, `123` spawn failure, `130` interrupted while queueing.

Output is colored on a terminal and plain everywhere else. The standard
opt-outs work: `--no-color`, or `NO_COLOR` set to any non-empty value, and
color never reaches `--json` or a pipe. `CLICOLOR_FORCE=1` turns it back on
for `| less -R` and its cousins.

Signals are forwarded to the child. On Windows the child runs inside a Job
Object with kill-on-close, so a `SIGKILL` of `incoda` still takes the whole
build tree with it. On Unix the child gets its own process group; a hard kill
of `incoda` can orphan it (see [limits](#known-limits)).

**Nested runs on a held key pass through.** `run` exports `INCODA_HELD` to
its child, listing the keys it holds. A nested `incoda run` on one of those
keys runs at once inside the parent's lane instead of queueing behind it,
which is what lets a build recipe take its own lane while an agent wraps the
whole recipe in `run` from outside. A nested run on a *different* key queues
as usual.

**Every job leaves its cost in `lane.log`.** The release line records the
peak memory and CPU time of the job tree (`peak_mem=3.2 GB cpu=4m12s`), from
the Job Object on Windows and from `rusage` on Unix, and a dead holder's
ticket is logged as `reaped` when the next scan deletes it. The point is to
size a queue from evidence: whether `--slots 2` fits is a question this log
can answer.

## Install

macOS and Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/deblasis/incoda/main/install.sh | sh
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/deblasis/incoda/main/install.ps1 | iex
```

Both scripts detect OS and architecture, download the matching release asset,
verify its SHA-256 against `SHA256SUMS`, and install to `~/.local/bin` or
`%LOCALAPPDATA%\Programs\incoda`. They refuse to install anything they could
not verify.

Or with Go 1.27+:

```bash
go install github.com/deblasis/incoda@latest
```

Prebuilt binaries for `windows/amd64`, `windows/arm64`, `darwin/arm64`,
`darwin/amd64`, `linux/amd64` and `linux/arm64` are on the
[Releases page](https://github.com/deblasis/incoda/releases).

## Using it with AI agents

The tool works by convention: it binds only what is routed through it. That is
a feature, not a gap, but it means agents have to be told. `AGENT-RULE.md` is a
copy-pasteable rule block for a machine's `~/.claude/CLAUDE.md` or a
repository's `AGENTS.md` that tells every agent session: these command classes
run under the lane, use this key, never bypass it. One paragraph, and a dozen
parallel sessions stop stepping on each other.

## Scope and non-goals

**It serialises. It does not make an oversized single job fit.** If one job
alone drives the machine into swap or an OOM kill, that job is too big for that
machine, and no amount of queueing fixes it. `status` shows a memory readout so
you can see this happening; it is a gauge, not a governor.

**It is advisory. It binds only what is routed through it.** There is no
process-creation interception and there deliberately is none. This works by
convention, which is why `AGENT-RULE.md` exists.

**When the lane makes you wait, that is the tool working.** If the wait is
long, surface it. Do not bypass it.

Not planned: cross-machine coordination (use a real queue or CI for that),
memory limits or cgroups, per-project state directories, distributed locks.

## Known limits

- **Machine-local and per-user.** Two machines, or two OS users on one machine,
  never serialise each other. State must be on a local filesystem; `doctor`
  fails loudly on network mounts that do not enforce locks.
- **Unix process trees survive a hard kill of `incoda`.** The process group
  covers signalled exits; only Windows gets the kill-on-close guarantee.
- **`--slots` disagreement is resolved to the minimum, not prevented.** A
  participant already running is never revoked.
- **The memory readout is not uniform.** macOS reports total and swap but not
  available physical memory, because that needs cgo and this binary stays pure
  Go. It says "unavailable" instead of printing a confident zero.
- **Polling costs up to one interval of handoff latency**, and `lane.log`
  grows without bound (slowly; a few lines per run).

## Design

The full rationale (locking protocol, the registry-lock window, ordering
guarantees and their caveats, platform process-tree behaviour, state directory
resolution) is in [`docs/DESIGN.md`](docs/DESIGN.md).

## License

[MIT](LICENSE)
