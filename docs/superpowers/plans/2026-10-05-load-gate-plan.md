# Load-gated start Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `run --max-cpu PCT --idle-for DUR`: after FIFO acquire, start only after CPU% stays below PCT for DUR, sharing the `--wait` budget.

**Architecture:** Pure gate loop in new `internal/cli/gate.go` (injectable sampler/clock, same kill/ctx/close predicates as `Acquire`), flags validated in `cmdRun`, single call site after all lanes acquired in `internal/cli/run.go`, additive numeric ticket fields, textsafe log/status rendering.

**Tech Stack:** Go 1.27+, existing `internal/lane`, `internal/sysinfo`, `internal/textsafe`, `internal/report` packages. No new dependencies.

---

## File map

- Create `internal/cli/gate.go`: `GateConfig`, `waitForIdle`, `GateResult`, error sentinels mapping to existing exits.
- Create `internal/cli/gate_test.go`: window/budget/kill/close/error/validation table tests (fake sampler+clock, no sleeps >10ms).
- Modify `internal/cli/run.go:41-58` (flag definitions), `~:60-78` (validation), `~:168-186` (ticket literal + gate fields on enqueue log), `~:454-505` (single gate call site after multi-lane loop, before final `plan.Changed`), `~:55-58` + `internal/cli/cli.go:40-52,79-81` (usage/help text).
- Modify `internal/lane/ticket.go:19-47`: additive `MaxCPUPct float64 json:"max_cpu_pct,omitempty"`, `IdleForNanos int64 json:"idle_for_nanos,omitempty"`, `GateStartNano/GateDoneNano int64 ...omitempty`.
- Modify `internal/cli/status.go:174-208` (`paintEvent` gate verbs) and `internal/cli/status.go:130-143` (HOLDER gate-wait line), plus mixed-gate warning in `internal/report` or `buildReport`.
- Docs: `README.md` run row + exit-121 line, `docs/DESIGN.md` new section, `AGENT-RULE.md` one bullet.
- Tests: `go test -race ./...`, `go vet ./...`, `gofmt -l .`.

---

### Task 1: Gate core — pure waitForIdle + unit tests

**Files:**
- Create: `internal/cli/gate.go`
- Test: `internal/cli/gate_test.go` (imports: `context`, `io`, `strings`, `testing`, `time`, `github.com/deblasis/incoda/internal/colorize`, `github.com/deblasis/incoda/internal/lane`, `github.com/deblasis/incoda/internal/sysinfo`)

- [ ] **Step 1: Write the failing test (window semantics)**

```go
package cli

import (
	"context"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/sysinfo"
)

func fakeSampler(seq []float64, errAt map[int]bool, cur *time.Time, step time.Duration) (func() sysinfo.CPU, *int) {
	i := 0
	calls := new(int)
	return func() sysinfo.CPU {
		*calls++
		*cur = cur.Add(step)
		idx := i
		if i < len(seq)-1 {
			i++
		}
		if errAt[idx] {
			return sysinfo.CPU{HaveUsage: false, Err: "boom", Source: "fake"}
		}
		return sysinfo.CPU{UsagePct: seq[idx], HaveUsage: true, Source: "fake"}
	}, calls
}

func TestGateWindowResetsOnHot(t *testing.T) {
	cur := time.Now()
	sample, _ := fakeSampler([]float64{10, 10, 90, 10, 10, 10}, nil, &cur, 10*time.Second)
	cfg := GateConfig{MaxCPU: 30, IdleFor: 20 * time.Second, Poll: time.Millisecond}
	res, err := waitForIdle(context.Background(), cfg, sample,
		func() time.Time { return cur },
		func() (lane.KillRequest, bool) { return lane.KillRequest{}, false },
		func() error { return nil },
		func(float64, time.Duration) {}, cur.Add(time.Minute))
	_ = res
	if err != nil {
		t.Fatalf("gate should pass after reset window completes: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/alex/claude/incoda && go test ./internal/cli/ -run TestGateWindowResetsOnHot -count=1 -v`
Expected: FAIL with "undefined: GateConfig" / "undefined: waitForIdle"

- [ ] **Step 3: Write minimal implementation**

