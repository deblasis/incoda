package cli

import (
	"encoding/json"
	"fmt"
	"io"
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
	Queues         []QueueReport  `json:"queues"`
}

// QueueReport is one queue inside a Report.
type QueueReport struct {
	Key            string       `json:"key"`
	Dir            string       `json:"dir"`
	Exists         bool         `json:"exists"`
	EffectiveSlots int          `json:"effective_slots"`
	Free           bool         `json:"free"`
	Holders        []lane.Entry `json:"holders"`
	Waiting        []lane.Entry `json:"waiting"`
	RecentEvents   []string     `json:"recent_events"`
}

func stateDirSource() string {
	if strings.TrimSpace(os.Getenv("INCODA_DIR")) != "" {
		return "INCODA_DIR"
	}
	return "platform default"
}

func cmdStatus(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("status", stderr)
	queue := fs.String("queue", "", "queue key (defaults to $INCODA_QUEUE)")
	all := fs.Bool("all", false, "report every queue with state on this machine")
	asJSON := fs.Bool("json", false, "emit the stable JSON report")
	events := fs.Int("events", 5, "how many recent log events to show")
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for status"}
	}
	rep, err := buildReport(*queue, *all, *events)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	renderReport(stdout, rep)
	return nil
}

func buildReport(queueFlag string, all bool, events int) (*Report, error) {
	dir, err := stateDir()
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	rep := &Report{
		Schema:         1,
		Version:        Version,
		StateDir:       dir,
		StateDirSource: stateDirSource(),
		Host:           host,
		Time:           time.Now().Format(time.RFC3339),
		Memory:         sysinfo.ReadMemory(),
		Queues:         []QueueReport{},
	}

	var keys []string
	if all {
		keys, err = lane.ListQueues(dir)
		if err != nil {
			return nil, exitWith(ExitState, "cannot list queues: %v", err)
		}
		sort.Strings(keys)
	} else {
		key, err := resolveKey(queueFlag)
		if err != nil {
			return nil, err
		}
		keys = []string{key}
	}

	for _, key := range keys {
		qr := QueueReport{
			Key:     key,
			Dir:     lane.QueueDir(dir, key),
			Exists:  lane.Exists(dir, key),
			Holders: []lane.Entry{},
			Waiting: []lane.Entry{},
		}
		if !qr.Exists {
			// A never-used queue is not an error: it is simply free.
			qr.EffectiveSlots = 1
			qr.Free = true
			rep.Queues = append(rep.Queues, qr)
			continue
		}
		q, err := lane.Open(dir, key)
		if err != nil {
			return nil, exitWith(ExitState, "%v", err)
		}
		snap, err := q.Observe(events)
		q.Close()
		if err != nil {
			return nil, exitWith(ExitState, "cannot read queue %q: %v", key, err)
		}
		qr.EffectiveSlots = snap.EffectiveSlots
		qr.Holders = snap.Holders
		qr.Waiting = snap.Waiting
		qr.RecentEvents = snap.RecentEvents
		qr.Free = len(snap.Holders) == 0
		rep.Queues = append(rep.Queues, qr)
	}
	return rep, nil
}

func renderReport(w io.Writer, rep *Report) {
	fmt.Fprintf(w, "state dir: %s  (%s)\n", rep.StateDir, rep.StateDirSource)
	if len(rep.Queues) == 0 {
		fmt.Fprintln(w, "no queues have state on this machine")
	}
	for i, qr := range rep.Queues {
		if i > 0 {
			fmt.Fprintln(w)
		}
		renderQueue(w, qr)
	}
	fmt.Fprintf(w, "\n%s\n", rep.Memory.String())
}

func renderQueue(w io.Writer, qr QueueReport) {
	if !qr.Exists {
		fmt.Fprintf(w, "queue %q: FREE (never used on this machine)\n", qr.Key)
		return
	}
	if qr.Free {
		fmt.Fprintf(w, "queue %q: FREE  (%d slot(s))\n", qr.Key, qr.EffectiveSlots)
	} else {
		fmt.Fprintf(w, "queue %q: %d/%d slot(s) held\n", qr.Key, len(qr.Holders), qr.EffectiveSlots)
	}
	for _, e := range qr.Holders {
		fmt.Fprintf(w, "  HOLDER  pid %-7d held %-10s %s\n", e.Ticket.PID, dur(e.HeldSeconds), e.Ticket.CommandString())
		fmt.Fprintf(w, "          in %s\n", orNone(e.Ticket.Dir))
		if e.Ticket.Reason != "" {
			fmt.Fprintf(w, "          reason: %s\n", e.Ticket.Reason)
		}
		if e.PayloadError != "" {
			fmt.Fprintf(w, "          ticket unreadable: %s\n", e.PayloadError)
		}
	}
	if len(qr.Waiting) == 0 {
		fmt.Fprintln(w, "  waiting: none")
	} else {
		fmt.Fprintln(w, "  WAITING (arrival order):")
		for n, e := range qr.Waiting {
			fmt.Fprintf(w, "    %2d. pid %-7d waited %-10s %s\n", n+1, e.Ticket.PID, dur(e.WaitingSeconds), e.Ticket.CommandString())
			fmt.Fprintf(w, "        in %s\n", orNone(e.Ticket.Dir))
			if e.Ticket.Reason != "" {
				fmt.Fprintf(w, "        reason: %s\n", e.Ticket.Reason)
			}
			if e.PayloadError != "" {
				fmt.Fprintf(w, "        ticket unreadable: %s\n", e.PayloadError)
			}
		}
	}
	if len(qr.RecentEvents) > 0 {
		fmt.Fprintln(w, "  recent events:")
		for _, l := range qr.RecentEvents {
			fmt.Fprintf(w, "    %s\n", l)
		}
	}
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(unknown directory)"
	}
	return s
}

func dur(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second))
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}
