// Package tui is the interactive `incoda watch`: an overview of every queue
// on the machine, a drill-down into one queue's holders and waiters, and a
// kill prompt that asks for a reason before it stops anything.
//
// It is a plain Bubble Tea model. Everything it needs from the outside (the
// report, the killer, the clock) comes in through Options, so the whole
// flow, overview to drill-down to kill, is exercised by tests that never
// open a terminal or a state directory.
package tui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/colorprofile"

	"github.com/deblasis/incoda/internal/joblog"
	"github.com/deblasis/incoda/internal/jobstatus"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/osc7501"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/report"
)

type screen int

const (
	screenOverview screen = iota
	screenQueue
	screenPty
	screenKillPrompt
	screenKillPending
)

// ackGrace is how long a kill waits for the participant to let go before
// offering to force it. A cooperative kill lands within one poll interval,
// so a participant still there after this is an old incoda or a wedged one.
const ackGrace = 5 * time.Second

// Options wires the model to the world.
type Options struct {
	Dir      string
	Version  string
	Key      string // start inside this queue; "" means the overview
	Interval time.Duration
	Events   int
	NoColor  bool // --no-color: paint nothing, whatever the terminal offers
	Killer   Killer
	Load     func() (*report.Report, error)
	Now      func() time.Time

	// PtyRead drains the job file past offset to its end; the default
	// reads the real capture. PtyAlive says whether the run process is
	// still there. PtyExit reads the code the run recorded beside the
	// capture, and whether there is one. All three are seams so the
	// streaming screen is testable without a terminal, a state directory
	// or a live job.
	PtyRead  func(path string, offset int64) ([]byte, int64, error)
	PtyAlive func(pid int) bool
	PtyExit  func(path string) (int, bool)

	// Clipboard writes a selected range's text to the system clipboard; the
	// default writes it directly and the screen falls back to OSC 52 when it
	// fails. A seam so selection tests never touch the real clipboard.
	Clipboard func(string) error
}

// participant is one row of the queue screen: a holder or a waiter.
type participant struct {
	entry   lane.Entry
	holding bool
}

// ptyView is the live capture of one pty job, shown the way attach streams
// it: from the start, read-only, ending by itself when the run is gone.
type ptyView struct {
	key     string
	pid     int
	path    string
	offset  int64
	buf     []byte
	lines   []string // buf split for display, last element may be partial
	total   int64    // newlines ever seen: the absolute number of the next line
	capped  bool     // the head was dropped to stay bounded
	ended   bool     // the run process is gone and the file is drained
	missed  bool     // the run ended before any byte was captured
	errText string   // last read error, shown instead of failing silently

	// scroll pauses the tail view so an older window can be read while the
	// job keeps writing; top is that window's first line, as an absolute
	// line number. scrollOn off means follow the newest line.
	scrollOn bool
	top      int64

	// exitWait counts the ticks spent after the run died waiting for the
	// exit sidecar the run writes once its own tail drain is done.
	exitWait int
	exitCode int
	haveExit bool

	// sel is the drag selection, anchored to absolute lines so it stays on
	// its text as new output moves the window; dragging says whether the
	// button is still held.
	sel      *ptySelection
	dragging bool
}

// ptyKeep bounds the capture the screen holds: the file keeps everything up
// to its own cap; the view only needs what it can show.
const ptyKeep = 64 * 1024

// ptyMaxLines bounds the display buffer even when the capture is a flood of
// very short lines: the scrollback is for reading the tail, not history.
const ptyMaxLines = 2000

// ptyTickEvery is the capture poll, the same cadence attach streams at.
const ptyTickEvery = 150 * time.Millisecond

// ptyExitTicks is how long the view keeps polling for the exit sidecar
// after the run process is gone. The run records the code once its own
// tail drain ends, which can trail the pid's death by about a second.
const ptyExitTicks = 20

