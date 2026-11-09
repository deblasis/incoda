package osc7501

import "testing"

type mark struct {
	start, end int
	id         string
	ok         bool
}

func collectMarks(t *testing.T, chunks ...string) []mark {
	t.Helper()
	var got []mark
	var s Scanner
	s.Mark = func(start, end int, r Record, ok bool) {
		got = append(got, mark{start: start, end: end, id: r.ID, ok: ok})
	}
	for _, c := range chunks {
		s.Scan([]byte(c), nil)
	}
	return got
}

// TestScannerMarksReportRanges: the byte range of a report covers ESC through
// a BEL terminator (included) and stops before a terminating ESC (which
// belongs to what follows), and nothing else is marked - not queries, not
// other OSC, not aborted sequences.
func TestScannerMarksReportRanges(t *testing.T) {
	stream := "a\x1b]7501;state=working:id=w\x07b\x1b]7501;state=done:id=w\x1b\\c" +
		"\x1b]7501;?\x07\x1b]0;title\x07\x1b]7501;state=working\x18d"
	got := collectMarks(t, stream)
	want := []mark{
		// "a" then the first report: ESC at 1 through the BEL at 26 inclusive
		{1, 27, "w", true},
		// the second report: its ESC at 28 through the byte before its
		// terminating ESC at 50, the ESC itself belongs to what follows
		{28, 50, "w", true},
		// the query, the title OSC and the aborted report (CAN): no mark
	}
	if len(got) != len(want) {
		t.Fatalf("want %d marks, got %d: %+v", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("mark %d: want %+v, got %+v", i, w, got[i])
		}
	}
}

// TestScannerMarksAndPendingAcrossChunks: a report cut by a chunk boundary is
// marked with a negative start on the chunk that completes it, and Pending
// says where the open candidate sits, or that what is open is not a report.
func TestScannerMarksAndPendingAcrossChunks(t *testing.T) {
	// Cut after the ESC: the candidate is a lone escape, carried.
	s0 := collectMarks(t, "x\x1b")
	if len(s0) != 0 {
		t.Fatalf("nothing complete yet: %+v", s0)
	}
	// from must point at the ESC's index in the chunk just scanned; the
	// remaining chunks re-use one scanner so Pending can be read mid-stream.
	var s Scanner
	var marks []mark
	s.Mark = func(start, end int, r Record, ok bool) {
		marks = append(marks, mark{start: start, end: end, id: r.ID, ok: ok})
	}
	s.Scan([]byte("x\x1b"), nil)
	if from, ok := s.Pending(); !ok || from != 1 {
		t.Fatalf("a lone ESC is a candidate beginning where it sat, got from=%d ok=%v", from, ok)
	}
	s.Scan([]byte("]7501;state=wor"), nil)
	if from, ok := s.Pending(); !ok || from != -1 {
		t.Fatalf("a report carried from an earlier chunk is reported from -1, got from=%d ok=%v", from, ok)
	}
	s.Scan([]byte([]byte("king:id=w\x07after")), nil)
	if _, ok := s.Pending(); ok {
		t.Fatalf("the report completed; nothing is open")
	}
	if len(marks) != 1 || marks[0] != (mark{start: -1, end: 10, id: "w", ok: true}) {
		t.Fatalf("the completed carried report is marked from -1 with its end in the last chunk: %+v", marks)
	}
	// An open OSC that is not 7501 is not a report candidate.
	s.Scan([]byte("\x1b]0;tit"), nil)
	if _, ok := s.Pending(); ok {
		t.Fatalf("another OSC is not a report candidate")
	}
}

// TestScannerMarkReportsABadBody: a 7501 sequence whose body does not parse
// is marked with ok=false, so a filter can pass it on untouched.
func TestScannerMarkReportsABadBody(t *testing.T) {
	got := collectMarks(t, "\x1b]7501;state=sleeping\x07")
	if len(got) != 1 || got[0].ok || got[0].start != 0 || got[0].end != 22 {
		t.Fatalf("an invalid body is one mark with ok=false over its exact bytes: %+v", got)
	}
}
