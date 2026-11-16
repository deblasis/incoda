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

// frameTopRows is the terminal row where the overview/queue body begins,
// derived from the same header, banner and gauge strings render() uses so
// hit testing stays aligned if any of them wraps.
func (m Model) frameTopRows() int {
	w := m.layoutWidth()
	rows := lipgloss.Height(m.renderHeader(w)) + lipgloss.Height(m.renderGauge(w)) + 1
	if b := m.renderBanner(w); b != "" {
		rows += lipgloss.Height(b)
	}
	return rows
}

func (m Model) bodyStartRow() int { return m.frameTopRows() }

// bodyRows is how many lines the body may draw: the terminal minus the fixed
// frame above it, the blank line that separates it from the bottom block, and
// the toast/help block itself. A viewport that ignores this is how a selection
// ends up below the last visible row.
func (m Model) bodyRows() int {
	rows := m.height - m.frameTopRows() - 1 - lipgloss.Height(m.renderBottom(m.layoutWidth()))
	if rows < 1 {
		rows = 1
	}
	return rows
}

// inBody reports whether y is a terminal row the body actually paints. A
// hit test that only compares rows can otherwise find a queue or participant
// scrolled out of the window, or a row on the help line, and select it.
func (m Model) inBody(y int) bool {
	start := m.bodyStartRow()
	return y >= start && y < start+m.bodyRows()
}

// windowTop keeps the selected span visible in a viewport top rows tall.
// top is the caller's current scroll position; it only moves when the
// selection would leave the window, so the view does not jump a full page
// on every step.
func windowTop(total, rows, top, selStart, selEnd int) int {
	if rows < 1 {
		rows = 1
	}
	maxTop := total - rows
	if maxTop < 0 {
		maxTop = 0
	}
	if top < 0 {
		top = 0
	} else if top > maxTop {
		top = maxTop
	}
	if selStart < 0 {
		selStart = 0
	}
	if selStart > total {
		selStart = total
	}
	if selEnd > total {
		selEnd = total
	}
	if selEnd < selStart {
		selEnd = selStart
	}
	if selStart < top {
		top = selStart
	}
	if selEnd > top+rows {
		top = selEnd - rows
	}
	if top > maxTop {
		top = maxTop
	}
	if top < 0 {
		top = 0
	}
	return top
}

// overviewLineOfQueue is the full-body line the queue's row occupies. Line
// zero is the column header; grouped sections add their blank and header.
func (m Model) overviewLineOfQueue(qidx int) int {
	if m.rep == nil {
		return 0
	}
	line := 1 // the column header
	grouped := poolsGrouped(m.rep)
	prevPool := false
	for n, idx := range overviewOrder(m.rep) {
		q := &m.rep.Queues[idx]
		if grouped && (n == 0 || q.IsPool != prevPool) {
			line += 2
		}
		prevPool = q.IsPool
		if idx == qidx {
			return line
		}
		line++
	}
	return line
}

// overviewTotalLines is the height of the full overview before the viewport
// clips it: the column header, section transitions, queue rows and the
// selected queue's summary (a blank and one line).
func (m Model) overviewTotalLines() int {
	if m.rep == nil || len(m.rep.Queues) == 0 {
		return 0
	}
	line := 1
	grouped := poolsGrouped(m.rep)
	prevPool := false
	for n, idx := range overviewOrder(m.rep) {
		if grouped && (n == 0 || m.rep.Queues[idx].IsPool != prevPool) {
			line += 2
		}
		prevPool = m.rep.Queues[idx].IsPool
		line++
	}
	if m.queueAt(m.qsel) != nil {
		line += 2
	}
	return line
}

// overviewTop is the first overview line the viewport draws. The stored
// qtop is only a preference; the selected queue always wins.
func (m Model) overviewTop() int {
	total := m.overviewTotalLines()
	if total == 0 {
		return 0
	}
	sel := m.overviewLineOfQueue(m.qsel)
	return windowTop(total, m.bodyRows(), m.qtop, sel, sel+1)
}

func (m Model) overviewQueueAt(y int) (idx int, ok bool) {
	if !m.inBody(y) {
		return 0, false
	}
	top := m.overviewTop()
	for _, r := range m.overviewLayout() {
		if r.y-top == y {
			return r.idx, true
		}
	}
	return 0, false
}

// overviewQueueRow is the terminal row for queue index i in the overview.
func (m Model) overviewQueueRow(i int) int {
	top := m.overviewTop()
	for _, r := range m.overviewLayout() {
		if r.idx == i {
			return r.y - top
		}
	}
	return m.bodyStartRow() + 1 + i - top
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
	if !m.inBody(y) {
		return 0, false
	}
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
// It reads the same block list renderQueue draws, so the kind line, section
// headers and any extra description cannot drift the hit test by a row.
func (m Model) participantRowsAt() []rowHit {
	b := m.queueBody(m.layoutWidth())
	top := m.queueTop(b)
	y0 := m.bodyStartRow()
	hits := make([]rowHit, 0, len(b.starts))
	for idx, start := range b.starts {
		hits = append(hits, rowHit{y: y0 + start - top, idx: idx})
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
		if m.screen == screenPty && m.pty != nil {
			// The wheel reads the capture, not the selection behind it.
			switch mouse.Button {
			case tea.MouseWheelUp:
				m.pty.scrollBy(-3, m.ptyRows())
			case tea.MouseWheelDown:
				m.pty.scrollBy(3, m.ptyRows())
			}
			return m, nil
		}
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
		if m.screen == screenPty && m.pty != nil {
			if c, ok := m.ptyCellAt(mouse.X, mouse.Y); ok {
				m.pty.beginSel(c)
			}
			return m, nil
		}
		m, _ = m.clickAt(mouse.Y)
		return m, nil
	case tea.MouseMotionMsg:
		mouse := msg.Mouse()
		if m.screen == screenPty && m.pty != nil && m.pty.dragging {
			if c, ok := m.ptyCellAt(mouse.X, mouse.Y); ok {
				m.pty.dragSel(c)
			}
			return m, nil
		}
	case tea.MouseReleaseMsg:
		// Cell motion reports the release as a button state, not a press:
		// the drag flag is the reliable "a selection was held" signal.
		if m.screen == screenPty && m.pty != nil && m.pty.dragging {
			return m.copySelection()
		}
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
					m.ptop = 0
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
