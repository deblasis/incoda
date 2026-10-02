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
		{"double quote on PowerShell", PowerShell, []string{`say "hi"`}, false},
		{"empty on PowerShell", PowerShell, []string{""}, false},
		{"space and trailing backslash on PowerShell", PowerShell, []string{`C:\a b\`}, false},
		{"double quote on POSIX", POSIX, []string{`say "hi"`}, true},
		{"empty on POSIX", POSIX, []string{""}, true},
		{"space and trailing backslash on POSIX", POSIX, []string{`C:\a b\`}, true},
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
		"  queue: q", "  pool: tests", "  flags: --exclusive", `  reason: r\x1b`, "  cwd: /w", "  command: a b",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("fields:\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	if got := RunLines(POSIX, base, `queue "q" requires --reason and this run has none`, "run it"); !strings.HasPrefix(got[0], `no runnable command (queue "q" requires --reason`) {
		t.Fatalf("a reason the caller knows rules the line out: %q", got)
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

// paste runs line in the native shell with the stub first on PATH and
// returns the arguments and directory the stub saw (ok false when it never
// ran).
func paste(t *testing.T, stub, line string) (args []string, cwd string, ok bool) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "argv.json")
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		ps, err := exec.LookPath("powershell")
		if err != nil {
			t.Skip("no powershell on this machine")
		}
		cmd = exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", line)
	} else {
		cmd = exec.Command("/bin/sh", "-c", line)
	}
	env := []string{"ARGV_OUT=" + out}
	for _, kv := range os.Environ() {
		if k, v, _ := strings.Cut(kv, "="); strings.EqualFold(k, "PATH") {
			env = append(env, k+"="+stub+string(os.PathListSeparator)+v)
		} else {
			env = append(env, kv)
		}
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
	argv := []string{"it's", `say "hi"`, "a;b", "$(echo pwned)", "two words", "back`tick", `a\b`, `grep 'a\.b'`}
	if Native() == POSIX {
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
