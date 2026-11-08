package cli

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/deblasis/incoda/internal/child"
	"github.com/deblasis/incoda/internal/lane"
)

// The names a limit goes by in the lane log and in `limit=` on the release
// line.
const (
	limitMaxRun = "max-run"
	limitStall  = "stall"
)

// limitBy is who a limit's kill is attributed to: the run itself, on behalf
// of the limit it was given.
const limitBy = "incoda"

// parseLimit reads a --max-run or --stall value: a Go duration (30m) or bare
// seconds (1800). Zero means no limit; a negative value is refused, because a
// limit that silently does not apply is worse than none.
func parseLimit(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if n, err := strconv.Atoi(s); err == nil {
		if n < 0 {
			return 0, fmt.Errorf("%q is negative; use 0 for no limit", s)
		}
		if int64(n) > math.MaxInt64/int64(time.Second) {
			return 0, fmt.Errorf("%q is too large to represent", s)
		}
		return time.Duration(n) * time.Second, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%q is neither a Go duration (30m, 90s) nor a whole number of seconds", s)
	}
	if d < 0 {
		return 0, fmt.Errorf("%q is negative; use 0 for no limit", s)
	}
	return d, nil
}

// limitValue is the flag.Value of --max-run and --stall. set tells a flag
// that was given as 0 (which turns a lane default off) from one that was not
// given at all.
type limitValue struct {
	d   time.Duration
	set bool
	raw string
}

func (v *limitValue) String() string {
	if v == nil {
		return ""
	}
	if v.raw != "" {
		return v.raw
	}
	return v.d.String()
}

func (v *limitValue) Set(s string) error {
	d, err := parseLimit(s)
	if err != nil {
		return err
	}
	v.d, v.set, v.raw = d, true, strings.TrimSpace(s)
	return nil
}

// given is the value as a pointer, nil when the flag was not passed.
func (v *limitValue) given() *time.Duration {
	if !v.set {
		return nil
	}
	d := v.d
	return &d
}

// resolveLimits settles the limits of a run. A flag that was given wins,
// whatever the lanes say, and 0 turns the limit off. Without a flag the
// lanes' defaults apply, and across several held lanes the tightest non-zero
// one does: every lane the run holds is entitled to its own bound on how
// long a slot may be kept.
func resolveLimits(flagMaxRun, flagStall *time.Duration, cfgs []lane.Config) (maxRun, stall time.Duration) {
	tightest := func(flag *time.Duration, of func(lane.Config) time.Duration) time.Duration {
		if flag != nil {
			return *flag
		}
		var out time.Duration
		for _, c := range cfgs {
			if d := of(c); d > 0 && (out == 0 || d < out) {
				out = d
			}
		}
		return out
	}
	return tightest(flagMaxRun, func(c lane.Config) time.Duration { return c.MaxRun }),
		tightest(flagStall, func(c lane.Config) time.Duration { return c.Stall })
}

// stallSupported says whether this OS can read the CPU a job tree has used.
// Elsewhere --stall cannot fire, which the run says out loud.
func stallSupported(goos string) bool {
	switch goos {
	case "linux", "darwin", "windows":
		return true
	}
	return false
}

// stallClock decides when a job tree has been idle for long enough. Any
// change of the CPU reading counts as activity, in either direction: the sum
// over live processes falls when a member exits, and that is something
// happening. A reading that could not be taken restarts the clock rather than
// counting as idleness.
type stallClock struct {
	d     time.Duration
	last  time.Duration
	have  bool
	since time.Time
}

func newStallClock(d time.Duration, _ time.Time) *stallClock { return &stallClock{d: d} }

// Idle reports whether the tree has used no CPU for the whole window, given
// the reading taken at now.
func (c *stallClock) Idle(now time.Time, cpu time.Duration, ok bool) bool {
	if !ok {
		c.have = false
		return false
	}
	if !c.have || cpu != c.last {
		c.have, c.last, c.since = true, cpu, now
		return false
	}
	return now.Sub(c.since) >= c.d
}

