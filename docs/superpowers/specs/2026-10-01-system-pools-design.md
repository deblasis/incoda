# System pools: design

Status: approved design, implementation pending (2026-10-01). Base: incoda main at 2554510 (v0.6.0
plus "configured slots are the width").

## 1. Summary

Pools are machine-wide caps per resource class (`builds`, `tests`, `computer-use`, `vm`). Each pool
is a real incoda queue registered in one machine file (`machine.json`). Every project queue links
to one or more pools, and a run takes its project lanes, then its pools, one at a time in one total
order, so caps hold across projects without weakening incoda's guarantees: kernel-held locks, FIFO,
deadlock freedom, a fail-safe bias and no stale state.

- Older binaries are fenced out by a layout change they cannot survive: state moves to
  `<state>/lanes/` and `<state>/queues` becomes a regular file, so every released binary (v0.1.0 to
  v0.6.0) stops with exit 122 on every key. The move is one transaction under `<state>/machine.lock`
  that runs only when no older binary holds a live ticket.
- Migration registers the pools and links nothing. An existing lane refuses with a suggestion until
  it is linked; an agent may make a first link only to that suggestion, everything else is the
  user's (`incoda link`, `incoda init`). `run` never prompts.
- A nested run passes through lanes held by a verified ancestor, waits only on lanes that sort after
  every live held lane, and takes an earlier lane only if it is free at that instant (non-blocking);
  otherwise it refuses with the exact top-level command to run instead.
- The change also fixes a shipped bug: since v0.3.0 a run's child never got its own process group,
  so `incoda kill` left grandchildren running while the lane read free (2.6, 3.2).

### 1.1 Problem

Projects create their own one-slot queues (`cap-gate`, `kungfoo-gate`, `cap-e2e`, `kungfoo-ui`)
that never block each other. On a laptop that kernel-panics under concurrent heavy jobs, three gates
or three desktop-driving runs then overlap although every project "uses the lane". The goal is
machine-wide caps per resource class that every project queue must take part in, with bootstrap,
mandatory linking and a collapsible pool tree in the UI.

## 2. Model

### 2.1 Kinds and the registry

A lane is either a `pool` (a machine-wide resource class: builds, tests, computer-use, vm, ...) or a
`project` lane. Two levels only: pools never link.

`<state>/machine.json` is the sole registry of which keys are pools and of the layout version. Pool
existence and kind are never derived from scanning lane configs. Shape:

```
{"schema": 1, "layout": 2, "generation": 7,
 "pools": ["builds", "computer-use", "tests", "vm"],
 "migrated_by": "incoda 0.7.0", "migrated_at": "2026-10-02T09:14:03Z"}
```

- `machine.json` is written only under `machine.lock`, by temp file plus rename (with the Windows
  retry of 4.4), and every write increments `generation`. Rewrites preserve fields the binary does
  not know.
- Readers (every run, status, watch) read it without a lock; rename makes the read atomic.
- Missing, unreadable or malformed `machine.json` on a migrated layout fails closed: every command
  except `doctor`, `version` and `help` exits 122 with `incoda: machine-state: <reason>; run incoda
  doctor`. Nothing re-bootstraps from that state (3.6).
- A `schema` or `layout` greater than the binary knows is the same exit 122, `incoda: machine-state:
  machine.json was written by a newer incoda; upgrade this one (<path>)`.

A pool is a real queue under `<state>/lanes/<key>/`: tickets, FIFO, slots, `--exclusive`, kill,
reaping and accounting behave exactly as today. A pool may be used directly (`incoda run --queue
builds -- ...`, the habit many agent instructions mandate), which counts against the cap and works from the
moment migration commits, before any project lane is linked.

### 2.2 One config file per lane

Each lane keeps one `config.json` (no second file). It gains a `schema` field (2); a file without it
is read as schema 1 and upgraded on its next write. A `schema` greater than the binary knows makes
every run whose lane set includes that lane, and every write to it, exit 122 (`machine-state: ...
written by a newer incoda`); rewrites preserve unknown fields.

| field | project | pool | binds runs that enroll on the pool via a link |
|---|---|---|---|
| `slots` | the lane's own width | the machine-wide cap | yes (it is the pool's width) |
| `description` | shown in status | shown in status and refusals | no |
| `closed` | refuses runs | refuses runs | yes (4.5) |
| `require_reason` | refuses runs without `--reason` | same | yes (4.5) |
| `pools` | the link: a set of pool keys | must be absent | n/a |
| `quiet_machine` | runs take quiet-machine (2.8) | must be absent | n/a |

Kind is not stored in `config.json`; it comes from `machine.json` alone. A project's own `slots`
still applies inside its pools (kungfoo-build 2, inside builds 1, gives an effective 1 until builds
is raised). The link lives on the project, not in a central policy file or in a member list on
the pool, so a project's own config says what it uses.

### 2.3 State-layout fence against older binaries

Verified with `git show <tag>` for every tag v0.1.0, v0.1.1, v0.2.0, v0.3.0, v0.4.0, v0.5.0,
v0.5.1, v0.6.0, and by running built binaries of all eight tags:

- `lane.QueuesDir` is `filepath.Join(stateDir, "queues")` and `QueueDir` is `QueuesDir/<key>` in all
  eight tags (internal/lane/statedir.go:43-46). `StateDir` resolution is identical in all eight.
- Every command that touches state (`run`, `status`, `watch` in both modes, `queues`, `config` and
  `kill` from v0.3.0, `force-release`, `doctor`) first calls `stateDir()`, which does
  `os.MkdirAll(<state>/queues)` and returns exit 122 on error. `run` calls it before reading
  `INCODA_HELD` and before any child starts.
- Go's `MkdirAll` on a path that exists and is not a directory returns `ENOTDIR` without touching
  the path. Old waiters caught mid-wait by the fence fail closed on their next poll (`Position`
  reads `queues/K`).

So once `<state>/queues` is a regular file, every released binary, on every key, with or without
`INCODA_HELD`, exits 122 with `incoda: cannot create state directory <state>: mkdir <state>/queues:
not a directory` before it can enroll or run anything. The new binary never creates `queues/`. The
file's content is a constant compiled into the new binary (install hint included, never read from
config or the environment):

```
This state directory is managed by incoda >= 0.7 (system pools).
Lanes now live in lanes/. An older incoda stops here with "not a directory"
(exit 122) on purpose: it does not know about the machine-wide pools.
Upgrade it: brew upgrade incoda, or the install script at
https://github.com/deblasis/incoda#install (SHA256SUMS-verified).
Do not delete this file: that lets old binaries run jobs outside the pools.
```

Consequences, stated:

- Old `status` and `watch` exit 122 instead of misreporting; `incoda doctor` explains (5.5). An
  older incoda earlier on PATH (for example a package-manager install at v0.5.1) must be upgraded
  first; migration warns about it (3.3 M0).
- Re-fence, only on a migrated layout (`machine.json` valid). Before migration a `queues/` directory
  is the expected state and only migration (3.3) places the fence. Every command that takes a ticket
  or writes config (`run`, `config`, `link`, `init`, `pools add|remove`) `lstat`s `<state>/queues`
  (one syscall); any regular file counts as the fence, whatever its content. If it is missing or a
  directory, the command takes machine.lock before it holds any ticket, re-checks under the lock
  (another process may have re-fenced already), moves a `queues/` directory to
  `<state>/strays/<unix-nanos>/`, re-places the file with the M4 race rule loop (an old binary's
  `MkdirAll` can recreate `queues/` between the move and the placement) and logs `event=refence`. `status`, `watch`,
  `queues`, `kill` and `doctor` never re-fence and never take machine.lock; they report a missing
  fence (5.3, 5.5).
- Unpooled old runs are counted (careless `rm` of the fence is in scope; deliberate deletion is
  not). A live ticket under `strays/<n>/<K>/`, or under `queues/<K>/` while the fence is missing, is
  an old run no pool admitted. It is probed like any ticket (2.6 step 2, under that directory's
  `registry.lock`, never creating files). Every new-binary acquisition counts each such holder as one
  held slot on: the pools linked from `lanes/<K>` if K is a linked project, pool K if K is a pool,
  and every pool otherwise (unknown or unlinked key). An orphan record (3.2) counts the same way.
  New runs therefore wait instead of starting beside it, and their busy line names it `unpooled run
  by an older incoda: pid N, key K`. A run whose parent chain contains a counted stray pid exits 120
  `upgrade-blocked:` at once instead of waiting for its own ancestor (2.6 self-wait). A re-fence
  deletes every `strays/<n>/<K>/` whose tickets are all dead, under its registry lock, appending its
  log fragment to `lanes/<K>/lane.log` when that exists; `doctor` and any acquisition poll that
  holds no other lock delete fully dead stray dirs the same way, so stray dirs whose last ticket died
  after a re-fence do not linger. Cost: one `ReadDir` of `strays/` and of `orphans/` per acquisition poll. Residual (section 7):
  runs that already held their pools when the old run started overlap it, because the old binary
  waits for nobody.
- No `chflags uchg` on the fence: it is macOS only (Linux `chattr +i` needs root), it makes
  legitimate removal of a state directory (uninstall, test harnesses) fail with "Operation not
  permitted", and counting strays already contains the careless case.

### 2.4 Lane set, total order, per-lane acquisition

A run's lane set is the named keys plus, for each named project key, the pools it uses (its whole
link, or the `--pool` subset, 4.2), deduplicated. A top-level `--queue` may mix project and pool
keys (`--queue builds,kungfoo-gate`). The total order is:

1. project lanes, sorted by key (byte order, as `sort.Strings` today);
2. then pools, sorted by key.

Lanes are joined one at a time: a lane is enrolled only after the previous one is acquired, as
run.go does today. Enroll-all-then-wait is forbidden: a ticket enrolled but not acquired still
blocks later arrivals (FIFO), which reintroduces cycles. One `--wait` budget, measured from process
start, covers machine.lock waits, migration waits and every lane.

Flag scope:

- `--slots` applies only to named project keys. A pool ticket always carries the pool's configured
  `slots`. A `--slots` value on a run that names a pool directly is checked against the pool's
  configuration with today's disagreement rule (refused if different), never written onto it.
- `--exclusive` on a project lane stays exclusive within that lane and does not propagate to pools.

