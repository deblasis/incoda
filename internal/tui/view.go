package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/report"
	"github.com/deblasis/incoda/internal/sysinfo"
)

func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

// render lays the screen out top to bottom: header, gauge, body, then the
// toast and help pinned to the bottom of the terminal.
func (m Model) render() string {
	w := m.width
	if w < 40 {
		w = 40
	}
	var body string
	switch m.screen {
	case screenOverview:
		body = m.renderOverview(w)
	default:
		body = m.renderQueue(w)
	}
	top := lipgloss.JoinVertical(lipgloss.Left, m.renderHeader(w), m.renderGauge(w), "", body)
	bottom := m.renderBottom(w)
	// Fill so the help line sits on the last row whatever the body height.
	gap := m.height - lipgloss.Height(top) - lipgloss.Height(bottom)
	if gap < 1 {
		gap = 1
	}
	return top + strings.Repeat("\n", gap) + bottom
}

func (m Model) renderHeader(w int) string {
	st := m.st
	left := st.brand.Render("incoda") + " " + st.accent.Render("watch")
	mid := ""
	if m.rep != nil {
		mid = st.dim.Render(m.rep.Host) + "  " + st.dim.Render("state: "+m.rep.StateDirSource)
	}
	right := st.dim.Render(m.opt.Now().Format("15:04:05"))
	if m.rep != nil && m.rep.Version != "" {
		right = st.dim.Render(m.rep.Version) + "  " + right
	}
	gap := w - lipgloss.Width(left) - lipgloss.Width(mid) - lipgloss.Width(right) - 4
	if gap < 1 {
		gap = 1
	}
	return left + "  " + mid + strings.Repeat(" ", gap) + right
}

// renderGauge is the memory readout as a bar: the same gauge `status`
// prints as a sentence, painted so a machine near its limit reads red
// before anyone parses a number.
func (m Model) renderGauge(w int) string {
	st := m.st
	if m.rep == nil {
		if m.loadErr != nil {
			return st.toastErr.Render("cannot read the state directory: " + m.loadErr.Error())
		}
		return st.dim.Render("loading…")
	}
	mem := m.rep.Memory
	if !mem.HaveTotal || !mem.HaveAvailable || mem.TotalBytes == 0 {
		return st.dim.Render(mem.String())
	}
	used := mem.TotalBytes - mem.AvailableBytes
	pct := float64(used) / float64(mem.TotalBytes)
	width := 24
	if w < 80 {
		width = 12
	}
	filled := int(pct*float64(width) + 0.5)
	fill := st.gaugeFill
	if pct > 0.85 {
		fill = st.gaugeHot
	}
	bar := fill.Render(strings.Repeat("█", filled)) + st.gaugeEmpty.Render(strings.Repeat("░", width-filled))
	line := st.dim.Render("memory ") + bar + " " + st.bold.Render(fmt.Sprintf("%.0f%%", pct*100)) +
		st.dim.Render(fmt.Sprintf(" used · %s free of %s", sysinfo.Human(mem.AvailableBytes), sysinfo.Human(mem.TotalBytes)))
	if mem.HaveSwap {
		line += st.dim.Render(" · swap " + sysinfo.Human(mem.SwapUsedBytes))
	}
	return line
}

