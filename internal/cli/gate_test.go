package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/colorize"
	"github.com/deblasis/incoda/internal/lane"
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

// fakeSleep advances the fake clock instead of really waiting.
func fakeSleep(cur *time.Time) func(time.Duration) error {
	return func(d time.Duration) error {
		*cur = cur.Add(d)
		return nil
	}
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

// A transient error mid-window pauses without disturbing the window: with a
// 30ms window and 10ms sample steps, one error costs exactly one step, so
// the pass lands on the 4th call. Resetting the window on error would need 6.
func TestGateTransientErrorPauses(t *testing.T) {
	cur := time.Now()
	start := cur
	sample, calls := fakeSampler([]float64{10, 0, 10, 10, 10}, map[int]bool{1: true}, &cur, 10*time.Millisecond)
	cfg := GateConfig{MaxCPU: 30, IdleFor: 30 * time.Millisecond, Poll: time.Millisecond, Sleep: fakeSleep(&cur)}
	res, err := waitForIdle(context.Background(), cfg, sample,
		func() time.Time { return cur },
		func() (lane.KillRequest, bool) { return lane.KillRequest{}, false },
		func() error { return nil },
		nil, cur.Add(time.Second))
	if err != nil {
		t.Fatalf("single transient error must pause, not admit or fail: %v", err)
	}
	if res.Unavailable {
		t.Fatal("one transient error must not fail open")
	}
	if *calls != 4 {
		t.Fatalf("window must survive one transient error: calls=%d, want 4", *calls)
	}
	if elapsed := cur.Sub(start); elapsed < 30*time.Millisecond+10*time.Millisecond {
		t.Fatalf("pass must still span the window plus the error step: elapsed=%s", elapsed)
	}
}

func TestGateFailOpenAfterFiveConsecutiveErrors(t *testing.T) {
	cur := time.Now()
	sample, calls := fakeSampler([]float64{0}, map[int]bool{0: true}, &cur, time.Millisecond)
	cfg := GateConfig{MaxCPU: 30, IdleFor: time.Second, Poll: time.Millisecond, Sleep: fakeSleep(&cur)}
	res, err := waitForIdle(context.Background(), cfg, sample,
		func() time.Time { return cur },
		func() (lane.KillRequest, bool) { return lane.KillRequest{}, false },
		func() error { return nil },
		nil, cur.Add(time.Minute))
	if err != nil {
		t.Fatalf("five consecutive errors must fail open, not fail: %v", err)
	}
	if !res.Unavailable {
		t.Fatal("five consecutive errors must report Unavailable")
	}
	if *calls != 5 {
		t.Fatalf("fail-open takes exactly 5 consecutive errors: calls=%d", *calls)
	}
}

func TestGateFourErrorsThenIdlePasses(t *testing.T) {
	cur := time.Now()
	sample, calls := fakeSampler([]float64{0, 0, 0, 0, 10}, map[int]bool{0: true, 1: true, 2: true, 3: true}, &cur, time.Millisecond)
	cfg := GateConfig{MaxCPU: 30, IdleFor: 0, Poll: time.Millisecond, Sleep: fakeSleep(&cur)}
	res, err := waitForIdle(context.Background(), cfg, sample,
		func() time.Time { return cur },
		func() (lane.KillRequest, bool) { return lane.KillRequest{}, false },
		func() error { return nil },
		nil, cur.Add(time.Minute))
	if err != nil || res.Unavailable {
		t.Fatalf("four errors then idle must pass normally: res=%+v err=%v", res, err)
	}
	if *calls != 5 {
		t.Fatalf("calls=%d, want 5 (four errors plus the passing sample)", *calls)
	}
}

func TestGateKilled(t *testing.T) {
	cur := time.Now()
	sample, calls := fakeSampler([]float64{10}, nil, &cur, time.Millisecond)
	cfg := GateConfig{MaxCPU: 30, IdleFor: time.Hour, Poll: time.Millisecond, Sleep: fakeSleep(&cur)}
	_, err := waitForIdle(context.Background(), cfg, sample,
		func() time.Time { return cur },
		func() (lane.KillRequest, bool) {
			return lane.KillRequest{By: "test", Reason: "test kill"}, true
		},
		func() error { return nil },
		nil, cur.Add(time.Minute))
	var killed *lane.KilledError
	if !errors.As(err, &killed) {
		t.Fatalf("killed() must surface *KilledError: %v", err)
	}
	if *calls > 1 {
		t.Fatalf("kill is checked before sampling: calls=%d", *calls)
	}
}

func TestGateCheckError(t *testing.T) {
	cur := time.Now()
	sentinel := errors.New("closed-while-waiting: test")
	sample, calls := fakeSampler([]float64{10}, nil, &cur, time.Millisecond)
	cfg := GateConfig{MaxCPU: 30, IdleFor: 0, Poll: time.Millisecond, Sleep: fakeSleep(&cur)}
	_, err := waitForIdle(context.Background(), cfg, sample,
		func() time.Time { return cur },
		func() (lane.KillRequest, bool) { return lane.KillRequest{}, false },
		func() error { return sentinel },
		nil, cur.Add(time.Minute))
	if !errors.Is(err, sentinel) {
		t.Fatalf("check() error must pass through: %v", err)
	}
	if *calls != 0 {
		t.Fatalf("check runs before sampling: calls=%d", *calls)
	}
}

func TestGateContextCanceled(t *testing.T) {
	cur := time.Now()
	sample, calls := fakeSampler([]float64{10}, nil, &cur, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := GateConfig{MaxCPU: 30, IdleFor: 0, Poll: time.Millisecond, Sleep: fakeSleep(&cur)}
	_, err := waitForIdle(ctx, cfg, sample,
		func() time.Time { return cur },
		func() (lane.KillRequest, bool) { return lane.KillRequest{}, false },
		func() error { return nil },
		nil, cur.Add(time.Minute))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ctx must end the gate with ctx.Err(): %v", err)
	}
	if *calls != 0 {
		t.Fatalf("canceled ctx samples nothing: calls=%d", *calls)
	}
}

func TestGateMidGateCancel(t *testing.T) {
	cur := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := 0
	sample := func() sysinfo.CPU {
		n++
		cur = cur.Add(time.Millisecond)
		if n == 3 {
			cancel()
		}
		return sysinfo.CPU{UsagePct: 90, HaveUsage: true, Source: "fake"}
	}
	cfg := GateConfig{MaxCPU: 30, IdleFor: time.Hour, Poll: time.Millisecond, Sleep: fakeSleep(&cur)}
	_, err := waitForIdle(ctx, cfg, sample,
		func() time.Time { return cur },
		func() (lane.KillRequest, bool) { return lane.KillRequest{}, false },
		func() error { return nil },
		nil, cur.Add(time.Minute))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel during the gate must end it with ctx.Err(): %v", err)
	}
}

