# Load-gated start (`--max-cpu` / `--idle-for`) — design

Date: 2026-10-05. Status: approved. Nineplus design review: round 1 scores
5/5/7/4 (min 4); all IN-scope classes closed below; ledger (outside repo):
`~/.nineplus/incoda/load-gate-design/`.

## Goal

`incoda run --queue KEY --max-cpu PCT --idle-for DUR -- <cmd>` starts only
when whole-machine CPU utilization is below PCT for DUR. Advisory
point-in-time admission, not a load guarantee (no cgroups, no throttle).

## Decisions (human)

- Metric: CPU utilization % via `sysinfo.ReadCPU` (linux/darwin/windows).
  Load average is not cross-platform (no Windows equivalent), so it is out.
- Scope: per-run flags. A job without them does not gate (documented
  bypass; `status` warns on mixed-gate lanes). No `--require-gate` in v1.
- Order: gate runs after the FIFO slot is held; head blocks the lane (no
  overtake); gate wait shares the one `--wait` budget (exit 121).

## Interface

- `--max-cpu PCT`: float, finite, `0 < PCT <= 100`. Else exit 120.
- `--idle-for DUR`: `waitValue` grammar (Go duration or bare seconds), `>= 0`.
  Negative, overflow, or unparseable: exit 120. `0` = one below-threshold
  sample passes.
- `--idle-for` without `--max-cpu`: exit 120. `--max-cpu` alone defaults
  `--idle-for 0`.
- `DUR > --wait` with finite `--wait`: exit 120 at parse (even zero slot
  wait could never satisfy it). Infinite `--wait` (`< 0`): gate unbounded.
  `--wait 0` with `DUR > 0`: immediate 121 without sampling (no budget for
  any wait); `DUR = 0`: exactly one sample, then run or 121.
- Help: `run -h` documents both flags, the bypass, and that 121 covers
  FIFO + gate waits. Root usage exit-code line updated.

## Mechanism

- Single gate after ALL lanes are acquired, before the final
  `plan.Changed` and child start. Not per lane. Fully re-entrant runs
  (every key inherited) skip the gate and log it; partial re-entry still
  gates once at the end.
- Gate loop predicate order per poll (same as `Acquire`): own
  `KillRequested` → multi-lane `Killed()` → `ctx.Done()` → `waitingCheck`
  (closed/newer) → CPU sample. Sleep is `select { <-ctx.Done();
  <-time.After(min(poll, remaining)) }`. Kill/close/interrupt map to the
  existing `ExitKilled`/`ExitUsage`/`ExitState` paths. Expiry returns
  `ErrTimeout` through the existing giveup path: `event=giveup` +
  `still waiting for idle (cpu>=PCT) after BUDGET`, exit 121.
- Sampling: pure `waitForIdle(ctx, cfg{MaxCPU, IdleFor, Poll, Deadline,
  Sample, Now, Killed, Check, OnGateWait})`. Production wires
  `Sample=sysinfo.ReadCPU` at one call site; tests inject fakes. Window by
  elapsed monotonic time, not sample count. Strict `<`: `>= PCT` resets
  `windowStart` to now (full reset, v1 — no leaky bucket). Pass iff
  `now - windowStart >= DUR`. Window starts at the first below-threshold
  sample after FIFO acquire, so a newcomer on an idle machine still waits
  the full DUR, and N queued gated jobs serialize N×DUR. `DUR < poll`
  costs at least one poll.
- Disclosure (help + DESIGN): polled samples, not continuous monitoring;
  sub-poll spikes are missed; first sample per process uses a ~100 ms
  baseline window, later samples average ~one poll interval; sampling is
  sparse and advisory.
- Errors: failure = `!HaveUsage || Err != ""`. A stale `lastPct` returned
  with `HaveUsage=true` counts as a valid sample. A transient failure
  pauses (sample skipped, window preserved) and retries within budget. Only
  after 5 consecutive failures — or structurally unsupported GOOS — does it
  warn loudly and proceed (`event=gate-pass reason=cpu-unavailable`),
  bypassing `--quiet` like the slots warning. Unsupported OS is documented
  as gating-disabled.
- CPU% limits documented: all-cores normalization (30% of 32 cores ≈ 9.6
  busy cores); Linux iowait counts as idle; single-core saturation reads
  ~small on many-core; no I/O or memory-pressure protection. With
  `--slots > 1` every holder gates independently (thundering herd possible;
  advisory only — recommend `slots=1` with a gate).

## State, log, status

- Ticket: additive numeric fields only (`max_cpu_pct`, `idle_for_ns`,
  `gate_start_nano`/`gate_done_nano`), `omitempty`; `effectiveSlots` and
  `SlotsDisagree` ignore them. Never raw flag strings.
- Logs (`textsafe`-escaped): `event=gate-wait` (first + every 60 s Notify,
  suppressed under `--quiet`) and `event=gate-pass` with
  `maxcpu=`/`idlefor=`/`cpu=`; expiry reuses `event=giveup`.
- `MarkAcquired` stays at FIFO acquire (`Holding` untouched); gate
  timestamps let `status`/`watch` split `held` vs `gate-wait`. `dur=`
  includes gate wait. Mixed-gate lanes get a `status` warning line.

## Tests (acceptance)

Window table (fake sampler+clock incl. reset, `DUR=0`); budget (spent
budget instant-121, `DUR>wait` parse refusal, `wait=0` single-sample);
kill/close/interrupt-during-gate within ~1 poll; transient-error-pauses
then 5-consecutive-proceed; unsupported-GOOS loud-proceed; validation
table (`-5/101/NaN/Inf`, negative/overflow DUR, bare seconds, idle-for
without max-cpu); log single-line/hostile-input; slots-2 herd
characterization; old-binary ticket compat.

## Non-goals

Queue-config default, `--require-gate`, shared cross-process idle history,
leaky-bucket/EWMA tolerance, loadavg metric, post-start throttling,
cgroups/limits, cross-machine.
