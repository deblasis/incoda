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
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/report"
)

type screen int

const (
	screenOverview screen = iota
	screenQueue
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
	Killer   Killer
	Load     func() (*report.Report, error)
	Now      func() time.Time
}

// participant is one row of the queue screen: a holder or a waiter.
type participant struct {
	entry   lane.Entry
	holding bool
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
	killRequestedMsg struct {
		key string
		pid int
		err error
	}
	killGoneMsg struct {
		key  string
		pid  int
		gone bool
		err  error
	}
	forceDoneMsg struct {
		key string
		pid int
		err error
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

	qsel int    // selected row in the overview
	key  string // queue open in the queue screen
	psel int    // selected participant in the queue screen

	input   textinput.Model
	pending *pendingKill
	toast   toast
	help    bool
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
	if opt.Load == nil {
		dir, version, key, events := opt.Dir, opt.Version, opt.Key, opt.Events
		opt.Load = func() (*report.Report, error) {
			keys := []string{key}
			if key == "" {
				var err error
				if keys, err = report.Keys(dir); err != nil {
					return nil, err
				}
			}
			return report.Build(dir, version, keys, events)
		}
	}
	in := textinput.New()
	in.Prompt = "reason: "
	in.Placeholder = "why this job should stop (its owner will read this)"
	in.CharLimit = 200
	m := Model{opt: opt, st: newStyles(true), input: in, width: 100, height: 30}
	if opt.Key != "" {
		m.single = true
		m.key = opt.Key
		m.screen = screenQueue
	}
	return m
}

// Run drives the model in a real terminal.
func Run(opt Options) error {
	_, err := tea.NewProgram(New(opt)).Run()
	return err
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(tea.RequestBackgroundColor, m.load(), m.tick())
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
		}
		m.clamp()
		return m, nil
	case tickMsg:
		if m.toast.text != "" && m.opt.Now().After(m.toast.until) {
			m.toast = toast{}
		}
		if m.pending != nil && !m.pending.unacked && !m.pending.forcing &&
			m.opt.Now().Sub(m.pending.since) > ackGrace {
			m.pending.unacked = true
		}
		return m, tea.Batch(m.load(), m.tick())
	case killRequestedMsg:
		if m.pending == nil || m.pending.pid != msg.pid {
			return m, nil
		}
		if msg.err != nil {
			m.say(fmt.Sprintf("kill refused: %v", msg.err), true)
			m.pending = nil
			m.screen = screenQueue
			return m, m.load()
		}
		return m, m.waitGone(msg.key, msg.pid, ackGrace)
	case killGoneMsg:
		if m.pending == nil || m.pending.pid != msg.pid {
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
		if m.pending == nil || m.pending.pid != msg.pid {
			return m, nil
		}
		if msg.err != nil {
			m.say(fmt.Sprintf("force: %v", msg.err), true)
		} else {
			m.say(fmt.Sprintf("pid %d terminated; the kernel released the lane", msg.pid), false)
		}
		m.pending = nil
		m.screen = screenQueue
		return m, m.load()
	case tea.KeyPressMsg:
		return m.key_(msg)
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
		case "k", "K", "shift+k":
			if p := m.participantAt(m.psel); p != nil {
				m.pending = &pendingKill{key: m.key, pid: p.entry.Ticket.PID, forcing: k != "k"}
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
				return m, m.force(m.pending.key, m.pending.pid, reason)
			}
			return m, m.request(m.pending.key, m.pending.pid, reason)
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
				return m, m.force(m.pending.key, m.pending.pid, m.pending.reason)
			}
		}
		return m, nil
	}
	return m, nil
}

func (m Model) request(key string, pid int, reason string) tea.Cmd {
	killer := m.opt.Killer
	return func() tea.Msg {
		return killRequestedMsg{key: key, pid: pid, err: killer.Request(key, pid, reason)}
	}
}

func (m Model) waitGone(key string, pid int, wait time.Duration) tea.Cmd {
	killer := m.opt.Killer
	return func() tea.Msg {
		gone, err := killer.Gone(key, pid, wait)
		return killGoneMsg{key: key, pid: pid, gone: gone, err: err}
	}
}

// force asks first and then terminates: even a forced kill leaves the reason
// on the ticket, so a participant that does wake up in time still learns
// why, and the log reads request then outcome either way.
func (m Model) force(key string, pid int, reason string) tea.Cmd {
	killer := m.opt.Killer
	return func() tea.Msg {
		if err := killer.Request(key, pid, reason); err != nil {
			return forceDoneMsg{key: key, pid: pid, err: err}
		}
		if gone, _ := killer.Gone(key, pid, time.Second); gone {
			return forceDoneMsg{key: key, pid: pid}
		}
		if err := killer.Force(key, pid, reason); err != nil {
			return forceDoneMsg{key: key, pid: pid, err: err}
		}
		_, _ = killer.Gone(key, pid, ackGrace)
		return forceDoneMsg{key: key, pid: pid}
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
