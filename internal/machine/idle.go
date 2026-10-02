package machine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/textsafe"
)

// phase is which idle check runs: M2 before the fence, M5 after it.
type phase int

const (
	// phaseM2 probes queues/<K>/: older runs on the old layout.
	phaseM2 phase = iota
	// phaseM5 probes lanes/<K>/ and strays/<n>/<K>/: an older run that
	// slipped in between M2 and the fence.
	phaseM5
)

// laneDirsFor lists the lane directories whose tickets the phase probes.
func laneDirsFor(stateDir string, ph phase) ([]string, error) {
	var roots []string
	switch ph {
	case phaseM2:
		roots = []string{lane.QueuesDir(stateDir)}
	case phaseM5:
		roots = []string{lane.LanesDir(stateDir)}
		batches, err := os.ReadDir(StraysDir(stateDir))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, b := range batches {
			if b.IsDir() {
				roots = append(roots, filepath.Join(StraysDir(stateDir), b.Name()))
			}
		}
	}
	var dirs []string
	for _, root := range roots {
		keys, err := lane.ListIn(root)
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			dirs = append(dirs, filepath.Join(root, k))
		}
	}
	return dirs, nil
}

// findBlockers probes every ticket the phase covers and returns the live
// ones, plus every orphan record whose tree still runs (deleting the
// others), sorted by key then pid, one entry per key and pid.
func findBlockers(stateDir string, ph phase) ([]Blocker, error) {
	dirs, err := laneDirsFor(stateDir, ph)
	if err != nil {
		return nil, err
	}
	var bs []Blocker
	for _, d := range dirs {
		live, err := lane.ProbeLane(d)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", d, err)
		}
		for _, p := range live {
			cmd := "(unreadable ticket)"
			if p.PayloadErr == nil && p.ProbeErr == nil {
				cmd = p.Ticket.CommandString()
			}
			bs = append(bs, Blocker{Key: filepath.Base(d), PID: p.PID(), Command: textsafe.Escape(cmd)})
		}
	}
	orphans, err := LiveOrphans(stateDir, true)
	if err != nil {
		return nil, fmt.Errorf("orphans/: %w", err)
	}
	seen := map[string]bool{}
	for _, b := range bs {
		seen[fmt.Sprintf("%s/%d", b.Key, b.PID)] = true
	}
	for _, o := range orphans {
		if seen[fmt.Sprintf("%s/%d", o.Key, o.PID)] {
			continue
		}
		bs = append(bs, Blocker{Key: o.Key, PID: o.PID, Orphan: true,
			Command: textsafe.Escape(o.CommandString()) + " (its job is still exiting after a kill: pids " + o.pidList() + ")"})
	}
	sort.Slice(bs, func(i, j int) bool {
		if bs[i].Key != bs[j].Key {
			return bs[i].Key < bs[j].Key
		}
		return bs[i].PID < bs[j].PID
	})
	return bs, nil
}

// waitIdle is the wait of spec 3.3 M2 and M5. The caller holds machine.lock
// and keeps it while waiting: while machine.json is absent no new-binary
// command can do useful work anyway, and waiters read the blockers from the
// note. It returns nil once no ticket the phase covers is live.
func waitIdle(stateDir string, lk *Lock, o Options, ph phase) error {
	deadline := budgetDeadline(o.Start, o.Wait)
	printed, noted := false, false
	for {
		bs, err := findBlockers(stateDir, ph)
		if err != nil {
			return stateErrorf("cannot probe the tickets of older runs: %s", textsafe.Escape(err.Error()))
		}
		if len(bs) == 0 {
			if noted {
				_ = lk.SetBlockers(nil)
			}
			return nil
		}
		if !o.Chain.Skip {
			for _, b := range bs {
				if o.Chain.Contains(b.PID) {
					return upgradeBlocked(b.PID, b.Key)
				}
			}
		}
		if err := lk.SetBlockers(bs); err != nil {
			return stateErrorf("cannot write the machine.lock note: %s", textsafe.Escape(err.Error()))
		}
		noted = true
		if !printed {
			fmt.Fprintf(o.stderr(), "incoda: %s\n", joinLines(upgradeWaitLines(bs, ph)))
			printed = true
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return &Timeout{Msg: joinLines(upgradeTimeoutLines(bs, ph, o.Wait))}
		}
		time.Sleep(o.poll())
	}
}

func upgradeWaitLines(bs []Blocker, ph phase) []string {
	lines := []string{fmt.Sprintf("upgrade-wait: state upgrade waits for %d run(s) by an older incoda:", len(bs))}
	return append(lines, blockerLines(bs, ph)...)
}

func upgradeTimeoutLines(bs []Blocker, ph phase, waited time.Duration) []string {
	lines := []string{fmt.Sprintf("upgrade-timeout: state upgrade still waits for %d run(s) by an older incoda after %s:", len(bs), waited)}
	lines = append(lines, blockerLines(bs, ph)...)
	return append(lines, "upgrade the older incoda on PATH; see incoda doctor")
}

// blockerLines lists the older runs, then the ask-the-user text and one
// stop line per run. Stopping another session's job is the user's call, so
// the lines are printed for them, never run.
func blockerLines(bs []Blocker, ph phase) []string {
	var lines []string
	for _, b := range bs {
		lines = append(lines, fmt.Sprintf("  %s pid %d: %s", b.Key, b.PID, b.Command))
	}
	var stops []string
	for _, b := range bs {
		if !b.Orphan {
			stops = append(stops, "  "+killLine(b))
		}
	}
	if len(stops) > 0 {
		lines = append(lines, "ask the user before stopping another session's job; they can run:")
		lines = append(lines, stops...)
	}
	return append(lines, "do not force-release them: the job keeps running and the upgrade would overlap it.")
}

// killLine is the stop line for one older run, before the fence (M2) and
// after it (M5) alike: kill ends an older incoda with its whole job by the
// old-holder kill of spec 3.2, with or without --force.
func killLine(b Blocker) string {
	return KillLine(b.Key, b.PID, UpgradeReason, false)
}

// UpgradeReason is the --reason of every stop line the upgrade prints
// (spec 3.2, 3.3).
const UpgradeReason = "incoda upgrade"