Hold-and-wait costs, stated: a run holds its project lanes while waiting for pools. For a lane used
by one project that delays only that project; for a shared project key (for example `compiles`) it
delays everyone who uses that key. A run linked to two pools holds the first while waiting for the
second, which idles part of that cap; multi-key runs already state this.

This changes today's purely alphabetical order only for runs that mix kinds.

### 2.5 Plan, enroll, verify

The order depends on kind and links, which can change while a run is in flight. Rules:

- Kind changes (3.4) happen only when the lane has no ticket at all, checked under that lane's
  registry lock in the same hold that writes `machine.json`. Enrollment happens under the same
  registry lock. So every lane a run holds or waits on has a frozen kind while the run's ticket
  exists.
- Verify points: after each enroll, before waiting on that lane, and once more after the last lane
  is acquired, before the child starts (the final verify). At each, the run re-reads `machine.json`
  and its named project keys' configs, and replans if any of these holds:
  1. `generation` changed and a lane in the plan changed kind or left the registry;
  2. a named project's link changed (beyond the run's `--pool` subset); configs are re-read
     regardless of `generation`, since links live in `config.json`;
  3. for a quiet plan (2.8), `machine.json` `pools` differs from the set the plan used.
  A replan releases every ticket the run holds, prints `incoda: replan: <what changed>` and plans
  again from scratch. Replans share the one `--wait` budget and lose FIFO position; each is logged
  `event=replan`.
- After the final verify, later link or registry changes do not affect the running job (stated
  exception in 2.8).
- `closed` is re-checked after each enroll and on every poll while waiting (Enroll checks it under
  the registry lock; the poll re-reads `config.json`). A lane that becomes closed while a run waits
  on it, including a pool, ends that run: it releases everything and exits 120 with `incoda:
  closed-while-waiting: "<key>": <closed text>`.
- Every linked pool must resolve, at plan time and at each verify, to a key in `machine.json`
  `pools` with a readable `config.json`. Otherwise exit 122, `incoda: machine-state: queue
  "<project>" links "<pool>": <reason>`. The lane set never shrinks silently.

Deadlock argument for top-level runs: kinds of every lane a run holds or waits on are frozen, so all
runs agree on the relative order of those lanes, and each run waits only on a lane after everything
it holds. Nested runs and the full argument are in 2.6.

### 2.6 Nested runs

`INCODA_HELD` now carries ticket identity: comma-separated `KEY=TICKET` entries, where `TICKET` is
the ticket file name, which already embeds the holder pid (`<20-digit arrival ns>-<pid>.ticket`).
Keys never contain `=` or `,` (ValidateKey). Example:
`INCODA_HELD=cap-gate=00001727853243000000-4711.ticket,tests=00001727853243100000-4711.ticket`.
This is a format change of a documented interface (release notes, section 10).

Verification, done once at the start of a nested run, before planning:

1. Parse. KEY must pass `ValidateKey` and TICKET must match the ticket-name grammar
   (`parseTicketName`: digits, `-`, digits, `.ticket`, no path separator), both checked before any
   filesystem access. A bare key (the pre-0.7 form) or any other shape is `malformed`.
2. Liveness. Open `lanes/<KEY>/registry.lock` (the lock Enroll uses) without `O_CREATE` and take
   it, open `lanes/<KEY>/<TICKET>` without `O_CREATE` (a missing lane directory, registry lock or
   ticket is `dead`), `TryLock` it: success means `dead` (unlock and close at once), refusal means
   live. Release the registry lock. The probe never creates or removes a file and never keeps a
   lock. The live entries form the set L.
3. Ancestry, Unix: the pid in the ticket name equals the payload `pid`, and it is on this process's
   parent chain, walked from `getppid()` up to pid 1, at most 64 hops (macOS `e_ppid` from
   `sysctl(CTL_KERN, KERN_PROC, KERN_PROC_PID, pid)`; Linux field 4 of `/proc/<pid>/stat`, parsed
   after the last `)`). Live entries that pass form the set P. Windows: P = L, because incoda's child
   runs in a job object with `KILL_ON_JOB_CLOSE` and no breakaway, so no process carrying the
   variable outlives the holder.

L decides ordering and the process group; P decides pass-through.

- A dead or malformed entry holds nothing and is dropped entirely: logged `event=held-dropped key=K
  ticket=T why=dead|malformed` in that lane's `lane.log` (values `%q`, 4.6), and printed `incoda:
  held-dropped: K (<why>)` unless `--quiet`.

Verification reads `INCODA_HELD` from the environment incoda was started with, captured once at
process start before anything mutates the process environment; incoda never calls `os.Setenv` on
itself (see "Process group, Unix").
- A live entry outside P (`why=not-ancestor`, or `unverifiable` when the ancestry lookup itself
  errors, for example a sandbox without `/proc`) is not passed through but still counts for
  ordering. Printed `incoda: held-dropped: K (<why>; still counts for ordering)`.

Ordering rule. Let m be the largest lane in L in the total order (none when L is empty, which makes
the run a top-level run). For each lane X of the run's set, in total order:

- X in P: pass through, no ticket.
- L empty, or X sorts after m: normal blocking acquisition (2.4).
- Otherwise (X sorts at or before m and is not in P; this includes a lane named by a live
  non-ancestor entry): non-blocking acquisition.

All non-blocking lanes sort before every blocking one, so they are attempted first.

Non-blocking acquisition of X, under X's registry lock in one hold: run every Enroll check (closed,
require_reason, and on a configured lane the slots-disagreement refusal); scan as Enroll does
(reaping dead tickets); the new ticket's position is last, because every ticket is created under
this lock with a later arrival stamp; apply today's admission rule to the live set plus the
would-be ticket (its `slots` and `exclusive`), exactly as `Position()` would see it after Enroll:
position below the effective slots (the minimum rule counts the new ticket's `slots`), a live
exclusive ticket narrowing the lane to 1, `--exclusive` needing zero other live tickets. If
admitted, create and lock the ticket in the same hold, stamped acquired, and keep it. If not,
create nothing, release the registry lock, release every ticket this run took, and refuse. A ticket
therefore exists only when it holds a slot: no other run can observe it as a waiter, so it never
enters anyone's FIFO. Implementations that reuse Enroll must unlink the ticket before releasing the
registry lock, which is equivalent.

Refusal, exit 120:

```
incoda: out-of-order-busy: "kungfoo-gate" is busy (held by pid 5120) and sorts before held "builds"
incoda: a nested run takes a lane that sorts before a held one only if it is free right now; waiting for it could deadlock.
incoda: run the outer job with every lane named at the top level instead:
incoda:   cd '/src/kungfoo' && incoda run --queue builds,kungfoo-gate --reason 'kungfoo gate' --wait '30m' -- 'just' 'gate'
```

The rerun is built from the root run (the outermost entry of P, 2.7): `--queue` is every key in P
that some ancestor named at top level (project keys, and pools whose ticket has empty `via`) plus
this run's named keys, deduplicated, in total order. Pools held through a link come along through
that link; a `--pool` subset of the outer run is not repeated, so the rerun over-holds (the safe
direction). Flags and quoting follow "Fix lines" below.

- P empty (the lane counts only because of a live non-ancestor or unverifiable entry): no rerun;
  `incoda: this run inherited INCODA_HELD from pid N, which it cannot verify as its ancestor; run it
  outside that environment or after pid N finishes`.
- Sibling holder: if the busy ticket's `root` (2.7) equals this run's root, the holder is a parallel
  step of the same outer run, and a rerun would let the steps overlap. No rerun line is printed:
  `incoda: out-of-order-busy: "kungfoo-gate" is held by pid 5121, a parallel step of the same outer
  run (pid 4711); run those steps one after another in the recipe, or ask the user`.

Fix lines. These rules apply to every printed rerun or link command (2.6, 3.2, 3.3, 4.1, 4.2).

- Unix, POSIX sh: `incoda`, its flag names and keys print bare (keys pass `ValidateKey`: letters,
  digits, `-`, `_`, `.`). Every other value (cwd, reason, owner, wait, each word of the command
  argv, stored as `[]string`) is one single-quoted word, each `'` inside written `'\''`. A cwd that
  differs from this process's adds `cd '<cwd>' && ` in front, so a failed `cd` never runs the
  command.
- Windows, PowerShell: every value is one single-quoted word with each `'` doubled, `--` prints as
  `'--'`, and a different cwd wraps the command as `Set-Location -LiteralPath '<cwd>'; if ($?) {
  incoda ... }`, so a failed `Set-Location` (missing or renamed directory, a non-terminating error)
  never runs the command in the paster's directory. cmd.exe syntax is never printed (the docs say
  so).
- Backslash. A backslash is literal inside POSIX and PowerShell single quotes, so it is printed
  as-is inside the quoted word. The escaper's backslash rule (4.6) applies to display text, never to
  the quoted arguments of a fix line; fix-line words are emitted raw inside their quotes and never
  re-escaped.
- Recorded cwd. An empty recorded cwd (the outer run could not read its directory) omits the `cd` or
  `Set-Location` part and the line is preceded by `incoda: the outer job's directory is unknown; run
  this from it`; a non-empty cwd that is not absolute gives no runnable line. Tickets record `cwd`
  only when `os.Getwd` succeeds (2.7).
- Carried flags for a rebuilt outer command: `--reason` from the root ticket, else this run's
  `--reason`; `--owner` and `--wait` from the root ticket; `--exclusive` if any ticket of the root
  run is exclusive; `--quiet-machine` if its pool tickets carry `quiet`. A top-level fix line (4.1)
  repeats every flag the caller passed except `--queue` and `--pool`, which the line sets.
- No placeholders. If the root payload is unreadable, any value contains a character of the
  escaper's control, bidi or invalid-UTF-8 classes (4.6; the backslash is not in this test), a lane
  in the line requires a reason and none is known, or, on Windows, a value contains `"`, a value is
  empty, or a value contains a space or tab and ends in `\` (Windows PowerShell 5.1 strips embedded
  double quotes, drops empty arguments, and re-quotes a trailing-backslash argument as `"...\"`,
  which the native program reads as an escaped quote, when calling a native program), no runnable
  line is printed. Instead: `incoda: no runnable command (<why>); rerun the outer job
  with these fields:` and one escaped line per field (`queue:`, `flags:`, `reason:`, `cwd:`,
  `command:`).