func (m Model) renderOverview(w int) string {
	st := m.st
	if m.rep == nil {
		return ""
	}
	if len(m.rep.Queues) == 0 {
		return st.dim.Render("no queues have state on this machine yet") + "\n" +
			st.dim.Render("run something: incoda run --queue builds --reason \"first\" -- <cmd>")
	}
	keyW := 5
	for _, q := range m.rep.Queues {
		keyW = max(keyW, len(q.Key))
	}
	const stateW, heldW, waitW, oldW = 13, 6, 8, 12
	descW := w - keyW - stateW - heldW - waitW - oldW - 12
	if descW < 8 {
		descW = 8
	}
	head := fmt.Sprintf("  %-*s  %-*s  %*s  %*s  %*s  %s", keyW, "QUEUE", stateW, "STATE", heldW, "HELD", waitW, "WAITING", oldW, "OLDEST WAIT", "WHAT IT GUARDS")
	lines := []string{st.colHead.Render(head)}
	for i, q := range m.rep.Queues {
		row := fmt.Sprintf("  %-*s  %-*s  %*s  %*s  %*s  %s",
			keyW, q.Key,
			stateW, plainState(q),
			heldW, fmt.Sprintf("%d/%d", len(q.Holders), q.EffectiveSlots),
			waitW, orDot(len(q.Waiting)),
			oldW, oldestWait(q),
			trunc(q.Config.Description, descW))
		if i == m.qsel {
			lines = append(lines, st.selected.Render(padRight(row, w)))
			continue
		}
		// Paint the state word only on unselected rows; the selected row
		// is one solid highlight so the eye finds it first. The badge's
		// padding is one space each side, so swapping it for the word
		// plus its neighbouring spaces keeps the columns aligned.
		painted := strings.Replace(row, " "+plainState(q)+" ", m.badge(q, false), 1)
		lines = append(lines, painted)
	}
	if q := m.queueAt(m.qsel); q != nil {
		lines = append(lines, "", m.renderQueueSummary(q, w))
	}
	return strings.Join(lines, "\n")
}

// renderQueueSummary is the one-line drill-down hint under the overview: the
// selected queue's holders, so the common question is answered without
// pressing enter.
func (m Model) renderQueueSummary(q *report.Queue, w int) string {
	st := m.st
	if q.Config.Closed != "" {
		return st.dim.Render("  closed: ") + trunc(q.Config.Closed, w-12)
	}
	if len(q.Holders) == 0 {
		if len(q.Waiting) > 0 {
			return st.dim.Render("  waiters but no holder; the next scan will hand over")
		}
		return st.dim.Render("  nobody holds " + q.Key + "; enter opens it, k inside kills")
	}
	parts := make([]string, 0, len(q.Holders))
	for _, e := range q.Holders {
		who := e.Ticket.Owner
		if who == "" {
			who = "pid " + fmt.Sprint(e.Ticket.PID)
		}
		parts = append(parts, fmt.Sprintf("%s %s", st.bold.Render(who), st.dim.Render(trunc(e.Ticket.CommandString(), 40))))
	}
	return "  " + st.dim.Render("holding: ") + strings.Join(parts, st.dim.Render(" · "))
}

func (m Model) renderQueue(w int) string {
	st := m.st
	q := m.current()
	if q == nil {
		if m.rep == nil {
			return ""
		}
		return st.dim.Render(fmt.Sprintf("queue %q has no state on this machine; it is free", m.key))
	}
	lines := []string{}
	title := st.title.Render(q.Key) + "  " + m.badge(*q, true) + "  " +
		st.dim.Render(fmt.Sprintf("%d/%d slot(s)", len(q.Holders), q.EffectiveSlots))
	if q.Config.RequireReason {
		title += "  " + st.dim.Render("reason required")
	}
	lines = append(lines, title)
	if q.Config.Description != "" {
		lines = append(lines, st.dim.Render(q.Config.Description))
	}
	if q.Config.Closed != "" {
		lines = append(lines, st.evBad.Render("closed: ")+q.Config.Closed)
	}
	if q.ConfigError != "" {
		lines = append(lines, st.evBad.Render("config unreadable: "+q.ConfigError))
	}
	lines = append(lines, "")

	ps := m.participants()
	idx := 0
	section := func(name string, n int) {
		lines = append(lines, st.colHead.Render(fmt.Sprintf("%s (%d)", name, n)))
	}
	section("HOLDING", len(q.Holders))
	if len(q.Holders) == 0 {
		lines = append(lines, st.dim.Render("  nobody"))
	}
	for _, p := range ps {
		if !p.holding {
			continue
		}
		lines = append(lines, m.renderParticipant(p, idx == m.psel, w)...)
		idx++
	}
	lines = append(lines, "")
	section("WAITING", len(q.Waiting))
	if len(q.Waiting) == 0 {
		lines = append(lines, st.dim.Render("  none"))
	}
	for _, p := range ps {
		if p.holding {
			continue
		}
		lines = append(lines, m.renderParticipant(p, idx == m.psel, w)...)
		idx++
	}
	if len(q.RecentEvents) > 0 {
		lines = append(lines, "", st.colHead.Render("RECENT"))
		for _, ev := range q.RecentEvents {
			lines = append(lines, "  "+m.paintEvent(trunc(ev, w-2)))
		}
	}
	switch m.screen {
	case screenKillPrompt:
		lines = append(lines, "", m.renderPrompt(w))
	case screenKillPending:
		lines = append(lines, "", m.renderPending(w))
	}
	return strings.Join(lines, "\n")
}

