package osc7501

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/textsafe"
)

func one(t *testing.T, seq string) (Record, bool) {
	t.Helper()
	got := collect(seq)
	if len(got) == 0 {
		return Record{}, false
	}
	return got[0], true
}

func scanAll(seqs ...string) []Record { return collect(strings.Join(seqs, "")) }

// The reply ends with the terminator the query used, so a program that
// scans its input for a BEL-terminated answer recognizes it.
func TestReplyMirrorsQueryTerminator(t *testing.T) {
	for term, want := range map[string]string{"\x07": "\x07", "\x1b\\": "\x1b\\"} {
		fw := &fakeWriter{}
		var s Scanner
		s.Respond(fw)
		s.Scan([]byte("\x1b]7501;?"+term), nil)
		if len(fw.writes) != 1 || !bytes.HasSuffix(fw.writes[0], []byte(want)) || !bytes.HasPrefix(fw.writes[0], []byte("\x1b]7501;?:states=")) {
			t.Fatalf("terminator %q: replies %q", term, fw.writes)
		}
		if bytes.Count(fw.writes[0], []byte("\x1b")) != 1+strings.Count(want, "\x1b") {
			t.Fatalf("terminator %q: reply carries the wrong terminator: %q", term, fw.writes[0])
		}
	}
}

// Padding is optional in msg and title.
func TestUnpaddedBase64(t *testing.T) {
	for _, s := range []string{"a", "ab", "abc", "abcd", "Syncing", "US East"} {
		raw := base64.RawStdEncoding.EncodeToString([]byte(s))
		r, ok := one(t, rec("state=working:msg="+raw+":title="+raw, "\x07"))
		if !ok || r.Msg != s || r.Title != s {
			t.Fatalf("%q unpadded: %+v ok=%v", s, r, ok)
		}
	}
}

// A report whose text does not decode, is not UTF-8 or carries a control
// character is discarded whole, as the terminal does.
func TestBadTextDiscardsTheReport(t *testing.T) {
	for name, seq := range map[string]string{
		"not base64":   rec("state=error:msg=!!!", "\x07"),
		"newline":      rec("state=error:msg="+b64("a\nb"), "\x07"),
		"ESC":          rec("state=error:msg="+b64("a\x1b[2Jb"), "\x07"),
		"C1":           rec("state=error:title="+b64("a\u0085b"), "\x07"),
		"DEL":          rec("state=error:msg="+b64("a\x7fb"), "\x07"),
		"invalid utf8": rec("state=error:msg="+base64.StdEncoding.EncodeToString([]byte{0xff, 0xfe}), "\x07"),
	} {
		if r, ok := one(t, seq); ok {
			t.Errorf("%s: want the report discarded, got %+v", name, r)
		}
	}
}

// A value with a byte outside the alphabet discards the report, whichever
// known key carried it (a skipped id would read as "the root").
func TestBadValueAlphabetDiscards(t *testing.T) {
	for name, seq := range map[string]string{
		"id bang":     rec("state=clear:id=a!b", "\x07"),
		"id space":    rec("state=working:id=a b", "\x07"),
		"id empty":    rec("state=working:id=", "\x07"),
		"id trailing": rec("state=working:id=a/", "\x07"),
	} {
		if r, ok := one(t, seq); ok {
			t.Errorf("%s: want the report discarded, got %+v", name, r)
		}
	}
	// Any known key outside the alphabet discards the report, app included:
	// which key was malformed does not change what the terminal does.
	if r, ok := one(t, rec("state=working:app=bad app!", "\x07")); ok {
		t.Errorf("bad app must discard the report, got %+v", r)
	}
	if r, ok := one(t, rec("state=idle:state=bad!x", "\x07")); ok {
		t.Errorf("a bad second state must discard the report, got %+v", r)
	}
	// An unknown key with any value stays ignored: the terminal does not
	// know it, so there is nothing to refuse.
	if r, ok := one(t, rec("state=working:zz=anything at!", "\x07")); !ok || r.State != "working" {
		t.Errorf("unknown keys stay ignored: %+v ok=%v", r, ok)
	}
}