// abortGate is the one door every reason to take the job down goes through:
// a kill request on a held ticket, or a limit. The first to arrive wins and
// closes Done; the rest are told they were late, so the job is torn down once
// and the run reports one cause.
type abortGate struct {
	ch    chan struct{}
	mu    sync.Mutex
	fired bool
	req   lane.KillRequest
	limit string
}

func newAbortGate() *abortGate { return &abortGate{ch: make(chan struct{})} }

// Done is closed when a trigger has won.
func (g *abortGate) Done() <-chan struct{} { return g.ch }

// Trigger records why the job is being taken down, with the name of the limit
// that asked (empty for a kill request), and reports whether it was first.
func (g *abortGate) Trigger(req lane.KillRequest, limit string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fired {
		return false
	}
	g.fired, g.req, g.limit = true, req, limit
	close(g.ch)
	return true
}

// Fired reports the winning trigger, if any.
func (g *abortGate) Fired() (lane.KillRequest, string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.req, g.limit, g.fired
}

// limitExit is the exit code of a run ended by the named limit; a kill
// request (no limit) keeps ExitKilled.
func limitExit(limit string) int {
	switch limit {
	case limitMaxRun:
		return ExitMaxRun
	case limitStall:
		return ExitStall
	}
	return ExitKilled
}

// cpuSampleEvery is how often the stall check reads the tree's CPU: a
// twentieth of the window, never faster than the poll interval and never
// slower than five seconds. Reading is a process listing on Unix, so it is
// not done at every poll of a long limit.
func cpuSampleEvery(stall, poll time.Duration) time.Duration {
	every := stall / 20
	if every > 5*time.Second {
		every = 5 * time.Second
	}
	if every < poll {
		every = poll
	}
	return every
}

// watchLimits enforces the limits while the job runs: it triggers the gate
// with the limit's name once the job has held the lane for maxRun, or its
// tree has used no CPU for stall. It returns a channel that closes when the
// watcher has stopped, which is when stop closes or after it has fired.
func watchLimits(maxRun, stall time.Duration, probe *child.Probe, poll time.Duration, begin time.Time, g *abortGate, stop <-chan struct{}) <-chan struct{} {
	done := make(chan struct{})
	if maxRun <= 0 && stall <= 0 {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		t := time.NewTicker(poll)
		defer t.Stop()
		var clock *stallClock
		var lastSample time.Time
		var every time.Duration
		if stall > 0 {
			clock = newStallClock(stall, begin)
			every = cpuSampleEvery(stall, poll)
		}
		for {
			select {
			case <-stop:
				return
			case now := <-t.C:
				if maxRun > 0 && now.Sub(begin) >= maxRun {
					g.Trigger(lane.KillRequest{
						By: limitBy, ByPID: os.Getpid(),
						Reason: fmt.Sprintf("--max-run %s reached: the job has held the lane for %s", maxRun, now.Sub(begin).Round(time.Second)),
					}, limitMaxRun)
					return
				}
				if clock == nil || now.Sub(lastSample) < every {
					continue
				}
				lastSample = now
				cpu, ok := probe.CPU()
				if clock.Idle(now, cpu, ok) {
					g.Trigger(lane.KillRequest{
						By: limitBy, ByPID: os.Getpid(),
						Reason: fmt.Sprintf("--stall %s reached: the job tree used no CPU time for %s", stall, now.Sub(clock.since).Round(time.Second)),
					}, limitStall)
					return
				}
			}
		}
	}()
	return done
}

// stallUnsupportedLine is the line printed when --stall is in force on an OS
// that cannot read a job tree's CPU.
func stallUnsupportedLine() string {
	return fmt.Sprintf("warning: CPU readout unavailable on %s; --stall cannot fire for this run", runtime.GOOS)
}
