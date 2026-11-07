package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/report"
)

// fakePty feeds canned chunks per read and reports the run alive as told.
type fakePty struct {
	chunks [][]byte
	calls  int
	alive  bool
}

func (f *fakePty) read(_ string, offset int64) ([]byte, int64, error) {
	var b []byte
	if f.calls < len(f.chunks) {
		b = f.chunks[f.calls]
	}
	f.calls++
	return b, offset + int64(len(b)), nil
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
func newPtyModel(owner string, fp *fakePty) Model {
	rep := ptySampleReport()
	m := New(Options{
		Killer:   &fakeKiller{},
		Load:     func() (*report.Report, error) { return rep, nil },
		Now:      func() time.Time { return time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC) },
		Dir:      "C:/state",
		Owner:    owner,
		PtyRead:  fp.read,
		PtyAlive: func(int) bool { return fp.alive },
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
	m := newPtyModel("alice", &fakePty{alive: true})
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
	m := press(newPtyModel("alice", fp), "enter")
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
// last read carries the tail and no further tick is scheduled.
func TestPtyStreamEndsWithTheRun(t *testing.T) {
	fp := &fakePty{chunks: [][]byte{[]byte("done\nexit 0\n")}, alive: false}
	m := press(newPtyModel("alice", fp), "enter")
	mm, cmd := m.Update(ptyTickMsg{})
	m = mm.(Model)
	if !strings.Contains(view(m), "job ended") {
		t.Fatalf("a dead run must say so, got:\n%s", view(m))
	}
	if !strings.Contains(view(m), "exit 0") {
		t.Fatalf("the final read must carry the tail, got:\n%s", view(m))
	}
	if cmd != nil {
		t.Fatal("no further capture read is scheduled once the run is gone")
	}
}

// The watch screen is not a side door around attach's owner gate: a foreign
// owner, a missing identity, a pipe-mode job and a waiter are named
// refusals, each staying on the queue screen.
func TestEnterPtyGates(t *testing.T) {
	cases := []struct {
		name    string
		owner   string
		psel    int
		pipeJob bool // force the selected holder out of pty mode
		want    string
	}{
		{name: "foreign owner", owner: "bob", want: "attach refused"},
		{name: "no identity", owner: "", want: "--owner or $INCODA_OWNER"},
		{name: "not a pty job", owner: "alice", pipeJob: true, want: "not a pty job"},
		{name: "still waiting", owner: "bob", psel: 1, want: "still waiting for the lane"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newPtyModel(tc.owner, &fakePty{alive: true})
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
	m := press(newPtyModel("alice", &fakePty{alive: true}), "enter")
	m = press(m, "esc")
	if m.screen != screenQueue || m.pty != nil {
		t.Fatalf("esc must return to the queue and drop the capture, got screen %v", m.screen)
	}
}