// progress only on working/blocked, digits only; kind only on blocked.
func TestProgressAndKindGating(t *testing.T) {
	if r, _ := one(t, rec("state=done:progress=50", "\x07")); r.Progress != nil || r.Line(40) != "[done]" {
		t.Errorf("done ignores progress: %q", r.Line(40))
	}
	if r, _ := one(t, rec("state=idle:kind=auth", "\x07")); r.Kind != "" {
		t.Errorf("idle ignores kind: %q", r.Kind)
	}
	if r, _ := one(t, rec("state=blocked:kind=auth:progress=7", "\x07")); r.Kind != "auth" || r.Progress == nil || *r.Progress != 7 {
		t.Errorf("blocked keeps both: %+v", r)
	}
	for _, p := range []string{"+5", "-0", " 5x", "1_0", "101", ""} {
		if r, _ := one(t, rec("state=working:progress="+p, "\x07")); r.Progress != nil {
			t.Errorf("progress=%q is not 0..100 digits, got %d", p, *r.Progress)
		}
	}
}

// Every value is checked, even one a later pair replaces; keys are capped;
// the whole sequence (ESC ] through the terminator) is capped at 4096.
func TestLimitsMirrorTheTerminal(t *testing.T) {
	cases := map[string]string{
		"key over 16":        rec("state=working:"+strings.Repeat("k", 17)+"=1", "\x07"),
		"id replaced by ok":  rec("state=working:id="+strings.Repeat("a", 33)+":id=ok", "\x07"),
		"msg replaced by ok": rec("state=working:msg="+b64(strings.Repeat("m", 2049))+":msg="+b64("ok"), "\x07"),
		"sequence 4100":      rec("state=working:zz="+strings.Repeat("A", 4091-len("state=working:zz=")), "\x1b\\"),
	}
	for name, seq := range cases {
		if r, ok := one(t, seq); ok {
			t.Errorf("%s: want the report discarded, got %+v", name, r)
		}
	}
	// 4087 body bytes behind a ST is the largest that fits.
	body := "state=working:zz="
	body += strings.Repeat("A", 4087-len(body))
	if _, ok := one(t, rec(body, "\x1b\\")); !ok {
		t.Errorf("a 4096-byte sequence must be accepted")
	}
}

// ESC ends the OSC the way the terminal's parser does and dispatches what
// it holds; CAN and SUB abort it instead, the xterm/vt500 answer to a
// cancelled string: no record, no reply. C0 controls inside the body are
// ignored.
func TestEscEndsTheSequenceLikeTheTerminal(t *testing.T) {
	for name, stream := range map[string]string{
		"CSI c after": "\x1b]7501;?\x1b[c",
		"ESC BEL":     "\x1b]7501;?\x1b\x07",
		"ESC ESC ST":  "\x1b]7501;?\x1b\x1b\\",
	} {
		fw := &fakeWriter{}
		var s Scanner
		s.Respond(fw)
		s.Scan([]byte(stream), nil)
		if len(fw.writes) != 1 {
			t.Errorf("%s: want one reply, got %d", name, len(fw.writes))
		}
	}
	got := collect("\x1b]7501;state=wor\x01king\x07")
	if len(got) != 1 || got[0].State != "working" {
		t.Errorf("a C0 control inside the body is ignored: %+v", got)
	}
	// A record is still dispatched by the ESC that follows it, and
	// cancelled by the CAN or SUB that follows it.
	if got := collect("\x1b]7501;state=done\x1b[0m"); len(got) != 1 {
		t.Errorf("ESC dispatches a record too: %+v", got)
	}
	for _, c := range []string{"\x18", "\x1a"} {
		if got := collect("\x1b]7501;state=done" + c); len(got) != 0 {
			t.Errorf("CAN/SUB must abort the record: %+v", got)
		}
		fw := &fakeWriter{}
		var s Scanner
		s.Respond(fw)
		s.Scan([]byte("\x1b]7501;?"+c), nil)
		if len(fw.writes) != 0 {
			t.Errorf("CAN/SUB must abort the query: %d replies", len(fw.writes))
		}
	}
	// Split anywhere gives the same replies.
	stream := "\x1b]7501;?\x1b[c"
	for i := range len(stream) + 1 {
		fw := &fakeWriter{}
		var s Scanner
		s.Respond(fw)
		s.Scan([]byte(stream[:i]), nil)
		s.Scan([]byte(stream[i:]), nil)
		if len(fw.writes) != 1 {
			t.Fatalf("split %d: %d replies", i, len(fw.writes))
		}
	}
}