// renderParticipant is two lines per row: who and how long on the first,
// the command and directory dimmed on the second. The selected row is a
// solid highlight on the first line only, so the command stays readable.
func (m Model) renderParticipant(p participant, selected bool, w int) []string {
	st := m.st
	t := p.entry.Ticket
	when := "held " + fmtSeconds(p.entry.HeldSeconds)
	if !p.holding {
		when = "waited " + fmtSeconds(p.entry.WaitingSeconds)
	}
	first := fmt.Sprintf("  pid %-7d %-16s", t.PID, when)
	if t.Exclusive {
		first += " EXCLUSIVE"
	}
	if t.Owner != "" {
		first += "  " + t.Owner
	}
	if t.Reason != "" {
		first += "  " + t.Reason
	}
	first = trunc(first, w)
	if selected {
		first = st.selected.Render(padRight("›"+first[1:], w))
	} else {
		if t.Exclusive {
			first = strings.Replace(first, "EXCLUSIVE", st.badgeExcl.Render("EXCLUSIVE"), 1)
		}
		first = strings.Replace(first, fmt.Sprintf("pid %-7d", t.PID), st.bold.Render(fmt.Sprintf("pid %-7d", t.PID)), 1)
	}
	second := st.dim.Render("    " + trunc(t.CommandString(), w-4))
	third := st.dim.Render("    in " + trunc(orUnknown(t.Dir), w-7))
	if p.entry.PayloadError != "" {
		third = st.evBad.Render("    ticket unreadable: " + p.entry.PayloadError)
	}
	return []string{first, second, third}
}

func (m Model) renderPrompt(w int) string {
	st := m.st
	if m.pending == nil {
		return ""
	}
	verb := "kill"
	if m.pending.forcing {
		verb = "force-kill"
	}
	head := st.bold.Render(fmt.Sprintf("%s pid %d on %s", verb, m.pending.pid, m.pending.key))
	note := st.dim.Render("the owner reads this on their stderr; enter to confirm, esc to cancel")
	return st.box.Width(min(w-2, 90)).Render(lipgloss.JoinVertical(lipgloss.Left, head, m.input.View(), note))
}

func (m Model) renderPending(w int) string {
	st := m.st
	if m.pending == nil {
		return ""
	}
	p := m.pending
	elapsed := m.opt.Now().Sub(p.since).Round(100 * time.Millisecond)
	var line string
	switch {
	case p.forcing:
		line = st.accent.Render(fmt.Sprintf("terminating pid %d… ", p.pid)) + st.dim.Render(elapsed.String())
	case p.unacked:
		line = st.toastErr.Render(fmt.Sprintf("pid %d has not acknowledged after %s", p.pid, elapsed)) + "\n" +
			st.dim.Render("an incoda from before kill existed, or wedged. ") + st.bold.Render("F") + st.dim.Render(" forces it (the kernel frees the lane); ") + st.bold.Render("esc") + st.dim.Render(" leaves the request on its ticket")
	default:
		line = st.accent.Render(fmt.Sprintf("kill requested for pid %d, waiting for it to let go… ", p.pid)) + st.dim.Render(elapsed.String())
	}
	return st.box.Width(min(w-2, 90)).Render(lipgloss.JoinVertical(lipgloss.Left, line, st.dim.Render("reason: "+p.reason)))
}