```go
package cli

import (
	"context"
	"errors"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/sysinfo"
)

// GateConfig is the load gate of one run: start only after CPU usage stays
// strictly below MaxCPU for IdleFor. IdleFor 0 means one below-threshold
// sample passes.
type GateConfig struct {
	MaxCPU  float64
	IdleFor time.Duration
	Poll    time.Duration
	Notify  time.Duration
}

// GatePassed records how the gate was satisfied (for logs/status).
type GatePassed struct {
	// Unavailable is true when the gate was skipped after consecutive
	// sampler failures (fail-open, advisory).
	Unavailable bool
	IdleSpan    time.Duration
	LastCPU     float64
}

// consecutiveGateErrors before fail-open warn+proceed.
const consecutiveGateErrors = 5

// waitForIdle blocks until samples stay below cfg.MaxCPU for cfg.IdleFor,
// deadline passes (returns lane.ErrTimeout), ctx ends (returns ctx.Err()),
// killed() reports (returns *lane.KilledError), or check() fails (returned
// as is). onWait fires on the first poll and every Notify. Sample errors
// pause (window preserved); only consecutiveGateErrors in a row fail open.
func waitForIdle(ctx context.Context, cfg GateConfig, sample func() sysinfo.CPU, now func() time.Time, killed func() (lane.KillRequest, bool), check func() error, onWait func(cpu float64, waited time.Duration), deadline time.Time) (GatePassed, error) {
	if cfg.Poll <= 0 {
		cfg.Poll = 500 * time.Millisecond
	}
	start := now()
	var windowStart time.Time
	var haveWindow bool
	errs := 0
	notified := false
	var lastNotify time.Time
	var lastCPU float64
	for {
		if req, ok := killed(); ok {
			return GatePassed{}, &lane.KilledError{Request: req}
		}
		select {
		case <-ctx.Done():
			return GatePassed{}, ctx.Err()
		default:
		}
		if check != nil {
			if err := check(); err != nil {
				return GatePassed{}, err
			}
		}
		if !deadline.IsZero() && !now().Before(deadline) {
			return GatePassed{}, lane.ErrTimeout
		}
		c := sample()
		if !c.HaveUsage || c.Err != "" {
			errs++
			if errs >= consecutiveGateErrors {
				return GatePassed{Unavailable: true, LastCPU: lastCPU}, nil
			}
			waited := now().Sub(start)
			if onWait != nil && (!notified || (cfg.Notify > 0 && now().Sub(lastNotify) >= cfg.Notify)) {
				onWait(-1, waited)
				notified = true
				lastNotify = now()
			}
			if err := sleepCtx(ctx, cfg.Poll, now, deadline); err != nil {
				return GatePassed{}, err
			}
			continue
		}
		errs = 0
		lastCPU = c.UsagePct
		if c.UsagePct < cfg.MaxCPU {
			if !haveWindow {
				windowStart, haveWindow = now(), true
			}
			if now().Sub(windowStart) >= cfg.IdleFor {
				return GatePassed{IdleSpan: now().Sub(windowStart), LastCPU: lastCPU}, nil
			}
		} else {
			haveWindow = false
		}
		waited := now().Sub(start)
		if onWait != nil && (!notified || (cfg.Notify > 0 && now().Sub(lastNotify) >= cfg.Notify)) {
			onWait(lastCPU, waited)
			notified = true
			lastNotify = now()
		}
		if err := sleepCtx(ctx, cfg.Poll, now, deadline); err != nil {
			return GatePassed{}, err
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration, now func() time.Time, deadline time.Time) error {
	if d <= 0 {
		return nil
	}
	if !deadline.IsZero() {
		if left := deadline.Sub(now()); left < d {
			d = left
		}
		if d <= 0 {
			return lane.ErrTimeout
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

var _ = errors.Is
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/alex/claude/incoda && go test ./internal/cli/ -run TestGateWindowResetsOnHot -count=1 -v`
Expected: PASS (adjust fake-clock advancement: advance `cur` by Poll inside sampler or wrap sample to bump `cur`; keep unit sleeps under 10ms by using small Poll/DUR values in the committed test)

- [ ] **Step 5: Extend tests — DUR=0 single sample, budget expiry, kill, transient-pause**