Outer trailer. Every refusal (exit 120 or 122) of a run with L non-empty, `unlinked:` and
`self-wait:` included, writes its prefix and fix lines into `<root ticket>.nested` in the root's lane
directory, under that lane's registry lock, scan then write as `incoda kill` writes a `.kill`
request (so it lands only on a live ticket). Self-wait has no `INCODA_HELD`; it writes to the `root`
recorded in the waited-on ancestor's ticket. With P empty and no such ancestor ticket the refusal is
printed only locally. Each refusal appends one entry to `.nested` (never overwrites). The root run
reads the file before Release and prints, as its last stderr lines whatever its child's exit status
(the exit code stays the child's) and even with `--quiet`, `incoda: nested-refused: a nested incoda
was refused (<prefix>) on "<key>"[; N more refusals]` followed by every fix line of the first entry,
or, when it has none, its ask-the-user line (for `unlinked:` without a suggestion, `ask the user;
they run: incoda link <key>`). So an agent sees the action even
when a wrapper hides exit 120 (`make` exit 2, `-` prefixes, `|| true`). Release, scan reaping and
the `.kill` orphan sweep (`reapKillFiles`) remove `.nested` files too.

Why this is deadlock-free. For a process R let v(R) be the largest lane among those R holds and
those named by entries of R's L that are live now. m, computed once at start, is at least the L
part of v(R), because liveness is monotone (ticket names are unique; an entry that dies never comes
back). Waits-for edges are of three kinds:

1. Holder edge: R waits on lane X held by S. R blocks only on lanes after m (and after its own
   earlier lanes by the total order), so X > v(R); S holds X, so v(S) >= X > v(R).
2. FIFO edge: R waits on X behind an earlier waiter W on X. W also waits on X; following earlier
   waiters by arrival order (a strict order, so acyclic) ends at a holder S of X. The segment from R
   to S goes from v(R) < X to v(S) >= X: a strict increase.
3. Descendant edge: R waits for a descendant C (its child, or any process that received R's
   environment). C's `INCODA_HELD` is R's live entries plus R's own tickets (below). Every one of
   them that is live now was live when C verified it, so v(C) >= v(R).

Non-blocking acquisitions add no edge from the acquirer (it never waits on them) and no FIFO edge for
others (a ticket exists only while holding). Along any cycle, v never decreases and strictly
increases on every lane segment; a cycle of descendant edges alone is impossible (the process tree
is acyclic). Contradiction. This covers parallel siblings and live non-ancestor entries. Two cases
sit outside it and are cut by self-wait: `INCODA_HELD` that did not reach C (an env-scrubbing
wrapper), and stray holders (2.3), which hold no real lane and never wait on a new-binary lane, so
their only edges are descendant edges into new runs.

Self-wait. While waiting on a lane, on Unix, the run checks each holder's pid, counted stray holders
and orphan records (2.3, 3.2) included, against its own parent chain (computed once). A holder that
is an ancestor means `INCODA_HELD` did not reach this process (`env -i`, a scrubbing wrapper). The
run releases everything and exits 120: `incoda: self-wait: "<key>" is held by ancestor pid N;
INCODA_HELD did not reach this process`, then `incoda: run it without the wrapper that clears the
environment, or name its lanes in the outer run`. An ancestor that is a stray gives `upgrade-blocked:`
(3.3 M2 text) instead. Residual (section 7): an indirect cycle through a scrubbed environment lasts until
`--wait` (permanent with negative `--wait`); Windows has no self-wait check.

Passing down. The child gets `INCODA_HELD` = L plus this run's own tickets, set only in the child's
environment (`cmd.Env`). Dead and malformed entries are not passed.

Process group, Unix. This change fixes a shipped bug (specified after design review; verified by
the process-group tests of section 9). From v0.3.0 to main, `run` sets `INCODA_HELD` on its own
environment (`os.Setenv`, internal/cli/run.go:283) before `newSupervisor`
(internal/child/child_unix.go:30) tests `os.Getenv("INCODA_HELD") == ""` to decide `Setpgid`. The
test never sees it empty, so no run since v0.3.0 has put its child in its own process group:
`incoda kill` ends the direct child, grandchildren keep running, and the lane reads free
(reproduced against v0.3.0, v0.4.0, v0.5.1 and v0.6.0). The new binary decides the group from the
verified L, computed from the environment incoda was started with (captured before any mutation),
and passes the child's `INCODA_HELD` only through `cmd.Env`; it never calls `os.Setenv` on itself.

Rule: the child opens its own process group if and only if L is empty, or this process's group
(`getpgrp()`) differs from the `pgid` recorded in every live L entry's ticket (2.7), that is, no
live outer incoda will tree-kill the group this process sits in; otherwise it stays in the group it
inherited. ("incoda leads its own group" alone is not a sound test: a nested incoda that is the
direct child of an outer incoda leads the outer job's group G.) Cases:

- Every entry dead or malformed (a stale variable in a shell profile). L is empty, so the run is a
  top-level run, its child leads its own group, and `incoda kill` on it reaches the tree.
- Entries live but not ancestors (`sh -c 'watcher &'` inside the outer job; the watcher is
  reparented to pid 1 but stays in the outer group G, then runs incoda). Its group equals the `pgid`
  of the outer ticket, so its child stays in G and the outer run's tree kill, `kill(-G, SIGKILL)`,
  ends it with the outer job.
- Live entries, but this process left the outer group (a `set -m` job started with `&`, `setsid`, a
  multiplexer server started under a run, a live entry leaked from another session). No outer
  tree kill reaches it, so its child opens its own group and its own `incoda kill` reaches the tree.

Cost, pre-existing and documented: a nested run that stays in the outer group and is killed through
its own ticket ends only its direct child (section 7).

Pass-through scope. A lane in P covers the whole subtree. In particular a pool reached through a
held project link covers nested `incoda run --queue builds -- ...` steps: a recipe that fans out two
such steps in parallel runs them both under the one outer ticket (an accepted cost, section 7). Queueing them would self-wait, and the outer ticket already reserves the pool for that
subtree, as today's same-key pass-through does. Sub-steps that name their own project lane still
exclude each other: such a lane sorts before any held pool, so it is taken non-blocking, and a
second concurrent sub-step on a full lane is refused (sibling holder, above) rather than queued.

`--exclusive` on a pass-through lane is ignored with `incoda: exclusive-ignored: "K" is held by an
ancestor; its ticket decides` (today's behaviour, now stated).

Held-lost. The nested run's kill watcher re-probes its pass-through entries on each poll (step 2).
If one becomes dead while the child runs (a detached nested run outliving its ancestor), it logs
`event=held-lost`, prints `incoda: held-lost: K`, and status shows the run as `pass-through lost`.
The job is not stopped (section 7).

### 2.7 Ticket payload additions

A pool ticket records `via: ["polymatto-gate"]` (the run's named project keys that link to that
pool; empty for direct use) and `quiet: true` for a quiet-machine ticket. Status and watch group
pool holders by `via`. Every ticket also records `wait` (the run's `--wait` as given) and `root`:
the `KEY=TICKET` of the first ticket of the plan the top-level run's child starts under. A replan
(2.5) releases the old tickets, so `root` is re-set on every replan before the child starts; that
ticket is then held until the root's Release. A run with P empty is its own root; a nested run
copies `root` from the payload of its outermost P entry (the holder furthest up its parent chain; on
Windows the earliest arrival). Fix lines, the outer trailer and the sibling check (2.6) read these
fields.

On Unix every ticket also records `pgid`, the process group its child runs in: for a run that stays
in its inherited group, `getpgrp()`, written at Enroll; for a run that opens a group, the child's
pid, written under the registry lock right after the child starts. A nested run that finds a live
entry without `pgid` re-reads it at `--poll` within its `--wait` budget, and stays in its inherited
group if the budget runs out (the pre-existing behaviour). `cwd` is recorded only when `os.Getwd`
succeeds, else empty (2.6 fix lines).

### 2.8 Quiet machine

`run --quiet-machine`, or `quiet_machine: true` in a project lane's config (set with `incoda config
KEY --quiet-machine` and cleared with `--quiet-machine=false`), enrolls an exclusive `quiet` ticket
on every pool in `machine.json` at plan time, one at a time in the total order (2.4). The name
avoids the existing `run --quiet` (suppress chatter). Unless `--quiet`, a run that took it from
config prints `incoda: quiet-machine (from config of "kungfoo-measure"): holding builds; waiting
tests`, so the caller knows to use a short `--wait`.

- Cost (accepted, section 7): while a later pool drains, the quiet run holds every
  earlier pool exclusively, blocking all work there for up to `--wait`, and on timeout (121) it ran
  nothing. Docs advise a short `--wait` (5m) and a retry, or a quiet time. Watch shows `⏸ quiet:
  holding builds, computer-use; waiting tests`.
- Exceptions to "drains everything", stated: a run that passed its final verify before a link was
  added holds no ticket on the newly linked pool; a pool added after the quiet run's final verify is
  not covered (one added before it triggers a replan, 2.5 trigger 3); work outside incoda is never
  covered.
- Nested: a nested quiet run (flag or config) is refused with exit 120, `incoda: quiet-nested: an
  ancestor holds pools without quiet-machine; run the measurement at top level`, unless P contains a
  `quiet` ticket on every pool in `machine.json`, in which case it passes through all of them.

## 3. Migration, bootstrap and the machine lock

### 3.1 machine.lock

`<state>/machine.lock` is a lockfile (same package) that no released binary knows about. It is taken
for: migration and its recovery, links, kind changes, `init` steps, re-fencing, registry rebuild. It
is never held while waiting for user input.

- Acquisition is `TryLock` plus poll at `--poll`, inside the caller's `--wait` budget but never less
  than 2s (a `--wait 0` run still gets 2s for this internal lock, whose normal holds last
  milliseconds); never the blocking `Lock()`. Waiters are not FIFO (section 7).
- The holder writes a note into the lock file (`Truncate`): `pid=N op=<op> since=<time>`, and in
  migration also `blockers=<pid>:<key>,...` (the live old holders found by M2 or M5). Waiters re-read
  the note on every poll. They print `incoda: waiting for machine.lock: pid N <op> since <time>`
  once, then every 60s, listing blockers when present. On budget expiry: exit 121, `incoda:
  machine-lock-timeout: held by pid N (<op>)`.
- Upgrade-blocked waiters. A waiter (Unix) whose parent chain contains a listed blocker pid exits
  120 at once with the `upgrade-blocked:` message of M2, without waiting. This breaks the cycle
  "old outer run waits for its new-binary child, the child waits for machine.lock, the migrator
  waits for the old run".
- The migrator keeps machine.lock across its idle wait. While `machine.json` is absent no
  new-binary command can do useful work anyway (every mutating command needs the migration;
  read-only commands, `kill`, `doctor`, `version` and `help` never take machine.lock), so holding it
  stalls nothing that could otherwise run, and waiters get the blocker list from the note.
- Lock order: machine.lock, then registry locks (several only in key order, 3.6), then ticket locks
  (non-blocking only). No path takes machine.lock while holding a registry lock or a ticket. A run
  needs machine.lock only for migration, a first link (4.2) or a re-fence (2.3), all before it holds
  any ticket; replans release all tickets first. After migration, no machine.lock holder waits on a
  ticket.
- An interrupt (exit 130) or a kill at any point of a machine.lock section leaves one of the states
  in the 3.3 recovery table; the kernel frees the lock with the process.

### 3.2 When migration runs

Mutating commands (`run`, `config`, `link`, `init`, `pools add|remove`) migrate when `machine.json`
is absent. Read-only commands (`status`, `watch`, `queues`) on an unmigrated layout show it read
only and never mutate. Their banner reads the machine.lock note: `state upgrade in progress by pid N
since T; waiting for older runs: builds pid 4711, kungfoo-ui pid 5120` when a migration holds it,
else `state not upgraded yet: the next mutating incoda command upgrades it`. `doctor` reports and
never migrates.

Clearing old holders that block migration is done with `incoda kill`, never with force-release:

- `kill` works on the unmigrated layout and addresses the layout it finds: before the fence it writes
  the request under `queues/<K>/`, the path old binaries poll (v0.3.0 and later acknowledge it;
  older ones need `--force`). After the fence (M5) an old holder polls `queues/<K>/...`, which is
  now `ENOTDIR`, so no request can reach it: for a target under `strays/`, or under `lanes/` while
  `machine.json` is absent, the new `kill` skips the request and, with `--force`, ends the holder
  and its job (below); without `--force` it exits 120 with `incoda: kill: pid N is an older incoda
  that cannot see kill requests; add --force`.
- `force-release --live` deletes a live ticket while its job keeps running, which empties the idle
  check and lets the upgrade overlap that job. The new binary therefore refuses it while
  `machine.json` is absent: exit 120, `incoda: upgrade-pending: force-release --live would hide a
  running job from the upgrade; ask the user before stopping another session's job; they can run:`
  followed by one `incoda kill --queue K --pid N --reason 'incoda upgrade'` line per live ticket
  (quoting as 2.6 fix lines; a real reason, no placeholder). Plain `force-release` (dead
  tickets only) still works. An old binary's own `force-release --live` before the fence cannot be
  detected (section 7).

Old-holder kill. An old-layout holder is a ticket under `queues/`, under `strays/`, or under
`lanes/` while `machine.json` is absent. Today's `kill --force` sends SIGKILL to the incoda pid only
(`proc.Terminate`). Only v0.1.x and v0.2.0 put a top-level run's child in its own group; from v0.3.0
on the child shares the old incoda's group (the `Setenv` bug of 2.6), so neither a group kill nor a
child kill reaches the grandchildren, and the job survives with ppid 1 while the lane reads free
(reproduced for v0.3.0 to v0.6.0). When the new binary force-ends an old-layout holder (pre-fence
after the request went unacknowledged within `--wait`, or post-fence at once), on Unix it:

0. refuses, exit 120, if the old incoda is on kill's own parent chain: `incoda: kill: pid N is an
   ancestor of this process; run kill from outside its job`;
1. sends SIGSTOP to the old incoda, so it cannot start or reap a child meanwhile;
2. re-checks the target: `TryLock` of the ticket file alone (no registry lock). Still locked means
   pid N is still the holder (ticket descriptors are close-on-exec); acquired means it is not: unlock,
   SIGCONT and abort with exit 120 `incoda: kill: pid N no longer holds "<key>"`;
3. walks the old incoda's full descendant tree: list every process (macOS `sysctl(CTL_KERN,
   KERN_PROC, KERN_PROC_ALL)`, `e_ppid`, `e_pgid` and start time; Linux every `/proc/<pid>/stat`,
   fields 4, 5 and 22), collect the transitive descendants of the old incoda by parent pid, SIGSTOP
   each newly found one, and repeat the listing until a pass finds no new descendant (a stopped
   process cannot fork, so the walk converges). Kill's own pid and its ancestors are excluded from
   the walk and from every signal;
4. writes `<state>/orphans/<old pid>-<unix-nanos>.orphan` (temp plus rename; key, old pid, command,
   and every descendant's pid and start time, plus each group G whose leader is a descendant and
   which contains neither kill nor any of its ancestors) before any terminating signal; old binaries
   never read `orphans/`;
5. sends SIGKILL to each recorded group, to each recorded descendant after re-checking its start
   time, and then to the old incoda. The job is frozen, so a graceful SIGTERM would buy nothing,
   and a resumed job could fork outside the walk.

The window from step 1 to the SIGKILL of the old incoda in step 5 is protected: kill ignores SIGINT,
SIGTERM and SIGHUP, takes no registry lock, and on every abort path (failed re-check, failed listing,
failed record write, any other error) sends SIGCONT to the old incoda and to every descendant it
stopped before exiting. Every idle check (M2, M5) and every stray count (2.3) treats each record as
a live holder on its key while any recorded descendant exists with its recorded start time or any
recorded group has members (`kill(-G, 0)` returning anything but ESRCH, EPERM included: macOS
returns EPERM for a zombie-only group), then deletes it. So the lane stays busy until the tree is
empty. Plain `force-release` also deletes stale records and `doctor` lists every record. If the
listing fails (no `/proc`, sysctl refused), kill sends SIGCONT and terminates nothing: exit 120,
`incoda: kill: cannot list the job of older incoda pid N (<error>); stop its job by hand, then
rerun`, so it never reports a lane free while the job runs.

`status` and `doctor` read the process state of every live holder (the same sysctl or `/proc`
read) and flag one in state `T` as `stopped holder: pid N; a kill was interrupted; rerun: incoda
kill --queue K --pid N --reason 'resume interrupted kill' --force` (SIGSTOP of a stopped process is a no-op, so the
rerun is idempotent), or `kill -CONT N` to resume it. Residual (section 7): a SIGKILL of kill itself inside
the window, where the listing fails, and descendants that left the tree (reparented to pid 1 in
another group) before the kill. Windows needs only the termination: every tag v0.1.0 to v0.6.0 creates
its child suspended and assigns it to a job object with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` before
it runs (internal/child/child_windows.go, newSupervisor and afterStart, verified with `git show` per
tag), so the kernel ends the whole tree when the old incoda dies.

### 3.3 The migration transaction

All steps after M0 run under machine.lock. Each step is idempotent and keyed on what exists, so
recovery is "take machine.lock and resume". Every ticket probe in M2 and M5 is the 2.6 step 2
probe: under that lane's `registry.lock` (old binaries lock the same inode, so this serialises with
their Enroll), ticket opened without `O_CREATE`, nothing created or removed.

