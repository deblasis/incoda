package held

import "testing"

const t1 = "00000000000000000001-4711.ticket"
const t2 = "00000000000000000002-4711.ticket"

func TestParse(t *testing.T) {
	got, bad := Parse("cap-gate=" + t1 + ", tests=" + t2 + ",,")
	if len(got) != 2 || got[0] != (Entry{"cap-gate", t1}) || got[1] != (Entry{"tests", t2}) || len(bad) != 0 {
		t.Fatalf("got %v bad %v", got, bad)
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	for _, raw := range []string{
		"cap-gate",   // the pre-0.7 bare key
		"cap-gate=",  // no ticket
		"=" + t1,     // no key
		"../x=" + t1, // key fails ValidateKey
		"k=../00000000000000000001-1.ticket",
		"k=00000000000000000001-1.txt",
		"k=-12-3.ticket", // signed number
		"k=00000000000000000001-x.ticket",
	} {
		got, bad := Parse(raw)
		if len(got) != 0 || len(bad) != 1 || bad[0].Raw != raw {
			t.Fatalf("Parse(%q) = %v, bad %v; want one malformed entry", raw, got, bad)
		}
	}
}

func TestParseKeepsFirstPerKey(t *testing.T) {
	got, _ := Parse("k=" + t1 + ",k=" + t2)
	if len(got) != 1 || got[0].Ticket != t1 {
		t.Fatalf("got %v", got)
	}
}

func TestFormatAndMerge(t *testing.T) {
	a := []Entry{{"tests", t2}, {"cap-gate", t1}}
	if got := Format(a); got != "cap-gate="+t1+",tests="+t2 {
		t.Fatalf("got %q", got)
	}
	m := Merge([]Entry{{"k", t1}, {"x", t1}}, []Entry{{"k", t2}})
	if Format(m) != "k="+t2+",x="+t1 {
		t.Fatalf("Merge must prefer the second list per key, got %v", m)
	}
}