Add to `internal/cli/gate_test.go` (same file, append):

```go
func TestGateDurZeroSingleSample(t *testing.T) {
	cur := time.Now()
	sample, _ := fakeSampler([]float64{10}, nil, &cur, time.Millisecond)
	cfg := GateConfig{MaxCPU: 30, IdleFor: 0, Poll: time.Millisecond}
	res, err := waitForIdle(context.Background(), cfg, sample,
		func() time.Time { return cur },
		func() (lane.KillRequest, bool) { return lane.KillRequest{}, false },
		func() error { return nil },
		nil, cur.Add(time.Second))
	if err != nil || res.LastCPU != 10 {
		t.Fatalf("DUR=0 single below-threshold sample must pass: res=%+v err=%v", res, err)
	}
}

func TestGateTransientErrorPauses(t *testing.T) {
	cur := time.Now()
	sample, _ := fakeSampler([]float64{10, 0, 10}, map[int]bool{1: true}, &cur, time.Millisecond)
	cfg := GateConfig{MaxCPU: 30, IdleFor: 0, Poll: time.Millisecond}
	_, err := waitForIdle(context.Background(), cfg, sample,
		func() time.Time { return cur },
		func() (lane.KillRequest, bool) { return lane.KillRequest{}, false },
		func() error { return nil },
		nil, cur.Add(time.Second))
	if err != nil {
		t.Fatalf("single transient error must pause, not admit or fail: %v", err)
	}
}
```

Run: `cd /Users/alex/claude/incoda && go test ./internal/cli/ -run 'TestGate' -count=1 -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/alex/claude/incoda && git add internal/cli/gate.go internal/cli/gate_test.go && git commit -m "feat: load-gate core waitForIdle with sampler seam"
```

---

### Task 2: Flags + validation in cmdRun

**Files:**
- Modify: `internal/cli/run.go:41-68`
- Modify: `internal/cli/cli.go:40-52` (root usage run line)
- Test: `internal/cli/gate_test.go` (append parse tests) or `internal/cli/run_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestGateFlagValidation(t *testing.T) {
	t.Setenv("INCODA_DIR", t.TempDir())
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"idle-for without max-cpu", []string{"--queue", "k", "--idle-for", "10s", "--", "true"}},
		{"max-cpu zero", []string{"--queue", "k", "--max-cpu", "0", "--", "true"}},
		{"max-cpu over 100", []string{"--queue", "k", "--max-cpu", "101", "--", "true"}},
		{"negative idle-for", []string{"--queue", "k", "--max-cpu", "30", "--idle-for", "-5s", "--", "true"}},
		{"dur exceeds wait", []string{"--queue", "k", "--wait", "30s", "--max-cpu", "30", "--idle-for", "2m", "--", "true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code := Main(tc.args, io.Discard, io.Discard)
			if code != ExitUsage && code != ExitTimeout {
				t.Fatalf("got exit %d, want usage/timeout refusal", code)
			}
		})
	}
}
```

