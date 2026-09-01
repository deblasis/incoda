package cli

import (
	"testing"
	"time"
)

func TestParseWait(t *testing.T) {
	ok := []struct {
		in   string
		want time.Duration
	}{
		// Go durations.
		{"30m", 30 * time.Minute},
		{"90s", 90 * time.Second},
		{"1h30m", 90 * time.Minute},
		{"500ms", 500 * time.Millisecond},
		// Bare integers are seconds: build-lane took --wait 1800 and that
		// muscle memory has to keep working.
		{"1800", 30 * time.Minute},
		{"0", 0},
		{"1", time.Second},
		{" 45 ", 45 * time.Second},
		// Negative means "wait forever".
		{"-1", -1},
	}
	for _, c := range ok {
		got, err := ParseWait(c.in)
		if err != nil {
			t.Errorf("ParseWait(%q) errored: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseWait(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "soon", "30x", "1.5", "m30"} {
		if _, err := ParseWait(bad); err == nil {
			t.Errorf("ParseWait(%q) should have failed", bad)
		}
	}
}
