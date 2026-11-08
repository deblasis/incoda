package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/colorize"
	"github.com/deblasis/incoda/internal/joblog"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/osc7501"
	"github.com/deblasis/incoda/internal/report"
	"github.com/deblasis/incoda/internal/sysinfo"
)

// Report and QueueReport are the shapes `status --json` emits. They live in
// the report package so the terminal UI can build the same view; the names
// are kept here so nothing in this package has to say "report.Report".
type (
	Report      = report.Report
	QueueReport = report.Queue
)

func stateDirSource() string { return report.StateDirSource() }

func cmdStatus(args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("status", stderr)
	queue := fs.String("queue", "", "queue key (defaults to $INCODA_QUEUE)")
	all := fs.Bool("all", false, "report every queue with state on this machine")
	asJSON := fs.Bool("json", false, "emit the stable JSON report")
	events := fs.Int("events", 5, "how many recent log events to show")
	noColor := fs.Bool("no-color", false, "never emit ANSI color, even on a terminal (the NO_COLOR environment variable does the same)")
	owner := fs.String("owner", os.Getenv("INCODA_OWNER"), "identity that may see a pty job's program status (--owner / $INCODA_OWNER, the same as attach)")
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for status"}
	}
	rep, err := buildReport(*queue, *all, *events)
	if err != nil {
		return err
	}
	if !*asJSON {
		printBanner(stderr, rep.Banner)
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	renderReport(stdout, paletteFor(stdout, *noColor), rep, *owner, nil)
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
	dir, err := lane.StateDir()
	if err != nil {
		return nil, exitWith(ExitState, "cannot resolve state directory: %v", err)
	}
	var keys []string
	if !all {
		key, err := resolveKey(queueFlag)
		if err != nil {
			return nil, err
		}
		keys = []string{key}
	}
	rep, err := report.Build(dir, Version, keys, all, events)
	if err != nil {
		return nil, machineExit(err)
	}
	for _, qr := range rep.Queues {
		var gated, ungated bool
		for _, e := range append(append([]lane.Entry(nil), qr.Holders...), qr.Waiting...) {
			if e.Ticket.MaxCPUPct > 0 {
				gated = true
			} else {
				ungated = true
			}
		}
		if gated && ungated {
			rep.Warnings = append(rep.Warnings,
				fmt.Sprintf("queue %q: mixed load gates — only runs with --max-cpu wait for idle; the rest bypass the gate", qr.Key))
		}
	}
	return rep, nil
}

// renderReport paints the human view of a Report. With the Plain palette the
// output is byte-for-byte what the uncolored renderer always produced, so
// scripts that scrape it keep working.

func renderReport(w io.Writer, p colorize.Palette, rep *Report, owner string, follow *statusFollowers) {
	fmt.Fprintf(w, "%s %s  %s\n", p.Dim("state dir:"), rep.StateDir, p.Dim("("+rep.StateDirSource+")"))
	if len(rep.Queues) == 0 {
		fmt.Fprintln(w, p.Dim("no queues have state on this machine"))
	}
	for i, qr := range rep.Queues {
		if i > 0 {
			fmt.Fprintln(w)
		}
		renderQueue(w, p, qr, owner, follow)
	}
	fmt.Fprintf(w, "\n%s\n", p.Dim(sysinfo.MachineLine(rep.Memory, rep.CPU)))
	// Warnings go after every existing line, so scrapers of the lines
	// above see them unchanged (spec 5.3).
	if len(rep.Warnings) > 0 {
		fmt.Fprintln(w)
		for _, l := range rep.Warnings {
			fmt.Fprintln(w, p.Yellow(l))
		}
	}
}

// ptyMark sits next to every pty-mode participant, in status and watch
// alike, so a glance tells which jobs carry a live captured stream.
const ptyMark = " 🖥️"

func ptyTag(e lane.Entry) string {
	if e.Ticket.PTY {
		return ptyMark
	}
	return ""
}

// statusFollowers is the plain renderer's per-process log cache: a viewer
// that polls (watch --plain) follows each job log incrementally, reading
// only the bytes appended since its last look, the same way the TUI does.
// A one-shot render passes nil instead and reads the whole log once.
type statusFollowers struct {
	logs map[string]*osc7501.LogStatus
}

func newStatusFollowers() *statusFollowers {
	return &statusFollowers{logs: map[string]*osc7501.LogStatus{}}
}

// follower returns the follower for one participant's log, following it.
func (f *statusFollowers) follower(laneDir, file string) *osc7501.LogStatus {
	k := laneDir + "\x00" + file
	ls := f.logs[k]
	if ls == nil {
		ls = &osc7501.LogStatus{}
		f.logs[k] = ls
	}
	return ls
}

// sweep drops the followers of participants the report no longer carries,
// so a viewer's memory does not grow with the jobs it has watched go by.
func (f *statusFollowers) sweep(rep *Report) {
	if f == nil || rep == nil {
		return
	}
	keep := map[string]bool{}
	for _, q := range rep.Queues {
		for _, group := range [][]lane.Entry{q.Holders, q.Waiting} {
			for _, e := range group {
				if e.Ticket.PTY {
					keep[q.Dir+"\x00"+e.File] = true
				}
			}
		}
	}
	for k := range f.logs {
		if !keep[k] {
			delete(f.logs, k)
		}
	}
}

