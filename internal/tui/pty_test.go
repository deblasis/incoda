package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/report"
)

// fakePty feeds canned chunks per read and reports the run alive as told;
// exit is the code the run recorded beside the capture, when there is one.
type fakePty struct {
	chunks [][]byte
	calls  int
	alive  bool
	exit   func() (int, bool)
}

func (f *fakePty) read(_ string, offset int64) ([]byte, int64, error) {
	var b []byte
	if f.calls < len(f.chunks) {
		b = f.chunks[f.calls]
	}
	f.calls++
	return b, offset + int64(len(b)), nil
}

func (f *fakePty) exitCode() (int, bool) {
	if f.exit == nil {
		return 0, false
	}
	return f.exit()
}

func ptySampleReport() *report.Report {
	h := lane.Entry{Holding: true, HeldSeconds: 10, File: "01794028541589289100-161600.ticket",
		Ticket: lane.Ticket{PID: 111, PTY: true, Owner: "alice", Command: []string{"python", "-u", "smoke.py"}, Dir: "C:/t"}}
	w := lane.Entry{WaitingSeconds: 5, File: "01794028541589289101-161700.ticket",
		Ticket: lane.Ticket{PID: 222, PTY: true, Owner: "bob", Command: []string{"zig", "build"}, Dir: "C:/t"}}
	return &report.Report{Schema: 1, Version: "v0.8.0-test", Host: "box", StateDirSource: "platform default",
		Queues: []report.Queue{{Key: "builds", Exists: true, EffectiveSlots: 1,
			Holders: []lane.Entry{h}, Waiting: []lane.Entry{w}}}}
}

// newPtyModel starts inside the builds queue, one pty holder owned by alice
// selected, so Enter is the only key between the model and the capture.
func newPtyModel(fp *fakePty) Model {
	rep := ptySampleReport()
	m := New(Options{
		Killer:   &fakeKiller{},
		Load:     func() (*report.Report, error) { return rep, nil },
		Now:      func() time.Time { return time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC) },
		Dir:      "C:/state",
		PtyRead:  fp.read,
		PtyAlive: func(int) bool { return fp.alive },
		PtyExit:  func(string) (int, bool) { return fp.exitCode() },
	})
	m.width, m.height = 120, 40
	m.key = "builds"
	m.screen = screenQueue
	mm, _ := m.Update(reportMsg{rep: rep})
	return mm.(Model)
}

// The queue screen marks pty jobs, the same mark status paints, so both
// views answer "which jobs can I open" the same way.
func TestWatchRowsMarkPtyJobs(t *testing.T) {
	m := newPtyModel(&fakePty{alive: true})
	v := view(m)
	if strings.Count(v, "🖥️") < 2 {
		t.Fatalf("holder and waiter rows must both carry the mark, got:\n%s", v)
	}
}

// Enter opens the selected pty job's capture and streams it: the first tick
// fills the screen with the file's bytes, and the next tick is scheduled
// while the run is alive.
func TestEnterStreamsPtyJob(t *testing.T) {
	fp := &fakePty{chunks: [][]byte{[]byte("building\n")}, alive: true}
	m := press(newPtyModel(fp), "enter")
	if m.screen != screenPty || m.pty == nil {
		t.Fatalf("enter on a pty holder must open the capture, got screen %v", m.screen)
	}
	mm, cmd := m.Update(ptyTickMsg{})
	m = mm.(Model)
	if !strings.Contains(view(m), "building") {
		t.Fatalf("the capture must be on screen, got:\n%s", view(m))
	}
	if cmd == nil {
		t.Fatal("a live run must schedule the next capture read")
	}
}

// The stream ends by itself when the run is gone, the way attach ends: the
// last read carries the tail, the head says how the job ended, and no
// further tick is scheduled.
func TestPtyStreamEndsWithTheRun(t *testing.T) {
	fp := &fakePty{chunks: [][]byte{[]byte("done\nexit 0\n")}, alive: false,
		exit: func() (int, bool) { return 0, true }}
	m := press(newPtyModel(fp), "enter")
	mm, cmd := m.Update(ptyTickMsg{})
	m = mm.(Model)
	v := view(m)
	if !strings.Contains(v, "job ended") {
		t.Fatalf("a dead run must say so, got:\n%s", v)
	}
	if !strings.Contains(v, "exit code 0") {
		t.Fatalf("the head must carry the job's exit code, got:\n%s", v)
	}
	if !strings.Contains(v, "exit 0") {
		t.Fatalf("the final read must carry the tail, got:\n%s", v)
	}
	if cmd != nil {
		t.Fatal("no further capture read is scheduled once the run is gone")
	}
}

// A non-zero code is the viewer's whole point for watching the ending.
func TestPtyStreamShowsNonZeroExitCode(t *testing.T) {
	fp := &fakePty{chunks: [][]byte{[]byte("boom\n")}, alive: false,
		exit: func() (int, bool) { return 3, true }}
	m := press(newPtyModel(fp), "enter")
	mm, _ := m.Update(ptyTickMsg{})
	if v := view(mm.(Model)); !strings.Contains(v, "exit code 3") {
		t.Fatalf("a failed job must show its code, got:\n%s", v)
	}
}

// The run records the code after its own tail drain, which can trail the
// process's death: the view polls for it, then stops.
func TestPtyWaitsForTheExitSidecar(t *testing.T) {
	code, have := 0, false
	fp := &fakePty{chunks: [][]byte{[]byte("tail\n")}, alive: false,
		exit: func() (int, bool) { return code, have }}
	m := press(newPtyModel(fp), "enter")
	mm, cmd := m.Update(ptyTickMsg{})
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("the view must keep polling while the exit code is not written yet")
	}
	if !strings.Contains(view(m), "job ended") {
		t.Fatalf("the view still says the job ended while it waits, got:\n%s", view(m))
	}
	code, have = 7, true
	mm, cmd = m.Update(ptyTickMsg{})
	m = mm.(Model)
	if cmd != nil {
		t.Fatal("the code landed: no further tick")
	}
	if v := view(m); !strings.Contains(v, "exit code 7") {
		t.Fatalf("the late code must show, got:\n%s", v)
	}
}