- M0. PATH check, before taking the lock, once per process. For each directory on PATH holding an
  executable `incoda` that is not the same file as this binary (`os.SameFile` after resolving
  symlinks), print `incoda: upgrade-warning: another incoda at <path>; if it is older than 0.7 it
  will stop with "not a directory" after this upgrade (incoda doctor shows its version)`. Nothing is
  executed; it is not a refusal.
- M1. Re-check: if `machine.json` is valid, another process won; release and continue with its
  registry. A leftover `queues.new` file next to a `queues/` directory is deleted.
- M2. Idle check (whenever `queues/` is a directory): for each `queues/<K>/`, probe its tickets;
  an orphan record (3.2) is live until its recorded tree is empty. If any is live, write the
  blockers into the lock note, wait holding machine.lock, re-probe every `--poll`, and print once:

  ```
  incoda: upgrade-wait: state upgrade waits for 2 run(s) by an older incoda:
  incoda:   builds pid 4711: zig build -Denable-llvm
  incoda:   kungfoo-ui pid 5120: just ui
  incoda: ask the user before stopping another session's job; they can run:
  incoda:   incoda kill --queue builds --pid 4711 --reason 'incoda upgrade'
  incoda:   incoda kill --queue kungfoo-ui --pid 5120 --reason 'incoda upgrade'
  incoda: do not force-release them: the job keeps running and the upgrade would overlap it.
  ```

  Budget expiry: exit 121, `incoda: upgrade-timeout: ...` with the same list and `upgrade the older
  incoda on PATH; see incoda doctor`. If a live holder is an ancestor of this process, exit 120 at
  once: `incoda: upgrade-blocked: an older incoda (pid N, an ancestor of this process) holds
  "<key>"; rerun the outer command after it exits`. Old binaries can keep starting runs during this
  wait; continuous old traffic keeps the new binary timing out, and the remedy is upgrading the PATH
  binary (section 7).
- M3. Plan, recomputed every time this step is reached (a plan from before a crash is never reused):
  read every lane's config now; write `<state>/migration.json` (temp plus rename): target layout 2,
  the bootstrap pools (3.5), and the lanes whose `config.json` is unreadable or malformed. No links.
- M4. Fence. Write the constant to `<state>/queues.new`. Where an atomic exchange exists (Linux
  `renameat2(RENAME_EXCHANGE)`, macOS `renamex_np(RENAME_SWAP)`, confirmed for a file/directory pair
  on APFS), swap `queues.new` with `queues`, then rename `queues.new` (now the directory)
  to `lanes`: no instant is unfenced. On EINVAL or ENOTSUP, and on Windows, the fallback is: rename
  `queues` to `lanes`, then `queues.new` to `queues`.
  - Windows: renaming a directory with any open handle inside fails. Old binaries keep
    `registry.lock` open for a whole run, so a failure means "not idle yet": back to M2, same budget.
  - Fallback race: an old binary can `MkdirAll(queues/K)` between the two renames. Then `queues.new`
    to `queues` fails and `lstat(queues)` shows a directory (decided by `lstat`, not by errno: APFS
    returns EEXIST): rename that directory to
    `<state>/strays/<unix-nanos>` and retry, at most 100 times, then exit 122 `incoda:
    machine-state: cannot place the queues fence`.
  - Empty or fresh state directory (no `queues` at all): create `lanes/`, then rename `queues.new` to
    `queues` with the same fallback race rule (an old binary's first run may have created `queues/`
    meanwhile; it goes to `strays/` and M5 waits for it). M2 has nothing to check. The fence is in
    place before `machine.json` exists, as on every path.
