package osc7501

import (
	"bytes"
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func rec(pairs string, term string) string { return "\x1b]7501;" + pairs + term }

func collect(chunks ...string) []Record {
	var s Scanner
	var got []Record
	for _, c := range chunks {
		s.Scan([]byte(c), func(r Record) { got = append(got, r) })
	}
	return got
}

// Noise between records is skipped: plain text, CSI sequences, other OSC
// sequences (a window title), a lone ESC. Both terminators close a record.
func TestScanNoiseAndBothTerminators(t *testing.T) {
	stream := "hello\r\n\x1b[31mred\x1b[0m" +
		"\x1b]0;window title\x07" +
		rec("state=working:id=build:progress=40:app=just", "\x07") +
		"between \x1b lone esc\r\n" +
		rec("state=done:id=build", "\x1b\\") +
		"tail"
	got := collect(stream)
	want := []Record{
		{ID: "build", State: "working", Progress: new(40), App: "just"},
		{ID: "build", State: "done"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// title and msg are base64 of UTF-8; multibyte text survives.
func TestScanBase64TitleAndMsg(t *testing.T) {
	got := collect(rec("state=blocked:kind=permission:title="+b64("Build ✓")+":msg="+b64("héllo wörld"), "\x07"))
	if len(got) != 1 {
		t.Fatalf("want one record, got %+v", got)
	}
	r := got[0]
	if r.Title != "Build ✓" || r.Msg != "héllo wörld" || r.Kind != "permission" || r.State != "blocked" {
		t.Fatalf("decode wrong: %+v", r)
	}
}

// A record split at every possible byte boundary parses the same as whole.
func TestScanSplitAtEveryBoundary(t *testing.T) {
	stream := "noise\x1b[1m" + rec("state=working:id=a/b:progress=7:msg="+b64("x"), "\x1b\\") +
		"mid" + rec("state=done", "\x07") + "\x1b]0;t\x07"
	whole := collect(stream)
	if len(whole) != 2 {
		t.Fatalf("whole parse: %+v", whole)
	}
	for i := range len(stream) + 1 {
		got := collect(stream[:i], stream[i:])
		if !reflect.DeepEqual(got, whole) {
			t.Fatalf("split at %d: got %+v, want %+v", i, got, whole)
		}
	}
	// And one byte at a time.
	var chunks []string
	for i := range len(stream) {
		chunks = append(chunks, stream[i:i+1])
	}
	if got := collect(chunks...); !reflect.DeepEqual(got, whole) {
		t.Fatalf("byte at a time: got %+v, want %+v", got, whole)
	}
}

// Caps: an over-long sequence is dropped without growing the scanner's
// buffer, and the stream after it still parses.
func TestScanCaps(t *testing.T) {
	huge := rec("state=working:msg="+strings.Repeat("A", 20000), "\x07")
	var s Scanner
	var got []Record
	s.Scan([]byte(huge+rec("state=done:id=after", "\x07")), func(r Record) { got = append(got, r) })
	if len(got) != 1 || got[0].ID != "after" {
		t.Fatalf("over-long sequence must be dropped and the next kept: %+v", got)
	}
	if cap(s.buf) > 2*MaxSequenceBytes {
		t.Fatalf("scanner buffer grew to %d for an over-long sequence", cap(s.buf))
	}

	cases := map[string]string{
		"msg decoded over 2048":  rec("state=working:msg="+b64(strings.Repeat("m", 2049)), "\x07"),
		"title decoded over 192": rec("state=working:title="+b64(strings.Repeat("t", 193)), "\x07"),
		"id over 128 bytes":      rec("state=working:id="+strings.Repeat("a/", 70)+"a", "\x07"),
		"id segment over 32":     rec("state=working:id="+strings.Repeat("a", 33), "\x07"),
		"id depth over 8":        rec("state=working:id=a/b/c/d/e/f/g/h/i", "\x07"),
		"id empty segment":       rec("state=working:id=a//b", "\x07"),
		"unknown state":          rec("state=exploding", "\x07"),
		"no state":               rec("id=x", "\x07"),
	}
	for name, seq := range cases {
		if got := collect(seq); len(got) != 0 {
			t.Errorf("%s: record should be refused, got %+v", name, got)
		}
	}
	// Exactly at the limit is accepted.
	ok := collect(rec("state=working:msg="+b64(strings.Repeat("m", 2048))+":title="+b64(strings.Repeat("t", 192))+":id="+strings.Repeat("a", 32)+"/b/c/d/e/f/g/h", "\x07"))
	if len(ok) != 1 || len(ok[0].Msg) != 2048 || len(ok[0].Title) != 192 {
		t.Fatalf("records at the limits must parse: %+v", ok)
	}
}

// Field-level tolerance: progress out of range or non-numeric is dropped, an
// unknown kind or key is skipped, a repeated key takes the last value. Bad
// text is not tolerated: that discards the report (see protocol_test.go).
func TestScanFieldTolerance(t *testing.T) {
	got := collect(
		rec("state=working:progress=101", "\x07"),
		rec("state=working:progress=abc", "\x07"),
		rec("state=working:progress=0", "\x07"),
		rec("state=blocked:kind=telepathy:future=1", "\x07"),
		rec("state=working:id=one:id=two", "\x07"),
	)
	want := []Record{
		{State: "working"},
		{State: "working"},
		{State: "working", Progress: new(0)},
		{State: "blocked"},
		{State: "working", ID: "two"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

// An ESC ends the OSC the way the terminal's parser does: what the sequence
// held is dispatched, and what follows parses on its own.
func TestScanEscDispatchesThenContinues(t *testing.T) {
	got := collect("\x1b]7501;state=working:id=first\x1b[31m" + rec("state=done:id=next", "\x07"))
	if len(got) != 2 || got[0].ID != "first" || got[1].ID != "next" {
		t.Fatalf("got %+v", got)
	}
}

func TestIsQueryAndSupportReply(t *testing.T) {
	for _, q := range []string{"\x1b]7501;?\x07", "\x1b]7501;?\x1b\\"} {
		if !IsQuery([]byte(q)) {
			t.Errorf("%q is a query", q)
		}
	}
	for _, n := range []string{
		"", "\x1b]7501;?", "\x1b]7501;state=working\x07", "\x1b]7501;?:states=idle\x07",
		"\x1b]0;?\x07", "plain ?",
	} {
		if IsQuery([]byte(n)) {
			t.Errorf("%q is not a query", n)
		}
	}
	want := "\x1b]7501;?:states=idle,working,done,blocked,error,clear:kinds=permission,question,auth\x1b\\"
	if got := string(SupportReply()); got != want {
		t.Fatalf("reply = %q, want %q", got, want)
	}
	if !IsQueryReplyShaped(SupportReply()) {
		t.Fatalf("the reply must not itself be mistaken for a query")
	}
}

// IsQueryReplyShaped: a support reply echoed back through a pty is neither
// a query nor a record.
func IsQueryReplyShaped(reply []byte) bool {
	if IsQuery(reply) {
		return false
	}
	return len(collect(string(reply))) == 0
}

type fakeWriter struct {
	writes [][]byte
}

func (f *fakeWriter) Write(p []byte) (int, error) {
	f.writes = append(f.writes, append([]byte(nil), p...))
	return len(p), nil
}

// A complete query makes the scanner write exactly one support reply, however
// the query is split; records and noise around it produce no write. The
// reply ends with the terminator its query used.
func TestQueryRepliesThroughWriter(t *testing.T) {
	stream := "abc" + rec("state=working", "\x07") + "\x1b]7501;?\x07" + "mid" + "\x1b]7501;?\x1b\\" + "\x1b]0;x\x07"
	wants := [][]byte{reply(true), SupportReply()}
	for i := range len(stream) + 1 {
		fw := &fakeWriter{}
		var s Scanner
		s.Respond(fw)
		var recs int
		s.Scan([]byte(stream[:i]), func(Record) { recs++ })
		s.Scan([]byte(stream[i:]), func(Record) { recs++ })
		if len(fw.writes) != 2 {
			t.Fatalf("split %d: want 2 replies, got %d", i, len(fw.writes))
		}
		for j, w := range fw.writes {
			if !bytes.Equal(w, wants[j]) {
				t.Fatalf("split %d: reply %d = %q", i, j, w)
			}
		}
		if recs != 1 {
			t.Fatalf("split %d: a query is not a record; got %d records", i, recs)
		}
	}
}

func TestRootIsFewestSegmentsLatestOnTie(t *testing.T) {
	rs := []Record{
		{ID: "a/b/c", State: "working"},
		{ID: "a/b", State: "done"},
		{ID: "x", State: "working", Msg: "first"},
		{ID: "y", State: "error", Msg: "second"},
		{ID: "a/b/c/d", State: "idle"},
	}
	r, ok := Root(rs)
	if !ok || r.ID != "y" {
		t.Fatalf("root = %+v ok=%v, want y (fewest segments, latest on tie)", r, ok)
	}
	if _, ok := Root(nil); ok {
		t.Fatal("no records, no root")
	}
	// A record with no id is the shallowest of all.
	r, _ = Root([]Record{{ID: "a", State: "working"}, {State: "done"}})
	if r.State != "done" {
		t.Fatalf("an id-less record is the root: %+v", r)
	}
}

func TestLine(t *testing.T) {
	cases := []struct {
		r    Record
		max  int
		want string
	}{
		{Record{State: "working", Progress: new(40), Msg: "compiling"}, 40, "[working 40%] compiling"},
		{Record{State: "done"}, 40, "[done]"},
		{Record{State: "error", Msg: "exit 3"}, 40, "[error] exit 3"},
		{Record{State: "working", Msg: "abcdefghij"}, 5, "[working] abcd…"},
		{Record{State: "blocked", Kind: "question", Msg: "proceed?"}, 40, "[blocked:question] proceed?"},
		{Record{State: "blocked"}, 40, "[blocked]"},
	}
	for _, c := range cases {
		if got := c.r.Line(c.max); got != c.want {
			t.Errorf("Line(%+v,%d) = %q, want %q", c.r, c.max, got, c.want)
		}
	}
}
