package machine

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/textsafe"
)

// Health is doctor's reading of the state directory (spec 5.5): the
// layout, the fence, strays/, orphan records, live unpooled holders and
// stopped holders. PATH versions are read by PathVersionLines.
//
// Every string in Problems and Attention is already escaped for display
// (textsafe.Escape applied once, here, to whatever came from state, a file
// or the environment); a caller that prints them must not escape them
// again, since textsafe.Escape is not idempotent.
type Health struct {
	// Layout is one line describing the layout.
	Layout string
	// Fence is "present" or "missing" on a migrated layout, else empty.
	Fence string
	// Strays describes each lane directory left under strays/; Orphans
	// each record under orphans/. Both are empty when there are none.
	Strays  []string
	Orphans []string
	// Problems make runs fail closed: doctor exits 122.
	Problems []string
	// Attention items are printed; doctor still exits 0.
	Attention []string
}

// Diagnose reads the layout like Inspect: no lock, no writes. Its lane
// probes share one deadline of lane.ViewProbeWait; a lane whose registry
// lock another process keeps past it is reported as cannot tell.
func Diagnose(stateDir string) Health {
	var h Health
	deadline := time.Now().Add(lane.ViewProbeWait)
	reg, err := ReadRegistry(stateDir)
	if errors.Is(err, ErrNoRegistry) && scanLayout(stateDir).row() == RowRegistryLost {
		// A migration may have committed between the two looks.
		reg, err = ReadRegistry(stateDir)
	}
	st := scanLayout(stateDir)
	switch {
	case err == nil:
		h.Layout = fmt.Sprintf("2 (machine.json schema %d, generation %d; pools %s)", reg.Schema, reg.Generation, strings.Join(reg.Pools, ", "))
		if st.Plan {
			h.Problems = append(h.Problems, fmt.Sprintf("migration unfinished (%s); the next mutating incoda command deletes migration.json", RowCommitted))
		}
		if FencePlaced(stateDir) {
			h.Fence = "present"
		} else {
			h.Fence = "missing"
			h.Problems = append(h.Problems, fmt.Sprintf("fence missing: %s is not a regular file, so an older incoda can run outside the pools; the next run or config re-places it", textsafe.Escape(lane.QueuesDir(stateDir))))
		}
		h.Attention = append(h.Attention, unreadableConfigs(stateDir)...)
		describeHolders(stateDir, &h, lane.LanesDir(stateDir), true, deadline)
	case !errors.Is(err, ErrNoRegistry):
		h.Layout = "2 (machine.json unusable)"
		h.Problems = append(h.Problems, strings.TrimPrefix(err.Error(), "machine-state: "))
	default:
		switch row := st.row(); row {
		case RowRegistryLost:
			h.Layout = "2 (machine.json missing)"
			keys, _ := lane.ListQueues(stateDir)
			sort.Strings(keys)
			lanes := "none"
			if len(keys) > 0 {
				lanes = strings.Join(keys, ", ")
			}
			h.Problems = append(h.Problems, fmt.Sprintf("machine.json: missing while lanes/ exists; nothing re-creates it on its own. Lanes: %s. A human decides which of them are pools and runs: incoda doctor --rebuild-registry POOL,POOL", lanes))
		case RowNotStarted:
			if st.Queues == aDir {
				h.Layout = "1 (queues/)"
				h.Attention = append(h.Attention, banner(stateDir))
				describeHolders(stateDir, &h, lane.QueuesDir(stateDir), false, deadline)
			} else {
				h.Layout = "none yet (the next mutating incoda command creates layout 2)"
			}
		default:
			describeHolders(stateDir, &h, unmigratedRoot(stateDir, st), false, deadline)
			h.Layout = "upgrade unfinished"
			if _, ok := migrationNote(stateDir); ok {
				h.Problems = append(h.Problems, fmt.Sprintf("migration in progress (%s): %s", row, banner(stateDir)))
			} else {
				h.Problems = append(h.Problems, fmt.Sprintf("migration unfinished (%s); the next mutating incoda command resumes it", row))
			}
		}
	}
	return h
}

// unreadableConfigs names every lane whose config.json cannot be read; the
// M8 text sends the user here.
func unreadableConfigs(stateDir string) []string {
	keys, _ := lane.ListQueues(stateDir)
	var out []string
	for _, k := range keys {
		if _, err := lane.ReadConfig(lane.LaneDir(stateDir, k)); err != nil {
			out = append(out, fmt.Sprintf("queue %q has an unreadable config.json: %s", k, textsafe.Escape(err.Error())))
		}
	}
	return out
}

// describeHolders fills in strays/, orphans/, the live unpooled holders
// (on a migrated layout) and every live ticket holder in the stopped state
// (spec 5.5). root is where the lanes are now. It only reads.
func describeHolders(stateDir string, h *Health, root string, migrated bool, deadline time.Time) {
	batches, _ := lane.ListIn(StraysDir(stateDir))
	sort.Strings(batches)
	for _, b := range batches {
		keys, _ := lane.ListIn(filepath.Join(StraysDir(stateDir), b))
		for _, k := range keys {
			live, err := lane.ProbeLane(filepath.Join(StraysDir(stateDir), b, k), deadline)
			if err == nil && lane.AnyCannotTell(live) {
				err = lane.ErrRegistryBusy
			}
			if err != nil {
				h.Strays = append(h.Strays, fmt.Sprintf("%s/%s: cannot probe (%s)", b, k, esc(err)))
				continue
			}
			h.Strays = append(h.Strays, fmt.Sprintf("%s/%s: %d live ticket(s)", b, k, len(live)))
		}
	}
	recs, bad, _ := ReadOrphans(stateDir)
	for _, o := range recs {
		state := "its job has exited; incoda force-release --queue " + o.Key + " deletes the record"
		if o.Live() {
			state = "its job still runs (pids " + o.pidList() + ")"
		}
		h.Orphans = append(h.Orphans, fmt.Sprintf("%s: key %s, older incoda pid %d: %s", textsafe.Escape(o.File), o.Key, o.PID, state))
	}
	for _, b := range bad {
		h.Orphans = append(h.Orphans, fmt.Sprintf("%s: unreadable (%s); delete it once you have checked that its job is gone", textsafe.Escape(b.File), esc(b.Err)))
	}
	var holders []Holder
	if migrated {
		us, err := ScanUnpooled(stateDir, false, deadline)
		if err != nil {
			h.Attention = append(h.Attention, fmt.Sprintf("cannot scan for unpooled runs: %s", esc(err)))
		}
		for _, u := range us {
			h.Attention = append(h.Attention, fmt.Sprintf("%s (%s): %s; new runs on its pools wait for it", u.Line(), u.Where, u.Command))
			if u.Where != "orphans" && !u.Unknown {
				holders = append(holders, Holder{Key: u.Key, PID: u.PID})
			}
		}
	}
	keys, _ := lane.ListIn(root)
	for _, k := range keys {
		live, _ := lane.ProbeLane(filepath.Join(root, k), deadline)
		for _, p := range live {
			if p.CannotTell() {
				h.Attention = append(h.Attention, fmt.Sprintf("queue %s: %s", k, lane.ErrRegistryBusy))
				continue
			}
			holders = append(holders, Holder{Key: k, PID: p.PID()})
		}
	}
	seen := map[int]bool{}
	for _, hd := range holders {
		if hd.PID <= 0 || seen[hd.PID] {
			continue
		}
		seen[hd.PID] = true
		if s, err := procinfo.Stopped(hd.PID); err == nil && s {
			h.Attention = append(h.Attention, StoppedLines(hd.Key, hd.PID)...)
		}
	}
}
