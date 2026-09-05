package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/colorize"
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
	Config         lane.Config  `json:"config"`
	ConfigError    string       `json:"config_error,omitempty"`
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
	noColor := fs.Bool("no-color", false, "never emit ANSI color, even on a terminal (the NO_COLOR environment variable does the same)")
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
	renderReport(stdout, paletteFor(stdout, *noColor), rep)
	return nil
}

// paletteFor resolves the palette for an output stream. --no-color wins over
// everything; after that the conventions decide (NO_COLOR, TERM=dumb,
// CLICOLOR_FORCE, whether w is a terminal at all).
func paletteFor(w io.Writer, noColor bool) colorize.Palette {
	if noColor {
		return colorize.Plain
	}
	return colorize.For(w)
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

// renderReport paints the human view of a Report. With the Plain palette the
// output is byte-for-byte what the uncolored renderer always produced, so
// scripts that scrape it keep working.
func renderReport(w io.Writer, p colorize.Palette, rep *Report) {
	fmt.Fprintf(w, "%s %s  %s\n", p.Dim("state dir:"), rep.StateDir, p.Dim("("+rep.StateDirSource+")"))
	if len(rep.Queues) == 0 {
		fmt.Fprintln(w, p.Dim("no queues have state on this machine"))
	}
	for i, qr := range rep.Queues {
		if i > 0 {
			fmt.Fprintln(w)
		}
		renderQueue(w, p, qr)
	}
	fmt.Fprintf(w, "\n%s\n", p.Dim(rep.Memory.String()))
}

func renderQueue(w io.Writer, p colorize.Palette, qr QueueReport) {
	if !qr.Exists {
		fmt.Fprintf(w, "queue %q: %s\n", qr.Key, p.BoldGreen("FREE")+" "+p.Dim("(never used on this machine)"))
		return
	}
	if qr.Free {
		fmt.Fprintf(w, "queue %q: %s\n", qr.Key, p.BoldGreen("FREE")+"  "+p.Dim(fmt.Sprintf("(%d slot(s))", qr.EffectiveSlots)))
	} else {
		fmt.Fprintf(w, "queue %q: %s\n", qr.Key, p.BoldYellow(fmt.Sprintf("%d/%d slot(s) held", len(qr.Holders), qr.EffectiveSlots)))
	}
	if qr.Config.Description != "" {
		fmt.Fprintf(w, "  %s\n", p.Dim(qr.Config.Description))
	}
	if qr.Config.Closed != "" {
		fmt.Fprintf(w, "  %s %s\n", p.BoldRed("CLOSED:"), qr.Config.Closed)
	}
	if qr.ConfigError != "" {
		fmt.Fprintf(w, "  %s\n", p.Red("config unreadable: "+qr.ConfigError))
	}
	for _, e := range qr.Holders {
		fmt.Fprintf(w, "  %s  pid %s held %s %s%s\n",
			p.BoldCyan("HOLDER"), p.Bold(fmt.Sprintf("%-7d", e.Ticket.PID)), p.Dim(fmt.Sprintf("%-10s", dur(e.HeldSeconds))), exclusiveTag(p, e), e.Ticket.CommandString())
		fmt.Fprintf(w, "          %s %s\n", p.Dim("in"), orNone(e.Ticket.Dir))
		if e.Ticket.Owner != "" {
			fmt.Fprintf(w, "          %s %s\n", p.Dim("owner:"), e.Ticket.Owner)
		}
		if e.Ticket.Reason != "" {
			fmt.Fprintf(w, "          %s %s\n", p.Dim("reason:"), e.Ticket.Reason)
		}
		if e.PayloadError != "" {
			fmt.Fprintf(w, "  %s\n", p.Red(fmt.Sprintf("        ticket unreadable: %s", e.PayloadError)))
		}
	}
	if len(qr.Waiting) == 0 {
		fmt.Fprintln(w, p.Dim("  waiting: none"))
	} else {
		fmt.Fprintf(w, "  %s\n", p.BoldYellow("WAITING (arrival order):"))
		for n, e := range qr.Waiting {
			fmt.Fprintf(w, "    %s pid %s waited %s %s%s\n",
				p.Dim(fmt.Sprintf("%2d.", n+1)), p.Bold(fmt.Sprintf("%-7d", e.Ticket.PID)), p.Dim(fmt.Sprintf("%-10s", dur(e.WaitingSeconds))), exclusiveTag(p, e), e.Ticket.CommandString())
			fmt.Fprintf(w, "        %s %s\n", p.Dim("in"), orNone(e.Ticket.Dir))
			if e.Ticket.Owner != "" {
				fmt.Fprintf(w, "        %s %s\n", p.Dim("owner:"), e.Ticket.Owner)
			}
			if e.Ticket.Reason != "" {
				fmt.Fprintf(w, "        %s %s\n", p.Dim("reason:"), e.Ticket.Reason)
			}
			if e.PayloadError != "" {
				fmt.Fprintf(w, "  %s\n", p.Red(fmt.Sprintf("      ticket unreadable: %s", e.PayloadError)))
			}
		}
	}
	if len(qr.RecentEvents) > 0 {
		fmt.Fprintf(w, "  %s\n", p.Dim("recent events:"))
		for _, l := range qr.RecentEvents {
			fmt.Fprintf(w, "    %s\n", paintEvent(p, l))
		}
	}
}

// paintEvent colors one lane.log line: the timestamp dim, the event verb by
// what it means (acquire green, enqueue cyan, release dim, giveups and
// force-releases red). Unknown shapes pass through untouched.
func paintEvent(p colorize.Palette, line string) string {
	const key = "event="
	i := strings.Index(line, key)
	if i < 0 {
		return line
	}
	rest := line[i+len(key):]
	name := rest
	tail := ""
	if end := strings.IndexByte(rest, ' '); end >= 0 {
		name, tail = rest[:end], rest[end:]
	}
	var verb string
	switch name {
	case "acquire":
		verb = p.Green("event=" + name)
	case "enqueue":
		verb = p.Cyan("event=" + name)
	case "release", "reenter":
		verb = p.Dim("event=" + name)
	case "giveup", "force-release", "reaped":
		verb = p.Red("event=" + name)
	case "config":
		verb = p.Yellow("event=" + name)
	default:
		verb = "event=" + name
	}
	out := line[:i] + verb + tail
	// The log stamps are "2006-01-02 15:04:05"; dim the stamp when the line
	// still starts with one.
	if len(line) >= 20 && line[4] == '-' && line[7] == '-' && line[10] == ' ' {
		out = p.Dim(line[:19]) + out[19:]
	}
	return out
}

// exclusiveTag marks a participant that asked for the queue alone, so a
// reader can see why a two-slot queue is admitting nobody.
func exclusiveTag(p colorize.Palette, e lane.Entry) string {
	if !e.Ticket.Exclusive {
		return ""
	}
	return p.BoldRed("EXCLUSIVE") + " "
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
