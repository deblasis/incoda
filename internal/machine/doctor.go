package machine

import (
	"errors"
	"fmt"
	"strings"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/textsafe"
)

// Health is doctor's reading of the layout: the parts of spec 5.5 that
// exist with layout 2. PATH versions, strays, orphan records and stopped
// holders are added by plan 2b.
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
	// Problems make runs fail closed: doctor exits 122.
	Problems []string
	// Attention items are printed; doctor still exits 0.
	Attention []string
}

// Diagnose reads the layout like Inspect: no lock, no writes.
func Diagnose(stateDir string) Health {
	var h Health
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
	case !errors.Is(err, ErrNoRegistry):
		h.Layout = "2 (machine.json unusable)"
		h.Problems = append(h.Problems, strings.TrimPrefix(err.Error(), "machine-state: "))
	default:
		switch row := st.row(); row {
		case RowRegistryLost:
			h.Layout = "2 (machine.json missing)"
			h.Problems = append(h.Problems, "machine.json: missing while lanes/ exists; nothing re-creates it on its own. A human decides which lanes are pools and runs: incoda doctor --rebuild-registry builds,computer-use,tests,vm")
		case RowNotStarted:
			if st.Queues == aDir {
				h.Layout = "1 (queues/)"
				h.Attention = append(h.Attention, banner(stateDir))
			} else {
				h.Layout = "none yet (the next mutating incoda command creates layout 2)"
			}
		default:
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
