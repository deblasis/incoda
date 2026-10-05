package fixline

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestQuote(t *testing.T) {
	for _, c := range []struct {
		sh   Shell
		in   string
		want string
	}{
		{POSIX, "it's", `'it'\''s'`},
		{POSIX, `a\b`, `'a\b'`},
		{PowerShell, "it's", `'it''s'`},
		{PowerShell, `C:\x y\`, `'C:\x y\'`},
		// PowerShell treats the Unicode smart quotes as single-quote
		// characters too (open, close, and doubled escape), so each one
		// must be doubled exactly like the ASCII apostrophe or it closes
		// the quoted string early.
		{PowerShell, "it\u2018s", "'it\u2018\u2018s'"},
		{PowerShell, "it\u2019s", "'it\u2019\u2019s'"},
		{PowerShell, "it\u201As", "'it\u201A\u201As'"},
		{PowerShell, "it\u201Bs", "'it\u201B\u201Bs'"},
		// $, a backtick and ; have no special meaning inside a PowerShell
		// single-quoted string: they print through unchanged.
		{PowerShell, "$HOME `cmd` ;x", "'$HOME `cmd` ;x'"},
	} {
		if got := Quote(c.sh, c.in); got != c.want {
			t.Errorf("Quote(%v, %q) = %s, want %s", c.sh, c.in, got, c.want)
		}
	}
}

func TestRenderRunLine(t *testing.T) {
	r := Run{
		Queue: []string{"builds", "kungfoo-gate"},
		Pool:  []string{"tests"},
		Flags: []Flag{{Name: "exclusive", Value: "true", Bool: true}, {Name: "quiet", Value: "false", Bool: true},
			{Name: "reason", Value: "kungfoo gate"}, {Name: "wait", Value: "30m"}},
		Argv: []string{"just", "gate"},
		Dir:  "/src/kungfoo", Here: "/src/kungfoo",
	}
	if got, want := r.Line().Render(POSIX).Text, `incoda run --queue builds,kungfoo-gate --pool tests --exclusive --quiet=false --reason 'kungfoo gate' --wait '30m' -- 'just' 'gate'`; got != want {
		t.Fatalf("POSIX:\n got %s\nwant %s", got, want)
	}
	if got, want := r.Line().Render(PowerShell).Text, `incoda run --queue 'builds,kungfoo-gate' --pool 'tests' --exclusive --quiet=false --reason 'kungfoo gate' --wait '30m' '--' 'just' 'gate'`; got != want {
		t.Fatalf("PowerShell:\n got %s\nwant %s", got, want)
	}
	r.Here = "/elsewhere"
	if got := r.Line().Render(POSIX).Text; !strings.HasPrefix(got, `cd '/src/kungfoo' && incoda run `) {
		t.Fatalf("a different directory changes into it first: %s", got)
	}
	r.Dir = `C:\src\kung foo`
	if got := r.Line().Render(PowerShell).Text; !strings.HasPrefix(got, `Set-Location -LiteralPath 'C:\src\kung foo'; if ($?) { incoda run `) || !strings.HasSuffix(got, " }") {
		t.Fatalf("PowerShell changes directory with Set-Location and if ($?): %s", got)
	}
	r.Dir = ""
	if rd := r.Line().Render(POSIX); rd.Why != "" || !rd.DirUnknown || strings.HasPrefix(rd.Text, "cd ") {
		t.Fatalf("an unknown directory prints the line without a directory part: %+v", rd)
	}
	r.Dir = "relative/dir"
	if rd := r.Line().Render(POSIX); rd.Text != "" || rd.Why == "" {
		t.Fatalf("a relative directory gives no runnable line: %+v", rd)
	}

	// A right single quotation mark (U+2019) in a flag value and in the cwd
	// is doubled like an ASCII apostrophe on PowerShell, not left to close
	// the quoted string early.
	rq := Run{Queue: []string{"q"}, Flags: []Flag{{Name: "reason", Value: "Alex\u2019s run"}},
		Argv: []string{"x"}, Dir: `C:\Users\Alex` + "\u2019s stuff", Here: `C:\Users\Alex` + "\u2019s stuff"}
	if got, want := rq.Line().Render(PowerShell).Text,
		"incoda run --queue 'q' --reason 'Alex\u2019\u2019s run' '--' 'x'"; got != want {
		t.Fatalf("smart quote in a value:\n got %s\nwant %s", got, want)
	}
	rq.Here = `C:\elsewhere`
	if got, want := rq.Line().Render(PowerShell).Text,
		"Set-Location -LiteralPath 'C:\\Users\\Alex\u2019\u2019s stuff'; if ($?) { incoda run --queue 'q' --reason 'Alex\u2019\u2019s run' '--' 'x' }"; got != want {
		t.Fatalf("smart quote in the cwd:\n got %s\nwant %s", got, want)
	}

	// A flag value that looks like a flag (starts with --) is still one
	// quoted word: quoting, not position, is what keeps it from being
	// read as a flag of the pasted command.
	rf := Run{Queue: []string{"q"}, Flags: []Flag{{Name: "reason", Value: "--sneaky"}}, Argv: []string{"x"}, Dir: "/d", Here: "/d"}
	if got, want := rf.Line().Render(POSIX).Text, `incoda run --queue q --reason '--sneaky' -- 'x'`; got != want {
		t.Fatalf("flag value starting with --: got %s want %s", got, want)
	}
}

// TestNoRunnableLine: a value no quoting can carry gives no runnable line
// and the fields instead, never a placeholder.
func TestNoRunnableLine(t *testing.T) {
	base := Run{Queue: []string{"q"}, Argv: []string{"x"}, Dir: "/d", Here: "/d"}
	for _, c := range []struct {
		name string
		sh   Shell
		argv []string
		ok   bool
	}{
		{"tab", POSIX, []string{"a\tb"}, false},
		{"escape", PowerShell, []string{"a\x1bb"}, false},
		{"bidi", POSIX, []string{"a\u202eb"}, false},
		{"bad utf-8", POSIX, []string{"a\xffb"}, false},
		{"newline", POSIX, []string{"a\nb"}, false},
		{"newline on PowerShell", PowerShell, []string{"a\nb"}, false},
		{"double quote on PowerShell", PowerShell, []string{`say "hi"`}, false},
		{"empty on PowerShell", PowerShell, []string{""}, false},
		{"space and trailing backslash on PowerShell", PowerShell, []string{`C:\a b\`}, false},
		{"no-break space and trailing backslash on PowerShell", PowerShell, []string{"C:\\a\u00a0b\\"}, false},
		{"mongolian vowel separator and trailing backslash on PowerShell", PowerShell, []string{"C:\\a\u180Eb\\"}, false},
		{"double quote on POSIX", POSIX, []string{`say "hi"`}, true},
		{"empty on POSIX", POSIX, []string{""}, true},
		{"space and trailing backslash on POSIX", POSIX, []string{`C:\a b\`}, true},
		{"no-break space and trailing backslash on POSIX", POSIX, []string{"C:\\a\u00a0b\\"}, true},
		{"mongolian vowel separator and trailing backslash on POSIX", POSIX, []string{"C:\\a\u180Eb\\"}, true},
		{"backslash on PowerShell", PowerShell, []string{`a\b`}, true},
	} {
		r := base
		r.Argv = c.argv
		lines := RunLines(c.sh, r, "", "run it")
		if c.ok != (len(lines) == 1) {
			t.Errorf("%s: %q", c.name, lines)
			continue
		}
		if !c.ok && (!strings.HasPrefix(lines[0], "no runnable command (") || !strings.HasSuffix(lines[0], "); run it with these fields:")) {
			t.Errorf("%s: %q", c.name, lines)
		}
	}
	lines := RunLines(POSIX, Run{Queue: []string{"q"}, Pool: []string{"tests"}, Flags: []Flag{{Name: "reason", Value: "r\x1b"}, {Name: "exclusive", Value: "true", Bool: true}},
		Argv: []string{"a", "b"}, Here: "/w"}, "", "run it")
	want := []string{
		"no runnable command (a value contains a control, bidi or invalid UTF-8 character); run it with these fields:",
		"  queue: q", "  pool: tests", `  flags: "--exclusive"`, `  reason: r\x1b`, "  cwd: (unknown)", `  command: "a" "b"`,
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("fields:\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	if got := RunLines(POSIX, base, `queue "q" requires --reason and this run has none`, "run it"); !strings.HasPrefix(got[0], `no runnable command (queue "q" requires --reason`) {
		t.Fatalf("a reason the caller knows rules the line out: %q", got)
	}

	// A command word with a backslash is quoted exactly once (strconv.Quote
	// alone), so the printed field can be read back to the original value;
	// textsafe.Escape first would double the backslash a second time and
	// make it unrecoverable.
	bs := RunLines(PowerShell, Run{Queue: []string{"q"}, Argv: []string{`C:\a b\`}, Here: "/w"}, "", "run it")
	wantBS := `  command: "C:\\a b\\"`
	found := false
	for _, l := range bs {
		if l == wantBS {
			found = true
		}
	}
	if !found {
		t.Fatalf("command field with a backslash:\n%s\nwant line %s", strings.Join(bs, "\n"), wantBS)
	}
}

// TestInvalidKey: every comma-separated part of --queue and --pool must
// pass lane.ValidateKey, and an unset Queue or Pool that would still print
// its flag (Pool non-nil but empty) is caught the same way, since joining
// gives the empty key lane.ValidateKey rejects.
func TestInvalidKey(t *testing.T) {
	for _, c := range []struct {
		name string
		r    Run
	}{
		{"queue has a bad character", Run{Queue: []string{"ok", "bad key!"}, Argv: []string{"x"}, Dir: "/d", Here: "/d"}},
		{"queue is empty", Run{Queue: nil, Argv: []string{"x"}, Dir: "/d", Here: "/d"}},
		{"pool set but empty", Run{Queue: []string{"ok"}, Pool: []string{}, Argv: []string{"x"}, Dir: "/d", Here: "/d"}},
		{"pool has a bad character", Run{Queue: []string{"ok"}, Pool: []string{"bad!", "ok"}, Argv: []string{"x"}, Dir: "/d", Here: "/d"}},
	} {
		for _, sh := range []Shell{POSIX, PowerShell} {
			if rd := c.r.Line().Render(sh); rd.Why != "invalid key" {
				t.Errorf("%s (%v): Why = %q, want %q", c.name, sh, rd.Why, "invalid key")
			}
			lines := RunLines(sh, c.r, "", "run it")
			if !strings.HasPrefix(lines[0], "no runnable command (invalid key); run it with these fields:") {
				t.Errorf("%s (%v): %q", c.name, sh, lines)
			}
		}
	}
	// A valid multi-key queue still renders.
	ok := Run{Queue: []string{"builds", "kungfoo-gate"}, Argv: []string{"x"}, Dir: "/d", Here: "/d"}
	if rd := ok.Line().Render(POSIX); rd.Why != "" {
		t.Fatalf("a valid key list must still render: %+v", rd)
	}
}

// TestFlagsFieldDefault: the no-runnable "flags" field reads "(none)" when
// the run carries no flags, never an empty value.
func TestFlagsFieldDefault(t *testing.T) {
	lines := RunLines(POSIX, Run{Queue: []string{"q"}, Argv: []string{"a\tb"}, Dir: "/d", Here: "/d"}, "", "run it")
	found := false
	for _, l := range lines {
		if l == "  flags: (none)" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no \"flags: (none)\" line: %q", lines)
	}
}

// argvStub builds internal/testprog/argv as "incoda" in a directory of its
// own and returns that directory.
func argvStub(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	name := "incoda"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, name), "github.com/deblasis/incoda/internal/testprog/argv")
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=auto")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build argv: %v\n%s", err, out)
	}
	return dir
}

// paste runs line in the native shell with PATH set to the stub directory
// followed only by the shell's own minimal bin directories, never the
// caller's full PATH, so nothing but the stub can ever be reached as
// "incoda" (or anything else a pasted line might name). Returns the
// arguments and directory the stub saw (ok false when it never ran).
func paste(t *testing.T, stub, line string) (args []string, cwd string, ok bool) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "argv.json")
	var cmd *exec.Cmd
	var path string
	if runtime.GOOS == "windows" {
		ps, err := exec.LookPath("powershell")
		if err != nil {
			t.Skip("no powershell on this machine")
		}
		cmd = exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", line)
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		path = stub + string(os.PathListSeparator) + root + `\System32` + string(os.PathListSeparator) + root + `\System32\WindowsPowerShell\v1.0`
	} else {
		cmd = exec.Command("/bin/sh", "-c", line)
		path = stub + string(os.PathListSeparator) + "/usr/bin:/bin"
	}
	env := []string{"ARGV_OUT=" + out, "PATH=" + path}
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.EqualFold(k, "PATH") || strings.EqualFold(k, "ARGV_OUT") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = env
	_ = cmd.Run()
	b, err := os.ReadFile(out)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Args []string `json:"args"`
		Cwd  string   `json:"cwd"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	return got.Args, got.Cwd, true
}

// TestPastedLineReproducesArgvAndDirectory: a printed run line, pasted into
// the native shell, reaches incoda with exactly the arguments and the
// directory it names, for every value class that survives quoting; and a
// directory that does not exist never runs the command (spec 9, fix-line
// reproduction).
func TestPastedLineReproducesArgvAndDirectory(t *testing.T) {
	stub := argvStub(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Windows PowerShell 5.1 strips an embedded double quote from an argument
	// passed to a native program, so a value carrying one is exactly the
	// class Problem() refuses rather than one it renders (spec 9). Only the
	// shell that can pass them on intact is exercised.
	argv := []string{"it's", "a;b", "$(echo pwned)", "two words", "back`tick", `a\b`, `grep 'a\.b'`, "--"}
	if Native() == POSIX {
		argv = append([]string{`say "hi"`}, argv...)
		argv = append(argv, `C:\Program Files\x\`, "")
	}
	r := Run{Queue: []string{"cap-gate"}, Pool: []string{"tests"},
		Flags: []Flag{{Name: "reason", Value: "it's a 'gate'"}, {Name: "exclusive", Value: "true", Bool: true}},
		Argv:  argv, Dir: dir, Here: "/"}
	rd := r.Line().Render(Native())
	if rd.Why != "" {
		t.Fatalf("no runnable line: %s", rd.Why)
	}
	args, cwd, ok := paste(t, stub, rd.Text)
	if !ok {
		t.Fatalf("the stub never ran: %s", rd.Text)
	}
	want := append([]string{"run", "--queue", "cap-gate", "--pool", "tests", "--reason", "it's a 'gate'", "--exclusive", "--"}, argv...)
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv:\n got %q\nwant %q\nline: %s", args, want, rd.Text)
	}
	if cwd != dir {
		t.Fatalf("cwd %s, want %s", cwd, dir)
	}

	r.Argv = []string{"x"}
	r.Dir = filepath.Join(dir, "does-not-exist")
	rd = r.Line().Render(Native())
	if _, _, ok := paste(t, stub, rd.Text); ok {
		t.Fatalf("a missing directory must never run the command: %s", rd.Text)
	}

	r.Dir = ""
	rd = r.Line().Render(Native())
	if !rd.DirUnknown {
		t.Fatalf("an empty directory is unknown: %+v", rd)
	}
	if args, _, ok := paste(t, stub, rd.Text); !ok || args[len(args)-1] != "x" {
		t.Fatalf("an unknown directory still runs the command where it is pasted: %v %q", ok, args)
	}
}
