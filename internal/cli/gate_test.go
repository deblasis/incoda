package cli

import (
	"context"
	"io"
	"testing"
	"time"

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

func TestGateFlagValidation(t *testing.T) {
	t.Setenv("INCODA_DIR", t.TempDir())
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"idle-for without max-cpu", []string{"run", "--queue", "k", "--idle-for", "10s", "--", "true"}},
		{"max-cpu zero", []string{"run", "--queue", "k", "--max-cpu", "0", "--", "true"}},
		{"max-cpu over 100", []string{"run", "--queue", "k", "--max-cpu", "101", "--", "true"}},
		{"negative idle-for", []string{"run", "--queue", "k", "--max-cpu", "30", "--idle-for", "-5s", "--", "true"}},
		{"dur exceeds wait", []string{"run", "--queue", "k", "--wait", "30s", "--max-cpu", "30", "--idle-for", "2m", "--", "true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code := Main(tc.args, io.Discard, io.Discard); code != ExitUsage {
				t.Fatalf("got exit %d, want usage refusal (%d)", code, ExitUsage)
			}
		})
	}
}