- M5. Sweep. Probe every ticket in `lanes/` and `strays/`. A live one belongs to an old binary that
  slipped in between M2 and M4 (a key created after the M2 listing, a ticket opened just before the
  swap, the fallback race, or a first run on an empty directory), and an orphan record (3.2) is
  live until its recorded tree is empty. Wait for it as in M2, blockers in the note; the stop line,
  under the same ask-the-user text, is `incoda kill --queue K --pid N --reason 'incoda upgrade'
  --force` (3.2, which also ends its job). Because
  `machine.json` is still absent, no new-binary run can start, so the slipped run never overlaps
  pooled work.
- M6. Merge strays: each `strays/<n>/<K>` whose `lanes/<K>` does not exist is renamed into `lanes/`;
  the rest hold only dead tickets and a log fragment: the fragment is appended to `lanes/<K>/lane.log`
  and the directory deleted, so `strays/` ends empty.
- M7. Apply: register the bootstrap pools. For a pool key with no lane, write a new `config.json`
  (schema 2, `slots: 1`). For an existing lane with a readable config, upgrade it to schema 2 keeping
  `description`, `slots`, `closed` and `require_reason` as read now, at M7, not from the plan. A
  lane with a malformed config is never rewritten: it was moved as-is with its directory, and if its
  key is a bootstrap pool it is registered as a pool with the file left in place, so runs through it
  exit 122 naming the file, exactly as runs on that lane do today, until a human fixes or deletes
  it (a missing pool config reads as `slots: 1`). Other lanes and other runs are unaffected; the
  migration never fails on a malformed lane config. All lanes are idle here, so a busy `builds` is
  never left half-converted.
- M8. Commit: write `machine.json` (temp plus rename, `generation` 1), then delete `migration.json`.
  Release machine.lock. Print once to stderr:

  ```
  incoda: migrated: pools builds, computer-use, tests, vm; 15 queues need a link before they run again
  incoda: ask the user to run incoda init: it shows each queue's suggested pools and asks (11 have one)
  ```

  (counts as for the example machine of section 6), plus `incoda: N queue(s) have an unreadable
  config.json: see incoda doctor` when N is not zero,

  and log `event=migrate` in each touched lane's `lane.log`.

Crash recovery (any later new-binary mutating command, under machine.lock):

| state found | meaning | action |
|---|---|---|
| `queues/` dir, no `migration.json`, no `lanes/` | not started | M1 onwards |
| `queues/` dir, `migration.json`, no `lanes/` | crashed in M3 or M4 before the fence | M2, M3 (plan recomputed), M4 |
| `queues` file, `queues.new` dir | crashed between swap and rename | rename `queues.new` to `lanes`, then M5 |
| `lanes/`, `migration.json`, no `queues` entry or a `queues/` dir | fallback or empty-dir path crashed before the fence was placed | place the fence (M4 race rule), then M5 |
| `migration.json` only (no `queues`, no `lanes/`) | empty-dir path crashed after M3 | M1 onwards (empty-dir path) |
| `queues` file, no `lanes/`, no `machine.json` | not produced by any step order (fence follows `lanes/`) | create `lanes/`, then M5 |
| `queues` file, `lanes/`, `migration.json` | crashed in M5 to M7 | resume at M5 |
| valid `machine.json` and `migration.json` | crashed after commit | delete `migration.json` |
| `lanes/`, no `machine.json`, no `migration.json` | registry lost (not a crash state) | place the fence if missing (M4 race rule), then fail closed, exit 122; see 3.6 |

Residual, fallback platforms only: after a crash between the two renames, old binaries can run in a
fresh `queues/` until any new binary runs recovery; M5 then waits for them. With the atomic exchange
this window does not exist.

### 3.4 Kind changes after migration

- `incoda pools add NAME --slots N --description TEXT`: creates a pool, or converts an existing
  project lane named NAME. Conversion requires the lane to have no ticket (checked under its
  registry lock, held across the `machine.json` write), a readable config and no `pools` link of its
  own. Refusal: exit 120, `incoda: kind-busy: "NAME" has live tickets` or `... links pools; unlink it
  first`, or exit 122 `machine-state:` for an unreadable config.
- `incoda pools remove NAME`: the pool becomes a project lane with no link (so runs on it then hit
  the unlinked refusal). Refused while any lane links it (`incoda: pool-linked: "NAME" is linked
  from cap-gate, kungfoo-gate`) or while it has a ticket (`kind-busy`).
- Both run under machine.lock, then the lane's registry lock, and bump `generation`. Links are also
  written under machine.lock (4.4), so "no lane links it" cannot race a new link.

### 3.5 Bootstrap content and suggestions

Bootstrap is part of migration (or of first use on an empty state dir, the empty-dir path of M4).
It is non-interactive, takes milliseconds once the lanes are idle, and registers `builds`, `tests`,
`computer-use`, `vm`, each `slots: 1` unless an existing lane of that name sets `slots`. It links
nothing: every existing project lane refuses with `incoda: unlinked:` and its suggestion (4.1)
until linked (an accepted cost, section 7).

Suggestions come from the key's name:

| pattern | suggested link | extra |
|---|---|---|
| `*-gate`, `*-test`, `*-tests` | tests | |
| `*-build`, `compiles` | builds | |
| `*-ui`, `*-desktop` | computer-use | |
| `*-e2e` | computer-use, tests | |
| `*-measure` | tests | `quiet_machine: true` |