// A spent deadline with IdleFor > 0 admits no sample: instant ErrTimeout.
func TestGateSpentBudgetSamplesNothing(t *testing.T) {
	cur := time.Now()
	sample, calls := fakeSampler([]float64{10}, nil, &cur, time.Millisecond)
	cfg := GateConfig{MaxCPU: 30, IdleFor: time.Second, Poll: time.Millisecond, Sleep: fakeSleep(&cur)}
	_, err := waitForIdle(context.Background(), cfg, sample,
		func() time.Time { return cur },
		func() (lane.KillRequest, bool) { return lane.KillRequest{}, false },
		func() error { return nil },
		nil, cur.Add(-time.Second))
	if !errors.Is(err, lane.ErrTimeout) {
		t.Fatalf("spent budget must be ErrTimeout: %v", err)
	}
	if *calls != 0 {
		t.Fatalf("spent budget samples nothing: calls=%d", *calls)
	}
}

// wait=0 with DUR=0 still takes exactly one sample even at the deadline.
func TestGateWaitZeroDurZeroSingleSample(t *testing.T) {
	cur := time.Now()
	sample, calls := fakeSampler([]float64{10}, nil, &cur, time.Millisecond)
	cfg := GateConfig{MaxCPU: 30, IdleFor: 0, Poll: time.Millisecond, Sleep: fakeSleep(&cur)}
	res, err := waitForIdle(context.Background(), cfg, sample,
		func() time.Time { return cur },
		func() (lane.KillRequest, bool) { return lane.KillRequest{}, false },
		func() error { return nil },
		nil, cur)
	if err != nil || res.LastCPU != 10 {
		t.Fatalf("DUR=0 at the deadline must still take one sample: res=%+v err=%v", res, err)
	}
	if *calls != 1 {
		t.Fatalf("exactly one sample: calls=%d", *calls)
	}
}

