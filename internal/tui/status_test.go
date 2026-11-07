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
	if !strings.Contains(v, "[working 55%] linking") {
		t.Fatalf("holder with records must show its status, got:\n%s", v)
	}
	if strings.Count(v, "[working") != 1 {
		t.Fatalf("the waiter has no log and must show nothing, got:\n%s", v)
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