// The terminal's table sends ESC out of every string state (DCS, SOS, PM,
// APC) into escape, so an OSC that follows an ESC inside one is a live OSC:
// the scanner answers it exactly as the terminal would, and payload bytes
// that are not ESC can never form a query.
func TestQueryInsideStringFollowsTheTerminal(t *testing.T) {
	for _, stream := range []string{
		"\x1bP1;\x1b]7501;?\x07\x1b\\",
		"\x1b_app\x1b]7501;?\x07\x1b\\",
		"\x1b^pm\x1b]7501;?\x07\x1b\\",
		"\x1bXsos\x1b]7501;?\x07\x1b\\",
	} {
		fw := &fakeWriter{}
		var s Scanner
		s.Respond(fw)
		s.Scan([]byte(stream), nil)
		if len(fw.writes) != 1 {
			t.Errorf("%q: the terminal leaves the string at ESC and answers; got %d replies", stream, len(fw.writes))
		}
	}
	fw := &fakeWriter{}
	var s Scanner
	s.Respond(fw)
	s.Scan([]byte("\x1bP1;]7501;?\x07 \x1b\\"), nil)
	if len(fw.writes) != 0 {
		t.Errorf("a query with no ESC of its own is payload, got %d replies", len(fw.writes))
	}
}

// Without a callback and without a query there is nothing to decode.
func TestScanWithoutCallbackDoesNotAllocate(t *testing.T) {
	var s Scanner
	for name, d := range map[string][]byte{
		"record":    []byte("\x1b]7501;state=working:progress=5:title=aGk=:msg=aGVsbG8gd29ybGQ=\x07"),
		"other OSC": []byte("\x1b]0;window title\x07"),
		"plain":     []byte("hello\r\n"),
	} {
		if a := testing.AllocsPerRun(100, func() { s.Scan(d, nil) }); a > 0 {
			t.Errorf("%s: %v allocations per Scan with no callback", name, a)
		}
	}
}

// clear is applied, not displayed: it removes the addressed record and
// everything beneath it; id-less clear removes everything.
func TestRootAppliesClear(t *testing.T) {
	e := func(p string) string { return rec(p, "\x07") }
	if r, ok := Root(scanAll(e("state=working:id=build"), e("state=clear:id=build"))); ok {
		t.Errorf("cleared: nothing remains, got %+v", r)
	}
	if r, ok := Root(scanAll(e("state=working:id=a"), e("state=clear:id=a"), e("state=working:id=a/b"))); !ok || r.ID != "a/b" {
		t.Errorf("reported after the clear: %+v ok=%v", r, ok)
	}
	if r, ok := Root(scanAll(e("state=working:id=x"), e("state=working:id=y/z"), e("state=clear:id=x"))); !ok || r.ID != "y/z" {
		t.Errorf("clear x leaves y/z: %+v ok=%v", r, ok)
	}
	if r, ok := Root(scanAll(e("state=working:id=x/y"), e("state=clear:id=x"))); ok {
		t.Errorf("clear x removes x/y: %+v", r)
	}
	if r, ok := Root(scanAll(e("state=working:id=x"), e("state=working:id=y"), e("state=clear"))); ok {
		t.Errorf("id-less clear removes all: %+v", r)
	}
	// Replace by id: the latest report of an id is the record.
	if r, ok := Root(scanAll(e("state=working:id=x:progress=1"), e("state=done:id=x"))); !ok || r.State != "done" || r.Progress != nil {
		t.Errorf("a report replaces its record: %+v", r)
	}
	// "x" must not clear "xy".
	if r, ok := Root(scanAll(e("state=working:id=xy"), e("state=clear:id=x"))); !ok || r.ID != "xy" {
		t.Errorf("clear x is not a prefix match on names: %+v ok=%v", r, ok)
	}
}

// textsafe covers the format controls a status line must not carry.
func TestLineEscapesFormatControls(t *testing.T) {
	for _, r := range []rune{0x061C, 0xFEFF, 0x2028, 0x2029} {
		if got := textsafe.Escape("a" + string(r) + "b"); got == "a"+string(r)+"b" {
			t.Errorf("U+%04X passes the escaper", r)
		}
	}
}

// Line caps by display cells, not runes.
func TestLineCapsByCells(t *testing.T) {
	r := Record{State: "working", Msg: strings.Repeat("漢", 50)}
	line := r.Line(20)
	if w := cellWidth(strings.TrimPrefix(line, "[working] ")); w > 20 {
		t.Fatalf("msg is %d cells, cap is 20: %q", w, line)
	}
	if !strings.HasSuffix(line, "…") {
		t.Fatalf("a cut msg ends with an ellipsis: %q", line)
	}
}
