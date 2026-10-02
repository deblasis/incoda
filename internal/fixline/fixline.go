// Package fixline builds the commands incoda prints for a person or an
// agent to paste after a refusal (spec 2.6, "Fix lines"): POSIX sh on Unix,
// PowerShell on Windows, never cmd.exe.
//
// Every value is one single-quoted word, emitted raw inside its quotes and
// never escaped for display: a backslash is literal inside single quotes in
// both shells. A value that no quoting can carry safely (a control, bidi or
// invalid UTF-8 character anywhere; on PowerShell also a double quote, an
// empty value, or a space before a trailing backslash) gives no runnable
// line at all, never a line with a placeholder: the caller prints the
// fields instead (NoRunnable, Fields).
package fixline

import (
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/textsafe"
)

// Shell is the syntax a line is written in.
type Shell int

const (
	// POSIX is sh: Unix.
	POSIX Shell = iota
	// PowerShell is Windows PowerShell 5.1 and later.
	PowerShell
)

// Native is the shell this platform's lines are written for.
func Native() Shell {
	if runtime.GOOS == "windows" {
		return PowerShell
	}
	return POSIX
}

type kind int

const (
	lit kind = iota
	key
	val
	sep
)

// Word is one word of a printed command.
type Word struct {
	text string
	kind kind
}

// Lit is printed bare in both shells: "incoda", a subcommand, a flag name.
func Lit(s string) Word { return Word{s, lit} }

// Key is a queue key or a comma-separated list of keys. Keys pass
// lane.ValidateKey (letters, digits, '-', '_', '.'), so POSIX prints them
// bare. PowerShell quotes them: a bare comma there builds an array, which
// reaches the program as several arguments.
func Key(s string) Word { return Word{s, key} }

// Val is every other value: a reason, an owner, a wait, a word of the
// command. It is always one single-quoted word.
func Val(s string) Word { return Word{s, val} }

// Sep is the "--" before the command: bare on POSIX, '--' on PowerShell,
// where a bare -- is the end-of-parameters token and is not passed on.
func Sep() Word { return Word{"--", sep} }

// isSmartQuote reports whether r is a character PowerShell's single-quoted
// strings also treat as a quote delimiter: the ASCII apostrophe, and the
// Unicode left, right, low-9 and reversed-9 single quotation marks, which
// PowerShell's tokenizer accepts in place of an ASCII quote and which
// CodeGeneration.EscapeSingleQuotedStringContent doubles just like it.
func isSmartQuote(r rune) bool {
	switch r {
	case '\'', '‘', '’', '‚', '‛':
		return true
	}
	return false
}