// append keeps the last ptyKeep bytes, remembering the cut, so a job that
// streams for hours still renders instantly.
func (p *ptyView) append(chunk []byte) {
	if len(chunk) > 0 {
		p.buf = append(p.buf, chunk...)
		p.total += int64(bytes.Count(chunk, []byte{'\n'}))
		if len(p.buf) > ptyKeep {
			p.buf = p.buf[len(p.buf)-ptyKeep:]
			p.capped = true
		}
	}
	p.lines = strings.Split(string(p.buf), "\n")
	if len(p.lines) > ptyMaxLines {
		p.lines = p.lines[len(p.lines)-ptyMaxLines:]
	}
}

// baseAbs is the absolute line number of lines[0]. The bytes cap can cut
// the head mid-line; that fragment is still one line of the display buffer,
// and the count of newlines held is what anchors it to the stream.
func (p *ptyView) baseAbs() int64 {
	if len(p.lines) == 0 {
		return p.total
	}
	return p.total - int64(len(p.lines)-1)
}

// tailTop is the absolute line number of the first row of the window that
// shows the newest line.
func (p *ptyView) tailTop(rows int) int64 {
	if len(p.lines) == 0 {
		return p.total
	}
	top := p.baseAbs() + int64(len(p.lines)-1) - int64(rows) + 1
	if base := p.baseAbs(); top < base {
		top = base
	}
	return top
}

// scrollBy moves the window delta lines, negative back in time, and turns
// following back on when the newest line is reached again.
func (p *ptyView) scrollBy(delta, rows int) {
	if len(p.lines) == 0 || int64(len(p.lines)) <= int64(rows) {
		p.scrollOn = false
		return
	}
	top := p.tailTop(rows)
	if p.scrollOn {
		top = p.top
	}
	top += int64(delta)
	if tail := p.tailTop(rows); top >= tail {
		p.scrollOn = false
		return
	}
	if base := p.baseAbs(); top < base {
		top = base
	}
	p.top, p.scrollOn = top, true
}

// windowTop is the window's first absolute line: the tail, unless the view
// is scrolled back, then the stored top clamped to what still exists.
func (p *ptyView) windowTop(rows int) int64 {
	if len(p.lines) == 0 || rows < 1 {
		return p.baseAbs()
	}
	top := p.tailTop(rows)
	if p.scrollOn {
		top = p.top
		if tail := p.tailTop(rows); top > tail {
			top = tail
		}
		if base := p.baseAbs(); top < base {
			top = base
		}
	}
	return top
}

// window is the rows lines to draw, newest last unless the view is scrolled
// back; short content is the caller's to pad.
func (p *ptyView) window(rows int) []string {
	if len(p.lines) == 0 || rows < 1 {
		return nil
	}
	start := int(p.windowTop(rows) - p.baseAbs())
	if start < 0 {
		start = 0
	}
	end := start + rows
	if end > len(p.lines) {
		end = len(p.lines)
	}
	return p.lines[start:end]
}

type pendingKill struct {
	key    string
	pid    int
	reason string
	since  time.Time
	// unacked is set once the grace elapsed with the participant still
	// there; the screen then offers to force.
	unacked bool
	forcing bool
	// seq ties the asynchronous replies to this attempt. A wait started
	// for an earlier attempt on the same pid, then cancelled with esc,
	// would otherwise report into a fresh one.
	seq int
}

type toast struct {
	text  string
	bad   bool
	until time.Time
}

// Messages.
type (
	reportMsg struct {
		rep *report.Report
		err error
	}
	tickMsg          time.Time
	ptyTickMsg       struct{}
	killRequestedMsg struct {
		seq int
		key string
		pid int
		err error
	}
	killGoneMsg struct {
		seq  int
		key  string
		pid  int
		gone bool
		err  error
	}
	forceDoneMsg struct {
		seq  int
		key  string
		pid  int
		gone bool
		err  error
	}
)

// Model is the watch UI state.
type Model struct {
	opt    Options
	st     styles
	screen screen
	single bool // started with a key: q quits rather than going back

	rep     *report.Report
	loadErr error
	width   int
	height  int

	qsel      int    // selected row in the overview
	key       string // queue open in the queue screen
	psel      int    // selected participant in the queue screen
	pty       *ptyView
	input     textinput.Model
	pending   *pendingKill
	killSeq   int
	toast     toast
	help      bool
	lastClick clickStamp

	// logs follow each owned pty participant's job log; status is the root
	// record they gave at the last report (see refreshStatus).
	logs   map[string]*jobstatus.LogStatus
	status map[string]osc7501.Record
}

