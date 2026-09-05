# Design

Why `incoda` is built the way it is. The [README](../README.md) is the
30-second version; this is the version for people who want to trust it, and for
the maintainer who has to change it in a year.

## State directory

State is per machine and per user, and never depends on where you are. This
matters more than it sounds. The real callers are several agent sessions, each
in its own git worktree of the same repository. `incoda run --queue builds`
from `C:\work\app`, from `C:\work\app-release`, and from `C:\` all contend for
the same lane. Nothing is resolved relative to the working directory, ever:
no walking up for a repo root, no `.incoda/` beside the caller. The queue key
is the only namespacing dimension there is.

Resolution order:

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
> form separate lanes, and stop serialising each other, and every fragment
> looks like a perfectly healthy, empty queue. Do not set it in a repo `.env`,
> a `direnv` file, or a per-worktree profile. `incoda doctor` warns when it is
> set, and both `status` and `doctor` print the resolved directory first so a
> fragmented setup is diagnosable in one command.

## Locking: the OS, not pid liveness

The shell-script predecessor of this tool used a pid file: it refused while the
recorded pid was alive and took the lock over if that pid had died. That works,
but it needs a liveness check, it is racy under pid reuse, and it needs a
separate implementation on every platform.

`incoda` does not check liveness. **Every participant creates a ticket file and
holds an OS-level exclusive lock on it for its entire lifetime**:
`LockFileEx` on Windows, `flock(LOCK_EX)` on Unix. The kernel releases that lock
when the process ends for *any* reason: normal exit, panic, `SIGKILL`,
`TerminateProcess`, or the machine losing power.

So the liveness scan is just: try to lock each ticket file, non-blocking. If
the lock is taken, its owner is alive. If it succeeds, the owner is gone and
the ticket is deleted. **Staleness heals itself. There is no takeover logic and
no `--force` needed for the common case.** A holder killed from Task Manager or
`kill -9` frees the lane within one poll interval.

`flock` is used rather than fcntl/POSIX record locks on purpose: POSIX locks
are released when the process closes *any* descriptor on the file, so a stray
`os.Open` in the same process could silently drop somebody else's lock. `flock`
locks live on the open file description and survive that.

**Two locks, not one.** A second, briefly-held lock (`registry.lock`)
serialises ticket creation, release and scanning. It closes the window between
"a ticket file exists" and "its owner has locked it": a scanner that observed
that window would find the lock free, conclude the owner was dead, and delete a
living participant's ticket. Holding the registry lock across create+lock,
across release, and across every scan also makes the arrival stamp order agree
with the order in which tickets become visible.

The lock order is registry-then-ticket, and ticket acquisition is always
non-blocking, so the pair cannot deadlock.

**The locked byte is at offset 2^62**, past the end of the payload, not at
offset 0. On Windows a byte-range lock also denies *reads* of that range, so
locking byte 0 would make the ticket's own JSON unreadable to `status` and to
the scanner that computes the slot count. SQLite uses the same trick for the
same reason.

Fail-safe bias: a ticket that cannot be opened or parsed is treated as *live*
(the queue waits) rather than dead, and `status` reports the payload error
instead of hiding it. An unreadable payload is never fatal to correctness,
which depends only on the OS lock, but a silently unreadable payload would
make every queue look like a one-slot queue.

## Ordering: FIFO, and it is tested

Ticket filenames are `<arrival-nanos>-<pid>.ticket`, zero-padded to 20 digits
so lexical and numeric order agree. **The ordering rule is: arrival nanosecond,
then pid, then filename**, fully deterministic, and every participant derives
it from the same directory listing, so they all reach the same answer. The
arrival stamp is taken while holding the registry lock, so stamp order and
file-visibility order cannot disagree.

A participant holds a slot when its ticket is among the N lowest-ordered *live*
tickets. Waiters poll (500 ms by default) instead of waiting on a signal: no
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
  lock. That is the only order anything can observe, and it is the order
  served.
- Fairness costs a little throughput: a freed slot can sit idle for up to one
  poll interval while the next in line notices. That is the deliberate trade
  for having no signalling and nothing that can go stale.

## Slots

Each queue has a slot count, default 1: plain mutual exclusion. `--slots N`
permits N concurrent holders, which is the point of the generalisation: not
every resource is exclusive.

The effective slot count for the current ticket set is the **minimum** asked
for by any live participant, floored at 1. Mixing values on one queue is a
configuration error; the minimum is the safe direction (the most restrictive
caller wins), and `run` warns when it observes a disagreement. It is not a full
guarantee: a participant already running is never revoked, so a late arrival
with a smaller `--slots` can briefly observe more holders than its own number.

## Re-entrancy

A queue is most useful when the recipe that needs it takes it itself: `just
fuzz` runs `incoda run --queue gui -- ...` inside, so nobody can run the
harness outside the lane by forgetting. The trap is an agent that then wraps
the whole recipe in `incoda run --queue gui` from outside. The inner run would
queue behind its own parent, wait out `--wait`, and exit 121 for a job that
never ran.

So `run` exports `INCODA_HELD`, a comma-separated list of the keys it holds,
into the child's environment. A nested `run` whose key is in that list does
not enroll: it logs `event=reenter` on the queue and runs the command inside
the parent's ticket. A key that is *not* in the list queues normally, so a
nested run on another queue still serialises.

This is inheritance, not a global: only descendants of the holding `incoda`
see the variable, so an unrelated session cannot claim a lane it does not
hold by setting it. Setting `INCODA_HELD` by hand is the same bypass as not
using the tool.

## Job accounting

Deciding how many slots a queue should have is a measurement, not a policy.
The claim behind `--slots 2` on a build queue is "two of these fit in memory
at once", and until the release line said what one of them peaked at, that
claim was a feeling.

On release `run` writes `peak_mem=` and `cpu=` on the queue's log line. On
Windows they come from the Job Object the child runs in
(`PeakJobMemoryUsed`, and the basic accounting user plus kernel time), so
they cover every process the job spawned, not just the shell that started it.
On Unix they come from the child's `rusage`, which the kernel only rolls
grandchildren into when the child waited for them; that is documented as an
approximation rather than hidden. A platform with neither writes nothing.

The scanner also logs `event=reaped` when it deletes a dead ticket. Before
that, a holder killed from Task Manager left an enqueue with no ending, and a
history that cannot say how a job finished cannot be used to size a queue.

## Signals and process trees

`SIGINT`/`SIGTERM`/Ctrl+C are forwarded to the child, and the ticket is
released on every exit path the program can reach. The OS lock covers the paths
it cannot: `SIGKILL`, crash, power loss.

On **Windows** the child is created suspended, put into a Job Object with
`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, and only then resumed, so it cannot spawn
a grandchild outside the job even in the microseconds after creation. Killing
`incoda` therefore takes the whole tree with it. This matters: a `zig build` or
`dotnet test` tree that outlives its lane holder keeps the machine busy while
the lane reports free, which is precisely the collision `incoda` exists to
prevent. `TestChildProcessTreeDiesWithIncoda` asserts it.

