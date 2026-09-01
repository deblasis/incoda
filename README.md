# incoda

*In coda* — Italian for "in the queue".

`incoda` serialises heavy processes through named, machine-local queues. Builds
and GUI/UI test runs go in one end; exactly as many as you allow come out the
other. Everything else waits its turn, in the order it arrived.

```
incoda run --queue wintty -- zig build -Denable-llvm
incoda run --queue wintty --reason "drag fuzz suite" -- dotnet test
incoda status --queue wintty
```

## Why this exists

A 24 GB M2 MacBook Air kernel-panicked three times in a single day on
2026-08-29. Each time, two memory-heavy jobs were running at once: two
`-Denable-llvm` zig builds, or an LLVM build plus a node job with an 8–12 GB
heap, across roughly twenty open agent sessions. The compressor reached 100% of
segments, swap hit its 63-file ceiling, and the watchdog starved and took the
whole machine down.

The cruel part is that there is no warning. macOS reports `memoryPressure` as
FALSE right up until the watchdog fires. You do not get a slow machine and a
chance to react; you get a panic.

The fix was not more RAM. It was making the collision impossible: one lane, and
heavy jobs run inside it. That was `build-lane`, a small `sh` script using
`shlock`. This is the generalisation of it — cross-platform, keyed, and with
real locking instead of pid liveness.

The same shape of problem shows up beyond memory. A GUI or UI test run needs the
desktop to itself: two of them at once fight over focus, the foreground window,
and synthesized input, and both fail in ways that look like product bugs.
`incoda` is for anything that needs the machine, or some part of it, to be quiet.

## The model

**Keys.** A queue is named. `incoda run --queue wintty` and
`incoda run --queue website` never block each other. Several repositories can
deliberately share one key: `wintty` and `wintty-release` both build the same
heavy thing, so they both use `wintty`.

**Slots.** Each queue has a slot count, default 1 — plain mutual exclusion.
`--slots 2` lets two holders run at once. That generalisation is the point: not
every resource is exclusive.

