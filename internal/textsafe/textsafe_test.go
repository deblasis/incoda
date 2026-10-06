package textsafe

import (
	"strings"
	"testing"
)

func TestEscape(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain text", "plain text"},
		{`a\b`, `a\\b`},
		{"line\nbreak", `line\x0abreak`},
		{"esc\x1b[31m", `esc\x1b[31m`},
		{"tab\there", `tab\x09here`},
		{"del\x7f", `del\x7f`},
		{"c1\u0085", `c1\x85`},
		{"rlo\u202Eevil", `rlo\u{202E}evil`},
		{"iso\u2066x", `iso\u{2066}x`},
		{"lrm\u200E", `lrm\u{200E}`},
		{"bad\xffbyte", `bad\xffbyte`},
		{"unicode ok: café ✓", "unicode ok: café ✓"},
	}
	for _, c := range cases {
		if got := Escape(c.in); got != c.want {
			t.Errorf("Escape(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestUnsafeIgnoresBackslash(t *testing.T) {
	if Unsafe(`C:\path\to`) {
		t.Fatal("a backslash alone is not unsafe")
	}
	for _, s := range []string{"a\nb", "a\tb", "\x1b", "\u202E", "\xff"} {
		if !Unsafe(s) {
			t.Fatalf("Unsafe(%q) should be true", s)
		}
	}
}

func TestCheckWrite(t *testing.T) {
	if err := CheckWrite("description", "fine text"); err != nil {
		t.Fatal(err)
	}
	err := CheckWrite("description", "two\nlines")
	if err == nil || err.Error() != "bad-text: description contains control characters" {
		t.Fatalf("got %v", err)
	}
	if err := CheckWrite("description", "rlo\u202E"); err == nil {
		t.Fatal("a bidi override must be refused")
	}
	if err := CheckWrite("closed", strings.Repeat("x", 201)); err == nil || !strings.Contains(err.Error(), "longer than 200") {
		t.Fatalf("got %v", err)
	}
}

func TestLogValue(t *testing.T) {
	cases := []struct{ in, want string }{
		{"zig", "zig"},
		{"zig build", `"zig build"`},
		{"a\nb", `"a\\x0ab"`},
		{"", `""`},
	}
	for _, c := range cases {
		if got := LogValue(c.in); got != c.want {
			t.Errorf("LogValue(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
