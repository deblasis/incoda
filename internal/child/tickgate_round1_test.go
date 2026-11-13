package child

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestTickGateFlushCarriesAnOpenCandidate: the drain can end while a report
// candidate is still open (a job killed mid-sequence, or the drain grace cut
// landing inside one). Those bytes are the job's own output and must reach
// the tee at flush, the way they reached it before the gate existed.
func TestTickGateFlushCarriesAnOpenCandidate(t *testing.T) {
	cases := []struct {
		name  string
		chunk []string
	}{
		{"one chunk cut mid-body", []string{"tail\r\n\x1b]7501;state=wor"}},
		{"bare ESC at the end", []string{"x\x1b"}},
		{"candidate carried across chunks", []string{"A\x1b]7501;state=wor", "king"}},
		// a cut inside the terminator itself: the ESC that may open the next
		// sequence is withheld with the candidate and must still land
		{"cut between ESC and its next byte", []string{"done\x1b]7501;state=ok\x07mid\x1b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &sink{}
			g := newTickGateWait(s, time.Hour)
			for _, c := range tc.chunk {
				if _, err := g.Write([]byte(c)); err != nil {
					t.Fatal(err)
				}
			}
			g.flush()
			want := ""
			for _, c := range tc.chunk {
				want += c
			}
			if got := s.String(); got != want {
				t.Fatalf("a candidate open at drain end is the job's own output and must land at flush:\nwant %q\n got %q", want, got)
			}
		})
	}
}

// TestTickGateFlushCarriesHeldAndOpenTogether: a drain can end with both a
// held report and an open candidate: both must land, the open candidate's
// bytes first (they are still plain output to the tee), then the held report.
func TestTickGateFlushCarriesHeldAndOpenTogether(t *testing.T) {
	s := &sink{}
	g := newTickGateWait(s, time.Hour)
	if _, err := g.Write([]byte(tickSeq("j", 1))); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Write([]byte(tickSeq("j", 2) + "\x1b]7501;state=par")); err != nil {
		t.Fatal(err)
	}
	g.flush()
	want := tickSeq("j", 1) + "\x1b]7501;state=par" + tickSeq("j", 2)
	if got := s.String(); got != want {
		t.Fatalf("both the open candidate and the held report must land at flush:\nwant %q\n got %q", want, got)
	}
}

// TestTickGateBoundsIDsPastTheCap: the gate must bound the storm a chatty
// reporter produces. When the burst comes from more distinct ids than the
// gate tracks (per-test ids under a sharded suite, say), the reports past
// the cap must still be bounded, not passed through unbounded.
func TestTickGateBoundsIDsPastTheCap(t *testing.T) {
	var b strings.Builder
	// 80 distinct ids, 10 ticks each: 800 reports in one burst.
	for i := 0; i < 80; i++ {
		id := "w" + strconv.Itoa(i)
		for n := 0; n < 10; n++ {
			b.WriteString(tickSeq(id, n))
		}
	}
	s := &sink{}
	g := newTickGateWait(s, time.Hour) // the window never closes: worst case
	if _, err := g.Write([]byte(b.String())); err != nil {
		t.Fatal(err)
	}
	before := strings.Count(s.String(), "\x1b]7501;")
	g.flush()
	total := strings.Count(s.String(), "\x1b]7501;")
	// With the cap refusing to track ids past 64, every one of the last 16
	// ids' 10 ticks flows on: 160 unbounded reports. The bound the gate
	// exists to provide must not silently vanish past the cap.
	if total > 2*80+16 {
		t.Fatalf("a burst from more ids than the cap passed %d of 800 reports through (before flush %d): past the cap the gate bounds nothing",
			total, before)
	}
	// And the newest report of a past-the-cap id must still survive.
	if !strings.Contains(s.String(), tickSeq("w79", 9)) {
		t.Fatalf("the newest report of a past-the-cap id was lost:\n%d reports", total)
	}
}

