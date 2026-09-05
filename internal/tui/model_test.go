package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/report"
)

// fakeKiller records what the prompt asked for and answers as told.
type fakeKiller struct {
	requests []string // "key/pid/reason"
	forced   []int
	gone     bool
	reqErr   error
}

func (k *fakeKiller) Request(key string, pid int, reason string) error {
	k.requests = append(k.requests, key+"/"+itoa(pid)+"/"+reason)
	return k.reqErr
}
func (k *fakeKiller) Gone(string, int, time.Duration) (bool, error) { return k.gone, nil }
func (k *fakeKiller) Force(_ string, pid int, _ string) error {
	k.forced = append(k.forced, pid)
	return nil
}

func itoa(i int) string {
	return strings.TrimSpace(strings.Repeat(" ", 0) + string(rune('0'+i/100)) + string(rune('0'+i/10%10)) + string(rune('0'+i%10)))
}

func sampleReport() *report.Report {
	h := lane.Entry{Holding: true, HeldSeconds: 250, Ticket: lane.Ticket{PID: 111, Slots: 2, Owner: "sess-a", Reason: "zig build", Command: []string{"zig", "build"}, Dir: "C:/wt/a"}}
	w := lane.Entry{WaitingSeconds: 61, Ticket: lane.Ticket{PID: 222, Slots: 2, Owner: "sess-b", Command: []string{"dotnet", "test"}, Dir: "C:/wt/b"}}
	return &report.Report{
		Schema: 1, Version: "v0.3.0-test", Host: "box", StateDirSource: "platform default",
		Queues: []report.Queue{
			{Key: "wintty-build", Exists: true, EffectiveSlots: 2, Config: lane.Config{Slots: 2, Description: "CPU and RAM"},
				Holders: []lane.Entry{h}, Waiting: []lane.Entry{w}, RecentEvents: []string{"2026-09-05 10:00:00 queue=wintty-build event=acquire pid=111 cmd=zig build"}},
			{Key: "wintty-desktop", Exists: true, EffectiveSlots: 1, Free: true, Config: lane.Config{Description: "the interactive desktop"},
				Holders: []lane.Entry{}, Waiting: []lane.Entry{}},
			{Key: "wintty", Exists: true, EffectiveSlots: 1, Free: true, Config: lane.Config{Closed: "retired: use wintty-build or wintty-desktop"},
				Holders: []lane.Entry{}, Waiting: []lane.Entry{}},
		},
	}
}

func newTestModel(k Killer) Model {
	rep := sampleReport()
	now := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	m := New(Options{
		Killer: k,
		Load:   func() (*report.Report, error) { return rep, nil },
		Now:    func() time.Time { return now },
	})
	m.width, m.height = 120, 40
	mm, _ := m.Update(reportMsg{rep: rep})
	return mm.(Model)
}

func press(m Model, keys ...string) Model {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "up":
			msg = tea.KeyPressMsg{Code: tea.KeyUp}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		default:
			r := []rune(k)
			msg = tea.KeyPressMsg{Code: r[0], Text: k}
		}
		mm, _ := m.Update(msg)
		m = mm.(Model)
	}
	return m
}

func typeText(m Model, s string) Model {
	for _, r := range s {
		mm, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = mm.(Model)
	}
	return m
}

func view(m Model) string { return m.View().Content }

func TestOverviewShowsEveryQueueAndItsState(t *testing.T) {
	m := newTestModel(&fakeKiller{})
	v := view(m)
	for _, want := range []string{"wintty-build", "1/2 HELD", "wintty-desktop", "FREE", "wintty", "CLOSED", "CPU and RAM", "the interactive desktop", "sess-a"} {
		if !strings.Contains(v, want) {
			t.Fatalf("overview missing %q:\n%s", want, v)
		}
	}
	if !strings.Contains(v, "q quit") {
		t.Fatalf("overview help missing:\n%s", v)
	}
}

func TestEnterDrillsIntoTheSelectedQueue(t *testing.T) {
	m := press(newTestModel(&fakeKiller{}), "enter")
	if m.screen != screenQueue || m.key != "wintty-build" {
		t.Fatalf("enter should open the first queue, got screen %d key %q", m.screen, m.key)
	}
	v := view(m)
	for _, want := range []string{"HOLDING (1)", "WAITING (1)", "pid 111", "pid 222", "zig build", "dotnet test", "sess-b", "event=", "k kill", "esc back"} {
		if !strings.Contains(v, want) {
			t.Fatalf("queue screen missing %q:\n%s", want, v)
		}
	}
	m = press(m, "esc")
	if m.screen != screenOverview {
		t.Fatal("esc should return to the overview")
	}
	m = press(m, "down", "enter")
	if m.key != "wintty-desktop" {
		t.Fatalf("down then enter should open the second queue, got %q", m.key)
	}
	if v := view(m); !strings.Contains(v, "nobody") {
		t.Fatalf("an empty queue says nobody holds it:\n%s", v)
	}
}

