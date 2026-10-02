// Package report builds the observer's view of the queues on this machine:
// the shape `status --json` emits and `watch` paints. It lives apart from
// the CLI so the terminal UI can read the same report without importing the
// command layer.
package report

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/sysinfo"
)

// Report is the stable machine-readable shape emitted by `status --json`.
// Fields are only ever added, never renamed or removed.
type Report struct {
	Schema   int    `json:"schema"`
	Version  string `json:"incoda_version"`
	StateDir string `json:"state_dir"`
	// StateDirSource records how the state directory was chosen, so a
	// fragmented setup (one agent with INCODA_DIR set, others without) is
	// visible in one command instead of looking like an empty queue.
	StateDirSource string         `json:"state_dir_source"`
	Host           string         `json:"hostname"`
	Time           string         `json:"time"`
	Memory         sysinfo.Memory `json:"memory"`
	CPU            sysinfo.CPU    `json:"cpu"`
	Queues         []Queue        `json:"queues"`
	// Banner is the read-only banner of a layout not upgraded yet (spec
	// 3.2). It is display text, not part of the JSON report; plan 5 adds
	// the layout fields to status --json.
	Banner string `json:"-"`
	// Warnings are the lines plain status adds at the end (spec 5.3):
	// unpooled runs of an older incoda, a missing fence, stopped holders.
	// Display text, not part of the JSON report.
	Warnings []string `json:"-"`
}

// Queue is one queue inside a Report.
type Queue struct {
	Key            string       `json:"key"`
	Dir            string       `json:"dir"`
	Exists         bool         `json:"exists"`
	EffectiveSlots int          `json:"effective_slots"`
	Free           bool         `json:"free"`
	Config         lane.Config  `json:"config"`
	ConfigError    string       `json:"config_error,omitempty"`
	Holders        []lane.Entry `json:"holders"`
	Waiting        []lane.Entry `json:"waiting"`
	RecentEvents   []string     `json:"recent_events"`
}

// StateDirSource names how the state directory was resolved.
func StateDirSource() string {
	if strings.TrimSpace(os.Getenv("INCODA_DIR")) != "" {
		return "INCODA_DIR"
	}
	return "platform default"
}

// Build observes the named queues, or every queue with state when all is
// set, in the layout machine.Inspect finds: lanes/ once migrated, the old
// queues/ read only before (spec 3.2), so a status on a layout not upgraded
// yet creates and reaps nothing. A key with no state is reported as free,
// because a never-used queue is simply free. A broken or lost registry is
// returned as its *machine.StateError so the caller fails closed.
func Build(stateDir, version string, keys []string, all bool, events int) (*Report, error) {
	v, err := machine.Inspect(stateDir)
	if err != nil {
		return nil, err
	}
	if all {
		keys, err = lane.ListIn(v.Root)
		if err != nil {
			return nil, fmt.Errorf("cannot list queues: %w", err)
		}
		sort.Strings(keys)
	}
	mode := lane.Existing
	if !v.Migrated {
		mode = lane.ReadOnly
	}
	host, _ := os.Hostname()
	rep := &Report{
		Schema:         1,
		Version:        version,
		StateDir:       stateDir,
		StateDirSource: StateDirSource(),
		Host:           host,
		Time:           time.Now().Format(time.RFC3339),
		Memory:         sysinfo.ReadMemory(),
		CPU:            sysinfo.ReadCPU(),
		Queues:         []Queue{},
		Banner:         v.Banner,
	}
	for _, key := range keys {
		qr := Queue{
			Key:     key,
			Dir:     filepath.Join(v.Root, key),
			Exists:  lane.ExistsIn(v.Root, key),
			Holders: []lane.Entry{},
			Waiting: []lane.Entry{},
		}
		var q *lane.Queue
		if qr.Exists {
			q, err = lane.OpenIn(v.Root, key, mode)
			if errors.Is(err, os.ErrNotExist) {
				qr.Exists = false
			} else if err != nil {
				return nil, err
			}
		}
		if !qr.Exists {
			qr.EffectiveSlots = 1
			qr.Free = true
			rep.Queues = append(rep.Queues, qr)
			continue
		}
		snap, err := q.Observe(events)
		q.Close()
		if err != nil {
			return nil, fmt.Errorf("cannot read queue %q: %w", key, err)
		}
		qr.EffectiveSlots = snap.EffectiveSlots
		qr.Config = snap.Config
		qr.ConfigError = snap.ConfigError
		qr.Holders = snap.Holders
		qr.Waiting = snap.Waiting
		qr.RecentEvents = snap.RecentEvents
		qr.Free = len(snap.Holders) == 0
		rep.Queues = append(rep.Queues, qr)
	}
	var holders []machine.Holder
	for _, qr := range rep.Queues {
		for _, e := range append(append([]lane.Entry(nil), qr.Holders...), qr.Waiting...) {
			holders = append(holders, machine.Holder{Key: qr.Key, PID: e.Ticket.PID})
		}
	}
	rep.Warnings = machine.StatusWarnings(stateDir, v, holders)
	return rep, nil
}