They appear in the unlinked refusal (4.1), `incoda init --print`, the interactive `incoda init`
(preselected), `status --tree` and watch (the unlinked branch), and `status --json` (5.2). `incoda
init --apply-suggestions` (the user's) applies every one of them in one step (4.3). `run` never
applies a suggestion on its own; a `run --pool` set equal to it, and only such a set, becomes the
first link (4.2). A suggestion is the only link an agent may make, because it comes from a fixed,
documented table rather than from the agent's guess.

### 3.6 Fail-closed registry

- `machine.json` unreadable or malformed: exit 122 for every mutating and run command (`incoda:
  machine-state: machine.json: <error>`); `status` and `watch` print the same line and exit 122;
  `doctor` explains.
- `lanes/` present without `machine.json` and without `migration.json` (a human deleted it): exit
  122 the same way. Nothing re-bootstraps, because that could recreate pools a human removed or
  convert project lanes.
- `incoda doctor --rebuild-registry builds,tests,...` (a human act, section 10) writes a new `machine.json`
  with exactly the pools named, under machine.lock, in this order: every name must pass
  `ValidateKey`; a named key whose `config.json` carries `pools` is refused (`kind-busy: "K" links
  pools`); then it takes every lane's registry lock in key order, refuses with `kind-busy:` if any
  lane has a live ticket (a rebuild may change any lane's kind, and runs that acquired before the
  loss may still be running), prints each lane's resulting kind, writes `machine.json` with `generation` set to the current
  Unix time in nanoseconds (so it differs from any earlier value), and releases the locks.
- A malformed `config.json` of one lane affects only runs whose lane set includes that lane (exit
  122 naming it), as today; it never changes what other runs believe about pools.

## 4. Linking and configuration

### 4.1 `run` never prompts

`run` never reads the terminal, TTY or not, nested or not. On a project lane with no link, after the
closed check (a closed lane is refused for being closed first) and before any ticket, it exits 120.
The check reads `lanes/<key>/config.json` without opening the lane, so the refusal creates no
directory, ticket or log (a typo leaves nothing behind). With a suggestion (3.5):

```
incoda: unlinked: wintty-gate
incoda: queue "wintty-gate" is not linked to any pool; every project queue names the machine-wide pools its jobs use.
incoda: pools on this machine:
incoda:   builds        1 slot   heavy compiler/toolchain builds (LLVM links, toolchain rebuilds)
incoda:   computer-use  1 slot   drives the desktop, a browser or a dev server
incoda:   tests         1 slot   test suites and gates
incoda:   vm            1 slot   the VM host
incoda: suggested: tests (name matches *-gate)
incoda: to link it to the suggestion (stored; every later run on this queue takes these pools):
incoda:   incoda run --queue wintty-gate --pool tests --reason 'wintty gate' -- 'just' 'gate'
incoda: if the suggestion does not fit, ask the user; they run: incoda link wintty-gate
```

The `run` line follows the fix-line rules of 2.6 (quoting, every caller flag except `--queue` and
`--pool`, no placeholders). Where the suggestion carries `quiet_machine`, the line's first link
sets it too and the line says so; if the caller gave no `--wait`, that line also adds `--wait '5m'`
and says why (quiet-machine holds drained pools while it waits, 2.8). With no pattern match there is
no runnable line:

```
incoda: unlinked: polymatto
incoda: queue "polymatto" is not linked to any pool; ...
incoda: pools on this machine: ...
incoda: suggested: none (no name pattern matches)
incoda: ask the user which pools this queue's jobs use; they run: incoda link polymatto
incoda: (incoda init links every queue in one pass)
incoda: do not pick a pool yourself, and never because it is free.
```

A multi-key `--queue` with several unlinked keys prints one `incoda config KEY --pool <suggestion>`
line per key that has a suggestion, then the run line without `--pool`; any key without one makes
the whole block ask the user.

Nested (L non-empty): the refusal also goes to the outer trailer (2.6), and its fix is to link, then
rerun the outer job, since running the inner step alone would skip the rest of the recipe. With a
suggestion it prints `incoda config kungfoo-gate --pool tests` (a first link equal to the
suggestion; with `--quiet-machine` when the suggestion carries it) and then the rebuilt outer command
of 2.6; without one, only the ask-the-user lines.
Descriptions are escaped (4.6) and truncated to 60 columns.

### 4.2 `--pool` on `run`

`--pool a,b` (alias `--pools`) is a set: order-insensitive, duplicates ignored, every name must be a
registered pool. It applies to every named project key. With no named project key it is refused:
`incoda: pool-mismatch: --pool needs a project key; "builds" is a pool`, then `incoda: rerun
without --pool:` and that line. Next to a project key, a named pool key ignores it with `incoda:
--pool ignored for pool key "builds"`.

- Unlinked key: `run` makes a first link only when the set equals the key's suggestion (3.5).
  Otherwise exit 120, before any lock or ticket:
  `incoda: link-needs-user: "cap-e2e" suggests computer-use,tests; a first link from run must equal
  it`, then the run line with the suggestion and `ask the user for anything else; they run: incoda
  link cap-e2e`. A key with no suggestion gets `incoda: link-needs-user: "polymatto" has no
  suggested pools; ask the user; they run: incoda link polymatto` and no runnable line. An allowed
  first link is a compare-and-set under machine.lock then the key's registry lock, before any
  ticket: reload the config; if still unlinked, write `pools` (and `quiet_machine` when the
  suggestion has it), log `event=link by=run old= new=<set>` and print `incoda: linked: KEY -> <set>
  (stored; every later run on KEY takes these pools)`; if now linked to the same set, proceed; if
  linked to a different set, refuse exit 120, `incoda: link-conflict: "KEY" was just linked to
  tests by pid N; rerun without --pool`.
- Linked key: the set must be a non-empty subset of the link, and the run takes only those pools.
  Otherwise exit 120:

  ```
  incoda: pool-mismatch: "polymatto" is linked to builds,computer-use,tests; --pool vm is not part of it
  incoda: to also hold vm for this run only, name it next to the queue (no link change):
  incoda:   incoda run --queue polymatto,vm --reason 'prod build' -- 'pnpm' 'build'
  incoda: changing the link is the user's call; ask them.
  ```

  Without `--pool` the run takes the whole link (over-holding is the safe default).
- Multi-key `--queue a,b --pool tests`: each named project key is handled by the two rules above
  independently; if any key would be refused, the whole run is refused before anything is written or
  enrolled.

### 4.3 Setup commands

Agents may make one kind of link write: a first link equal to the printed suggestion, by running
the printed line (`run --pool`, or for the nested case `config KEY --pool`). Every other link write
is the user's, and the AGENT-RULE.md text (section 10) says so: `config --pool` with anything but the
suggestion, `--replace`, `--add-pool`, `--remove-pool`, `--unlink`, `link`, `init` and
`init --apply-suggestions`. This is documentation, not enforcement, except for `run` (4.2).

- `incoda link KEY`: interactive multi-select of pools (descriptions shown, the suggestion
  preselected). Needs a terminal on stdin and stderr; otherwise exit 120 `incoda: needs-terminal:
  ask the user to run incoda link KEY in a terminal`. No lock is held while the picker is open. On
  confirm it takes machine.lock and the registry lock and writes the link only if the link is still
  the value it showed; otherwise it shows the new value and asks again. An empty selection writes
  nothing and exits 120.
- `incoda config KEY --pool a,b` sets the link of an unlinked key; `--add-pool` on an unlinked key
  is the same as `--pool`. On a linked key `--pool` is refused unless `--replace` is given: exit
  120, `incoda: link-exists: "KEY" is linked to builds,tests; changing a link is the user's call:
  ask them`. A `--pool S` (or `--add-pool` whose pools the link already contains) on a key already
  linked to S is a no-op: exit 0, `incoda: already linked: KEY -> S`, so a concurrent first link to
  the same set never escalates. `--add-pool a,b` and `--remove-pool a,b` edit the set (removing the last pool is
  refused; use `--unlink`). `--unlink` removes the link, `--quiet-machine[=false]` sets the field.
  Every link change echoes `link: builds,tests -> builds,tests,vm` and logs `event=link by=config
  old=... new=...`. Links are validated against `machine.json` under machine.lock. Setting `pools`
  on a pool is refused.
- `incoda init`: migrates first if needed (non-interactive, bounded by `--wait`), then asks, holding
  no lock while asking: each pool's cap, extra pools ("any other shared resources? a VM, a printer,
  a device"), and a link for each unlinked open lane, its suggestion preselected. Each answer is
  applied in its own short step under machine.lock (plus the lane's registry lock), re-reading state
  first; if the state changed since the question, the question is shown again with the new value.
  Ctrl-C keeps the steps already applied and nothing else. Without a terminal: exit 120 `incoda:
  needs-terminal: ask the user to run incoda init in a terminal`.
- `incoda init --print` (no lock, no writes) prints each unlinked open lane with its suggestion and,
  only for a lane that has a suggestion, the exact `incoda config` command; for the rest it prints
  `ask the user; they run: incoda link KEY`. It flags lanes whose own `slots` exceed the suggested pool's
  (`kungfoo-build: slots 2 inside builds 1, effective 1`).
- `incoda init --apply-suggestions` (non-interactive, for the user) links every unlinked open lane
  that matches a pattern, sets `quiet_machine` where the table says so, each in its own step as
  above, logs `event=link by=init-suggest`, and prints one line per lane it changed (`linked
  cap-gate -> tests`, `linked kungfoo-measure -> tests, quiet_machine`), then the lanes it left
  unlinked. It never changes an existing link.
- `incoda pools` lists pools (key, slots, held, waiting, linked lanes, description); `incoda pools
  --json` gives the same as JSON with the `status --json` field names.
- `config`, `link`, `init` and `pools` gain `--wait` (default 1m) for machine.lock and migration
  waits.

### 4.4 Config writes

Every `config.json` write (config, link, run's first link, init, migration, pools add/remove) is
load, modify, store inside one hold of that lane's registry lock, fixing today's unlocked load in
`cmdConfig`. Writes that touch links or kind also hold machine.lock first. Writes are temp plus
rename and preserve unknown fields; on Windows a rename refused because a reader has the file open
is retried 10 times at 50ms, then exit 122. The same retry applies to `machine.json` and
`migration.json`.

### 4.5 Closed, reasons and description on linked runs

A pool's `closed` and `require_reason` are checked for a run that enrolls a ticket on that pool
(blocking or non-blocking) or names it directly. A pool reached only through a link and passed
through (2.6) is not re-checked: the ancestor's ticket was admitted under those rules. Named project
keys are checked as today, held or not.

- A linked run on a closed pool is refused up front with today's message plus the path: `incoda:
  queue "vm" is closed: maintenance (pool, via cap-e2e)`; a waiter refuses on its next poll (2.5).
- A pool with `require_reason` refuses linked runs without `--reason`: `incoda: queue "builds"
  requires --reason (pool, via kungfoo-build): ...`. In the example of section 6, `wintty-build` (no
  reason today) gains that requirement once linked to builds, except for nested recipe steps that
  pass through an ancestor's builds ticket.
- A pool's description never binds runs.

### 4.6 Text hygiene

- Rejected on write, exit 120 `incoda: bad-text: <field> contains control characters`: C0 and C1
  controls (including ESC, CR, LF, TAB), DEL and Unicode bidi overrides, in descriptions and closed
  texts written by `config`, `pools add` and `init`, and a 200-character cap. Pool names are keys
  and already pass `ValidateKey`.
- `run --reason` and `--owner` keep accepting any text (non-regression), as do values written by
  older binaries.
- Universal render rule: every string incoda prints that came from stored state, argv, the
  environment, another process or a file (cmd, cwd, reason, owner, description, closed text, kill
  reason, ticket and key tokens from `INCODA_HELD`, the machine.lock note, `lane.log` values, paths)
  goes through one escaper (`\xNN` / `\u{NNNN}` for the characters above, for LRM/RLM U+200E and
  U+200F, the overrides U+202A to U+202E and the isolates U+2066 to U+2069; invalid UTF-8 bytes as
  `\xNN`; a backslash as `\\`) on every human-facing output: plain and tree status, watch, every refusal, busy, holder and informational line, the kill
  notice on the killed job's stderr, `config` echo, `pools`, `init`, `doctor`. In `lane.log` such
  values are also quoted with Go `%q` when they contain a space or an escaped character, so one event
  is one line. One exception: the words of a runnable fix line are printed raw inside their single
  quotes (2.6). A fix line is printed only when no word contains a character of the control, bidi or
  invalid-UTF-8 classes, so the only character this leaves unescaped is the backslash, which is
  literal inside both quote styles. `--json` output is data, not a render: values keep their raw text
  in JSON string encoding. Fix commands are printed on their own lines, never inside a quoted
  description.

## 5. Observability

### 5.1 Exit codes and prefixes

Exit codes are unchanged. Each refusal block starts with a line carrying a stable prefix,
documented in `incoda help`, README and AGENT-RULE.md. That line is the first line attributable to
the refusal, not necessarily the first stderr line: informational lines from earlier steps
(`upgrade-warning:`, `migrated:`, `waiting for machine.lock:`, `held-dropped:`, `replan:`) can
precede it. Scripts and agents match the prefix on any line that starts with `incoda: <prefix>:`.

| prefix | exit | meaning |
|---|---|---|
| `incoda: unlinked:` | 120 | project lane has no link (4.1) |
| `incoda: link-conflict:` | 120 | lost a concurrent first link (4.2) |
| `incoda: pool-mismatch:` | 120 | `--pool` not a subset of the link, or no project key (4.2) |
| `incoda: link-exists:` | 120 | `config --pool` on a linked key without `--replace` (4.3) |
| `incoda: link-needs-user:` | 120 | `run --pool` first link that differs from the suggestion, or no suggestion (4.2) |
| `incoda: out-of-order-busy:` | 120 | nested run's earlier lane is not free right now (2.6) |
| `incoda: self-wait:` | 120 | waiting on an ancestor's lane (2.6) |
| `incoda: quiet-nested:` | 120 | nested quiet-machine without a quiet ancestor (2.8) |
| `incoda: closed-while-waiting:` | 120 | a lane closed while queued (2.5) |
| `incoda: upgrade-blocked:` | 120 | an ancestor older incoda blocks migration, or is a counted stray (2.3, 3.1, 3.3) |
| `incoda: upgrade-pending:` | 120 | `force-release --live` refused during migration (3.2) |
| `incoda: kind-busy:`, `incoda: pool-linked:` | 120 | refused kind change or rebuild (3.4, 3.6) |
| `incoda: needs-terminal:`, `incoda: bad-text:` | 120 | setup refusals |
| `incoda: machine-lock-timeout:`, `incoda: upgrade-timeout:` | 121 | budget spent before any lane |
| `incoda: machine-state:` | 122 | registry missing, malformed, newer, or a link does not resolve |

The earlier `incoda: out-of-order:` warning no longer exists. Informational lines: `incoda:
upgrade-wait:`, `incoda: upgrade-warning:`, `incoda: waiting for machine.lock:`, `incoda: replan:`,
`incoda: held-dropped:`, `incoda: held-lost:`, `incoda: exclusive-ignored:`, `incoda:
quiet-machine`, `incoda: nested-refused:`, `incoda: migrated:`, `incoda: linked:`.

Updated existing texts: the busy line names the role, `queue "tests" busy (pool, via cap-gate; 1
slot(s), 1 ahead of you), waited 2m0s`, and holder lines append `via <keys>` (or `unpooled run by an
older incoda`, 2.3); the 121 timeout line for a pool reads `queue "tests" (pool, via cap-gate) still
busy after 30m0s. Check incoda status --queue cap-gate ...`. Lines for project lanes are
byte-identical to today apart from escaping (4.6). The old out-of-order warning is gone (2.6).

### 5.2 status --json

Additive; `schema` stays 1.

- `queues[].config` gains `kind` (always emitted, from `machine.json`), `pools`, `quiet_machine`,
  `schema`. In a `config.json` file a missing `pools` means unlinked.
- `queues[].free` keeps its meaning (this lane has a free slot). New `queues[].runnable`: a default
  run on this key would acquire now (lane free, every linked pool free, nothing closed). New
  `queues[].blocked_by`: `[{"pool": "tests", "held": 1, "slots": 1, "holders": [{"pid": 4711, "via":
  ["cap-gate"]}]}]`, empty when nothing blocks; unpooled stray holders appear with `"unpooled":
  true`.
- `queues[].dir` now points under `lanes/` (release notes, section 10).
- Ticket payloads gain `via` and `quiet`.
- Top level gains `layout`, `generation`, `pools`, `unlinked` and `strays`. `pools` lists the pools
  linked from the requested keys plus requested pool keys (every pool with `--all`), each with key,
  slots, holders, waiting, `linked`; `pools[].holders` are the same entries as that pool's
  `queues[]` record. `unlinked` is machine-wide always, excludes closed lanes, and holds
  `{"key": "cap-gate", "suggested": ["tests"]}` entries. `strays` lists live unpooled holders.

### 5.3 status (plain text)

Existing lines are byte-identical, so scrapers keep working. Two optional lines are added inside a
project lane's block, after the description, only when it is linked:

```
queue "polymatto-gate": FREE  (1 slot(s))
  polymatto unit suites (vitest, ~7min, 140 files). ...
  pools: tests 1/1 held, builds 0/1
  blocked by pool "tests": pid 4711 via cap-gate, pid 5120 (direct)
  waiting: none
```

`pools:` is one comma-separated line; there is one `blocked by` line per blocking pool, its holders
comma-separated. An unlinked lane gets `  unlinked; suggested: tests`. Live strays add a warning
block at the end: `unpooled run by an older incoda: pid N, key K`; a missing fence on a migrated
layout adds `fence missing: the next run re-places it (incoda doctor)`; a live holder in process
state `T` adds the `stopped holder:` line of 3.2. The grouped tree is `incoda
status --tree` (new output, no compatibility promise) and the interactive watch. `watch --plain`
stays the plain status.

### 5.4 watch: collapsible pool tree

```
POOL / QUEUE              HELD  SLOTS   STATE                      WHO
builds                    1/1   ▰       held
  ├ (direct)              1/1   ●       held 6m12s                 zig build · selfhost
  ├ compiles              0/1   ·       idle
  └ wintty-build          0/1   ·       idle
tests                     1/1   ▰       held · 1 waiting
  ├ cap-gate              1/1   ●       held 3m40s                 cargo test · Cap
  ├ kungfoo-gate          1/1   ◐       holds lane, #1 for tests   cargo test · kungfoo
  └ cap-e2e ↗cu           0/1   ·       idle
▸ computer-use            0/1   ▱       free · 3 queues idle
▸ vm                      0/1   ▱       free · no queues linked
unlinked                                 3 lanes need a link: polymatto, test (suggested: none), ...
```

- `(direct)` rows (row id `pool/<p>/direct`) appear under a pool when it has direct holders or
  waiters (empty `via`), with HELD, state and WHO like a lane row, killable under the same rules;
  `status --tree` shows them too. Default expansion counts them.
- Rows have stable ids (`pool/<p>`, `pool/<p>/q/<k>`, `pool/<p>/direct`, `unlinked/<k>`).
  Selection is the row id, kept across refreshes and toggles; if the selected row disappears,
  selection moves to its pool row, else to the row at the same index.
- Keys: space toggles the branch of the selected pool; left or `h` collapses (or moves to the parent
  pool row); right or `l` expands a collapsed pool and otherwise drills in; enter or double-click
  drills into the row's lane, pools included (a pool's screen lists its holders and waiters with
  `via`); a click on the `▸`/`▾` glyph toggles; a click elsewhere selects. A lane shown under two
  pools drills into the same project screen. That screen, and `watch` opened on `INCODA_QUEUE`, show
  the same `pools:` and `blocked by` lines as plain status (5.3).
- Waiting states: `○ waiting lane #N` (queued on its own project lane, no pool ticket yet) and `◐
  holds lane, #N for <pool>`. A pool header's waiting count counts only that pool's tickets.
- Default expansion: a pool with holders or waiters is expanded, an idle pool collapsed; a manual
  toggle wins for the session.
- HELD is numeric. SLOTS draws at most 8 glyphs, then `+N`.
- Other pools of a multi-linked lane: `↗` then each pool's abbreviation, comma separated. The
  abbreviation is the initials of its `-`/`_`-separated words (computer-use gives `cu`), or the
  first 2 characters of a one-word name (`te`, `bu`); if two pools would collide, both grow one
  character at a time. Above 12 columns the list becomes `↗+N`.
- Narrow terminals: below 100 columns WHO is dropped, below 80 durations shorten to the largest unit
  (`6m`), below 60 the tree indent is 1 column and names are truncated with `…`; the existing
  40-column floor stays.
- Quiet runs show `⏸ quiet: holding <pools>; waiting <pool>` on every pool row they touch. Unlinked
  lanes are a red branch at the bottom with their suggestions; live strays are a red `unpooled
  (older incoda)` branch.
- Kill: the queue screen's kill keys (`k`, and its force key) also work on the overview's lane, pool
  and `(direct)` rows. The confirm prompt shows pid, owner, command, `via` and every lane that run
  holds; from a pool row with several holders, a picker lists them with the same fields first. The
  kill targets that run by pid through all its tickets (unchanged mechanism). AGENT-RULE.md adds: a
  pool holder whose `via` is another project is not yours to kill.

### 5.5 doctor

Reports: layout and `machine.json` state (including a newer `schema`/`layout`), any unfinished
migration step, `strays/` content, orphan records (3.2) and live unpooled holders, every `incoda` on PATH with its version
(doctor executes `<path> version` with stdin from /dev/null in its own process group, kills the
group at 5s, sets `cmd.WaitDelay` and reads at most 4 KiB of output; flags < 0.7, and
gives an `attention:` line for a `dev` or unparseable version or a timeout), the `queues` fence, holders in stopped state (3.2), lanes with an unreadable
`config.json`, unlinked lanes with their suggestions, and `INCODA_DIR` when set (it splits pools
across state directories). Exit codes: 0 when healthy or when only attention items exist (each
printed on a line starting `attention:`), 122 when anything makes runs fail closed (registry
missing, malformed or newer, unfinished migration, fence missing).

## 6. Worked example

A machine with these queues, with the migration's action and the suggestion that `incoda init
--apply-suggestions` would apply:

| lane | before | after migration | suggestion |
|---|---|---|---|
| builds | require_reason | pool, slots 1, keeps description and require_reason | n/a |
| compiles | require_reason | unlinked | builds |
| cap-e2e | slots 1 | unlinked | computer-use, tests |
| cap-gate | slots 1 | unlinked | tests |
| cap-measure | slots 1 | unlinked | tests, quiet_machine |
| kf-migrate-smoke | closed | untouched (closed) | none needed |
| kungfoo-build | slots 2 | unlinked | builds (init flags: effective 1 until builds is raised) |
| kungfoo-gate | slots 1 | unlinked | tests |
| kungfoo-measure | slots 1 | unlinked | tests, quiet_machine |
| kungfoo-ui | slots 1 | unlinked | computer-use |
| polymatto | slots 1, all heavy work of one project | unlinked | none; the user links it (for example builds, tests, computer-use, with `--pool` subsets per job) |
| polymatto-build | closed | untouched (closed) | none needed |
| polymatto-e2e | closed | untouched (closed) | none needed |
| polymatto-gate | slots 1 | unlinked | tests |
| test | no config | unlinked | none; the user links it (or closes it; it sits next to the `tests` pool) |
| wintty-build | no config | unlinked | builds |
| wintty-desktop | no config | unlinked | computer-use |
| wintty-publish | no config | unlinked | none; the user links it |
| wintty-tooling | no config | unlinked | none; the user links it |
| tests, computer-use, vm | absent | new pools, slots 1 | n/a |

Fifteen lanes need a link; eleven have a suggestion. Order of operations: upgrade every older
`incoda` on PATH first; run any mutating `incoda` command (or `incoda init`) to migrate; review
with `incoda init --print`; apply with `incoda init --apply-suggestions` (or interactive `incoda
init`); link polymatto, test, wintty-publish and wintty-tooling with `incoda link`. `--queue builds`
works from the moment of migration; each project lane refuses `unlinked:` until its link exists.

## 7. Accepted costs and known limits

Decisions, each with its reason:

- After the upgrade every existing project lane refuses until linked. Explicit links are preferred
  over guessed automatic ones, because a wrong automatic link silently drops a cap; the user links
  every lane in one step with `incoda init` or `incoda init --apply-suggestions` (3.5), and agents
  may link only to a printed suggestion (4.3).
- Quiet-machine holds drained pools exclusively while waiting for later ones, up to `--wait` (2.8).
  Measurements need a quiet machine and are rare; the docs and the printed line advise a short
  `--wait` and a retry, and watch shows the holdings.
- Nested refusal. A nested run takes a lane that sorts before a held one only when it is free at
  that instant, because waiting for it could deadlock. A self-locking recipe wrapped in a different
  lane (for example `--queue builds` wrapping `just gate`, which takes `kungfoo-gate`) works when the
  inner lane is free and is refused `out-of-order-busy` when it is busy, where it used to wait.
  Parallel sub-steps under a pool holder that name the same full project lane: the second is
  refused instead of queued, with no rerun line (2.6 sibling holder). Deadlock freedom wins over
  convenience.
- Nested sub-steps under one outer ticket share a pool. Pass-through of a pool covers the whole
  subtree, so nested steps on that pool do not serialize among themselves (2.6). The outer ticket
  reserves the pool for its subtree, as same-key pass-through already does, and queueing the steps
  would make them wait on their own ancestor.

Known limits:

- Link rules for agents beyond `run --pool` are documentation: an agent that types `incoda config
  KEY --pool X` or `--replace` on its own is not stopped (4.3). `run` enforces the suggestion rule.
- Indirect wait cycles through an env-scrubbed nested run, and on Windows the direct self-wait and
  the upgrade-blocked waiter check (no ancestry walk), last until `--wait` (2.6, 3.1).
- A live `INCODA_HELD` entry from a non-ancestor (a detached child of a still-running holder, a
  build daemon) constrains ordering without granting pass-through: lanes at or before it are taken
  only if free (2.6).
- A detached nested run that outlives its ancestor's ticket on Unix keeps running; it is flagged
  `held-lost`, not stopped (2.6).
- A nested run that stays in the outer group (2.6) and is killed through its own ticket ends only
  its direct child (pre-existing). Its own tickets, pool tickets included, free at once, so a
  machine-wide cap can read free while that child's descendants still run.
- Old-holder kill (3.2): where processes cannot be listed (no `/proc`, sysctl refused) `kill
  --force` on an old holder refuses and the job must be stopped by hand; descendants that left the
  old incoda's tree (reparented to pid 1 in another group) before the kill are not reached; a
  SIGKILL of `kill` itself between its SIGSTOP and its SIGKILL of the old incoda leaves that holder
  stopped, still holding its lane (the safe direction), flagged `stopped holder` by status and
  doctor, and recovered by rerunning the same `kill --force`.
- Continuous old-binary traffic starves migration; the fix is upgrading the old binary (3.3 M2).
  Migration makes every new-binary mutating command wait while an old run finishes (one-time,
  messaged, bounded).
- An old binary's own `force-release --live` before the fence hides its job from the idle check
  (3.2).
- Fallback platforms only: a crash between the two renames lets old binaries run until the next
  new-binary command recovers (3.3).
- After a careless fence deletion, an old run that starts overlaps runs that already held their
  pools; new runs wait for it (2.3). Deliberate deletion is out of scope.
- A replan loses FIFO position (2.5). machine.lock waiters are not FIFO (3.1). `--wait 0` can wait
  up to 2s for machine.lock (3.1).
- Keys are compared by byte order, but APFS is case-insensitive by default, so case variants of one
  key name one directory at two positions in the order; two top-level runs naming case variants in
  opposite order can wait on each other until `--wait`. Pre-existing; pools make it no worse.

## 8. Out of scope

- An MCP server exposing status, watch, queues, kill and config: a separate follow-up project built
  on this model.
- Pools of pools; memory-based admission; cross-machine coordination.
- Deliberately malicious local users (a hand-set `INCODA_HELD`, edited state files, wrong links on
  purpose).
- Separate state directories via `INCODA_DIR` or `sudo`: pools are per state directory (the docs
  say so; doctor flags `INCODA_DIR`).

## 9. Testing

Unit and integration tests. Four mechanisms were specified after design review and are verified by
the tests named here: the process-group fix and the old-holder descendant walk (2.6, 3.2), the
protected SIGSTOP window (3.2), backslash and PowerShell handling in fix lines (2.6), and `root`
across a replan (2.7).

- Ordering and planning: total order; replan on kind, link and (quiet) pool-set change, including
  at the final verify; `root` re-set on a replan before the child starts, so a nested refusal
  after a replan still reaches the trailer; closed-while-waiting.
- Nested acquisition: non-blocking acquisition (free: taken; busy: `out-of-order-busy`, no ticket
  ever visible to a concurrent scanner, fix line holds a superset of the refused tree's lanes and
  carries exclusive, quiet, owner and wait, uses the outer command, keeps directly held pools;
  sibling holder prints no rerun; slots disagreement and the would-be ticket's slots counted);
  live non-ancestor entry counted; `INCODA_HELD` verification (dead, malformed and path-like
  tokens, non-ancestor, detached child, probe creates no file, missing lane directory); self-wait,
  including a stray ancestor; quiet-nested; held-lost.
- Fix-line reproduction: `sh -c` and PowerShell against a stub `incoda` reproduce the exact argv and
  cwd for values with `'`, `"`, `;`, `$()`, a space, a backtick and a backslash (`a\b`,
  `grep 'a\.b'`); on POSIX a Windows-style path with a space and a trailing `\` and an empty word
  reproduce exactly too, while on Windows both give no runnable line; a TAB gives no runnable line; a
  nonexistent cwd never runs the command (`cd ... &&` on POSIX, `if ($?)` on PowerShell); an empty
  recorded cwd prints the line without the `cd` part.
- Outer trailer: through `make` and through `|| true` (exit 0), for `unlinked:` and for self-wait;
  printed under `--quiet`; two refusals in one tree append two entries and the trailer shows the
  first with every fix line and the count.
- Process group (regression tests for the shipped bug): a top-level run's child has pgid equal to
  its pid; kill a holder whose child spawns a grandchild (`sh -c 'sleep 60 & wait'`): both die and
  the lane frees only after both are gone; `INCODA_HELD` never appears in incoda's own
  environment; all entries dead gives an own group; a reparented nested run with live entries
  stays in the outer group and dies with the outer tree kill; a live-entry run outside the outer
  group (a `set -m` job started with `&`) opens its own group and its own kill reaches its grandchild.
- Links: first-link race; `run --pool` first link equal to the suggestion only
  (`link-needs-user:` otherwise); `--pool` subset and multi-key; `config --pool` link guard and
  the `already linked:` no-op; `init --print` prints no config line for a lane without a
  suggestion.
- Migration with live old tickets: wait, timeout, ancestor, blocked waiter on machine.lock, kill
  before and after the fence, force-release --live refused.
- Old-holder kill: `kill --force` on a v0.6.0 holder whose child spawns a grandchild
  (`sh -c 'sleep 60 & wait'`) and on a v0.2.0 holder with a `sleep 60` child leaves no orphan
  (every descendant is gone before kill exits 0, the orphan record keeps the lane busy until
  then); a nested v0.6.0 holder's outer group and the outer job are not signalled; kill of an
  ancestor refused; kill run inside a process group it must not signal leaves that group alone;
  the target released its ticket between the liveness check and SIGSTOP: SIGCONT and abort;
  SIGINT and SIGTERM to kill inside the window are ignored and kill completes; a failed record
  write sends SIGCONT to the old incoda and every stopped descendant; a holder left in state `T`
  is shown as `stopped holder` by status and doctor, and rerunning `kill --force` recovers it.
- Layout: re-fence never runs on an unmigrated layout; crash injection at M3 to M8 plus every
  recovery row; the fallback race; concurrent old and new first run on an empty dir; malformed
  lane config during migration; v0.6.0 and v0.2.0 binaries against a migrated dir (exit 122, no
  ticket written); fence deletion with a live stray counted; malformed, missing and newer
  `machine.json`; rebuild-registry checks.
- Text: control-character rejection and an ESC in a holder's argv escaped on a waiter's busy
  line; doctor's version probe bounded for a probe whose child holds stdout open.

## 10. Deliverables

- Code: the model, migration, linking, observability and kill changes above.
- Tests: section 9.
- README and docs/DESIGN.md: plain-text promise restated as "existing lines unchanged, lines may be
  added"; retire "a nested run on a different key queues as usual".
- AGENT-RULE.md: prefixes; rewrite the bullet "a recipe that takes its own lane is safe to wrap in `run` on the
  same key" to "wrapping in the same key passes through; a recipe that takes its own project lane
  must not be wrapped in a different lane or a pool: run it bare, or name every lane at the top
  level (`--queue builds,kungfoo-gate`); wrap heavy work with your project key, whose link brings
  the pools; on `out-of-order-busy:` or `nested-refused:` run the printed command if one is
  printed, otherwise ask the user; do not retry in a loop"; the `INCODA_QUEUE` interaction (an
  `INCODA_QUEUE` project lane inside a `--queue builds` wrapper is the same case); the general
  order rule (project lanes sort before pools, pools sort builds < computer-use < tests < vm; a
  nested lane that sorts before a held one is taken only if free, so `--queue kungfoo-gate` wrapping
  a recipe that runs `incoda run --queue builds` is refused whenever builds is busy; name all
  lanes at the top level); match prefixes on any `incoda:` line (5.1); the kill clause, including
  "stopping another session's job, `upgrade-wait:` lines included, is the user's call"; "never
  pick a pool yourself, never because it is free, and never pass `--pool` to avoid a busy pool";
  links: "you may make a first link only to the suggestion incoda printed, by running the printed
  line; a lane with no suggestion, any other pool set, and every change to an existing link
  (`config --pool` other than the printed one, `--replace`, `--add-pool`, `--remove-pool`,
  `--unlink`, `link`, `init`, `init --apply-suggestions`) are the user's: ask them"; other
  human-only items to surface rather than run (`pools add|remove`, `doctor --rebuild-registry`,
  `upgrade-*` refusals, force-release); fix lines are POSIX sh on Unix and PowerShell on Windows.
- The demo tape and gif, re-recorded.
- Release notes: upgrade the PATH binary first; old `status` and `watch` exit 122; existing lanes
  refuse until linked; nested refusal (a nested lane that sorts before a held one is non-blocking, so
  a wrapped self-locking recipe is refused when its inner lane is busy); the `INCODA_HELD` format
  changed from keys to `KEY=TICKET`; `status --json` `queues[].dir` now under `lanes/`; the
  process-group fix (since v0.3.0 `kill` left grandchildren running while the lane read free).
