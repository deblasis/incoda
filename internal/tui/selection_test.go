package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// ptyStreamModel opens the capture on one canned chunk, with the clipboard
// seam replaced so a copy is a slice append, not a real clipboard write.
func ptyStreamModel(t *testing.T, stream string, clip func(string) error) Model {
	t.Helper()
	fp := &fakePty{chunks: [][]byte{[]byte(stream)}, alive: true}
	m := newPtyModel(fp)
	if clip != nil {
		m.opt.Clipboard = clip
	}
	m = press(m, "enter")
	mm, _ := m.Update(ptyTickMsg{})
	return mm.(Model)
}

// rowCol finds a needle in the painted view and returns the cell to click to
// land on its first character. The screen is read as the reader sees it.
func rowCol(t *testing.T, v, needle string) (x, y int) {
	t.Helper()
	for i, line := range strings.Split(v, "\n") {
		if at := strings.Index(ansi.Strip(line), needle); at >= 0 {
			return at, i
		}
	}
	t.Fatalf("view does not show %q:\n%s", needle, v)
	return 0, 0
}

// drag walks a selection from one cell to another the way cell motion
// reports it: press, motion with the button held, release.
func drag(m Model, fromX, fromY, toX, toY int) (Model, tea.Cmd) {
	mm, _ := m.Update(tea.MouseClickMsg{X: fromX, Y: fromY, Button: tea.MouseLeft})
	m = mm.(Model)
	mm, _ = m.Update(tea.MouseMotionMsg{X: toX, Y: toY, Button: tea.MouseLeft})
	m = mm.(Model)
	mm, cmd := m.Update(tea.MouseReleaseMsg{X: toX, Y: toY, Button: tea.MouseLeft})
	return mm.(Model), cmd
}

// finish runs the copy command and feeds its result back, the way the
// runtime would.
func finish(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		t.Fatal("a drag that selected text must return a copy command")
	}
	mm, _ := m.Update(cmd())
	return mm.(Model)
}

// A drag across rows copies from the anchor column to the end of its line,
// whole lines between, and the cursor column on the last line.
func TestPtyDragSelectionCopiesRows(t *testing.T) {
	var got []string
	m := ptyStreamModel(t, "alpha\nbravo\ncharlie\n", func(s string) error {
		got = append(got, s)
		return nil
	})
	x0, y0 := rowCol(t, view(m), "alpha")
	x1, y1 := rowCol(t, view(m), "charlie")
	m, cmd := drag(m, x0+2, y0, x1+3, y1)
	m = finish(t, m, cmd)
	if len(got) != 1 {
		t.Fatalf("one drag must write the clipboard once, got %d: %q", len(got), got)
	}
	if want := "pha\nbravo\nchar"; got[0] != want {
		t.Fatalf("selection text: want %q, got %q", want, got[0])
	}
	if v := view(m); !strings.Contains(v, "copied 3 lines, 14 chars") {
		t.Fatalf("the toast must say what was copied:\n%s", v)
	}
}

// A drag within one row copies just those cells, ANSI stripped, because the
// pane copies what the reader sees.
func TestPtyDragSelectionStripsANSI(t *testing.T) {
	var got []string
	m := ptyStreamModel(t, "\x1b[31mred\x1b[0m text\n", func(s string) error {
		got = append(got, s)
		return nil
	})
	x, y := rowCol(t, view(m), "red")
	m, cmd := drag(m, x+1, y, x+4, y)
	finish(t, m, cmd)
	if len(got) != 1 || got[0] != "ed t" {
		t.Fatalf("single-row selection: want %q, got %q", "ed t", got)
	}
	if strings.Contains(got[0], "\x1b") {
		t.Fatalf("copied text must not carry the stream's escapes: %q", got[0])
	}
}

// The selected cells paint in reverse video; the text around them keeps its
// own painting.
func TestPtySelectionPaintsInReverseVideo(t *testing.T) {
	m := ptyStreamModel(t, "alpha\nbravo\ncharlie\n", func(string) error { return nil })
	x, y := rowCol(t, view(m), "bravo")
	m, cmd := drag(m, x, y, x+4, y)
	m = finish(t, m, cmd)
	rows := strings.Split(view(m), "\n")
	if !strings.Contains(rows[y], "\x1b[7mbravo") {
		t.Fatalf("the selected row must paint in reverse video, got %q", rows[y])
	}
	if strings.Contains(rows[y-1], "\x1b[7m") {
		t.Fatalf("a row outside the selection must stay unpainted: %q", rows[y-1])
	}
}

// A click that never moved is not a copy: it clears the previous selection
// and leaves the clipboard alone.
func TestPtyClickWithoutDragClearsSelection(t *testing.T) {
	var got []string
	m := ptyStreamModel(t, "alpha\nbravo\ncharlie\n", func(s string) error {
		got = append(got, s)
		return nil
	})
	x, y := rowCol(t, view(m), "bravo")
	m, cmd := drag(m, x, y, x+3, y)
	m = finish(t, m, cmd)
	if m.pty.sel == nil {
		t.Fatal("the selection should stay painted until the next click")
	}
	mm, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	m = mm.(Model)
	mm, cmd = m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	m = mm.(Model)
	if cmd != nil {
		t.Fatal("a click without a drag must not copy")
	}
	if m.pty.sel != nil {
		t.Fatal("a click must clear the previous selection")
	}
	if len(got) != 1 {
		t.Fatalf("the clipboard must still hold the first copy only, got %q", got)
	}
}