**The state is per machine and per user, and never depends on where you are.**
This matters more than it sounds. The real callers are several agent sessions,
each in its own git worktree of the same repository. `incoda run --queue wintty`
from `C:\work\wintty-tabs`, from `C:\work\wintty-release`, and from `C:\` all
contend for the same lane. Nothing is resolved relative to the working
directory, ever — no walking up for a repo root, no `.incoda/` beside the
caller. The queue key is the only namespacing dimension there is.

State directory resolution, in order:

| | |
|---|---|
| `$INCODA_DIR` | machine-level override (see the warning below) |
| Windows | `%LOCALAPPDATA%\incoda` |
| macOS | `~/Library/Application Support/incoda` |
| Linux | `$XDG_STATE_HOME/incoda`, else `~/.local/state/incoda` |

Per-queue state lives in `<dir>/queues/<key>/`. Keys become directory names, so
they are validated: letters, digits, `-`, `_`, `.`, at most 64 bytes, no path
separators, no `.`/`..`, no Windows device names. An invalid key is refused
rather than sanitised, because silently rewriting a key would let two callers
who meant different queues share one.

> **`INCODA_DIR` is a machine-level override, not a per-project one.** If one
> caller has it set and another does not, they use different state directories,
> form separate lanes, and stop serialising each other — and every fragment
> looks like a perfectly healthy, empty queue. Do not set it in a repo `.env`,
> a `direnv` file, or a per-worktree profile. `incoda doctor` warns when it is
> set, and both `status` and `doctor` print the resolved directory first so a
> fragmented setup is diagnosable in one command.

### Locking: the OS, not pid liveness

`build-lane` used a `shlock` pid file: it refused while the recorded pid was
alive and took the lock over if that pid had died. That works, but it needs a
liveness check, it is racy under pid reuse, and it needs a separate
implementation on every platform.

`incoda` does not check liveness. **Every participant creates a ticket file and
holds an OS-level exclusive lock on it for its entire lifetime** —
`LockFileEx` on Windows, `flock(LOCK_EX)` on Unix. The kernel releases that lock
when the process ends for *any* reason: normal exit, panic, `SIGKILL`,
`TerminateProcess`, or the machine losing power.

So the liveness scan is just: try to lock each ticket file, non-blocking. If the
lock is taken, its owner is alive. If it succeeds, the owner is gone and the
ticket is deleted. **Staleness heals itself. There is no takeover logic and no
`--force` needed for the common case.** A holder you kill with Task Manager
frees the lane within one poll interval.

A second, briefly-held lock (`registry.lock`) serialises ticket creation,
release and scanning. It closes the window between "a ticket file exists" and
"its owner has locked it" — a scanner that observed that window would find the
lock free, conclude the owner was dead, and delete a living participant's
ticket.

The locked byte is at offset 2^62, past the end of the payload, not at offset 0.
On Windows a byte-range lock also denies *reads* of that range, so locking byte
0 would make the ticket's own JSON unreadable to `status` and to the scanner
that computes the slot count. (SQLite uses the same trick for the same reason.)

### Ordering: FIFO, and it is tested

Ticket filenames are `<arrival-nanos>-<pid>.ticket`, zero-padded so lexical and
numeric order agree. **The ordering rule is: arrival nanosecond, then pid, then
filename** — fully deterministic, and every participant derives it from the same
directory listing, so they all reach the same answer. The arrival stamp is taken
while holding the registry lock, so stamp order and file-visibility order cannot
disagree.

A participant holds a slot when its ticket is among the N lowest-ordered *live*
tickets. Waiters poll (500 ms by default) instead of waiting on a signal — no
cron, no IPC, nothing to go stale.

This is genuine FIFO: a later arrival cannot overtake an earlier one that is
still waiting. `TestFIFOOrder` proves it by launching waiters one at a time,
confirming each is enrolled (visible in `status --json`) before starting the
next, then asserting that service order matches enrollment order. Enrolling one
at a time is deliberate: launching them concurrently would test the OS process
scheduler, not the queue.

Two honest caveats:

- The claim is FIFO **by enrollment**, not by wall-clock intent. Two processes
  started at the same instant enroll in whatever order they reach the registry
  lock. That is the only order anything can observe, and it is the order served.
- Fairness costs a little throughput: a freed slot can sit idle for up to one
  poll interval while the next in line notices. That is the deliberate trade for
  having no signalling and nothing that can go stale.

## Commands

```
incoda run --queue KEY [--slots N] [--wait DUR] [--reason TEXT] [--] <cmd...>
incoda status [--queue KEY] [--all] [--json]
incoda watch [--queue KEY] [--interval 2s] [--once]
incoda queues
incoda force-release --queue KEY [--live]
incoda doctor
incoda version
```

`run` — acquire a slot, run the command, release on every exit path.

- `--wait` accepts a Go duration (`30m`, `90s`) **and** a bare integer read as
  seconds, because `build-lane` took `--wait 1800` and that habit should keep
  working. Default `30m`. `--wait 0` fails immediately if no slot is free.
  A negative value waits forever.
- `--slots N` permits N concurrent holders. If live participants disagree about
  the slot count, the smallest value wins and `run` prints a warning. Mixing
  values on one queue is a configuration error; the minimum is the safe
  direction, but a participant already running is never revoked, so a late
  arrival with a smaller `--slots` can briefly observe more holders than its own
  number.
- `--reason TEXT` is free text shown in `status`. Worth using.
- `--poll DUR` (default `500ms`) and `--quiet` are also available.
- The queue key comes from `--queue` or `INCODA_QUEUE`. **There is no default
  key**: an unkeyed `run` is refused, because two unrelated projects silently
  sharing one lane is exactly the failure this tool exists to prevent.

`status` — holders and waiters in arrival order, each with pid, elapsed time,
command, **working directory** and reason, plus the effective slot count, the
last few log events, and a memory readout. It prints the resolved state
directory first. A queue that has never been used is reported as free, not as an
error. `--all` covers every queue on the machine. `--json` emits a stable,
versioned (`"schema": 1`) document intended for scripting: fields are added,
never renamed or removed.

`watch` — repaint `status` on an interval. `--once` paints and exits.

`queues` — every queue with state on this machine, and whether it is busy.

`force-release` — delete a queue's tickets. **It refuses while any live
participant exists unless you pass `--live`**, and the error says why: a blind
force-release once caused a real collision in the tool this replaces. Deleting a
live participant's ticket does not stop that process. It only removes the record
that was keeping the next caller out of its way. You should almost never need
this command — a dead holder's lock is released by the kernel and reaped
automatically.

`doctor` — resolved state directory and how it was chosen, an `INCODA_DIR`
warning if it is set, writability, and a real locking probe: it takes an
exclusive lock and then checks through a second independent handle that the lock
is actually *refused*. A filesystem that ignores locks (some network mounts do)
fails here, loudly, instead of failing later as two heavy builds running at once.

### Exit codes

`run` passes the child's own exit status through unchanged. Lane-level failures
use a band a build tool is unlikely to produce. These are part of the interface;
scripts may rely on them.

| Code | Meaning |
|---|---|
| *child's* | `run` succeeded in acquiring; this is the command's own status |
| `120` | usage error — bad flags, missing or invalid queue key, or a `force-release` refused because the queue has live participants |
| `121` | `--wait` elapsed while still queued |
| `122` | state directory or OS file locking unusable |
| `123` | the lane was acquired but the command could not be started |
| `130` | `incoda` was interrupted while queueing |

### Signals and process trees

`SIGINT`/`SIGTERM`/Ctrl+C are forwarded to the child, and the ticket is released
on every exit path.

On **Windows** the child is created suspended, put into a Job Object with
`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, and only then resumed — so it cannot spawn
a grandchild outside the job even in the microseconds after creation. Killing
`incoda` therefore takes the whole tree with it. This matters: a `zig build` or
`dotnet test` tree that outlives its lane holder keeps the machine busy while the
lane reports free, which is precisely the collision `incoda` exists to prevent.
`TestChildProcessTreeDiesWithIncoda` asserts it.