func TestKillAsksForAReasonThenRequests(t *testing.T) {
	k := &fakeKiller{gone: true}
	m := press(newTestModel(k), "enter", "down", "k")
	if m.screen != screenKillPrompt {
		t.Fatalf("k on a participant opens the prompt, got screen %d", m.screen)
	}
	if v := view(m); !strings.Contains(v, "kill pid 222") || !strings.Contains(v, "reason:") {
		t.Fatalf("prompt should name the pid and ask for a reason:\n%s", v)
	}
	// An empty reason is refused and the prompt stays.
	m = press(m, "enter")
	if m.screen != screenKillPrompt || len(k.requests) != 0 {
		t.Fatal("an empty reason must not request a kill")
	}
	if v := view(m); !strings.Contains(v, "reason is required") {
		t.Fatalf("the refusal should be visible:\n%s", v)
	}
	m = typeText(m, "queued by mistake")
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.screen != screenKillPending || cmd == nil {
		t.Fatalf("enter with a reason moves to pending and issues the request, got screen %d cmd nil=%v", m.screen, cmd == nil)
	}
	// Run the command chain the way the runtime would.
	msg := cmd()
	if _, ok := msg.(killRequestedMsg); !ok {
		t.Fatalf("first command should be the request, got %T", msg)
	}
	if len(k.requests) != 1 || k.requests[0] != "wintty-build/222/queued by mistake" {
		t.Fatalf("request not made as typed: %v", k.requests)
	}
	mm, cmd = m.Update(msg)
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("after the request the model waits for the participant to go")
	}
	mm, _ = m.Update(cmd())
	m = mm.(Model)
	if m.screen != screenQueue || m.pending != nil {
		t.Fatalf("a participant that let go returns to the queue screen, got %d pending=%v", m.screen, m.pending != nil)
	}
	if v := view(m); !strings.Contains(v, "pid 222 released the lane") {
		t.Fatalf("the outcome should be announced:\n%s", v)
	}
	if len(k.forced) != 0 {
		t.Fatal("a cooperative kill must not force")
	}
}

func TestEscapeCancelsThePrompt(t *testing.T) {
	k := &fakeKiller{}
	m := press(newTestModel(k), "enter", "k", "esc")
	if m.screen != screenQueue || m.pending != nil || len(k.requests) != 0 {
		t.Fatal("esc must cancel without requesting anything")
	}
}

func TestUnacknowledgedKillOffersForce(t *testing.T) {
	k := &fakeKiller{gone: false}
	m := press(newTestModel(k), "enter", "k")
	m = typeText(m, "wedged")
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	mm, cmd = m.Update(cmd())
	m = mm.(Model)
	mm, _ = m.Update(cmd()) // Gone reports false
	m = mm.(Model)
	if m.pending == nil || !m.pending.unacked {
		t.Fatal("a participant still there after the grace is marked unacknowledged")
	}
	if v := view(m); !strings.Contains(v, "not acknowledged") || !strings.Contains(v, "F force") {
		t.Fatalf("the pending box should offer to force:\n%s", v)
	}
	mm, cmd = m.Update(tea.KeyPressMsg{Code: 'F', Text: "F"})
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("F should issue the force")
	}
	mm, _ = m.Update(cmd())
	m = mm.(Model)
	if len(k.forced) != 1 || k.forced[0] != 111 {
		t.Fatalf("force should terminate the selected pid, got %v", k.forced)
	}
	if v := view(m); !strings.Contains(v, "terminated") {
		t.Fatalf("the forced outcome should be announced:\n%s", v)
	}
}

func TestKillRefusalIsShown(t *testing.T) {
	k := &fakeKiller{reqErr: errors.New("no live participant with that pid")}
	m := press(newTestModel(k), "enter", "k")
	m = typeText(m, "gone already")
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm, _ = mm.(Model).Update(cmd())
	m = mm.(Model)
	if m.screen != screenQueue {
		t.Fatal("a refused request returns to the queue screen")
	}
	if v := view(m); !strings.Contains(v, "kill refused") {
		t.Fatalf("the refusal should be shown:\n%s", v)
	}
}

func TestSingleQueueModeQuitsOnQ(t *testing.T) {
	rep := sampleReport()
	m := New(Options{Key: "wintty-build", Killer: &fakeKiller{}, Load: func() (*report.Report, error) { return rep, nil }})
	mm, _ := m.Update(reportMsg{rep: rep})
	m = mm.(Model)
	if m.screen != screenQueue {
		t.Fatal("a key option starts inside that queue")
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil {
		t.Fatal("q in single mode quits")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q in single mode should produce QuitMsg")
	}
}