// y re-copies the selection that is still on screen.
func TestPtyYRecopiesSelection(t *testing.T) {
	var got []string
	m := ptyStreamModel(t, "alpha\nbravo\n", func(s string) error {
		got = append(got, s)
		return nil
	})
	x, y := rowCol(t, view(m), "alpha")
	m, cmd := drag(m, x, y, x+4, y)
	m = finish(t, m, cmd)
	mm, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = finish(t, mm.(Model), cmd)
	if len(got) != 2 || got[1] != "alpha" {
		t.Fatalf("y must write the same selection again, got %q", got)
	}
}

// The first esc drops the selection; only the next one leaves the screen.
func TestPtyEscClearsSelectionBeforeLeaving(t *testing.T) {
	m := ptyStreamModel(t, "alpha\nbravo\n", func(string) error { return nil })
	x, y := rowCol(t, view(m), "alpha")
	m, _ = drag(m, x, y, x+2, y)
	m = press(m, "esc")
	if m.screen != screenPty || m.pty.sel != nil {
		t.Fatalf("esc must clear the selection and stay, got screen %v sel %v", m.screen, m.pty.sel)
	}
	m = press(m, "esc")
	if m.screen != screenQueue {
		t.Fatalf("the second esc must leave the capture, got screen %v", m.screen)
	}
}

// Scrolling moves the window, not the selection: the anchor is the stream's
// own line number, so the same text stays selected after a wheel notch.
func TestPtyScrollKeepsSelectionAnchored(t *testing.T) {
	m := ptyStreamModel(t, "alpha\nbravo\ncharlie\n", func(string) error { return nil })
	x, y := rowCol(t, view(m), "bravo")
	m, _ = drag(m, x, y, x+3, y)
	before := m.pty.selText(m.layoutWidth())
	mm, _ := m.Update(tea.MouseWheelMsg{X: x, Y: y, Button: tea.MouseWheelUp})
	m = mm.(Model)
	if m.pty.sel == nil {
		t.Fatal("scrolling must not drop the selection")
	}
	if after := m.pty.selText(m.layoutWidth()); after != before {
		t.Fatalf("the selection must stay on its text: before %q, after %q", before, after)
	}
}

// Clicks outside the capture's content rows are not selections: the help
// line and the padding under a short stream select nothing.
func TestPtyClicksOutsideThePaneAreIgnored(t *testing.T) {
	m := ptyStreamModel(t, "alpha\n", func(string) error { return nil })
	// The last screen row is the help line.
	mm, _ := m.Update(tea.MouseClickMsg{X: 2, Y: m.height - 1, Button: tea.MouseLeft})
	m = mm.(Model)
	if m.pty.sel != nil || m.pty.dragging {
		t.Fatal("a click on the help line must not start a selection")
	}
	// The row right under the last stream line is padding: the split keeps
	// one empty line after the trailing newline, so the padding starts past
	// everything window() returns.
	padRow := m.ptyContentTop() + len(m.pty.window(m.ptyRows()))
	mm, _ = m.Update(tea.MouseClickMsg{X: 2, Y: padRow, Button: tea.MouseLeft})
	m = mm.(Model)
	if m.pty.sel != nil || m.pty.dragging {
		t.Fatal("a click on the padding must not start a selection")
	}
}

// A clipboard that refuses is reported, and the text still goes to the
// terminal through OSC 52 instead of disappearing.
func TestPtyCopyFallsBackToOSC52(t *testing.T) {
	m := ptyStreamModel(t, "alpha\nbravo\n", func(string) error {
		return errTestClipboard
	})
	x, y := rowCol(t, view(m), "alpha")
	m, cmd := drag(m, x, y, x+4, y)
	if cmd == nil {
		t.Fatal("the drag must return a copy command")
	}
	mm, fallback := m.Update(cmd())
	m = mm.(Model)
	if fallback == nil {
		t.Fatal("a failed clipboard write must fall back to OSC 52")
	}
	if v := view(m); !strings.Contains(v, "clipboard unavailable") {
		t.Fatalf("the failure must be visible, got:\n%s", v)
	}
}

type testClipboardError struct{}

func (testClipboardError) Error() string { return "no clipboard here" }

var errTestClipboard = testClipboardError{}

// The painter inserts markers instead of cutting the row, so the stream's own
// colors survive after the selection exactly as the job wrote them.
func TestPtyPaintSelKeepsTheStreamsColors(t *testing.T) {
	line := "\x1b[31mabcdef\x1b[0m"
	got := ptyPaintSel(line, 1, 3)
	want := "\x1b[31ma\x1b[7mbc\x1b[27mdef\x1b[0m"
	if got != want {
		t.Fatalf("painted row: want %q, got %q", want, got)
	}
}

// A reset inside the selection turns reverse off; the painter re-arms it so
// the whole range stays visible.
func TestPtyPaintSelRearmsAfterAReset(t *testing.T) {
	got := ptyPaintSel("ab\x1b[0mcd", 0, 4)
	if n := strings.Count(got, selOn); n != 2 {
		t.Fatalf("want reverse re-armed after the reset, got %d markers in %q", n, got)
	}
	if !strings.HasSuffix(got, selOff) {
		t.Fatalf("the selection must close at its end: %q", got)
	}
}

// A wide character is painted whole: the boundaries are cell positions, and a
// marker never lands inside a grapheme.
func TestPtyPaintSelKeepsWideCharactersWhole(t *testing.T) {
	got := ptyPaintSel("你好x", 0, 1)
	want := "\x1b[7m你\x1b[27m好x"
	if got != want {
		t.Fatalf("wide selection: want %q, got %q", want, got)
	}
}