// programStatus is the participant's OSC 7501 status for the end of its
// line: the root record of its job log, painted by state. Only a pty job
// has a log that can carry records, and only its owner sees what it wrote
// (the same rule attach and the watch capture view apply, lane.MayAttach);
// everything else, and a log with no records, gives "" and the line is what
// it always was. A polling viewer (follow) reads only appended bytes; a
// one-shot render reads the whole log, so a record survives however much
// output was written after it.
func programStatus(p colorize.Palette, laneDir string, e lane.Entry, owner string, follow *statusFollowers) string {
	if !e.Ticket.PTY || e.PayloadError != "" || lane.MayAttach(e.Ticket, owner) != nil {
		return ""
	}
	path, err := joblog.Path(laneDir, e.File)
	if err != nil {
		return ""
	}
	var r osc7501.Record
	var ok bool
	if follow != nil {
		r, ok = follow.follower(laneDir, e.File).Read(path)
	} else {
		var ls osc7501.LogStatus
		r, ok = ls.ReadWhole(path)
	}
	if !ok {
		return ""
	}
	line := r.Line(statusMsgWidth)
	switch r.State {
	case "working":
		// yellow is the plain palette's amber: the accent working is
		// painted in on a color terminal (the TUI's accent is the same hue)
		line = p.Yellow(line)
	case "done":
		line = p.Green(line)
	case "error":
		line = p.Red(line)
	case "blocked":
		line = p.Yellow(line)
	default:
		line = p.Dim(line)
	}
	return "  " + line
}

// statusMsgWidth is how much of a record's msg the participant line keeps.
const statusMsgWidth = 40

func renderQueue(w io.Writer, p colorize.Palette, qr QueueReport, owner string, follow *statusFollowers) {
	if !qr.Exists {
		fmt.Fprintf(w, "queue %q: %s\n", qr.Key, p.BoldGreen("FREE")+" "+p.Dim("(never used on this machine)"))
		return
	}
	if qr.ProbeError != "" {
		fmt.Fprintf(w, "queue %q: %s\n", qr.Key, p.BoldYellow(qr.ProbeError))
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
		fmt.Fprintf(w, "  %s%s  pid %s held %s %s%s%s\n",
			p.BoldCyan("HOLDER"), ptyTag(e), p.Bold(fmt.Sprintf("%-7d", e.Ticket.PID)), p.Dim(fmt.Sprintf("%-10s", dur(e.HeldSeconds))), exclusiveTag(p, e), e.Ticket.CommandString(), programStatus(p, qr.Dir, e, owner, follow))
		fmt.Fprintf(w, "          %s %s\n", p.Dim("in"), orNone(e.Ticket.Dir))
		if e.Ticket.Owner != "" {
			fmt.Fprintf(w, "          %s %s\n", p.Dim("owner:"), e.Ticket.Owner)
		}
		if e.Ticket.Reason != "" {
			fmt.Fprintf(w, "          %s %s\n", p.Dim("reason:"), e.Ticket.Reason)
		}
		if e.Ticket.MaxCPUPct > 0 {
			fmt.Fprintf(w, "          %s %s\n", p.Dim("gate:"), gateText(e.Ticket))
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
			fmt.Fprintf(w, "    %s%s pid %s waited %s %s%s%s\n",
				p.Dim(fmt.Sprintf("%2d.", n+1)), ptyTag(e), p.Bold(fmt.Sprintf("%-7d", e.Ticket.PID)), p.Dim(fmt.Sprintf("%-10s", dur(e.WaitingSeconds))), exclusiveTag(p, e), e.Ticket.CommandString(), programStatus(p, qr.Dir, e, owner, follow))
			fmt.Fprintf(w, "        %s %s\n", p.Dim("in"), orNone(e.Ticket.Dir))
			if e.Ticket.Owner != "" {
				fmt.Fprintf(w, "        %s %s\n", p.Dim("owner:"), e.Ticket.Owner)
			}
			if e.Ticket.Reason != "" {
				fmt.Fprintf(w, "        %s %s\n", p.Dim("reason:"), e.Ticket.Reason)
			}
			if e.Ticket.MaxCPUPct > 0 {
				fmt.Fprintf(w, "        %s %s\n", p.Dim("gate:"), gateText(e.Ticket))
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
	if qr.Lockless {
		// The participants above are real (liveness is per ticket); say what
		// the read could not rule out, so nobody reads a complete-looking list
		// as proof that a job which started a moment ago is absent.
		fmt.Fprintf(w, "  %s\n", p.Dim("read without the registry lock (another process holds it); a ticket created during this read may not be listed yet"))
	}
}

// paintEvent colors one lane.log line: the timestamp dim, the event verb by
// what it means (acquire and gate-pass green, enqueue cyan, release dim,
// gate-wait yellow like config, giveups and force-releases red). Unknown
// shapes pass through untouched.
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
	case "acquire", "gate-pass":
		verb = p.Green("event=" + name)
	case "enqueue":
		verb = p.Cyan("event=" + name)
	case "release", "reenter":
		verb = p.Dim("event=" + name)
	case "giveup", "force-release", "reaped", "kill", "kill-request":
		verb = p.Red("event=" + name)
	case "config", "gate-wait":
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

// gateText renders the advisory load gate of a ticket; numeric fields only,
// so there is nothing to escape beyond the fixed format.
func gateText(t lane.Ticket) string {
	s := fmt.Sprintf("cpu<%.0f%% for %s", t.MaxCPUPct, time.Duration(t.IdleForNanos))
	if t.GateDoneNano > 0 {
		s += " (passed)"
	} else if t.GateStartNano > 0 {
		s += " (waiting)"
	}
	return s
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
