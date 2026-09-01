// Package colorize renders the small ANSI palette incoda uses to make its
// human output readable at a glance: green for free, yellow for waiting,
// cyan for holders, dim for metadata.
//
// The policy follows the conventions people already know:
//
//   - Color is on only for terminal output; pipes and files get plain text.
//   - NO_COLOR (https://no-color.org), set to any non-empty value, turns
//     color off even on a terminal. This is the standard opt-out and always
//     wins.
//   - TERM=dumb turns color off.
//   - --no-color turns it off for one invocation.
//   - CLICOLOR_FORCE, set to anything other than 0, turns color on even for
//     a pipe, for `incoda status | less -R` and its siblings.
//
// The codes are the classic 8-color SGR set plus bold and dim, so the output
// adapts to the terminal's own theme instead of fighting it. JSON output is
// never painted; the palette only touches the human renderers.
package colorize

import (
	"io"
	"os"
)

// Plain is a palette that never paints.
var Plain = Palette{}

// Palette paints strings with SGR attributes, or returns them unchanged when
// color is off. A Palette is immutable and safe to share.
type Palette struct {
	enabled bool
}

// For decides whether w should be painted and returns the palette for it.
func For(w io.Writer) Palette {
	tty := isTerminal(w)
	if !decide(os.Getenv("NO_COLOR") != "", os.Getenv("TERM") == "dumb", envTrue("CLICOLOR_FORCE"), tty) {
		return Plain
	}
	if !prepare(w) {
		return Plain
	}
	return Palette{enabled: true}
}

// decide is the portable heart of For, separated out so the policy can be
// tested without a terminal. noColorEnv is NO_COLOR set to a non-empty
// value; force is CLICOLOR_FORCE set to something other than 0; tty reports
// whether the output is a terminal.
func decide(noColorEnv, termDumb, force, tty bool) bool {
	if noColorEnv {
		return false
	}
	if force {
		return true
	}
	if termDumb || !tty {
		return false
	}
	return true
}

func envTrue(key string) bool {
	v := os.Getenv(key)
	return v != "" && v != "0"
}

// paint wraps s in the SGR sequence for code, or returns it unchanged.
func (p Palette) paint(code, s string) string {
	if !p.enabled || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (p Palette) Red(s string) string        { return p.paint("31", s) }
func (p Palette) Green(s string) string      { return p.paint("32", s) }
func (p Palette) Yellow(s string) string     { return p.paint("33", s) }
func (p Palette) Cyan(s string) string       { return p.paint("36", s) }
func (p Palette) Bold(s string) string       { return p.paint("1", s) }
func (p Palette) Dim(s string) string        { return p.paint("2", s) }
func (p Palette) BoldRed(s string) string    { return p.paint("1;31", s) }
func (p Palette) BoldGreen(s string) string  { return p.paint("1;32", s) }
func (p Palette) BoldYellow(s string) string { return p.paint("1;33", s) }
func (p Palette) BoldCyan(s string) string   { return p.paint("1;36", s) }