// hasSpace reports whether s contains any Unicode space character, not
// only ASCII space and tab: Windows PowerShell 5.1's native-argument
// re-quoting is triggered by any character unicode.IsSpace accepts (for
// example U+00A0 no-break space or U+3000 ideographic space), not only
// " \t".
func hasSpace(s string) bool {
	for _, r := range s {
		if unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

// Quote renders s as one single-quoted word: on POSIX each single quote
// inside is closed, escaped with a backslash and reopened; on PowerShell
// every character isSmartQuote accepts is doubled.
func Quote(sh Shell, s string) string {
	if sh == PowerShell {
		var b strings.Builder
		b.Grow(len(s) + 2)
		b.WriteByte('\'')
		for _, r := range s {
			b.WriteRune(r)
			if isSmartQuote(r) {
				b.WriteRune(r)
			}
		}
		b.WriteByte('\'')
		return b.String()
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Problem says why s cannot be printed as a word of a runnable line in sh,
// or "" when it can.
func Problem(sh Shell, s string) string {
	if textsafe.Unsafe(s) {
		return "a value contains a control, bidi or invalid UTF-8 character"
	}
	if sh != PowerShell {
		return ""
	}
	// Windows PowerShell 5.1 calling a native program strips embedded
	// double quotes, drops empty arguments, and re-quotes an argument with a
	// space that ends in a backslash as "...\", which the program reads as
	// an escaped quote.
	switch {
	case strings.Contains(s, `"`):
		return "a value contains a double quote, which PowerShell does not pass on intact"
	case s == "":
		return "a value is empty, which PowerShell does not pass on"
	case hasSpace(s) && strings.HasSuffix(s, `\`):
		return "a value with a space ends in a backslash, which PowerShell does not pass on intact"
	}
	return ""
}

// invalidKeyReason reports why s cannot be printed as a queue or pool key
// list, or "" when every comma-separated part of s passes
// lane.ValidateKey. An empty s (an unset Queue or Pool where the line
// would print a flag with no value) is invalid: splitting "" on "," yields
// one empty part, which lane.ValidateKey rejects.
func invalidKeyReason(s string) string {
	for _, part := range strings.Split(s, ",") {
		if err := lane.ValidateKey(part); err != nil {
			return "invalid key"
		}
	}
	return ""
}

// Line is a command to print and the directory it must run from.
type Line struct {
	Words []Word
	// Dir is the directory the command must run from and Here this
	// process's directory. When they differ the line changes into Dir
	// first, in a form that never runs the command anywhere else. An empty
	// Dir with a non-empty Here means the directory is unknown: the line
	// has no directory part and Rendered.DirUnknown is set.
	Dir, Here string
}

// Rendered is a line ready to print, or why there is none.
type Rendered struct {
	// Text is the runnable line; empty when Why is set.
	Text string
	// Why says why no runnable line can be printed.
	Why string
	// DirUnknown is set when the line has no directory part because the
	// directory it must run from is not known (DirUnknownText).
	DirUnknown bool
}

// DirUnknownText goes on the line before a line whose directory is
// unknown.
const DirUnknownText = "the outer job's directory is unknown; run this from it"

// Render builds the line for sh.
func (l Line) Render(sh Shell) Rendered {
	parts := make([]string, 0, len(l.Words))
	for _, w := range l.Words {
		switch w.kind {
		case lit:
			parts = append(parts, w.text)
		case key:
			if why := invalidKeyReason(w.text); why != "" {
				return Rendered{Why: why}
			}
			if sh == PowerShell {
				if why := Problem(sh, w.text); why != "" {
					return Rendered{Why: why}
				}
				parts = append(parts, Quote(sh, w.text))
			} else {
				parts = append(parts, w.text)
			}
		case sep:
			if sh == PowerShell {
				parts = append(parts, Quote(sh, w.text))
			} else {
				parts = append(parts, w.text)
			}
		default:
			if why := Problem(sh, w.text); why != "" {
				return Rendered{Why: why}
			}
			parts = append(parts, Quote(sh, w.text))
		}
	}
	cmd := strings.Join(parts, " ")
	switch {
	case l.Dir == l.Here:
		return Rendered{Text: cmd}
	case l.Dir == "":
		return Rendered{Text: cmd, DirUnknown: true}
	case !isAbs(sh, l.Dir):
		return Rendered{Why: "the directory it must run from is not an absolute path"}
	}
	if why := Problem(sh, l.Dir); why != "" {
		return Rendered{Why: why}
	}
	if sh == PowerShell {
		return Rendered{Text: "Set-Location -LiteralPath " + Quote(sh, l.Dir) + "; if ($?) { " + cmd + " }"}
	}
	return Rendered{Text: "cd " + Quote(sh, l.Dir) + " && " + cmd}
}

// isAbs is filepath.IsAbs for the shell's platform, so the PowerShell form
// can be tested on Unix too.
func isAbs(sh Shell, p string) bool {
	if sh == Native() {
		return filepath.IsAbs(p)
	}
	if sh == PowerShell {
		return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') || strings.HasPrefix(p, `\\`)
	}
	return strings.HasPrefix(p, "/")
}

// NoRunnable is the line printed instead of a runnable one: lead says what
// to do with the fields that follow ("run it", "rerun the outer job").
func NoRunnable(why, lead string) string {
	return "no runnable command (" + why + "); " + lead + " with these fields:"
}

// Field is one line of the fields printed instead of a runnable command.
type Field struct{ Name, Value string }

// FieldLines renders fields as "name: value" lines. The "flags" and
// "command" fields Run.Fields builds are already rendered as one escaped,
// double-quoted Go string per word (quoteWords), which keeps a space
// inside one word from reading as a boundary between two; those two
// values are printed as-is. Every other field's value is escaped for
// display (spec 4.6).
func FieldLines(fs []Field) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		switch f.Name {
		case "flags", "command":
			out[i] = f.Name + ": " + f.Value
		default:
			out[i] = f.Name + ": " + textsafe.Escape(f.Value)
		}
	}
	return out
}

// quoteWords renders the no-runnable "flags" and "command" fields so
// argument boundaries are recoverable from the printed text: each word is
// escaped for display (textsafe.Escape) and then wrapped in Go double
// quotes (strconv.Quote), joined by single spaces, for example
// `"just" "a b"`. "(none)" when there are no words.
func quoteWords(words []string) string {
	if len(words) == 0 {
		return "(none)"
	}
	parts := make([]string, len(words))
	for i, w := range words {
		parts[i] = strconv.Quote(textsafe.Escape(w))
	}
	return strings.Join(parts, " ")
}

// Flag is one flag of a printed run line. A switch carries Bool: it prints
// as --name when Value is "true" and as --name=false otherwise.
type Flag struct {
	Name, Value string
	Bool        bool
}

// Run is an incoda run command line.
type Run struct {
	// Queue is every key the line names, in the order to print.
	Queue []string
	// Pool is the --pool set; nil prints no --pool.
	Pool []string
	// Flags are the carried flags, in the order to print.
	Flags []Flag
	// Argv is the command after --.
	Argv []string
	// Dir and Here are Line's.
	Dir, Here string
}

// Line is the run line as words.
func (r Run) Line() Line {
	w := []Word{Lit("incoda"), Lit("run"), Lit("--queue"), Key(strings.Join(r.Queue, ","))}
	if r.Pool != nil {
		w = append(w, Lit("--pool"), Key(strings.Join(r.Pool, ",")))
	}
	for _, f := range r.Flags {
		switch {
		case f.Bool && f.Value == "true":
			w = append(w, Lit("--"+f.Name))
		case f.Bool:
			w = append(w, Lit("--"+f.Name+"=false"))
		default:
			w = append(w, Lit("--"+f.Name), Val(f.Value))
		}
	}
	w = append(w, Sep())
	for _, a := range r.Argv {
		w = append(w, Val(a))
	}
	return Line{Words: w, Dir: r.Dir, Here: r.Here}
}

// Fields are the run line's parts for NoRunnable: queue, pool (when set),
// flags (every carried flag but the reason, "(none)" when there are none),
// reason (when set), cwd ("(unknown)" when Dir is empty, since an empty
// Dir never means "here": printing Here instead would fabricate the outer
// job's directory) and the command, one line each.
func (r Run) Fields() []Field {
	fs := []Field{{"queue", strings.Join(r.Queue, ",")}}
	if r.Pool != nil {
		fs = append(fs, Field{"pool", strings.Join(r.Pool, ",")})
	}
	var flagWords []string
	reason, hasReason := "", false
	for _, f := range r.Flags {
		switch {
		case f.Name == "reason":
			reason, hasReason = f.Value, true
		case f.Bool && f.Value == "true":
			flagWords = append(flagWords, "--"+f.Name)
		case f.Bool:
			flagWords = append(flagWords, "--"+f.Name+"=false")
		default:
			flagWords = append(flagWords, "--"+f.Name, f.Value)
		}
	}
	fs = append(fs, Field{"flags", quoteWords(flagWords)})
	if hasReason {
		fs = append(fs, Field{"reason", reason})
	}
	dir := r.Dir
	if dir == "" {
		dir = "(unknown)"
	}
	fs = append(fs, Field{"cwd", dir}, Field{"command", quoteWords(r.Argv)})
	return fs
}

// RunLines is what a refusal prints for r: the runnable line indented
// under a lead, or NoRunnable and the fields when there is none. why, when
// not empty, is a reason the caller already knows that rules a runnable
// line out (a lane in the line requires a reason and none is known).
// Every returned line is the text after "incoda: ".
func RunLines(sh Shell, r Run, why, lead string) []string {
	var rd Rendered
	if why == "" {
		rd = r.Line().Render(sh)
		why = rd.Why
	}
	if why != "" {
		out := []string{NoRunnable(why, lead)}
		for _, f := range FieldLines(r.Fields()) {
			out = append(out, "  "+f)
		}
		return out
	}
	if rd.DirUnknown {
		return []string{DirUnknownText, "  " + rd.Text}
	}
	return []string{"  " + rd.Text}
}
