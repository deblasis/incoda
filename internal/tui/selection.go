package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/parser"
)

// ptyCell is one cell of the capture pane: the stream's own line number and
// a column in cells of that line as the pane clips it. Anchoring to absolute
// lines keeps a selection on its text while the job keeps writing and the
// window moves under it.
type ptyCell struct {
	line int64
	col  int
}

// ptySelection is the drag in progress or the one that was just copied:
// anchor is where the button went down, cursor where it is now.
type ptySelection struct {
	anchor, cursor ptyCell
}

// ptyCopyDone carries a finished clipboard write back to the update loop, so
// a broken clipboard cannot stall the screen.
type ptyCopyDone struct {
	lines int
	chars int
	text  string
	err   error
}

// ptyContentTop is the terminal row of the window's first content line: the
// frame above the pane (bodyStartRow, shared with the table screens), the
// capture's head line, then its notes. Hit testing and render() both go
// through it, so a click lands on the row it paints.
func (m Model) ptyContentTop() int {
	n := m.bodyStartRow() + 1 // the pane's head line
	n += len(m.ptyNotes(m.layoutWidth()))
	return n
}

// ptyCellAt maps a terminal cell to the pane's own coordinates; false when
// the point is outside the content or on the padding under a short stream.
func (m Model) ptyCellAt(x, y int) (ptyCell, bool) {
	if m.pty == nil {
		return ptyCell{}, false
	}
	w, rows := m.layoutWidth(), m.ptyRows()
	i := y - m.ptyContentTop()
	if i < 0 || i >= rows || i >= len(m.pty.window(rows)) {
		return ptyCell{}, false
	}
	col := clampRange(x, 0, w-1)
	return ptyCell{line: m.pty.windowTop(rows) + int64(i), col: col}, true
}

// beginSel starts a selection at c; the drag flag distinguishes a held button
// from the idle releases cell motion never reports.
func (p *ptyView) beginSel(c ptyCell) {
	p.sel = &ptySelection{anchor: c, cursor: c}
	p.dragging = true
}

// dragSel extends the selection to c, only while the button is held.
func (p *ptyView) dragSel(c ptyCell) {
	if p.sel == nil || !p.dragging {
		return
	}
	p.sel.cursor = c
}

// clearSel drops the selection and any drag.
func (p *ptyView) clearSel() {
	p.sel, p.dragging = nil, false
}

// endSel ends a drag and reports the selected text. A click that never moved
// is not a copy: it clears instead.
func (p *ptyView) endSel(w int) (string, bool) {
	p.dragging = false
	if p.sel == nil {
		return "", false
	}
	if p.sel.anchor == p.sel.cursor {
		p.clearSel()
		return "", false
	}
	return p.selText(w), true
}

// selRange orders the two ends so a drag up or left reads like a drag down.
func (p *ptyView) selRange() (a, b ptyCell, ok bool) {
	if p.sel == nil {
		return ptyCell{}, ptyCell{}, false
	}
	a, b = p.sel.anchor, p.sel.cursor
	if a.line > b.line || (a.line == b.line && a.col > b.col) {
		a, b = b, a
	}
	return a, b, true
}

// selSpan is the selected cell range [left, right) on one absolute line,
// clamped to w cells; ok is false when the line is outside the selection or
// the range is empty.
func (p *ptyView) selSpan(line int64, w int) (left, right int, ok bool) {
	a, b, ok := p.selRange()
	if !ok || line < a.line || line > b.line {
		return 0, 0, false
	}
	right = w
	if line == a.line {
		left = a.col
	}
	if line == b.line {
		right = b.col + 1
	}
	left = clampRange(left, 0, w)
	right = clampRange(right, 0, w)
	if right <= left {
		return 0, 0, false
	}
	return left, right, true
}