// New builds a model. Missing options get the real implementations.
func New(opt Options) Model {
	if opt.Interval <= 0 {
		opt.Interval = 2 * time.Second
	}
	if opt.Events <= 0 {
		opt.Events = 8
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Killer == nil {
		opt.Killer = NewLaneKiller(opt.Dir)
	}
	if opt.PtyRead == nil {
		opt.PtyRead = drainPtyFile
	}
	if opt.PtyAlive == nil {
		opt.PtyAlive = procinfo.Alive
	}
	if opt.PtyExit == nil {
		opt.PtyExit = joblog.ReadExit
	}
	if opt.Clipboard == nil {
		opt.Clipboard = clipboard.WriteAll
	}
	if opt.Load == nil {
		dir, version, key, events := opt.Dir, opt.Version, opt.Key, opt.Events
		opt.Load = func() (*report.Report, error) {
			var keys []string
			if key != "" {
				keys = []string{key}
			}
			return report.Build(dir, version, keys, key == "", events)
		}
	}
	in := textinput.New()
	in.Prompt = "reason: "
	in.Placeholder = "why this job should stop (its owner will read this)"
	in.CharLimit = 200
	m := Model{opt: opt, st: newStyles(true), input: in, width: 100, height: 30, logs: map[string]*jobstatus.LogStatus{}}
	if opt.Key != "" {
		m.single = true
		m.key = opt.Key
		m.screen = screenQueue
	}
	return m
}

// Run drives the model in a real terminal.
func Run(opt Options) error {
	stop := installTTYRestore()
	defer stop()
	defer restoreWatchTTY()

	var popts []tea.ProgramOption
	if opt.NoColor {
		popts = append(popts, tea.WithColorProfile(colorprofile.Ascii))
	}
	_, err := tea.NewProgram(New(opt), popts...).Run()
	return err
}

// Init asks the terminal for its background and loads once; each report
// schedules the next tick, so loads never overlap however slow the state
// directory is (see the tickMsg case).
func (m Model) Init() tea.Cmd {
	return tea.Batch(tea.RequestBackgroundColor, m.load())
}

func (m Model) load() tea.Cmd {
	return func() tea.Msg {
		rep, err := m.opt.Load()
		return reportMsg{rep: rep, err: err}
	}
}

func (m Model) tick() tea.Cmd {
	return tea.Tick(m.opt.Interval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// ptyTick schedules the next capture read. The first one fires immediately
// (zero delay), so opening a job with history fills the screen at once.
func (m Model) ptyTick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return ptyTickMsg{} })
}