// A run from before the sidecar existed never writes one: after the grace
// the view settles on "ended" instead of polling forever.
func TestPtyStopsWaitingForAnAbsentExitSidecar(t *testing.T) {
	m := press(newPtyModel(&fakePty{alive: false}), "enter")
	stopped := false
	for i := 0; i < ptyExitTicks+2; i++ {
		mm, cmd := m.Update(ptyTickMsg{})
		m = mm.(Model)
		if cmd == nil {
			stopped = true
			break
		}
	}
	if !stopped {
		t.Fatal("the view must stop polling for an exit code that will never come")
	}
	if v := view(m); !strings.Contains(v, "job ended") || strings.Contains(v, "exit code") {
		t.Fatalf("a job without a recorded code must say so without inventing one, got:\n%s", v)
	}
}

// The capture is a window of fixed height: the newest lines fill it, a
// short capture is padded, and every row fits the width so a long line
// cannot wrap and displace the frame.
func TestPtyWindowIsFixed(t *testing.T) {
	var b strings.Builder
	b.WriteString(strings.Repeat("wide ", 60) + "\n")
	for i := 1; i <= 100; i++ {
		b.WriteString("line " + fmt.Sprintf("%03d", i) + "\n")
	}
	fp := &fakePty{chunks: [][]byte{[]byte(b.String())}, alive: true}
	m := press(newPtyModel(fp), "enter")
	mm, _ := m.Update(ptyTickMsg{})
	m = mm.(Model)
	body := m.renderPty(m.width)
	rows := m.ptyRows()
	if want := 1 + rows; strings.Count(body, "\n")+1 != want {
		t.Fatalf("the capture body must be the head plus exactly %d rows, got %d:\n%s", rows, strings.Count(body, "\n")+1, body)
	}
	for _, ln := range strings.Split(body, "\n") {
		if w := lipgloss.Width(ln); w > m.width {
			t.Fatalf("a rendered row is %d cells wide on a %d-cell screen: %q", w, m.width, ln)
		}
	}
	if !strings.Contains(body, "line 100") {
		t.Fatalf("the newest line must be on screen, got:\n%s", body)
	}
	if strings.Contains(body, "line 001\n") {
		t.Fatalf("the oldest line must be off screen in a 100-line capture, got:\n%s", body)
	}
}

// Scrolling reads back through the bounded tail, and new output does not
// drag the window away from the reader; End follows the tail again.
func TestPtyScrollsBackAndFreezes(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		b.WriteString("line " + fmt.Sprintf("%03d", i) + "\n")
	}
	fp := &fakePty{chunks: [][]byte{[]byte(b.String())}, alive: true}
	m := press(newPtyModel(fp), "enter")
	mm, _ := m.Update(ptyTickMsg{})
	m = mm.(Model)

	m = press(m, "pgup")
	body := m.renderPty(m.width)
	if !strings.Contains(body, "line 033") {
		t.Fatalf("PgUp must page back to older lines, got:\n%s", body)
	}
	if strings.Contains(body, "line 100") {
		t.Fatalf("the newest line must leave the window when scrolled back, got:\n%s", body)
	}
	// The job keeps writing; the reader's window must not slide.
	fp.chunks = append(fp.chunks, []byte("line 101\nline 102\nline 103\n"))
	mm, _ = m.Update(ptyTickMsg{})
	m = mm.(Model)
	if body2 := m.renderPty(m.width); !strings.Contains(body2, "line 033") {
		t.Fatalf("new output must not drag a scrolled window away, got:\n%s", body2)
	}

	m = press(m, "end")
	if body := m.renderPty(m.width); !strings.Contains(body, "line 103") {
		t.Fatalf("End must follow the tail again, got:\n%s", body)
	}
}

// The capture view is open to everyone, so the only named refusals left
// are the job's own shape: a pipe-mode job and a waiter, each staying on
// the queue screen. (Who may TYPE at the job is answer's gate, not the
// viewer's.)
func TestEnterPtyGates(t *testing.T) {
	cases := []struct {
		name    string
		psel    int
		pipeJob bool // force the selected holder out of pty mode
		want    string
	}{
		{name: "not a pty job", pipeJob: true, want: "not a pty job"},
		{name: "still waiting", psel: 1, want: "still waiting for the lane"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newPtyModel(&fakePty{alive: true})
			m.psel = tc.psel
			if tc.pipeJob {
				q := m.rep.Queues[0]
				h := q.Holders[0]
				h.Ticket.PTY = false
				q.Holders[0] = h
			}
			m = press(m, "enter")
			if m.screen != screenQueue || m.pty != nil {
				t.Fatalf("%s must not open the capture", tc.name)
			}
			if !strings.Contains(view(m), tc.want) {
				t.Fatalf("%s refusal must name it (%q), got:\n%s", tc.name, tc.want, view(m))
			}
		})
	}
}

// Leaving the capture drops it: the next Enter starts from the newly
// selected job's own capture, not the previous one's tail.
func TestEscLeavesTheCapture(t *testing.T) {
	m := press(newPtyModel(&fakePty{alive: true}), "enter")
	m = press(m, "esc")
	if m.screen != screenQueue || m.pty != nil {
		t.Fatalf("esc must return to the queue and drop the capture, got screen %v", m.screen)
	}
}
