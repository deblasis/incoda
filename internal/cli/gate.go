package cli

import (
	"context"
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
	// Sleep waits between polls. Nil means a real wait honoring ctx and the
	// deadline; tests inject a fake that advances a fake clock.
	Sleep func(d time.Duration) error
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
//
// A spent deadline admits no sample when IdleFor > 0 (instant ErrTimeout),
// but with IdleFor 0 the first sample still runs: one below-threshold sample
// passes even at the deadline.
func waitForIdle(ctx context.Context, cfg GateConfig, sample func() sysinfo.CPU, now func() time.Time, killed func() (lane.KillRequest, bool), check func() error, onWait func(cpu float64, waited time.Duration), deadline time.Time) (GatePassed, error) {
	if cfg.Poll <= 0 {
		cfg.Poll = 500 * time.Millisecond
	}
	sleep := cfg.Sleep
	if sleep == nil {
		sleep = func(d time.Duration) error { return sleepCtx(ctx, d, now, deadline) }
	}
	start := now()
	var windowStart time.Time
	var haveWindow bool
	errs := 0
	samples := 0
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
		if !deadline.IsZero() && !now().Before(deadline) && (cfg.IdleFor != 0 || samples != 0) {
			return GatePassed{}, lane.ErrTimeout
		}
		c := sample()
		samples++
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
			if err := sleep(cfg.Poll); err != nil {
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
		if err := sleep(cfg.Poll); err != nil {
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