// selText is the clipboard text of the selection: every line clipped the way
// the pane clips it, cut on grapheme boundaries, stripped of the stream's own
// ANSI, trailing spaces removed, trailing blank lines dropped.
func (p *ptyView) selText(w int) string {
	a, b, ok := p.selRange()
	if !ok {
		return ""
	}
	base := p.baseAbs()
	out := make([]string, 0, b.line-a.line+1)
	for ln := a.line; ln <= b.line; ln++ {
		left, right, ok := p.selSpan(ln, w)
		var seg string
		if ok {
			if idx := int(ln - base); idx >= 0 && idx < len(p.lines) {
				clipped := ptyClip(p.lines[idx], w)
				lw := ansi.StringWidth(clipped)
				left, right = clampRange(left, 0, lw), clampRange(right, 0, lw)
				if right > left {
					seg = ansi.Strip(ansi.Cut(clipped, left, right))
				}
			}
		}
		out = append(out, strings.TrimRight(seg, " "))
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// selOn and selOff are the selection's attributes, reverse video, emitted by
// hand: the row is not restyled, so the stream keeps its own colors, and a
// reset inside the range is followed by reverse again.
const (
	selOn  = "\x1b[7m"
	selOff = "\x1b[27m"
)

// ptyPaintSel paints the cells [left, right) of one clipped row in reverse
// video. Markers go in at grapheme starts, so a wide character is painted
// whole; the stream's own sequences pass through untouched, which keeps the
// colors after the selection exactly as the job wrote them.
func ptyPaintSel(line string, left, right int) string {
	if right <= left {
		return line
	}
	var b strings.Builder
	b.Grow(len(line) + 2*len(selOn) + len(selOff))
	p := ansi.GetParser()
	defer ansi.PutParser(p)
	state := byte(parser.GroundState)
	cell, inside := 0, false
	for i := 0; i < len(line); {
		seq, width, n, next := ansi.DecodeSequence(line[i:], state, p)
		if n <= 0 {
			break // no progress would spin; keep what was written
		}
		state = next
		if width > 0 {
			want := cell >= left && cell < right
			if want != inside {
				if want {
					b.WriteString(selOn)
				} else {
					b.WriteString(selOff)
				}
				inside = want
			}
		}
		b.WriteString(seq)
		if width > 0 {
			cell += width
		}
		if inside && isSGR(seq) {
			b.WriteString(selOn) // the stream reset the attributes under us
		}
		i += n
	}
	if inside {
		b.WriteString(selOff)
	}
	return b.String()
}

// isSGR reports whether a decoded sequence is a CSI ... m attribute change.
func isSGR(seq string) bool {
	return len(seq) >= 3 && seq[0] == 0x1b && seq[1] == '[' && seq[len(seq)-1] == 'm'
}

// copySelection copies the drag that just ended; a click that did not move
// copies nothing.
func (m Model) copySelection() (Model, tea.Cmd) {
	w := m.layoutWidth()
	text, ok := m.pty.endSel(w)
	if !ok || text == "" {
		return m, nil
	}
	return m, m.copyText(text)
}

// copySelected re-copies a selection that is still on screen.
func (m Model) copySelected() tea.Cmd {
	if m.pty == nil {
		return nil
	}
	text := m.pty.selText(m.layoutWidth())
	if text == "" {
		return nil
	}
	return m.copyText(text)
}

// copyText writes text through the clipboard seam off the update path.
func (m Model) copyText(text string) tea.Cmd {
	write := m.opt.Clipboard
	return func() tea.Msg {
		return ptyCopyDone{lines: strings.Count(text, "\n") + 1, chars: utf8.RuneCountInString(text), text: text, err: write(text)}
	}
}

// ptyCopyLine is the toast for a finished clipboard write.
func ptyCopyLine(msg ptyCopyDone) string {
	if msg.err != nil {
		return fmt.Sprintf("clipboard unavailable (%v); sending the text to the terminal instead", msg.err)
	}
	plural := "s"
	if msg.lines == 1 {
		plural = ""
	}
	return fmt.Sprintf("copied %d line%s, %d chars", msg.lines, plural, msg.chars)
}

// clampRange holds v inside [lo, hi]; an inverted range collapses to lo.
func clampRange(v, lo, hi int) int {
	if hi < lo {
		hi = lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