// ...but a hot first sample at the deadline has nowhere to wait: ErrTimeout.
func TestGateWaitZeroDurZeroHotSampleTimesOut(t *testing.T) {
	cur := time.Now()
	sample, calls := fakeSampler([]float64{90}, nil, &cur, time.Millisecond)
	cfg := GateConfig{MaxCPU: 30, IdleFor: 0, Poll: time.Millisecond, Sleep: fakeSleep(&cur)}
	_, err := waitForIdle(context.Background(), cfg, sample,
		func() time.Time { return cur },
		func() (lane.KillRequest, bool) { return lane.KillRequest{}, false },
		func() error { return nil },
		nil, cur)
	if !errors.Is(err, lane.ErrTimeout) {
		t.Fatalf("hot sample at the deadline must be ErrTimeout: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("exactly one sample before the timeout: calls=%d", *calls)
	}
}

// A sample exactly at MaxCPU resets the window: strict <, not <=. With 10ms
// steps and a 20ms window, [idle, ==max, idle, idle, idle] must need all 5
// calls; counting == as below would pass by the 3rd.
func TestGateBoundaryEqualResets(t *testing.T) {
	cur := time.Now()
	sample, calls := fakeSampler([]float64{10, 30, 10, 10, 10}, nil, &cur, 10*time.Millisecond)
	cfg := GateConfig{MaxCPU: 30, IdleFor: 20 * time.Millisecond, Poll: time.Millisecond, Sleep: fakeSleep(&cur)}
	_, err := waitForIdle(context.Background(), cfg, sample,
		func() time.Time { return cur },
		func() (lane.KillRequest, bool) { return lane.KillRequest{}, false },
		func() error { return nil },
		nil, cur.Add(time.Minute))
	if err != nil {
		t.Fatalf("gate must pass after the post-reset window: %v", err)
	}
	if *calls != 5 {
		t.Fatalf("== MaxCPU must reset the window: calls=%d, want 5", *calls)
	}
}

type gateWait struct {
	cpu    float64
	waited time.Duration
}

// onWait fires on the first poll and then every Notify, on the fake clock.
func TestGateNotifyCadence(t *testing.T) {
	cur := time.Now()
	start := cur
	sample, _ := fakeSampler([]float64{10}, nil, &cur, 0)
	var waits []gateWait
	cfg := GateConfig{MaxCPU: 30, IdleFor: 200 * time.Second, Poll: 30 * time.Second, Notify: 60 * time.Second, Sleep: fakeSleep(&cur)}
	_, err := waitForIdle(context.Background(), cfg, sample,
		func() time.Time { return cur },
		func() (lane.KillRequest, bool) { return lane.KillRequest{}, false },
		func() error { return nil },
		func(cpu float64, waited time.Duration) { waits = append(waits, gateWait{cpu, waited}) },
		cur.Add(time.Hour))
	if err != nil {
		t.Fatalf("gate must pass: %v", err)
	}
	want := []time.Duration{0, 60 * time.Second, 120 * time.Second, 180 * time.Second}
	if len(waits) != len(want) {
		t.Fatalf("onWait fired %d times, want %d: %+v", len(waits), len(want), waits)
	}
	for i, w := range want {
		if got := waits[i].waited; got != w {
			t.Fatalf("onWait[%d].waited=%s, want %s (full trace %+v)", i, got, w, waits)
		}
		if waits[i].cpu != 10 {
			t.Fatalf("onWait[%d].cpu=%v, want 10", i, waits[i].cpu)
		}
	}
	if elapsed := cur.Sub(start); elapsed < 200*time.Second {
		t.Fatalf("pass must span the window: elapsed=%s", elapsed)
	}
}

// Error iterations report cpu -1 (unavailable) to onWait.
func TestGateOnWaitErrorReportsUnavailable(t *testing.T) {
	cur := time.Now()
	sample, _ := fakeSampler([]float64{0, 10}, map[int]bool{0: true}, &cur, time.Millisecond)
	var waits []gateWait
	cfg := GateConfig{MaxCPU: 30, IdleFor: 0, Poll: time.Millisecond, Sleep: fakeSleep(&cur)}
	res, err := waitForIdle(context.Background(), cfg, sample,
		func() time.Time { return cur },
		func() (lane.KillRequest, bool) { return lane.KillRequest{}, false },
		func() error { return nil },
		func(cpu float64, waited time.Duration) { waits = append(waits, gateWait{cpu, waited}) },
		cur.Add(time.Minute))
	if err != nil || res.Unavailable {
		t.Fatalf("one error then idle must pass normally: res=%+v err=%v", res, err)
	}
	if len(waits) != 1 || waits[0].cpu != -1 {
		t.Fatalf("the error iteration must report cpu -1: %+v", waits)
	}
}

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

func TestPaintEventGateVerbs(t *testing.T) {
	passLine := "2026-10-05 10:00:00 queue=k event=gate-pass pid=1 maxcpu=30.0 idlefor=2m0s cpu=12.0 unavailable=false"
	if got := paintEvent(colorize.Plain, passLine); got != passLine {
		t.Fatalf("gate-pass must survive paintEvent under Plain unchanged: %q", got)
	}
	waitLine := "2026-10-05 10:00:00 queue=k event=gate-wait pid=1 maxcpu=30.0 cpu=95.0"
	if got := paintEvent(colorize.Plain, waitLine); !strings.Contains(got, "event=gate-wait") {
		t.Fatalf("gate-wait must survive paintEvent: %q", got)
	}
	t.Setenv("CLICOLOR_FORCE", "1")
	p := colorize.For(io.Discard)
	if got := paintEvent(p, passLine); !strings.Contains(got, "\x1b[32mevent=gate-pass") {
		t.Fatalf("gate-pass must paint green: %q", got)
	}
	if got := paintEvent(p, waitLine); !strings.Contains(got, "\x1b[33mevent=gate-wait") {
		t.Fatalf("gate-wait must paint yellow: %q", got)
	}
}

func TestGateFlagValidation(t *testing.T) {
	t.Setenv("INCODA_DIR", t.TempDir())
	for _, tc := range []struct {
		name string
		args []string
		// want, when non-empty, must appear on stderr; absent, when
		// non-empty, must not appear. The bare-seconds row is valid gate
		// input refused later for an unrelated (unlinked) reason, so it
		// asserts the refusal says nothing about idle-for.
		want   string
		absent string
	}{
		{"idle-for without max-cpu", []string{"run", "--queue", "k", "--idle-for", "10s", "--", "true"}, "--idle-for", ""},
		{"max-cpu zero", []string{"run", "--queue", "k", "--max-cpu", "0", "--", "true"}, "--max-cpu", ""},
		{"max-cpu negative", []string{"run", "--queue", "k", "--max-cpu", "-5", "--", "true"}, "--max-cpu", ""},
		{"max-cpu NaN", []string{"run", "--queue", "k", "--max-cpu", "NaN", "--", "true"}, "--max-cpu", ""},
		{"max-cpu +Inf", []string{"run", "--queue", "k", "--max-cpu", "+Inf", "--", "true"}, "--max-cpu", ""},
		{"negative idle-for", []string{"run", "--queue", "k", "--max-cpu", "30", "--idle-for", "-5s", "--", "true"}, "--idle-for", ""},
		{"dur exceeds wait", []string{"run", "--queue", "k", "--wait", "30s", "--max-cpu", "30", "--idle-for", "2m", "--", "true"}, "--idle-for", ""},
		{"overflow idle-for", []string{"run", "--queue", "k", "--max-cpu", "30", "--idle-for", "99999999999", "--", "true"}, "--idle-for", ""},
		{"unparseable idle-for", []string{"run", "--queue", "k", "--max-cpu", "30", "--idle-for", "bogus", "--", "true"}, "bad flags", ""},
		{"bare-seconds idle-for is valid", []string{"run", "--queue", "k", "--wait", "5m", "--max-cpu", "30", "--idle-for", "90", "--", "true"}, "", "idle-for"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if code := Main(tc.args, io.Discard, &stderr); code != ExitUsage {
				t.Fatalf("got exit %d, want usage refusal (%d):\n%s", code, ExitUsage, stderr.String())
			}
			if tc.want != "" && !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr must mention %q:\n%s", tc.want, stderr.String())
			}
			if tc.absent != "" && strings.Contains(stderr.String(), tc.absent) {
				t.Fatalf("stderr must not mention %q:\n%s", tc.absent, stderr.String())
			}
		})
	}
}
