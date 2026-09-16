package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// bodyStartRow is the terminal row where the overview/queue body begins:
// header, gauge, then a blank line.
func (m Model) bodyStartRow() int {
	return 3
}

// overviewQueueAt maps a terminal row to a queue index in the overview.
func (m Model) overviewQueueAt(y int) (idx int, ok bool) {
	if m.rep == nil || len(m.rep.Queues) == 0 {
		return 0, false
	}
	rel := y - m.bodyStartRow() - 1 // skip the column header
	if rel < 0 || rel >= len(m.rep.Queues) {
		return 0, false
	}
	return rel, true
}

// overviewQueueRow is the terminal row for queue index i in the overview.
func (m Model) overviewQueueRow(i int) int {
	return m.bodyStartRow() + 1 + i
}

// participantHit maps a terminal row to a participant index on the queue
// screen. Only the first line of each participant block is clickable.
func (m Model) participantHit(y int) (idx int, ok bool) {
	for _, h := range m.participantRowsAt() {
		if y == h.y {
			return h.idx, true
		}
	}
	return 0, false
}

type rowHit struct {
	y   int
	idx int
}

// participantRowsAt returns the terminal row of each selectable participant.
func (m Model) participantRowsAt() []rowHit {
	q := m.current()
	if q == nil {
		return nil
	}
	y := m.bodyStartRow()
	y++ // title
	if q.Config.Description != "" {
		y++
	}
	if q.Config.Closed != "" {
		y++
	}
	if q.ConfigError != "" {
		y++
	}
	y++ // blank before sections

	var hits []rowHit
	idx := 0

	y++ // HOLDING header
	if len(q.Holders) == 0 {
		y++ // "nobody"
	} else {
		for range q.Holders {
			hits = append(hits, rowHit{y: y, idx: idx})
			y += 3
			idx++
		}
	}

	y++ // blank between sections

	y++ // WAITING header
	if len(q.Waiting) == 0 {
		y++
	} else {
		for range q.Waiting {
			hits = append(hits, rowHit{y: y, idx: idx})
			y += 3
			idx++
		}
	}
	return hits
}

const doubleClickWindow = 400 * time.Millisecond

func (m Model) mouse_(msg tea.MouseMsg) (Model, tea.Cmd) {
	// Kill prompt: let bubbles/textinput handle clicks for cursor placement.
	if m.screen == screenKillPrompt {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	// Pending and other modal-ish screens: keyboard only.
	if m.screen == screenKillPending {
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		mouse := msg.Mouse()
		switch mouse.Button {
		case tea.MouseWheelUp:
			return m.nudgeSelection(-1), nil
		case tea.MouseWheelDown:
			return m.nudgeSelection(1), nil
		}
	case tea.MouseClickMsg:
		mouse := msg.Mouse()
		if mouse.Button != tea.MouseLeft {
			return m, nil
		}
		m, _ = m.clickAt(mouse.Y)
		return m, nil
	}
	return m, nil
}

func (m Model) nudgeSelection(delta int) Model {
	switch m.screen {
	case screenOverview:
		m.qsel += delta
	case screenQueue:
		m.psel += delta
	}
	m.clamp()
	return m
}

func (m Model) clickAt(y int) (Model, tea.Cmd) {
	now := m.opt.Now()
	switch m.screen {
	case screenOverview:
		if idx, ok := m.overviewQueueAt(y); ok {
			double := m.lastClick.row == y && now.Sub(m.lastClick.at) <= doubleClickWindow
			m.lastClick = clickStamp{at: now, row: y}
			m.qsel = idx
			if double {
				if q := m.queueAt(m.qsel); q != nil {
					m.key = q.Key
					m.psel = 0
					m.screen = screenQueue
				}
			}
			m.clamp()
		}
	case screenQueue:
		if idx, ok := m.participantHit(y); ok {
			m.psel = idx
			m.clamp()
		}
	}
	return m, nil
}

type clickStamp struct {
	at  time.Time
	row int
}
