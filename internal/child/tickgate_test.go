package child

import (
	"bytes"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// sink is a thread-safe capture of what the gate sends downstream: the
// release timer writes from its own goroutine, and the test reads while it
// may still be running.
type sink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *sink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *sink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func tickSeq(id string, n int) string {
	if id == "" {
		return "\x1b]7501;state=working:progress=" + strconv.Itoa(n) + "\x07"
	}
	return "\x1b]7501;state=working:id=" + id + ":progress=" + strconv.Itoa(n) + "\x07"
}

// TestTickGateIsABytePipeWhenEveryReportIsFree: with a zero window every
// report goes downstream at the position it arrived, so the stream the gate
// emits is the stream it got, byte for byte, records, queries, other OSC and
// all. This is the "no behaviour change for everything else" pin.
func TestTickGateIsABytePipeWhenEveryReportIsFree(t *testing.T) {
	in := "text\r\n" + tickSeq("", 1) + "\x1b[2J\x1b[m\x1b[H" + "mid" + tickSeq("a", 2) +
		"\x1b]7501;?\x07" + "\x1b]0;title\x07" + "end\r\n"
	s := &sink{}
	g := newTickGateWait(s, 0)
	// Fed in one piece and dripped one byte at a time: the bytes that come
	// out must be the same either way.
	if _, err := g.Write([]byte(in)); err != nil {
		t.Fatal(err)
	}
	g.flush()
	if got := s.String(); got != in {
		t.Fatalf("one write: the gate must be a byte pipe when no window holds:\nwant %q\n got %q", in, got)
	}
	s.buf.Reset()
	g2 := newTickGateWait(s, 0)
	for i := 0; i < len(in); i++ {
		if _, err := g2.Write([]byte(in[i : i+1])); err != nil {
			t.Fatal(err)
		}
	}
	g2.flush()
	if got := s.String(); got != in {
		t.Fatalf("byte-dripped: the gate must be a byte pipe when no window holds:\nwant %q\n got %q", in, got)
	}
}

// TestTickGateCollapsesABurstOntoItsNewest: a same-id burst in one chunk
// costs one report at its own place plus the newest when the drain ends. The
// reports between them never go downstream.
func TestTickGateCollapsesABurstOntoItsNewest(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 50; i++ {
		b.WriteString(tickSeq("", i))
	}
	s := &sink{}
	g := newTickGateWait(s, time.Hour) // the window never closes on its own here
	if _, err := g.Write([]byte(b.String())); err != nil {
		t.Fatal(err)
	}
	if got := s.String(); got != tickSeq("", 0) {
		t.Fatalf("before the flush only the burst's first report, at its own place, belongs downstream:\n%q", got)
	}
	g.flush()
	if got := s.String(); got != tickSeq("", 0)+tickSeq("", 49) {
		t.Fatalf("after the drain the burst is its first and its newest, nothing between:\n%q", got)
	}
}

// TestTickGateReleasesOnTheWindowWithoutNewOutput: a held report must not
// wait for more stream: the window itself sends it, which is what keeps a
// job's last status (a blocked report with nothing after it) reachable.
func TestTickGateReleasesOnTheWindowWithoutNewOutput(t *testing.T) {
	s := &sink{}
	g := newTickGateWait(s, 20*time.Millisecond)
	if _, err := g.Write([]byte(tickSeq("job", 1) + tickSeq("job", 2))); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(s.String(), tickSeq("job", 2)) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the newest report never left the gate on its own window:\n%q", s.String())
}

// TestTickGateKeepsTheOtherIDs: coalescing is per id. Three ids burst
// together and each keeps its own newest report.
func TestTickGateKeepsTheOtherIDs(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 30; i++ {
		for _, id := range []string{"w1", "w2", "w3"} {
			b.WriteString(tickSeq(id, i))
		}
	}
	s := &sink{}
	g := newTickGateWait(s, time.Hour)
	if _, err := g.Write([]byte(b.String())); err != nil {
		t.Fatal(err)
	}
	g.flush()
	got := s.String()
	for _, id := range []string{"w1", "w2", "w3"} {
		if !strings.Contains(got, tickSeq(id, 0)) || !strings.Contains(got, tickSeq(id, 29)) {
			t.Fatalf("id %s must keep its first report at its place and its newest at the drain:\n%q", id, got)
		}
	}
	if n := strings.Count(got, "\x1b]7501;"); n != 6 {
		t.Fatalf("three ids cost six reports (first and newest each), got %d:\n%q", n, got)
	}
}

// TestTickGateLeavesPacedReportsAndPlainTextAlone: a report that arrives
// outside the window goes straight through, interleaved output lines are
// never held back with it, and a burst between two lines costs only the
// lines' order around the newest report, never the lines themselves.
func TestTickGateLeavesPacedReportsAndPlainTextAlone(t *testing.T) {
	s := &sink{}
	g := newTickGateWait(s, 30*time.Millisecond)
	if _, err := g.Write([]byte("L0\r\n" + tickSeq("j", 1) + "L1\r\n")); err != nil {
		t.Fatal(err)
	}
	if got, want := s.String(), "L0\r\n"+tickSeq("j", 1)+"L1\r\n"; got != want {
		t.Fatalf("a paced report keeps its place and its lines:\nwant %q\n got %q", want, got)
	}
	time.Sleep(60 * time.Millisecond)
	in := tickSeq("j", 2) + "L2\r\n" + tickSeq("j", 3) + tickSeq("j", 4) + "L3\r\n"
	if _, err := g.Write([]byte(in)); err != nil {
		t.Fatal(err)
	}
	got := s.String()
	for _, want := range []string{"L2\r\n", "L3\r\n", tickSeq("j", 2)} {
		if !strings.Contains(got, want) {
			t.Fatalf("lost %q to tick coalescing:\n%q", want, got)
		}
	}
	if strings.Contains(got, tickSeq("j", 3)) {
		t.Fatalf("a report superseded inside the window went downstream anyway:\n%q", got)
	}
}

// TestTickGateCarriesAReportCutByAChunkBoundary: the reader's chunks have
// nothing to do with the stream, so a report split across any number of
// writes still collapses, and the plain bytes around it still arrive. The
// held report follows the plain bytes that outlived it - a report the window
// holds leaves the gate when the window closes, not when the stream moves on
// - so it is asserted present, and last, not at the position it arrived at.
func TestTickGateCarriesAReportCutByAChunkBoundary(t *testing.T) {
	in := "A" + tickSeq("j", 1) + tickSeq("j", 2) + "B\r\n"
	s := &sink{}
	g := newTickGateWait(s, time.Hour)
	for i := 0; i < len(in); i++ {
		if _, err := g.Write([]byte(in[i : i+1])); err != nil {
			t.Fatal(err)
		}
	}
	g.flush()
	want := "A" + tickSeq("j", 1) + "B\r\n" + tickSeq("j", 2)
	if got := s.String(); got != want {
		t.Fatalf("a report split across every byte boundary must still come out whole:\nwant %q\n got %q", want, got)
	}
}

// TestTickGatePassesABadReportThrough: a 7501 sequence whose body is not a
// valid report is output like any other bytes, not a coalescing decision.
func TestTickGatePassesABadReportThrough(t *testing.T) {
	bad := "\x1b]7501;state=sleeping\x07"
	s := &sink{}
	g := newTickGateWait(s, time.Hour)
	if _, err := g.Write([]byte(bad + bad + "x")); err != nil {
		t.Fatal(err)
	}
	if got := s.String(); got != bad+bad+"x" {
		t.Fatalf("a report the terminal would discard is the job's own output:\nwant %q\n got %q", bad+bad+"x", got)
	}
}