// TestTickGateKeepsThePerIDBoundPastTheCap: the bound is per id even where
// the id is one of the overflow reporters sharing one window: a report of
// such an id still passes at most once per window, and its superseded
// reports still never go downstream.
func TestTickGateKeepsThePerIDBoundPastTheCap(t *testing.T) {
	s := &sink{}
	g := newTickGateWait(s, time.Hour)
	var b strings.Builder
	// 64 tracked ids first, so "late" lands in the shared overflow window.
	for i := 0; i < 64; i++ {
		b.WriteString(tickSeq("t"+strconv.Itoa(i), 0))
	}
	for n := 0; n < 10; n++ {
		b.WriteString(tickSeq("late", n))
	}
	if _, err := g.Write([]byte(b.String())); err != nil {
		t.Fatal(err)
	}
	g.flush()
	got := s.String()
	if n := strings.Count(got, "id=late"); n > 2 {
		t.Fatalf("an id past the cap passed %d of its 10 reports: the shared window must bound it too", n)
	}
	if !strings.Contains(got, tickSeq("late", 9)) {
		t.Fatalf("the overflow id's newest report was lost:\n%s", got)
	}
}

// failSink fails every write after the first ok ones, recording each
// attempt, so a probe can see whether the gate surfaces downstream
// failures that happen on its timer and flush paths.
type failSink struct {
	mu       sync.Mutex
	ok       int
	attempt  int
	failures int
	buf      strings.Builder
}

func (f *failSink) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempt++
	if f.attempt > f.ok {
		f.failures++
		return 0, errors.New("sink is gone")
	}
	f.buf.Write(p)
	return len(p), nil
}

func (f *failSink) attempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempt
}

func (f *failSink) String() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.buf.String()
}

// TestTickGateSurfacesAReleaseWriteFailure: a tee failure must tear the run
// down wherever it happens. On the reader path it does (the runner kills the
// job and says so on stderr). When the failure first lands on the gate's
// timer release path, a pure tick storm behind it carries no passthrough
// bytes, so every later Write is held-only and must still surface the
// break: the run cannot keep going, exit 0, and have lost every report
// since the break with no signal at all.
func TestTickGateSurfacesAReleaseWriteFailure(t *testing.T) {
	f := &failSink{ok: 1}
	g := newTickGateWait(f, 10*time.Millisecond)
	if _, err := g.Write([]byte(tickSeq("j", 1))); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := g.Write([]byte(tickSeq("j", 2))); err != nil {
		t.Fatalf("second write: %v", err)
	}
	// The window releases report 2 onto the now-failing sink.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f.attempts() > f.ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if f.attempts() <= f.ok {
		t.Fatalf("the release never ran")
	}
	// The tee is provably broken. A pure tick storm now: every chunk is
	// held-only. The gate must not keep reporting success.
	for i := 3; i < 30; i++ {
		if _, err := g.Write([]byte(tickSeq("j", i))); err != nil {
			return // the break surfaced: PASS
		}
	}
	t.Fatalf("the tee failed on the release path and 27 held-only writes later the gate still reported success: the break is invisible for as long as the storm lasts")
}

// TestTickGateSurfacesAFlushWriteFailure: the drain-end flush is the last
// writer there is, so a failure there cannot ride a later Write: it must be
// said on stderr, the way the runner says the reader's own failures.
func TestTickGateSurfacesAFlushWriteFailure(t *testing.T) {
	f := &failSink{ok: 1}
	g := newTickGateWait(f, time.Hour)
	if _, err := g.Write([]byte(tickSeq("j", 1) + tickSeq("j", 2))); err != nil {
		t.Fatalf("first write: %v", err)
	}
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	g.flush()
	w.Close()
	os.Stderr = old
	var said bytes.Buffer
	_, _ = io.Copy(&said, r)
	if !strings.Contains(said.String(), "writing job output failed") {
		t.Fatalf("a flush onto a dead tee said nothing on stderr:\n%q", said.String())
	}
}