// drainPtyFile reads every byte currently in the capture past offset. A
// missing file is not an error: the run creates it between acquiring the
// lane and starting the job, and until then there is nothing to show.
func drainPtyFile(path string, offset int64) ([]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, offset, nil
		}
		return nil, offset, err
	}
	defer f.Close()
	var out []byte
	buf := make([]byte, 32*1024)
	for {
		n, err := f.ReadAt(buf, offset)
		if n > 0 {
			out = append(out, buf[:n]...)
			offset += int64(n)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, offset, nil
			}
			return out, offset, err
		}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.SetWidth(max(20, m.width-14))
		return m, nil
	case tea.BackgroundColorMsg:
		m.st = newStyles(msg.IsDark())
		return m, nil
	case reportMsg:
		if msg.err != nil {
			m.loadErr = msg.err
		} else {
			m.loadErr = nil
			m.rep = msg.rep
			// The order the model sorts is the order the screen draws and
			// the cursor walks (qsel indexes this slice): pools first, then
			// project lanes, closed keys sinking within their group, each
			// group by key. A retired key is worth seeing, not worth being
			// the first row every time.
			sort.SliceStable(m.rep.Queues, func(i, j int) bool {
				qi, qj := m.rep.Queues[i], m.rep.Queues[j]
				if qi.IsPool != qj.IsPool {
					return qi.IsPool
				}
				ci, cj := qi.Config.Closed != "", qj.Config.Closed != ""
				if ci != cj {
					return !ci
				}
				return qi.Key < qj.Key
			})
		}
		m.refreshStatus(m.rep)
		m.clamp()
		// The next load is scheduled from here, not from the tick, so a
		// slow scan (many queues, a busy disk) delays the next one instead
		// of piling concurrent loads on top of it.
		return m, m.tick()
	case tickMsg:
		if m.toast.text != "" && m.opt.Now().After(m.toast.until) {
			m.toast = toast{}
		}
		if m.pending != nil && !m.pending.unacked && !m.pending.forcing &&
			m.opt.Now().Sub(m.pending.since) > ackGrace {
			m.pending.unacked = true
		}
		return m, m.load()
	case killRequestedMsg:
		if m.pending == nil || m.pending.seq != msg.seq {
			return m, nil
		}
		if msg.err != nil {
			m.say(fmt.Sprintf("kill refused: %v", msg.err), true)
			m.pending = nil
			m.screen = screenQueue
			return m, m.load()
		}
		return m, m.waitGone(msg.seq, msg.key, msg.pid, ackGrace)
	case killGoneMsg:
		if m.pending == nil || m.pending.seq != msg.seq {
			return m, nil
		}
		if msg.err != nil {
			m.say(fmt.Sprintf("kill: %v", msg.err), true)
			m.pending = nil
			m.screen = screenQueue
			return m, m.load()
		}
		if msg.gone {
			m.say(fmt.Sprintf("pid %d released the lane", msg.pid), false)
			m.pending = nil
			m.screen = screenQueue
			return m, m.load()
		}
		m.pending.unacked = true
		return m, nil
	case forceDoneMsg:
		if m.pending == nil || m.pending.seq != msg.seq {
			return m, nil
		}
		switch {
		case msg.err != nil:
			m.say(fmt.Sprintf("force: %v", msg.err), true)
		case !msg.gone:
			m.say(fmt.Sprintf("pid %d was terminated but its ticket is still held; check incoda status", msg.pid), true)
		default:
			m.say(fmt.Sprintf("pid %d terminated; the kernel released the lane", msg.pid), false)
		}
		m.pending = nil
		m.screen = screenQueue
		return m, m.load()
	case ptyTickMsg:
		if m.screen != screenPty || m.pty == nil {
			return m, nil
		}
		p := m.pty
		chunk, off, err := m.opt.PtyRead(p.path, p.offset)
		if err != nil {
			p.errText = err.Error()
		} else {
			p.errText = ""
			p.offset = off
			p.append(chunk)
		}
		// Death is checked after the drain, the order attach uses, so this
		// read (or the next one) carries the exit tail before the view ends.
		if !m.opt.PtyAlive(p.pid) {
			p.ended = true
			p.missed = len(p.buf) == 0
			if !p.haveExit && p.exitWait < ptyExitTicks {
				p.exitWait++
				if code, ok := m.opt.PtyExit(p.path); ok {
					p.exitCode, p.haveExit = code, true
				}
				if !p.haveExit {
					// The run records the code after its own tail drain;
					// the process's death can land before that write.
					return m, m.ptyTick(ptyTickEvery)
				}
			}
			return m, nil
		}
		return m, m.ptyTick(ptyTickEvery)
	case ptyCopyDone:
		m.say(ptyCopyLine(msg), msg.err != nil)
		if msg.err != nil {
			// OSC 52 is the terminal's own clipboard: best effort, and a
			// no-op on the terminals that do not implement it.
			return m, tea.SetClipboard(msg.text)
		}
		return m, nil
	case tea.KeyPressMsg:
		return m.key_(msg)
	case tea.MouseMsg:
		return m.mouse_(msg)
	}
	if m.screen == screenKillPrompt {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

// key_ routes a key press by screen. The names come from KeyPressMsg.String:
// "enter", "esc", "up", "k", "ctrl+c" and so on.
func (m Model) key_(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if k == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.screen {
	case screenOverview:
		switch k {
		case "q":
			return m, tea.Quit
		case "up", "shift+tab":
			m.qsel--
		case "down", "tab":
			m.qsel++
		case "enter", "right", "l":
			if q := m.queueAt(m.qsel); q != nil {
				m.key = q.Key
				m.psel = 0
				m.screen = screenQueue
			}
		case "r":
			return m, m.load()
		case "?":
			m.help = !m.help
		}
		m.clamp()
		return m, nil
	case screenQueue:
		switch k {
		case "q":
			if m.single {
				return m, tea.Quit
			}
			m.screen = screenOverview
		case "esc", "escape", "left", "h", "backspace":
			if m.single {
				return m, tea.Quit
			}
			m.screen = screenOverview
		case "up", "shift+tab":
			m.psel--
		case "down", "tab":
			m.psel++
		case "enter", "right", "l":
			if p := m.participantAt(m.psel); p != nil {
				return m.openPty(p)
			}
		case "k", "K", "shift+k":
			if p := m.participantAt(m.psel); p != nil {
				m.killSeq++
				m.pending = &pendingKill{key: m.key, pid: p.entry.Ticket.PID, forcing: k != "k", seq: m.killSeq}
				m.input.Reset()
				m.screen = screenKillPrompt
				return m, m.input.Focus()
			}
		case "r":
			return m, m.load()
		case "?":
			m.help = !m.help
		}
		m.clamp()
		return m, nil
	case screenPty:
		if m.pty == nil {
			m.screen = screenQueue
			return m, nil
		}
		switch k {
		case "esc", "escape", "left", "h", "backspace":
			// The first esc drops the selection; the next one leaves.
			if m.pty.sel != nil || m.pty.dragging {
				m.pty.clearSel()
				return m, nil
			}
			m.pty = nil
			m.screen = screenQueue
		case "q":
			m.pty = nil
			m.screen = screenQueue
		case "y":
			return m, m.copySelected()
		case "up", "k":
			m.pty.scrollBy(-1, m.ptyRows())
		case "down", "j":
			m.pty.scrollBy(1, m.ptyRows())
		case "pgup":
			m.pty.scrollBy(-max(m.ptyRows()-1, 1), m.ptyRows())
		case "pgdown":
			m.pty.scrollBy(max(m.ptyRows()-1, 1), m.ptyRows())
		case "home":
			m.pty.scrollBy(-ptyMaxLines, m.ptyRows())
		case "end":
			m.pty.scrollOn = false
		}
		return m, nil
	case screenKillPrompt:
		switch k {
		case "esc", "escape":
			m.pending = nil
			m.input.Blur()
			m.screen = screenQueue
			return m, nil
		case "enter":
			reason := strings.TrimSpace(m.input.Value())
			if reason == "" {
				m.say("a reason is required: the job's owner will read it", true)
				return m, nil
			}
			m.input.Blur()
			m.pending.reason = reason
			m.pending.since = m.opt.Now()
			m.screen = screenKillPending
			if m.pending.forcing {
				return m, m.force(m.pending.seq, m.pending.key, m.pending.pid, reason)
			}
			return m, m.request(m.pending.seq, m.pending.key, m.pending.pid, reason)
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	case screenKillPending:
		switch k {
		case "esc", "escape":
			if m.pending != nil && !m.pending.forcing {
				m.say(fmt.Sprintf("stopped waiting for pid %d; the request stays on its ticket", m.pending.pid), false)
				m.pending = nil
				m.screen = screenQueue
			}
		case "f", "F", "shift+f":
			if m.pending != nil && m.pending.unacked && !m.pending.forcing {
				m.pending.forcing = true
				return m, m.force(m.pending.seq, m.pending.key, m.pending.pid, m.pending.reason)
			}
		}
		return m, nil
	}
	return m, nil
}

// openPty switches to the live capture of the selected participant. The
// gates attach passes are the gates here, so the watch screen is not a side
// door around the owner rule: a non-pty job and a waiter are named refusals,
// and only the job's own owner gets the stream.
func (m Model) openPty(p *participant) (tea.Model, tea.Cmd) {
	t := p.entry.Ticket
	if !t.PTY {
		m.say(fmt.Sprintf("pid %d is not a pty job: its output goes to its run's stdout, and there is no capture to show", t.PID), true)
		return m, nil
	}
	if !p.holding {
		m.say(fmt.Sprintf("pid %d is still waiting for the lane; open it again once it holds and the job starts", t.PID), true)
		return m, nil
	}
	path, err := joblog.Path(lane.LaneDir(m.opt.Dir, m.key), p.entry.File)
	if err != nil {
		m.say(fmt.Sprintf("cannot open the job capture: %v", err), true)
		return m, nil
	}
	m.pty = &ptyView{key: m.key, pid: t.PID, path: path}
	m.screen = screenPty
	return m, m.ptyTick(0)
}

func (m Model) request(seq int, key string, pid int, reason string) tea.Cmd {
	killer := m.opt.Killer
	return func() tea.Msg {
		return killRequestedMsg{seq: seq, key: key, pid: pid, err: killer.Request(key, pid, reason)}
	}
}

func (m Model) waitGone(seq int, key string, pid int, wait time.Duration) tea.Cmd {
	killer := m.opt.Killer
	return func() tea.Msg {
		gone, err := killer.Gone(key, pid, wait)
		return killGoneMsg{seq: seq, key: key, pid: pid, gone: gone, err: err}
	}
}

// force asks first and then terminates: even a forced kill leaves the reason
// on the ticket, so a participant that does wake up in time still learns
// why, and the log reads request then outcome either way.
func (m Model) force(seq int, key string, pid int, reason string) tea.Cmd {
	killer := m.opt.Killer
	return func() tea.Msg {
		if err := killer.Request(key, pid, reason); err != nil {
			return forceDoneMsg{seq: seq, key: key, pid: pid, err: err}
		}
		if gone, _ := killer.Gone(key, pid, time.Second); gone {
			return forceDoneMsg{seq: seq, key: key, pid: pid, gone: true}
		}
		if err := killer.Force(key, pid, reason); err != nil {
			return forceDoneMsg{seq: seq, key: key, pid: pid, err: err}
		}
		gone, err := killer.Gone(key, pid, ackGrace)
		return forceDoneMsg{seq: seq, key: key, pid: pid, gone: gone, err: err}
	}
}

func (m *Model) say(text string, bad bool) {
	m.toast = toast{text: text, bad: bad, until: m.opt.Now().Add(8 * time.Second)}
}

func (m *Model) clamp() {
	n := 0
	if m.rep != nil {
		n = len(m.rep.Queues)
	}
	m.qsel = clampInt(m.qsel, n)
	m.psel = clampInt(m.psel, len(m.participants()))
}

func clampInt(i, n int) int {
	if n == 0 {
		return 0
	}
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}

func (m Model) queueAt(i int) *report.Queue {
	if m.rep == nil || i < 0 || i >= len(m.rep.Queues) {
		return nil
	}
	return &m.rep.Queues[i]
}

// current is the queue open in the queue screen, looked up by key so a
// refresh that reorders the report cannot swap it for a neighbour.
func (m Model) current() *report.Queue {
	if m.rep == nil {
		return nil
	}
	for i := range m.rep.Queues {
		if m.rep.Queues[i].Key == m.key {
			return &m.rep.Queues[i]
		}
	}
	return nil
}

func (m Model) participants() []participant {
	q := m.current()
	if q == nil {
		return nil
	}
	out := make([]participant, 0, len(q.Holders)+len(q.Waiting))
	for _, e := range q.Holders {
		out = append(out, participant{entry: e, holding: true})
	}
	for _, e := range q.Waiting {
		out = append(out, participant{entry: e})
	}
	return out
}

func (m Model) participantAt(i int) *participant {
	ps := m.participants()
	if i < 0 || i >= len(ps) {
		return nil
	}
	return &ps[i]
}
