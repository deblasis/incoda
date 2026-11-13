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
	// IsPool says the key is a machine-wide pool rather than a project
	// lane: it comes from machine.json, never from the lane's own config,
	// because kind is a fact about the machine. watch groups on it.
	IsPool bool `json:"is_pool,omitempty"`
	// ProbeError is set when the queue could not be read because another
	// process kept its registry lock past the view's deadline (a stopped
	// incoda): the queue is then reported busy, never free.
	ProbeError string `json:"probe_error,omitempty"`
	// Lockless says the queue was read without its registry lock because
	// another process held it past the view's deadline. Holders and Waiting are
	// still the live participants; only a ticket created or released during the
	// read can be missing from them.
	Lockless bool `json:"lockless,omitempty"`
	// UpstreamWaiting lists the lanes that link to this pool and have
	// tickets queued on the lane that have not entered the pool yet. A pool
	// holds only forwarded runs, so its own Waiting is empty by construction
	// and a pool-scoped view would otherwise read "0 waiting" while work
	// queues behind it. Empty for a project lane.
	UpstreamWaiting []UpstreamWaiting `json:"upstream_waiting,omitempty"`
}

// UpstreamWaiting is one project lane's queue depth as seen from a pool it
// links to: Waiting counts the tickets queued on the lane, none of which
// has reached the pool.
type UpstreamWaiting struct {
	Lane    string `json:"lane"`
	Waiting int    `json:"waiting"`
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
	// One deadline bounds every registry lock this view waits for, so a
	// lock another process keeps for ever cannot hang status or watch.
	deadline := time.Now().Add(lane.ViewProbeWait)
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
			IsPool:  v.Registry != nil && v.Registry.IsPool(key),
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
		snap, err := q.ObserveBy(events, deadline)
		q.Close()
		if errors.Is(err, lane.ErrRegistryBusy) {
			qr.EffectiveSlots = 1
			qr.ProbeError = lane.ErrRegistryBusy.Error()
			rep.Queues = append(rep.Queues, qr)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("cannot read queue %q: %w", key, err)
		}
		qr.EffectiveSlots = snap.EffectiveSlots
		qr.Config = snap.Config
		qr.ConfigError = snap.ConfigError
		qr.Holders = snap.Holders
		qr.Waiting = snap.Waiting
		qr.RecentEvents = snap.RecentEvents
		qr.Lockless = snap.Lockless
		qr.Free = len(snap.Holders) == 0
		rep.Queues = append(rep.Queues, qr)
	}
	var holders []machine.Holder
	for _, qr := range rep.Queues {
		for _, e := range append(append([]lane.Entry(nil), qr.Holders...), qr.Waiting...) {
			holders = append(holders, machine.Holder{Key: qr.Key, PID: e.Ticket.PID})
		}
	}
	// Pools only forward. A waiter on a lane that links to a pool is real
	// work queued behind that pool, but its ticket is on the lane, so the
	// pool's own Waiting can never show it; attach the lane counts to the
	// pool entries so a pool-scoped view states the depth it is read for.
	if hasPool(rep.Queues) {
		up := ScanUpstream(v.Root, mode, deadline)
		for qi := range rep.Queues {
			if rep.Queues[qi].IsPool {
				rep.Queues[qi].UpstreamWaiting = up[rep.Queues[qi].Key]
			}
		}
	}
	rep.Warnings = machine.StatusWarnings(stateDir, v, holders, deadline)
	return rep, nil
}

// hasPool reports whether the report carries a pool, so the upstream scan
// (which lists and probes the lanes) only runs when somebody can read it.
func hasPool(queues []Queue) bool {
	for _, q := range queues {
		if q.IsPool {
			return true
		}
	}
	return false
}

// ScanUpstream counts, per pool key, the tickets queued on the lanes that
// link to it. Links are config facts, read without the registry lock; each
// linking lane is observed once on the caller's deadline. Display detail:
// a lane whose config or state cannot be read is skipped, never an error.
func ScanUpstream(root string, mode lane.Mode, deadline time.Time) map[string][]UpstreamWaiting {
	keys, err := lane.ListIn(root)
	if err != nil {
		return nil
	}
	sort.Strings(keys)
	out := map[string][]UpstreamWaiting{}
	for _, k := range keys {
		cfg, err := lane.ReadConfig(filepath.Join(root, k))
		if err != nil || len(cfg.Pools) == 0 {
			continue
		}
		q, err := lane.OpenIn(root, k, mode)
		if err != nil {
			continue
		}
		snap, err := q.ObserveBy(0, deadline)
		q.Close()
		if err != nil || len(snap.Waiting) == 0 {
			continue
		}
		for _, pool := range cfg.Pools {
			if pool == k {
				continue
			}
			out[pool] = append(out[pool], UpstreamWaiting{Lane: k, Waiting: len(snap.Waiting)})
		}
	}
	return out
}
