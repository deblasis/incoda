package procinfo

import "testing"

func TestParseStat(t *testing.T) {
	line := "4711 (we (ird) name) T 4700 4690 4690 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 987654 1000 10 18446744073709551615\n"
	p, err := parseStat(line)
	if err != nil {
		t.Fatal(err)
	}
	if p != (Proc{PID: 4711, PPID: 4700, PGID: 4690, Start: 987654, State: 'T'}) {
		t.Fatalf("parseStat = %+v", p)
	}
	for _, bad := range []string{"", "4711 (x", "4711 (x) R 1", "x (y) R 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19"} {
		if _, err := parseStat(bad); err == nil {
			t.Fatalf("parseStat(%q) must fail", bad)
		}
	}
	if p, _ := parseStat("9 (z) Z 1 9 9 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 5 0 0 0"); p.State != 'Z' {
		t.Fatalf("zombie state %q", p.State)
	}
}
