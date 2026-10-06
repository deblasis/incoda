package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/deblasis/incoda/internal/report"
)

func (m Model) layoutWidth() int {
	w := m.width
	if w < 40 {
		w = 40
	}
	return w
}

// bodyStartRow is the terminal row where the overview/queue body begins,
// derived from the same header, banner and gauge strings render() uses so
// hit testing stays aligned if any of them wraps.
func (m Model) bodyStartRow() int {
	w := m.layoutWidth()
	rows := lipgloss.Height(m.renderHeader(w)) + lipgloss.Height(m.renderGauge(w)) + 1
	if b := m.renderBanner(w); b != "" {
		rows += lipgloss.Height(b)
	}
	return rows
}

// overviewQueueAt maps a terminal row to a queue index in the overview.
// The walk is the same one renderOverview draws, so a click lands on the
// queue whose row the eye sees, headers included.
func (m Model) overviewQueueAt(y int) (idx int, ok bool) {
	for _, r := range m.overviewLayout() {
		if r.y == y {
			return r.idx, true
		}
	}
	return 0, false
}

// overviewQueueRow is the terminal row for queue index i in the overview.
func (m Model) overviewQueueRow(i int) int {
	for _, r := range m.overviewLayout() {
		if r.idx == i {
			return r.y
		}
	}
	return m.bodyStartRow() + 1 + i
}

// overviewRow is one queue's place on screen: the terminal row its line
// starts on, and the queue's index in rep.Queues.
type overviewRow struct{ y, idx int }

// overviewLayout walks the overview the way renderOverview draws it: the
// column header, then (when the pools system is present) a blank line and a
// POOLS section header, the pools, a blank line and a PROJECT LANES header,
// and the lanes. Rendering and hit testing both go through this walk so
// they can never disagree.
func (m Model) overviewLayout() []overviewRow {
	if m.rep == nil || len(m.rep.Queues) == 0 {
		return nil
	}
	y := m.bodyStartRow() + 1 // below the column header
	var out []overviewRow
	grouped := poolsGrouped(m.rep)
	order := overviewOrder(m.rep)
	for n, idx := range order {
		if grouped {
			// A section transition draws two lines — the blank and the
			// header — before the section's first queue row.
			if n == 0 || m.rep.Queues[idx].IsPool != m.rep.Queues[order[n-1]].IsPool {
				y += 2
			}
		}
		out = append(out, overviewRow{y: y, idx: idx})
		y++
	}
	return out
}

// overviewOrder is the display order of rep.Queues: pools first, then
// project lanes, each group in the report's (alphabetical) order. Without
// pools on the machine the report order stands as it is.
func overviewOrder(rep *report.Report) []int {
	order := make([]int, len(rep.Queues))
	for i := range rep.Queues {
		order[i] = i
	}
	if !poolsGrouped(rep) {
		return order
	}
	var pools, lanes []int
	for i, q := range rep.Queues {
		if q.IsPool {
			pools = append(pools, i)
		} else {
			lanes = append(lanes, i)
		}
	}
	return append(pools, lanes...)
}

// poolsGrouped says the machine carries the pools system: at least one pool
// or one linked lane. A state directory from before pools has neither, and
// its overview stays one flat list.
func poolsGrouped(rep *report.Report) bool {
	for _, q := range rep.Queues {
		if q.IsPool || len(q.Config.Pools) > 0 {
			return true
		}
	}
	return false
}

// participantHit maps a terminal row to a participant index on the queue
// screen. All three lines of each block count as the same row.
func (m Model) participantHit(y int) (idx int, ok bool) {
	for _, h := range m.participantRowsAt() {
		if y >= h.y && y < h.y+3 {
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
	// Modal screens: keyboard only; clicks must not move selection behind.
	if m.screen == screenKillPrompt || m.screen == screenKillPending {
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