// TestTickGateStressUnderTimerAndReader: many writes with held reports and a
// short window, timer firing against the reader constantly. Asserts no
// report content goes downstream twice (a double-firing release timer must
// not double-write a hold), every non-report byte lands in order, and the
// newest report reaches the tail. Under -race this is also the data-race
// probe.
func TestTickGateStressUnderTimerAndReader(t *testing.T) {
	s := &sink{}
	g := newTickGateWait(s, 2*time.Millisecond)
	const rounds = 400
	var b strings.Builder
	for i := 0; i < rounds; i++ {
		b.WriteString(tickSeq("j", i))
		if i%7 == 0 {
			b.WriteString("LINE-" + strconv.Itoa(i) + "\r\n")
		}
	}
	payload := []byte(b.String())
	// Feed in irregular chunks to keep the candidate boundary moving.
	for off := 0; off < len(payload); off += 37 {
		end := off + 37
		if end > len(payload) {
			end = len(payload)
		}
		if _, err := g.Write(payload[off:end]); err != nil {
			t.Fatal(err)
		}
	}
	g.flush()
	got := s.String()
	for i := 0; i < rounds; i++ {
		if n := strings.Count(got, tickSeq("j", i)); n > 1 {
			t.Fatalf("report %d went downstream %d times: a hold must be written exactly once however the timer fires", i, n)
		}
	}
	// A released report can be written between any two chunks, so a plain
	// line can come out split by a report the reader never put there: no
	// byte is lost, but downstream is not the payload verbatim. What the
	// gate owes the stream is every non-report byte, in order, so compare
	// with the reports parsed away.
	if got, want := stripTicks(got), stripTicks(string(payload)); got != want {
		i := 0
		for i < len(got) && i < len(want) && got[i] == want[i] {
			i++
		}
		t.Fatalf("non-report bytes diverged at %d of %d:\ngot  %q\nwant %q",
			i, len(want), clip(got, i), clip(want, i))
	}
	if !strings.Contains(got, tickSeq("j", rounds-1)) {
		t.Fatalf("the newest report never landed")
	}
}

// stripTicks removes every complete 7501 report from s. The reports in this
// stream end with BEL, their only terminator here.
func stripTicks(s string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, "\x1b]7501;")
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		bel := strings.IndexByte(s[i:], 0x07)
		if bel < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		s = s[i+bel+1:]
	}
}

// clip returns s around i for a failure message.
func clip(s string, i int) string {
	lo, hi := i-40, i+40
	if lo < 0 {
		lo = 0
	}
	if hi > len(s) {
		hi = len(s)
	}
	return s[lo:hi]
}

// TestTickGateHoldsAnSTerminatedReportWithItsTerminator: a report ended with
// ESC \ and held for its window comes back ended the same way: the gate may
// not manufacture a BEL the job never sent.
func TestTickGateHoldsAnSTerminatedReportWithItsTerminator(t *testing.T) {
	st := "\x1b]7501;state=working:id=j:progress=7\x1b\\"
	s := &sink{}
	g := newTickGateWait(s, time.Hour)
	// The BEL report opens the window inline, so the ST-ended one is the one
	// the gate holds.
	if _, err := g.Write([]byte(tickSeq("j", 6) + st)); err != nil {
		t.Fatal(err)
	}
	g.flush()
	got := s.String()
	if !strings.Contains(got, st) {
		t.Fatalf("the held report must come back with the terminator it was sent with:\n%q", got)
	}
	if strings.Contains(got, "progress=7\x07") {
		t.Fatalf("the gate replaced the job's ST terminator with a BEL:\n%q", got)
	}
}

// TestTickGateCollapsesABurstOnTheProductionWindow: the window the run ships
// with, not a test's own, is the one the README promises. A burst collapses
// under it and the newest lands when the real window closes.
func TestTickGateCollapsesABurstOnTheProductionWindow(t *testing.T) {
	s := &sink{}
	g := newTickGateWait(s, tickWindow)
	var b strings.Builder
	for i := 0; i < 30; i++ {
		b.WriteString(tickSeq("j", i))
	}
	if _, err := g.Write([]byte(b.String())); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(s.String(), "\x1b]7501;"); got > 2 {
		t.Fatalf("the production window does not collapse a burst: %d of 30 reports through at once", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(s.String(), tickSeq("j", 29)) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the newest report never left on the production window:\n%q", s.String())
}
