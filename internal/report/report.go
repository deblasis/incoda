// Package report builds the observer's view of the queues on this machine:
// the shape `status --json` emits and `watch` paints. It lives apart from
// the CLI so the terminal UI can read the same report without importing the
// command layer.
package report

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/lane"
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
	Queues         []Queue        `json:"queues"`
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

// Keys lists every queue with state under dir, sorted.
func Keys(dir string) ([]string, error) {
	keys, err := lane.ListQueues(dir)
	if err != nil {
		return nil, fmt.Errorf("cannot list queues: %w", err)
	}
	sort.Strings(keys)
	return keys, nil
}

// Build observes the named queues under dir. A key with no state is reported
// as free rather than as an error, because a never-used queue is simply free.
func Build(dir, version string, keys []string, events int) (*Report, error) {
	host, _ := os.Hostname()
	rep := &Report{
		Schema:         1,
		Version:        version,
		StateDir:       dir,
		StateDirSource: StateDirSource(),
		Host:           host,
		Time:           time.Now().Format(time.RFC3339),
		Memory:         sysinfo.ReadMemory(),
		Queues:         []Queue{},
	}
	for _, key := range keys {
		qr := Queue{
			Key:     key,
			Dir:     lane.QueueDir(dir, key),
			Exists:  lane.Exists(dir, key),
			Holders: []lane.Entry{},
			Waiting: []lane.Entry{},
		}
		if !qr.Exists {
			qr.EffectiveSlots = 1
			qr.Free = true
			rep.Queues = append(rep.Queues, qr)
			continue
		}
		q, err := lane.Open(dir, key)
		if err != nil {
			return nil, err
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
	return rep, nil
}
