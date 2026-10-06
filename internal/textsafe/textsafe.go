// Package textsafe renders text that came from somewhere else (stored state,
// argv, the environment, another process) so it can be printed to a terminal
// or written as one log line without changing either.
//
// One escaper is used for every human-facing output. A string with an ESC
// sequence, a newline or a bidi override would otherwise repaint a terminal,
// forge a log line, or reorder what a person reads.
package textsafe

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// maxWrite is the longest description or closed text a write accepts.
const maxWrite = 200

// unsafeRune reports whether r is in the control, DEL, C1 or bidi classes.
// The backslash is handled separately because it is safe inside a quoted fix
// line but must be doubled in display text.
func unsafeRune(r rune) bool {
	switch {
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
		return true
	case r == 0x200e, r == 0x200f:
		return true
	case r >= 0x202a && r <= 0x202e:
		return true
	case r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// Escape renders s for display: control characters, DEL and C1 as \xNN, LRM,
// RLM, the bidi overrides and isolates as \u{NNNN}, invalid UTF-8 bytes as
// \xNN, and a backslash as \\. Everything else is unchanged.
func Escape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\\':
			b.WriteString(`\\`)
		case unsafeRune(r) && r < 0x100:
			fmt.Fprintf(&b, `\x%02x`, r)
		case unsafeRune(r):
			fmt.Fprintf(&b, `\u{%04X}`, r)
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// Unsafe reports whether s holds anything Escape would change other than a
// backslash: a control, DEL, C1 or bidi character, or invalid UTF-8.
func Unsafe(s string) bool {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if (r == utf8.RuneError && size == 1) || unsafeRune(r) {
			return true
		}
		i += size
	}
	return false
}

// CheckWrite refuses text a config write must not store: control, DEL, C1
// and bidi characters, invalid UTF-8, or more than 200 characters.
func CheckWrite(field, s string) error {
	if Unsafe(s) {
		return fmt.Errorf("bad-text: %s contains control characters", field)
	}
	if utf8.RuneCountInString(s) > maxWrite {
		return fmt.Errorf("bad-text: %s is longer than %d characters", field, maxWrite)
	}
	return nil
}

// LogValue renders s as one k=v value in lane.log: escaped, and quoted with
// Go %q when it is empty, contains a space or a quote, or needed escaping, so
// one event is always one line.
func LogValue(s string) string {
	e := Escape(s)
	if s == "" || e != s || strings.ContainsAny(s, " \"") {
		return strconv.Quote(e)
	}
	return e
}