func (m Model) renderBottom(w int) string {
	st := m.st
	var help string
	switch m.screen {
	case screenOverview:
		help = "↑/↓ select · enter open · r refresh · ? help · q quit"
		if m.help {
			help += "\n" + "inside a queue: k kills the selected job with a reason, K forces it · every kill is logged with who and why"
		}
	case screenQueue:
		if m.single {
			help = "↑/↓ select · k kill · K force-kill · r refresh · q quit"
		} else {
			help = "↑/↓ select · k kill · K force-kill · r refresh · esc back · q quit"
		}
	case screenKillPrompt:
		help = "type the reason · enter confirm · esc cancel"
	case screenKillPending:
		help = "esc stop waiting"
		if m.pending != nil && m.pending.unacked {
			help = "F force · esc stop waiting"
		}
	}
	lines := []string{}
	if m.toast.text != "" {
		if m.toast.bad {
			lines = append(lines, st.toastErr.Render(trunc(m.toast.text, w)))
		} else {
			lines = append(lines, st.toastOK.Render(trunc(m.toast.text, w)))
		}
	} else if m.loadErr != nil && m.rep != nil {
		lines = append(lines, st.toastErr.Render(trunc("refresh failed: "+m.loadErr.Error(), w)))
	}
	lines = append(lines, st.help.Render(lipgloss.Wrap(help, w, "")))
	return strings.Join(lines, "\n")
}

// badge paints the queue's state word. wide adds the count for a title.
func (m Model) badge(q report.Queue, wide bool) string {
	st := m.st
	s := plainState(q)
	switch {
	case q.Config.Closed != "":
		return st.badgeClosed.Render(s)
	case hasExclusive(q):
		return st.badgeExcl.Render(s)
	case q.Free:
		return st.badgeFree.Render(s)
	default:
		return st.badgeHeld.Render(s)
	}
}

// plainState is the unpainted state word, the same text the badge paints,
// so a row can be laid out by width before color is applied.
func plainState(q report.Queue) string {
	switch {
	case q.Config.Closed != "":
		return "CLOSED"
	case hasExclusive(q):
		return "EXCLUSIVE"
	case q.Free:
		return "FREE"
	default:
		return fmt.Sprintf("%d/%d HELD", len(q.Holders), q.EffectiveSlots)
	}
}

func hasExclusive(q report.Queue) bool {
	for _, e := range q.Holders {
		if e.Ticket.Exclusive {
			return true
		}
	}
	return false
}

func oldestWait(q report.Queue) string {
	oldest := 0.0
	for _, e := range q.Waiting {
		if e.WaitingSeconds > oldest {
			oldest = e.WaitingSeconds
		}
	}
	if oldest == 0 {
		return "·"
	}
	return fmtSeconds(oldest)
}

// paintEvent colors a lane.log line by its verb, the same reading as the
// plain status view: acquire green, enqueue amber, release dim, anything
// that ended badly red.
func (m Model) paintEvent(line string) string {
	st := m.st
	const key = "event="
	i := strings.Index(line, key)
	if i < 0 {
		return st.dim.Render(line)
	}
	rest := line[i+len(key):]
	name, tail := rest, ""
	if end := strings.IndexByte(rest, ' '); end >= 0 {
		name, tail = rest[:end], rest[end:]
	}
	var verb string
	switch name {
	case "acquire":
		verb = st.evAcquire.Render(key + name)
	case "enqueue":
		verb = st.evEnqueue.Render(key + name)
	case "giveup", "force-release", "reaped", "kill", "kill-request":
		verb = st.evBad.Render(key + name)
	case "config":
		verb = st.evConfig.Render(key + name)
	default:
		verb = st.evRelease.Render(key + name)
	}
	stamp := line[:i]
	if len(stamp) >= 20 {
		stamp = st.dim.Render(stamp[:19]) + stamp[19:]
	}
	return stamp + verb + st.dim.Render(tail)
}

func fmtSeconds(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second))
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}

func orDot(n int) string {
	if n == 0 {
		return "·"
	}
	return fmt.Sprint(n)
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(unknown directory)"
	}
	return s
}

// trunc cuts plain text to n cells with an ellipsis. It runs before any
// style is applied, so a byte count of runes is the right measure.
func trunc(s string, n int) string {
	if n <= 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func padRight(s string, w int) string {
	if pad := w - lipgloss.Width(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

var _ = lane.Entry{}