On **Unix** the child gets its own process group and signals go to the group.
That covers signalled exits but not a `SIGKILL` of `incoda` itself; a
`PR_SET_PDEATHSIG` or cgroup-based approach could close this on Linux and is
not implemented.

## Exit codes

`run` passes the child's own exit status through unchanged. Lane-level
failures use a band a build tool is unlikely to produce. These are part of the
interface; scripts may rely on them.

| Code | Meaning |
|---|---|
| *child's* | `run` succeeded in acquiring; this is the command's own status |
| `120` | usage error: bad flags, missing or invalid queue key, or a `force-release` refused because the queue has live participants |
| `121` | `--wait` elapsed while still queued |
| `122` | state directory or OS file locking unusable |
| `123` | the lane was acquired but the command could not be started |
| `130` | `incoda` was interrupted while queueing |

## The memory readout

It is a gauge, not a governor, and each field is explicitly optional: the
renderer says "unavailable" rather than printing a confident zero.

- **Windows**: total, available and page-file commit, via
  `GlobalMemoryStatusEx`. This is the one hand-rolled syscall in the tree,
  because `x/sys/windows` does not wrap it.
- **Linux**: total, available and swap from `/proc/meminfo`.
- **macOS**: total from `hw.memsize` and swap from `vm.swapusage`, but **not**
  available physical memory: that needs a mach `host_statistics64` call and
  therefore cgo, and this binary stays pure Go.

## Color

Human output is painted with a small hand-rolled ANSI palette
(`internal/colorize`), so the dependency count stays at one. The codes are the
classic 8-color set plus bold and dim, which adapt to the terminal's theme
instead of fighting it.

The decision follows the conventions people already know, in priority order:
`NO_COLOR` (https://no-color.org, any non-empty value) always wins; then
`CLICOLOR_FORCE` (anything but `0`) forces color on, even for a pipe; then
color requires a terminal and a `TERM` that is not `dumb`. `--no-color` is
the per-invocation switch and beats everything except `NO_COLOR`. On Windows
the palette only turns on after `SetConsoleMode` accepts
`ENABLE_VIRTUAL_TERMINAL_PROCESSING`, so a console that cannot draw the
sequences gets plain text instead of escape soup.

With color off, the renderers are byte-for-byte what the uncolored ones
always produced; scripts that scrape the plain text keep working, and
`status --json` is never painted.

## Dependencies

The Go standard library, plus `golang.org/x/sys` for `LockFileEx`, `flock`,
Job Objects and the sysctl reads. That is the only dependency. Building
requires Go 1.27.0 or newer; with `GOTOOLCHAIN=auto` (the default) the
toolchain is fetched automatically.

## Release and install

`ci.yml` runs the test suite (with `-race` on Linux), vet and `gofmt` on
Linux, macOS and Windows on every push and PR. The same three commands are
what a contributor runs locally:

```sh
go test -race ./...
go vet ./...
gofmt -l .
```

`release.yml` triggers on `v*` tags (or a manual `workflow_dispatch`), runs the
tests, cross-compiles the six release targets with version information stamped
in via `-ldflags -X`, and publishes the binaries plus a `SHA256SUMS` file to
the GitHub release. The install scripts download the right asset, verify it
against `SHA256SUMS`, and refuse to install anything unverifiable.