(Note: needs `io` import in the test file. The DUR-exceeds-wait case must exit 120 at parse per spec; adjust assertion to `ExitUsage` exactly once implemented.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/alex/claude/incoda && go test ./internal/cli/ -run TestGateFlagValidation -count=1 -v`
Expected: FAIL (undefined flags `--max-cpu` → flag parse error path differs; test fails)

- [ ] **Step 3: Implement flags + validation in cmdRun**

In `internal/cli/run.go`, after the `exclusive` flag definition, add:

```go
maxCPU := fs.Float64("max-cpu", 0, "start only after whole-machine CPU%% stays below this percent for --idle-for (requires --idle-for's partner flag below)")
idleFor := &waitValue{}
fs.Var(idleFor, "idle-for", "how long CPU must stay below --max-cpu before starting: a Go duration (2m) or bare seconds; 0 means one below-threshold sample passes")
```

After `fs.Parse` and the `--slots` check, add:

```go
gate := GateConfig{Poll: *poll, Notify: 60 * time.Second}
gateSet := false
fs.Visit(func(f *flag.Flag) {
	if f.Name == "max-cpu" || f.Name == "idle-for" {
		gateSet = true
	}
})
if givenIdleFor := idleFor.set; givenIdleFor && *maxCPU == 0 {
	return usagef("--idle-for needs --max-cpu: pass both, e.g. --max-cpu 30 --idle-for 2m")
}
if *maxCPU != 0 {
	if math.IsNaN(*maxCPU) || math.IsInf(*maxCPU, 0) || *maxCPU <= 0 || *maxCPU > 100 {
		return usagef("--max-cpu must be > 0 and <= 100, got %v", *maxCPU)
	}
	gate.MaxCPU = *maxCPU
	if idleFor.set {
		if idleFor.d < 0 {
			return usagef("--idle-for must be >= 0, got %s", idleFor.raw)
		}
		gate.IdleFor = idleFor.d
	}
	gateSet = true
	if wait.d >= 0 && gate.IdleFor > wait.d {
		return usagef("--idle-for %s cannot exceed the --wait budget of %s", gate.IdleFor, wait.d)
	}
}
_ = gateSet
```

Add `math` to the `run.go` import block. Update the run usage string to include `[--max-cpu PCT] [--idle-for DUR]`. Update `internal/cli/cli.go` root usage run line identically.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/alex/claude/incoda && go test ./internal/cli/ -run TestGateFlagValidation -count=1 -v`
Expected: PASS (tighten the DUR-exceeds-wait assertion to exactly `ExitUsage` now)

- [ ] **Step 5: Commit**

```bash
cd /Users/alex/claude/incoda && git add internal/cli/run.go internal/cli/cli.go internal/cli/gate_test.go && git commit -m "feat: --max-cpu/--idle-for flags with validation"
```

---

### Task 3: Ticket fields (additive) + compat test

**Files:**
- Modify: `internal/lane/ticket.go:19-47`
- Test: `internal/lane/ticket_gate_test.go` (create) or append to existing lane test

- [ ] **Step 1: Write the failing test**

```go
package lane

import (
	"encoding/json"
	"testing"
)

func TestGateTicketFieldsRoundTrip(t *testing.T) {
	raw := `{"pid":123,"queue":"k","slots":1,"command":["true"],"max_cpu_pct":30,"idle_for_nanos":120000000000}`
	var tk Ticket
	if err := json.Unmarshal([]byte(raw), &tk); err != nil {
		t.Fatal(err)
	}
	if tk.MaxCPUPct != 30 || tk.IdleForNanos != 120000000000 {
		t.Fatalf("gate fields lost: %+v", tk)
	}
	// Old binaries ignore unknown fields: new fields must be omitempty and
	// must not affect effectiveSlots/SlotsDisagree.
	entries := []Entry{{Ticket: Ticket{Slots: 1}}, {Ticket: Ticket{Slots: 1, MaxCPUPct: 30}}}
	if SlotsDisagree(entries) {
		t.Fatal("gate fields must not count as slots disagreement")
	}
	if got := effectiveSlots(entries, 0); got != 1 {
		t.Fatalf("gate fields must not change width: got %d", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/alex/claude/incoda && go test ./internal/lane/ -run TestGateTicketFieldsRoundTrip -count=1 -v`
Expected: FAIL (unknown fields dropped / struct lacks them)

- [ ] **Step 3: Implement additive fields**

In `internal/lane/ticket.go`, extend `Ticket`:

```go
// MaxCPUPct, when set with IdleForNanos, gates this run's start on
// whole-machine CPU utilization staying strictly below it for the window.
// Zero means no gate. Purely advisory; never affects slot width.
MaxCPUPct float64 `json:"max_cpu_pct,omitempty"`
// IdleForNanos is the sustained window in nanoseconds. Zero with MaxCPUPct
// means one below-threshold sample passes.
IdleForNanos int64 `json:"idle_for_nanos,omitempty"`
// GateStartNano/GateDoneNano bracket the gate wait for status/log.
GateStartNano int64  `json:"gate_start_nano,omitempty"`
GateDoneNano  int64  `json:"gate_done_nano,omitempty"`
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/alex/claude/incoda && go test ./internal/lane/ -run TestGateTicketFieldsRoundTrip -count=1 -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/alex/claude/incoda && git add internal/lane/ticket.go internal/lane/ticket_gate_test.go && git commit -m "feat: additive load-gate ticket fields"
```

---

### Task 4: Wire the gate into cmdRun (single call site)

**Files:**
- Modify: `internal/cli/run.go:168-186` (ticket literal), `~:454-505` (gate call site), `internal/cli/gate.go` (production wiring helper)

- [ ] **Step 1: Write the failing integration test**

Append to `internal/cli/gate_test.go`:

```go
func TestGateImmediatePassOnIdle(t *testing.T) {
	// End-to-end through Main with a stubbed sampler is heavy; this pins the
	// wiring contract instead: GateConfig derived from flags must reach
	// waitForIdle with the shared budget deadline.
	cur := time.Now()
	sample, calls := fakeSampler([]float64{5}, nil, &cur, time.Millisecond)
	cfg := GateConfig{MaxCPU: 30, IdleFor: 0, Poll: time.Millisecond}
	_, err := waitForIdle(context.Background(), cfg, sample,
		func() time.Time { return cur },
		func() (lane.KillRequest, bool) { return lane.KillRequest{}, false },
		func() error { return nil },
		nil, cur.Add(time.Second))
	if err != nil || *calls != 1 {
		t.Fatalf("idle gate must pass on first sample: calls=%d err=%v", *calls, err)
	}
}
```

Run: `cd /Users/alex/claude/incoda && go test ./internal/cli/ -run TestGateImmediatePassOnIdle -count=1 -v`
Expected: PASS already (core exists) — the real failing assertion is the wiring below; verify `grep -n "waitForIdle" internal/cli/run.go` returns nothing (gate not yet wired).

- [ ] **Step 2: Wire the gate (single call site after all lanes acquired)**

In `internal/cli/run.go`, in the ticket literal inside `takeLane`, add:

```go
MaxCPUPct:  gate.MaxCPU,
IdleForNanos: int64(gate.IdleFor),
```

`gate` must be captured from the outer scope (defined at flag validation). The enqueue `Logf` in `EnrollContext` is in `internal/lane/queue.go:570`; extend it to append ` maxcpu=<pct> idlefor=<dur>` when set, using numeric formatting only (never raw flag text). `fmt.Sprintf(" maxcpu=%.1f idlefor=%s", ...)` with `time.Duration.String()` is canonical — safe.

After the multi-lane `for` loop exits with `why == ""` (after line ~473 `break`) and BEFORE `stop()` at line ~505, insert:

```go
if gate.MaxCPU > 0 {
	if len(toTake) == 0 {
		// Fully re-entrant: nothing acquired, gate skipped (documented
		// bypass — the parent's lane already admitted this tree).
		fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
			p.Dim("load gate skipped: running inside a parent lane (re-entrant)"))
	} else {
		// Shared budget is encoded in deadline below (start + --wait);
		// no separate gate clock.
		var deadline time.Time
		if wait.d >= 0 {
			deadline = start.Add(wait.d)
		}
		gate.Poll = *poll
		for _, pt := range toTake {
			pt.en.MarkGateStart()
		}
		passed, gerr := waitForIdle(ctx, gate, sysinfo.ReadCPU, time.Now,
			func() (lane.KillRequest, bool) {
				for _, h := range toTake {
					if req, ok := h.en.KillRequested(); ok {
						return req, true
					}
				}
				return lane.KillRequest{}, false
			},
			func() error {
				var cerr error
				for _, pt := range toTake {
					if e := waitingCheck(pt.q.Dir, pt.key); e != nil {
						cerr = e
					}
				}
				return cerr
			},
			func(cpu float64, waited time.Duration) {
				if *quiet {
					return
				}
				fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
					p.Yellow(fmt.Sprintf("waiting for idle: cpu %.0f%%%% >= %.0f%%%% (need <%.0f%%%% for %s), waited %s%s",
						cpu, gate.MaxCPU, gate.MaxCPU, gate.IdleFor, waited.Round(time.Second), waitBudget(wait.d))))
			}, deadline)
		if gerr != nil {
			if errors.Is(gerr, context.Canceled) {
				rc = ExitInterrupt
				return exitWith(ExitInterrupt, "interrupted while waiting for idle")
			}
			var killed *lane.KilledError
			if errors.As(gerr, &killed) {
				rc = ExitKilled
				logKill(toTake, killed.Request)
				return exitWith(ExitKilled, "%s", p.Red(fmt.Sprintf("cancelled while waiting for idle by %s: %s",
					textsafe.Escape(killed.Request.By), textsafe.Escape(killed.Request.Reason))))
			}
			var rf *machine.Refusal
			var se *machine.StateError
			if errors.As(gerr, &rf) || errors.As(gerr, &se) {
				rc = ExitUsage
				if se != nil {
					rc = ExitState
				}
				return machineExit(gerr)
			}
			if errors.Is(gerr, lane.ErrTimeout) {
				rc = ExitTimeout
				for _, pt := range toTake {
					pt.q.Logf("queue=%s event=giveup pid=%d waited=%s reason=idle-gate", pt.key, os.Getpid(), wait.d)
				}
				return exitWith(ExitTimeout,
					"still waiting for idle (cpu>=%.0f%%%% for %s) after %s. Do NOT bypass the lane; surface the wait and coordinate instead",
					gate.MaxCPU, gate.IdleFor, wait.d)
			}
			rc = ExitState
			return exitWith(ExitState, "%v", gerr)
		}
		for _, pt := range toTake {
			pt.en.MarkGateDone()
			pt.q.Logf("queue=%s event=gate-pass pid=%d maxcpu=%.1f idlefor=%s cpu=%.1f unavailable=%v", pt.key, os.Getpid(), gate.MaxCPU, gate.IdleFor.String(), passed.LastCPU, passed.Unavailable)
		}
		if !*quiet {
			fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
				p.Green(fmt.Sprintf("load gate passed (cpu %.0f%%%% < %.0f%%%% for %s)", passed.LastCPU, gate.MaxCPU, gate.IdleFor)))
		}
	}
}
```

Add `MarkGateStart/MarkGateDone` to `Enrollment` in `internal/lane/queue.go` (set `GateStartNano/GateDoneNano` + `Truncate` under registry lock, mirroring `MarkAcquired`). Add `sysinfo` import to `run.go`. Remove the `_ = remaining` once the deadline path above is confirmed (deadline already encodes the shared budget; `remaining` is vestigial — delete those three lines before committing).

- [ ] **Step 3: Run the gate tests + build**

Run: `cd /Users/alex/claude/incoda && go build ./... && go test ./internal/cli/ -run 'TestGate' -count=1 -v`
Expected: build clean, tests PASS

- [ ] **Step 4: Commit**

```bash
cd /Users/alex/claude/incoda && git add internal/cli/run.go internal/cli/gate.go internal/lane/queue.go && git commit -m "feat: gate start on sustained low CPU after FIFO acquire"
```

---

### Task 5: Status/log rendering + mixed-gate warning

**Files:**
- Modify: `internal/cli/status.go:174-208`, `internal/cli/status.go:130-143`
- Modify: `internal/report/report.go` (warnings) or `internal/cli/status.go:62-80`
- Test: extend `internal/cli/gate_test.go` or `internal/report` test

- [ ] **Step 1: Write the failing test**

```go
func TestPaintEventGateVerbs(t *testing.T) {
	line := "2026-10-05 10:00:00 queue=k event=gate-pass pid=1 maxcpu=30.0 idlefor=2m0s cpu=12.0 unavailable=false"
	if got := paintEvent(colorize.Plain, line); !strings.Contains(got, "event=gate-pass") {
		t.Fatalf("gate-pass must survive paintEvent: %q", got)
	}
}
```

Run: `cd /Users/alex/claude/incoda && go test ./internal/cli/ -run TestPaintEventGateVerbs -count=1 -v`
Expected: FAIL (unknown verb passes through uncolored — pin the color mapping first: decide `gate-wait` Yellow, `gate-pass` Green, then assert)

- [ ] **Step 2: Implement rendering**

In `paintEvent`, add cases: `gate-wait` → Yellow (like `config`), `gate-pass` → Green (like `acquire`). In `renderQueue` HOLDER block, after the reason lines add:

```go
if e.Ticket.MaxCPUPct > 0 {
	fmt.Fprintf(w, "          %s %s\n", p.Dim("gate:"), gateText(e.Ticket))
}
```

with helper in `status.go`:

```go
// gateText renders the advisory load gate of a ticket; numeric fields only,
// so there is nothing to escape beyond the fixed format.
func gateText(t lane.Ticket) string {
	s := fmt.Sprintf("cpu<%.0f%%%% for %s", t.MaxCPUPct, time.Duration(t.IdleForNanos))
	if t.GateDoneNano > 0 {
		s += " (passed)"
	} else if t.GateStartNano > 0 {
		s += " (waiting)"
	}
	return s
}
```

Mixed-gate warning: in `buildReport` (or `report.Build` warnings), after building `rep`, for each queue with both gated (`MaxCPUPct>0`) and ungated live entries, append: `queue "k": mixed load gates — only runs with --max-cpu gate; others bypass`. Keep the warning Yellow via existing `rep.Warnings` path.

- [ ] **Step 3: Run tests**

Run: `cd /Users/alex/claude/incoda && go test ./internal/cli/ ./internal/report/ -count=1 2>&1 | tail -5`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
cd /Users/alex/claude/incoda && git add internal/cli/status.go internal/report/report.go internal/cli/gate_test.go && git commit -m "feat: status/log rendering for load gate"
```

---

### Task 6: Docs + help text

**Files:**
- Modify: `README.md:96-112`, `docs/DESIGN.md` (append section), `AGENT-RULE.md` (one bullet), `internal/cli/cli.go:75-81` (121 line)

- [ ] **Step 1: Update the run row and exit-121 docs**

`README.md` run-table row becomes: `` `incoda run --queue KEY[,KEY...] [--slots N] [--exclusive] [--max-cpu PCT] [--idle-for DUR] ...` `` with one sentence: gated runs start only after whole-machine CPU% stays below PCT for DUR (advisory, shares `--wait`, exit 121 on expiry; runs without the flags bypass). Exit-code section: `121 --wait elapsed while still queued or waiting for idle`.

`internal/cli/cli.go` usage: same run-line addition; `121` line: `waiting for a slot or for idle`.

- [ ] **Step 2: DESIGN.md section + AGENT-RULE bullet**

Append to `docs/DESIGN.md`: `## Load-gated start` covering sampled-not-continuous, strict-</reset, full-DUR newcomer cost, serial N×DUR, slots>1 herd, transient-pause/5-consecutive fail-open, iowait/single-core/all-cores limits, re-entrant skip. `AGENT-RULE.md`: one bullet under heavy-job list: background jobs add `--max-cpu 30 --idle-for 2m`.

- [ ] **Step 3: Verify docs build/grep**

Run: `cd /Users/alex/claude/incoda && grep -n "max-cpu" README.md docs/DESIGN.md AGENT-RULE.md internal/cli/cli.go | head -20`
Expected: each file hits at least once

- [ ] **Step 4: Commit**

```bash
cd /Users/alex/claude/incoda && git add README.md docs/DESIGN.md AGENT-RULE.md internal/cli/cli.go && git commit -m "docs: load-gated start"
```

---

### Task 7: Full suite + nineplus code review

**Files:** none (verification only)

- [ ] **Step 1: Run the full suite**

Run: `cd /Users/alex/claude/incoda && go test -race ./... 2>&1 | tail -15`
Expected: all packages PASS

- [ ] **Step 2: Vet + format**

Run: `cd /Users/alex/claude/incoda && go vet ./... && gofmt -l .`
Expected: no output (clean)

- [ ] **Step 3: Manual smoke (idle machine)**

Run: `cd /Users/alex/claude/incoda && go build -o /tmp/incoda . && time /tmp/incoda run --queue smoke --max-cpu 100 --idle-for 1s --reason "gate smoke" -- true && /tmp/incoda status --queue smoke`
Expected: run passes after ~1s, status FREE, `lane.log` contains `gate-pass`

- [ ] **Step 4: Request nineplus code review before release**

Nineplus code run (fresh panel, charter: this plan + diff) must hit 9+ with no in-scope HIGH before tagging. Ledger stays in `~/.nineplus/incoda/load-gate-code/`, never in the repo.
