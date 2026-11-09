package child

import (
	"io"
	"sync"
	"time"

	"github.com/deblasis/incoda/internal/osc7501"
)

// tickWindow is the least time between two status reports of one id going
// downstream. A report arriving inside the window replaces the one already
// waiting there (a later report for an id always supersedes an earlier one),
// and the newest is what goes out when the window closes. However fast a job
// ticks it cannot repaint its row faster than this, and nothing else in the
// stream is touched (#36).
const tickWindow = 200 * time.Millisecond

// tickMaxIDs bounds the ids the gate tracks, the way a viewer's record set is
// bounded. Past it a report flows on rather than evicting a reporter whose
// waiting report the gate would then drop.
const tickMaxIDs = 64

// tickGate coalesces OSC 7501 status reports in a pty drain path. Everything
// that is not a report passes through verbatim at the position it arrived,
// and so does a report paced slower than the window; only a reporter ticking
// faster than the window is collapsed onto its newest report per id. It sits
// between the reader and the tee, so the run's stdout and every capture see
// the same bounded stream. Safe for concurrent use: the reader and the
// release timer are the only writers, and both go through the mutex.
type tickGate struct {
	next   io.Writer
	window time.Duration

	mu    sync.Mutex
	sc    osc7501.Scanner
	marks []tickMark
	carry []byte // a report candidate cut by the end of the last Write

	ids   map[string]*tickID
	order []string // ids with a report waiting, oldest wait first
	timer *time.Timer
}

type tickMark struct {
	start, end int
	rec        osc7501.Record
	ok         bool
}

// tickID is what the gate knows about one reporter id: when its newest report
// went downstream, and the report still waiting for the window, if any.
type tickID struct {
	last time.Time
	held []byte
	due  time.Time
}

func newTickGate(next io.Writer) *tickGate { return newTickGateWait(next, tickWindow) }

// newTickGateWait is newTickGate with the window given, for tests.
func newTickGateWait(next io.Writer, window time.Duration) *tickGate {
	g := &tickGate{next: next, window: window, ids: map[string]*tickID{}}
	g.sc.Mark = func(start, end int, r osc7501.Record, ok bool) {
		g.marks = append(g.marks, tickMark{start: start, end: end, rec: r, ok: ok})
	}
	return g
}

// Write passes p on, minus the reports the window holds back. A report
// candidate still open at the end of p is carried into the next Write rather
// than passed on incomplete.
func (g *tickGate) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	carried := g.carry
	g.carry = nil
	g.marks = g.marks[:0]
	g.sc.Scan(p, nil)
	out := make([]byte, 0, len(p))
	cursor, consumed := 0, false
	for _, m := range g.marks {
		if m.start > cursor {
			// the output between this report and the one before it
			out = append(out, p[cursor:m.start]...)
		}
		seq := append([]byte{}, p[max(m.start, 0):m.end]...)
		if m.start < 0 {
			seq = append(carried, seq...)
			consumed = true
		}
		if !m.ok || !g.limit(m.rec, seq) {
			out = append(out, seq...)
		}
		cursor = m.end
	}
	if from, open := g.sc.Pending(); open {
		if from < 0 {
			// The candidate began in the carried bytes and is still open, so
			// it owns this whole chunk and no mark can have fired.
			g.carry = append(carried, p...)
		} else {
			out = append(out, p[cursor:from]...)
			g.carry = append([]byte{}, p[from:]...)
		}
	} else {
		out = append(out, p[cursor:]...)
		if len(carried) > 0 && !consumed {
			// The carried candidate died without becoming a report: its bytes
			// were never anything but the job's own output.
			out = append(append([]byte{}, carried...), out...)
		}
	}
	g.arm()
	if len(out) == 0 {
		return len(p), nil
	}
	_, err := g.next.Write(out)
	return len(p), err
}

// limit sends seq downstream when rec's id is free to report again and holds
// it for the window when not, replacing what the id already holds: only the
// newest report ever waits, and only that goes out. It reports whether the
// report was held.
func (g *tickGate) limit(rec osc7501.Record, seq []byte) bool {
	now := time.Now()
	id, ok := g.ids[rec.ID]
	if !ok {
		if len(g.ids) >= tickMaxIDs {
			return false
		}
		id = &tickID{}
		g.ids[rec.ID] = id
	}
	if id.held == nil && (id.last.IsZero() || now.Sub(id.last) >= g.window) {
		// The window is open and nothing waits, so the report keeps its own
		// place in the stream. A waiting report implies its window is still
		// closed - due is anchored to the same last - so the hold below is
		// the only other case there is.
		id.last = now
		return false
	}
	if id.held == nil {
		g.order = append(g.order, rec.ID)
	}
	held := append([]byte{}, seq...)
	if held[len(held)-1] != 0x07 {
		// The sequence ended at an ESC, which belongs to what follows it: a
		// report held past its neighbours needs a terminator of its own.
		held = append(held, 0x07)
	}
	id.held, id.due = held, id.last.Add(g.window)
	return true
}

// arm points the release timer at the earliest due report, or stops it when
// nothing waits.
func (g *tickGate) arm() {
	if len(g.order) == 0 {
		if g.timer != nil {
			g.timer.Stop()
			g.timer = nil
		}
		return
	}
	due := g.ids[g.order[0]].due
	for _, id := range g.order[1:] {
		if d := g.ids[id].due; d.Before(due) {
			due = d
		}
	}
	wait := time.Until(due)
	if wait < 0 {
		wait = 0
	}
	if g.timer == nil {
		g.timer = time.AfterFunc(wait, g.release)
		return
	}
	g.timer.Reset(wait)
}

// release sends the reports whose window closed: a chatty reporter gets
// exactly one report through per window, always its newest.
func (g *tickGate) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.timer = nil
	now := time.Now()
	kept := g.order[:0]
	for _, id := range g.order {
		t := g.ids[id]
		if t.due.After(now) {
			kept = append(kept, id)
			continue
		}
		_, _ = g.next.Write(t.held)
		t.held, t.last = nil, now
	}
	g.order = kept
	g.arm()
}

// flush sends every waiting report: the drain has ended, so no window will
// ever close on its own again, and the newest status a job sent is part of
// its tail.
func (g *tickGate) flush() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
	for _, id := range g.order {
		t := g.ids[id]
		_, _ = g.next.Write(t.held)
		t.held, t.last = nil, time.Now()
	}
	g.order = nil
}