On **Unix** the child gets its own process group and signals go to the group.
That covers signalled exits but not a `SIGKILL` of `incoda` itself; see Known
limits.

## Install

Releases are built by GitHub Actions on tag `v*` for `windows/amd64`,
`windows/arm64`, `darwin/arm64`, `darwin/amd64`, `linux/amd64` and `linux/arm64`,
with a `SHA256SUMS` file.

**This repository is private**, so a plain `curl` of a release asset will not
work — private release assets need an authenticated API call. The install
scripts use the `gh` CLI for exactly that reason.

```powershell
# Windows
gh auth status                      # must be logged in
./install.ps1                       # or: ./install.ps1 -Version v0.1.0
```

```sh
# macOS / Linux
gh auth status
./install.sh                        # or: ./install.sh v0.1.0
```

Both scripts detect OS and architecture, `gh release download` the matching
asset, verify its SHA-256 against `SHA256SUMS`, and install to
`%LOCALAPPDATA%\Programs\incoda` or `~/.local/bin`. They fail loudly if `gh` is
missing or unauthenticated rather than falling back to something unverified.

**Your `gh` token needs `repo` scope** to download a private repository's
release assets. Check with `gh auth status`.

To install without the scripts:

```sh
go install github.com/deblasis/incoda@latest    # needs Go 1.27+
```

## Scope

Carried over from `build-lane`, because it is still the honest framing:

**It serialises. It does not make an oversized single job fit.** If one
lane-holder run alone drives the machine into swap or into an OOM kill, that job
is too big for that machine, and no amount of queueing fixes it. `incoda status`
shows a memory readout so you can see this happening; it is a gauge, not a
governor.

**It is advisory. It binds only what is routed through it.** A build you start
by hand in another terminal, or an agent that skips the lane "just this once",
collides exactly as before. There is no enforcement, and there deliberately is
none — enforcement would mean intercepting process creation. This works by
convention, which is why `AGENT-RULE.md` exists.

**When the lane makes you wait, that is the tool working.** If the wait is long,
surface it. Do not bypass it.

## Known limits

- **Machine-local only.** There is no cross-machine coordination. Two machines
  with the same queue key know nothing about each other. The state directory
  must be on a local filesystem; `doctor`'s locking probe will fail on a network
  mount that does not enforce locks, which is the correct outcome.
- **Per-user.** State lives in a per-user directory, so two OS users on one
  machine get separate lanes and do not serialise each other.
- **Advisory, as above.** Only what is routed through `incoda` is bound.
- **Unix process trees survive a hard kill of `incoda`.** The process group
  covers signalled exits; a `SIGKILL` to `incoda` leaves the group orphaned.
  Windows gets a real guarantee from the Job Object, Unix gets a best effort.
  (A `PR_SET_PDEATHSIG` or cgroup-based approach could close this on Linux; it
  is not implemented.)
- **`--slots` disagreement is warned about, not prevented.** See `run` above.
- **The memory readout is not uniform.** Windows reports total, available and
  page-file commit; Linux reports total, available and swap from `/proc/meminfo`;
  macOS reports total and `vm.swapusage` but **not** available physical memory,
  because that needs a mach `host_statistics64` call and therefore cgo, and this
  binary stays pure Go. The renderer says "unavailable" instead of printing a
  confident zero. `build-lane`'s Mac-only swap colour gauge is not carried over.
- **Polling costs up to one interval of latency** on handoff. See Ordering.
- **No per-holder process-tree view.** `build-lane status` walked `pgrep -P` and
  showed each child's CPU and RSS. That is three platform-specific
  implementations and is not implemented; `status` shows the command and the
  holder pid instead.
- **No `run` output capture or log rotation.** The child's stdio is passed
  straight through, and `lane.log` grows without bound (slowly — a few lines per
  run).

## Dependencies

The standard library, plus `golang.org/x/sys` for `LockFileEx`, `flock`, Job
Objects and the sysctl reads. That is the only dependency. One syscall
(`GlobalMemoryStatusEx`) is bound by hand because `x/sys/windows` does not wrap
it.

Building requires Go 1.27.0 or newer; with `GOTOOLCHAIN=auto` (the default) the
toolchain is fetched automatically.

## See also

`AGENT-RULE.md` — a copy-pasteable rule block for `~/.claude/CLAUDE.md` or a
repository's `AGENTS.md`, so agent sessions on a machine use the lane by default.
