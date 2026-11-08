package tui

import (
	"encoding/base64"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/report"
)

// The queue screen shows the root OSC 7501 record from each pty participant's
// job log on its line, resolved the way the capture view resolves the log;
// a participant without records, or a pipe job, has no status.
func TestWatchShowsProgramStatus(t *testing.T) {
	state := t.TempDir()
	jobs := filepath.Join(lane.LaneDir(state, "builds"), "jobs")
	if err := os.MkdirAll(jobs, 0o755); err != nil {
		t.Fatal(err)
	}
	msg := base64.StdEncoding.EncodeToString([]byte("linking"))
	log := "x\x1b]7501;state=working:id=job:progress=55:msg=" + msg + "\x07"
	if err := os.WriteFile(filepath.Join(jobs, "01794028541589289100-161600.out"), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := ptySampleReport() // holder 111 (…100), waiter 222 (…101, no log)
	m := New(Options{
		Killer:   &fakeKiller{},
		Load:     func() (*report.Report, error) { return rep, nil },
		Now:      func() time.Time { return time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC) },
		Dir:      state,
		PtyAlive: func(int) bool { return true },
	})
	m.width, m.height = 120, 40
	m.key = "builds"
	m.screen = screenQueue
	mm, _ := m.Update(reportMsg{rep: rep})
	v := view(mm.(Model))
	if !strings.Contains(v, "🖥️ 🔄 55%") {
		t.Fatalf("holder with records must badge the mark with its progress, got:\n%s", v)
	}
	if !strings.Contains(v, "linking") {
		t.Fatalf("the record's msg must still end the line, got:\n%s", v)
	}
	if strings.Count(v, "🔄") != 1 {
		t.Fatalf("the waiter has no log and must show nothing, got:\n%s", v)
	}
}

// The overview's one-line summary badges its marks the same way, so the
// "and what is it doing" reads without drilling in.
func TestWatchSummaryBadgesPtyMarks(t *testing.T) {
	m := statusModel(t, "linking")
	m.screen = screenOverview
	v := view(m)
	if !strings.Contains(v, "🖥️ 🔄 55% alice") {
		t.Fatalf("the summary must badge the holder's mark with its progress, got:\n%s", v)
	}
}

func statusModel(t *testing.T, msg string) Model {
	t.Helper()
	state := t.TempDir()
	jobs := filepath.Join(lane.LaneDir(state, "builds"), "jobs")
	if err := os.MkdirAll(jobs, 0o755); err != nil {
		t.Fatal(err)
	}
	log := "\x1b]7501;state=working:id=job:progress=55:msg=" + base64.StdEncoding.EncodeToString([]byte(msg)) + "\x07"
	if err := os.WriteFile(filepath.Join(jobs, "01794028541589289100-161600.out"), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := ptySampleReport()
	m := New(Options{
		Killer:   &fakeKiller{},
		Load:     func() (*report.Report, error) { return rep, nil },
		Now:      func() time.Time { return time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC) },
		Dir:      state,
		PtyAlive: func(int) bool { return true },
	})
	m.width, m.height = 120, 40
	m.key = "builds"
	m.screen = screenQueue
	mm, _ := m.Update(reportMsg{rep: rep})
	return mm.(Model)
}

// Painting never touches the log: the status was read when the report
// arrived, so a redraw (mouse motion, resize) costs no file access.
func TestViewDoesNotReadTheLog(t *testing.T) {
	m := statusModel(t, "linking")
	for _, ls := range m.logs {
		before := ls.BytesRead()
		for range 20 {
			_ = view(m)
		}
		if ls.BytesRead() != before {
			t.Fatalf("View read the log: %d -> %d", before, ls.BytesRead())
		}
	}
	if len(m.logs) == 0 {
		t.Fatal("setup: the owned pty holder must be followed")
	}
}

// At a narrow terminal the line never exceeds the width (a wrapped line
// shifts every row below it and breaks click hit-testing): the msg gives
// way first, then the whole status; the badge beside the mark stays.
func TestWatchStatusFitsNarrowWidths(t *testing.T) {
	for _, w := range []int{40, 44, 50, 60, 80} {
		m := statusModel(t, "compiling a long thing")
		m.width = w
		rows := 0
		for _, line := range strings.Split(view(m), "\n") {
			if !strings.Contains(line, "pid ") {
				continue
			}
			rows++
			if lipgloss.Width(line) > w {
				t.Errorf("w=%d: participant row is %d cells: %q", w, lipgloss.Width(line), line)
			}
		}
		if rows != 2 {
			t.Errorf("w=%d: want both participant rows, saw %d", w, rows)
		}
	}
}

func TestStatusStyleByState(t *testing.T) {
	m := New(Options{Killer: &fakeKiller{}})
	fg := func(c color.Color) string { return lipgloss.NewStyle().Foreground(c).Render("x") }
	for state, want := range map[string]string{
		"working": fg(m.st.amber), "done": fg(m.st.green), "error": fg(m.st.red),
		"blocked": fg(m.st.gold), "idle": fg(m.st.muted), "clear": fg(m.st.muted),
	} {
		if got := m.statusStyle(state).Render("x"); got != want {
			t.Errorf("%s: got %q, want %q", state, got, want)
		}
	}
}
