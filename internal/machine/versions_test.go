package machine

import (
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	for _, tc := range []struct {
		out          string
		text         string
		major, minor int
		known        bool
	}{
		{"incoda v0.6.0\ncommit: abc\nbuilt:  x\n", "v0.6.0", 0, 6, true},
		{"incoda 0.5.1\n", "0.5.1", 0, 5, true},
		{"incoda v0.7.0-rc1\n", "v0.7.0-rc1", 0, 7, true},
		{"incoda v1.2.3+meta\n", "v1.2.3+meta", 1, 2, true},
		{"incoda dev\ncommit: none\n", "dev", 0, 0, false},
		{"incoda v0.0.0-20261001-abcdef\n", "v0.0.0-20261001-abcdef", 0, 0, true},
	} {
		v, err := parseVersion(tc.out)
		if err != nil || v.Text != tc.text || v.Known != tc.known || (tc.known && (v.Major != tc.major || v.Minor != tc.minor)) {
			t.Fatalf("parseVersion(%q) = %+v %v", tc.out, v, err)
		}
	}
	for _, bad := range []string{"", "hello there\n", "incoda\n", "incoda v1 extra\n", "Incoda v0.7.0\n"} {
		if _, err := parseVersion(bad); err == nil {
			t.Fatalf("parseVersion(%q) must fail", bad)
		}
	}
	if _, err := parseVersion("\x1b[31mincoda\n"); err == nil || strings.ContainsRune(err.Error(), '\x1b') {
		t.Fatalf("an unparseable line is reported escaped: %v", err)
	}
}

func TestCappedBufferKeepsFourKiB(t *testing.T) {
	var c cappedBuffer
	chunk := strings.Repeat("x", 3000)
	for i := 0; i < 3; i++ {
		if n, err := c.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("Write must accept everything: %d %v", n, err)
		}
	}
	if len(c.String()) != probeOutputCap {
		t.Fatalf("kept %d bytes, want %d", len(c.String()), probeOutputCap)
	}
}
