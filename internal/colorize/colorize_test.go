package colorize

import (
	"bytes"
	"strings"
	"testing"
)

func TestDecideFollowsTheConventions(t *testing.T) {
	cases := []struct {
		name                      string
		noColor, dumb, force, tty bool
		want                      bool
	}{
		// Color on a terminal is the normal case.
		{"terminal", false, false, false, true, true},
		// Pipes and files stay plain.
		{"pipe", false, false, false, false, false},
		// NO_COLOR is the standard opt-out and always wins, even forced.
		{"no-color", true, false, false, true, false},
		{"no-color beats force", true, false, true, true, false},
		// TERM=dumb suppresses color unless it is forced back on.
		{"dumb", false, true, false, true, false},
		{"force beats dumb", false, true, true, true, true},
		{"force beats pipe", false, false, true, false, true},
	}
	for _, c := range cases {
		if got := decide(c.noColor, c.dumb, c.force, c.tty); got != c.want {
			t.Errorf("%s: decide() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPainting(t *testing.T) {
	on := Palette{enabled: true}
	if got := on.Green("ok"); got != "\x1b[32mok\x1b[0m" {
		t.Errorf("Green() = %q", got)
	}
	if got := on.BoldCyan("HOLDER"); !strings.HasPrefix(got, "\x1b[1;36m") {
		t.Errorf("BoldCyan() = %q", got)
	}
	// Empty strings are never wrapped: a stray escape pair is noise.
	if got := on.Dim(""); got != "" {
		t.Errorf("Dim(\"\") = %q", got)
	}
	// Plain returns text unchanged, byte for byte.
	for _, s := range []string{"ok", "", "queue \"x\": FREE"} {
		if got := Plain.Red(s); got != s {
			t.Errorf("Plain.Red(%q) = %q", s, got)
		}
	}
}

func TestForOnAPipe(t *testing.T) {
	// A bytes.Buffer is never a terminal, so without forcing, color is off.
	if p := For(new(bytes.Buffer)); p.enabled {
		t.Error("For(pipe) should be plain")
	}
}
