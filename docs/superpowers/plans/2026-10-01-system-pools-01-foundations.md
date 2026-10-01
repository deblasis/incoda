# System pools, plan 1: Foundations

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the shipped process-group bug, give `INCODA_HELD` ticket identity with liveness and ancestry verification, add one terminal-safe escaper, and make config writes a locked read-modify-write that keeps unknown fields, all on today's `queues/` layout.

**Architecture:** incoda captures its start environment once and never calls `os.Setenv` on itself; the child's environment and process-group decision are passed to `child.Run` explicitly. A new `internal/held` package parses, verifies and formats `KEY=TICKET` entries, using a new `internal/procinfo` package for the parent chain and a create-free `lockfile.OpenExisting` for probes. A new `internal/textsafe` package owns escaping. `lane.Config` gains schema 2 fields and `Queue.UpdateConfig`.

**Tech Stack:** Go 1.27, `golang.org/x/sys` (unix, windows), standard library. Tests: `go test`, integration tests that build and run the real binary.

**Spec:** `docs/superpowers/specs/2026-10-01-system-pools-design.md` (sections 2.2, 2.6 "Process group, Unix", 2.6 verification steps 1 to 3, 4.4, 4.6). Plan index: `docs/superpowers/plans/2026-10-01-system-pools-00-index.md`.

## Global Constraints

- Go 1.27.0 or newer; no new module dependencies (standard library, `golang.org/x/sys`, the existing charm libraries only).
- `just ci` must pass at the end of every task: `gofmt` no-op, `go mod tidy` no-op, `go vet ./...`, `go test -race ./...` (plain `go test` on Windows).
- The Windows build must keep compiling: run `GOOS=windows go vet ./...` before each commit.
- Plain prose in comments, docs and commit messages: no em dashes or en dashes, no emoji.
- Commit messages carry no `Co-Authored-By` or other AI attribution lines.
- Never run the real `incoda` on PATH or touch the real state directory. Tests build their own binary and set `INCODA_DIR` to a temp dir (the existing `laneEnv` helper does this).
- Exit codes unchanged: 120 usage, 121 timeout, 122 state, 123 spawn, 124 killed, 125 kill pending, 130 interrupt.
- Work on branch `feat/system-pools`.

---

### Task 1: Child process group from the start environment (the shipped bug)

Since v0.3.0 `run` calls `os.Setenv("INCODA_HELD", ...)` (internal/cli/run.go:283) before `child.newSupervisor` reads `os.Getenv("INCODA_HELD")` (internal/child/child_unix.go:30) to decide `Setpgid`, so no child has had its own process group and `incoda kill` leaves grandchildren running. This task captures the start environment, passes the child's environment and the group decision explicitly, and removes the `os.Setenv`.

**Files:**
- Create: `internal/cli/env.go`
- Create: `internal/cli/env_test.go`
- Create: `internal/testprog/tree/main.go`
- Create: `process_group_test.go`
- Modify: `internal/child/child.go` (the `Run` signature and env)
- Modify: `internal/child/child_unix.go` (`newSupervisor`)
- Modify: `internal/child/child_windows.go` (`newSupervisor` signature only)
- Modify: `internal/cli/run.go` (both `child.Run` calls, remove `os.Setenv`, `heldKeys`)

**Interfaces:**
- Produces: `child.Options{Env []string; OwnGroup bool}`; `child.Run(argv []string, stdin *os.File, stdout, stderr *os.File, abort <-chan struct{}, opt Options) (Result, error)`; in package `cli`: `var startEnv []string`, `func startGetenv(key string) string`, `func childEnv(base []string, held string) []string`.

- [ ] **Step 1: Write the tree test program**

`internal/testprog/tree/main.go`:

```go
//go:build !windows

// tree records its pid, parent pid and process group, starts a grandchild
// copy of itself that only sleeps, records the grandchild's pid, and waits.
// Tests use it to check that a kill through incoda reaches the whole tree.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "sleep" {
		time.Sleep(60 * time.Second)
		return
	}
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: tree OUTFILE")
		os.Exit(2)
	}
	gc := exec.Command(os.Args[0], "sleep")
	if err := gc.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	pgid, _ := syscall.Getpgid(os.Getpid())
	body := fmt.Sprintf("pid %d\nppid %d\npgid %d\ngrandchild %d\n", os.Getpid(), os.Getppid(), pgid, gc.Process.Pid)
	tmp := os.Args[1] + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Rename(tmp, os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = gc.Wait()
}
```

- [ ] **Step 2: Write the failing integration tests**

`process_group_test.go`:

```go
//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var (
	treeOnce sync.Once
	treeBin  string
	treeErr  error
)

// treeBinary builds internal/testprog/tree next to the incoda test binary.
func treeBinary(t *testing.T) string {
	t.Helper()
	incoda, _ := binaries(t)
	treeOnce.Do(func() {
		treeBin = filepath.Join(filepath.Dir(incoda), "tree")
		cmd := exec.Command("go", "build", "-o", treeBin, "./internal/testprog/tree")
		cmd.Env = append(os.Environ(), "GOTOOLCHAIN=auto")
		if out, err := cmd.CombinedOutput(); err != nil {
			treeErr = fmt.Errorf("build tree: %v\n%s", err, out)
		}
	})
	if treeErr != nil {
		t.Fatal(treeErr)
	}
	return treeBin
}

type treeInfo struct{ pid, ppid, pgid, grandchild int }

// readTree waits for the tree program's report file and parses it.
func readTree(t *testing.T, path string) treeInfo {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		b, err := os.ReadFile(path)
		if err == nil {
			var ti treeInfo
			for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
				k, v, _ := strings.Cut(line, " ")
				n, _ := strconv.Atoi(v)
				switch k {
				case "pid":
					ti.pid = n
				case "ppid":
					ti.ppid = n
				case "pgid":
					ti.pgid = n
				case "grandchild":
					ti.grandchild = n
				}
			}
			return ti
		}
		if time.Now().After(deadline) {
			t.Fatalf("tree never wrote %s", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// gone reports whether pid no longer exists, waiting up to d.
func gone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestTopLevelChildOwnsItsGroup: a top-level run puts its child in a new
// process group, so the child's pgid is its own pid.
func TestTopLevelChildOwnsItsGroup(t *testing.T) {
	incoda, _ := binaries(t)
	tree := treeBinary(t)
	state := t.TempDir()
	out := filepath.Join(t.TempDir(), "tree.txt")

	holder := exec.Command(incoda, "run", "--queue", "pg", "--quiet", "--poll", "50ms", "--", tree, out)
	holder.Env = laneEnv(state)
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Process.Kill(); _ = holder.Wait() }()

	ti := readTree(t, out)
	defer func() { _ = syscall.Kill(-ti.pgid, syscall.SIGKILL) }()
	if ti.pgid != ti.pid {
		t.Fatalf("a top-level run's child must lead its own process group: pid %d pgid %d", ti.pid, ti.pgid)
	}
}

// TestKillReachesGrandchild is the regression test for the shipped bug:
// `incoda kill` on a holder whose child spawned a grandchild ends both.
func TestKillReachesGrandchild(t *testing.T) {
	incoda, _ := binaries(t)
	tree := treeBinary(t)
	state := t.TempDir()
	out := filepath.Join(t.TempDir(), "tree.txt")

	holder := exec.Command(incoda, "run", "--queue", "pgk", "--quiet", "--poll", "50ms", "--", tree, out)
	holder.Env = laneEnv(state)
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	ti := readTree(t, out)
	defer func() { _ = syscall.Kill(ti.grandchild, syscall.SIGKILL) }()

	msg, code := runIncoda(t, incoda, state, "kill", "--queue", "pgk", "--pid", strconv.Itoa(holder.Process.Pid), "--reason", "test")
	if code != 0 {
		t.Fatalf("kill: exit %d\n%s", code, msg)
	}
	if got := exitCodeOf(holder.Wait()); got != 124 {
		t.Fatalf("killed holder exits 124, got %d", got)
	}
	if !gone(ti.pid, 5*time.Second) {
		t.Fatalf("child pid %d survived the kill", ti.pid)
	}
	if !gone(ti.grandchild, 5*time.Second) {
		t.Fatalf("grandchild pid %d survived the kill: the child was not in its own process group", ti.grandchild)
	}
}
```

- [ ] **Step 3: Write the failing unit tests for the env helpers**

`internal/cli/env_test.go`:

```go
package cli

import (
	"io"
	"os"
	"runtime"
	"testing"
)

func TestChildEnvReplacesHeld(t *testing.T) {
	base := []string{"A=1", "INCODA_HELD=old", "B=2"}
	got := childEnv(base, "k=00000000000000000001-7.ticket")
	want := []string{"A=1", "B=2", "INCODA_HELD=k=00000000000000000001-7.ticket"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if got := childEnv(base, ""); len(got) != 2 {
		t.Fatalf("an empty held value removes the variable, got %v", got)
	}
}

func TestStartGetenvReadsFirstMatch(t *testing.T) {
	saved := startEnv
	defer func() { startEnv = saved }()
	startEnv = []string{"X=first", "X=second", "Y="}
	if got := startGetenv("X"); got != "first" {
		t.Fatalf("got %q", got)
	}
	if got := startGetenv("Y"); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := startGetenv("Z"); got != "" {
		t.Fatalf("got %q", got)
	}
}

// TestRunLeavesOwnEnvironmentAlone: incoda must never set INCODA_HELD on its
// own process; only the child gets it.
func TestRunLeavesOwnEnvironmentAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /usr/bin/true")
	}
	t.Setenv("INCODA_DIR", t.TempDir())
	os.Unsetenv("INCODA_HELD")
	if code := Main([]string{"run", "--queue", "envq", "--quiet", "--", "true"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("run exited %d", code)
	}
	if v, ok := os.LookupEnv("INCODA_HELD"); ok {
		t.Fatalf("incoda set INCODA_HELD=%q on itself", v)
	}
}
```

- [ ] **Step 4: Run the tests to verify they fail**

Run: `go test ./internal/cli/ -run 'TestChildEnv|TestStartGetenv|TestRunLeaves' && go test . -run 'TestTopLevelChildOwnsItsGroup|TestKillReachesGrandchild' -v`
Expected: the cli package fails to compile (`childEnv`, `startEnv`, `startGetenv` undefined). After adding a stub `env.go`, `TestTopLevelChildOwnsItsGroup` fails with "must lead its own process group" and `TestKillReachesGrandchild` fails with "grandchild ... survived the kill".

- [ ] **Step 5: Add the env helpers**

`internal/cli/env.go`:

```go
package cli

import (
	"os"
	"runtime"
	"strings"
)

// startEnv is the environment incoda was started with, captured during
// package initialisation, before any code can change the process
// environment. Decisions about inherited lanes read this copy and the
// child's environment is built from it, so incoda never calls os.Setenv on
// itself. Setting INCODA_HELD on the process once made the process-group
// decision see a value that only the child was meant to have, and no child
// got its own group from v0.3.0 on.
var startEnv = os.Environ()

// startGetenv is os.Getenv against startEnv: the first entry for key wins,
// as it does for the real environment.
func startGetenv(key string) string {
	for _, kv := range startEnv {
		k, v, ok := strings.Cut(kv, "=")
		if ok && envKeyEqual(k, key) {
			return v
		}
	}
	return ""
}

// childEnv returns base without any INCODA_HELD entry, plus
// INCODA_HELD=held when held is not empty.
func childEnv(base []string, held string) []string {
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if envKeyEqual(k, "INCODA_HELD") {
			continue
		}
		out = append(out, kv)
	}
	if held != "" {
		out = append(out, "INCODA_HELD="+held)
	}
	return out
}

// envKeyEqual compares variable names the way the platform does: Windows
// names are case-insensitive.
func envKeyEqual(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
```

- [ ] **Step 6: Change `child.Run` to take explicit options**

In `internal/child/child.go`, add above `Run`:

```go
// Options says how to start the child. The caller decides both: incoda
// builds the child's environment from the one it was started with, and
// decides the process group from the lanes it verified, never from its own
// mutable environment.
type Options struct {
	// Env is the child's complete environment. Nil means os.Environ().
	Env []string
	// OwnGroup puts the child in a new process group on Unix, so a kill
	// through the lane can signal the whole tree. Ignored on Windows, where
	// the Job Object contains the tree.
	OwnGroup bool
}
```

Change the signature and the two lines that set the env and create the supervisor:

```go
func Run(argv []string, stdin *os.File, stdout, stderr *os.File, abort <-chan struct{}, opt Options) (Result, error) {
	if len(argv) == 0 {
		return Result{}, errors.New("no command given")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = opt.Env
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}

	sup, err := newSupervisor(cmd, opt.OwnGroup)
```

The rest of `Run` is unchanged.

- [ ] **Step 7: Make the supervisors take the decision**

In `internal/child/child_unix.go` replace the `newSupervisor` doc comment and body:

```go
// newSupervisor puts the child in its own process group when ownGroup is
// set, so incoda can signal the entire tree with one kill(-pgid). The trade
// is that a terminal Ctrl+C no longer reaches the child on its own, which
// is why forward relays it explicitly.
//
// The caller passes ownGroup false for a nested incoda whose live outer
// incoda will tree-kill the group this process sits in. If such a run
// opened a group of its own, a kill of the outer run would end the nested
// incoda while its job kept running with the lane free. Staying in the
// outer group costs the nested run its own group-wide kill: it can only end
// its direct child, a documented limit.
func newSupervisor(cmd *exec.Cmd, ownGroup bool) (*supervisor, error) {
	if ownGroup {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	return &supervisor{}, nil
}
```

Remove the now unused `"os"` import from `child_unix.go` only if `go vet` reports it unused (it is still used by `forward`'s `os.Signal`, so it stays).

In `internal/child/child_windows.go` change only the signature line of `newSupervisor` to:

```go
func newSupervisor(cmd *exec.Cmd, _ bool) (*supervisor, error) {
```

- [ ] **Step 8: Use the start environment in `run`**

In `internal/cli/run.go`:

1. Replace `heldKeys` with a version that reads the start environment:

```go
// heldKeys parses INCODA_HELD from the environment incoda was started with:
// the comma-separated keys an ancestor incoda holds on this process's behalf.
func heldKeys() map[string]bool {
	held := map[string]bool{}
	for _, k := range strings.Split(startGetenv("INCODA_HELD"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			held[k] = true
		}
	}
	return held
}
```

2. In the branch where every key is the parent's (`if len(toTake) == 0 {`), replace the `child.Run` call with:

```go
		res, runErr := child.Run(argv, os.Stdin, os.Stdout, os.Stderr, nil, child.Options{
			Env:      childEnv(startEnv, joinHeld(held, nil)),
			OwnGroup: len(held) == 0,
		})
```

3. Delete these lines after `stop()`:

```go
	// The child inherits the environment, so this is how the held keys
	// reach a nested incoda. Set on the process rather than on the child's
	// env slice because child.Run copies os.Environ() itself.
	_ = os.Setenv("INCODA_HELD", joinHeld(held, keys))
```

4. Replace the main `child.Run` call with:

```go
	// The held keys reach a nested incoda through the child's environment
	// only. Setting them on this process would make every later decision
	// that reads the environment see the child's value.
	res, runErr := child.Run(argv, os.Stdin, os.Stdout, os.Stderr, abort, child.Options{
		Env:      childEnv(startEnv, joinHeld(held, keys)),
		OwnGroup: len(held) == 0,
	})
```

`len(held) == 0` is the interim rule: Task 6 replaces it with "no live verified entry".

- [ ] **Step 9: Run the tests to verify they pass**

Run: `go test ./internal/cli/ ./internal/child/ && go test . -run 'TestTopLevelChildOwnsItsGroup|TestKillReachesGrandchild|TestReentrant' -v`
Expected: PASS.

- [ ] **Step 10: Run the gates and commit**

Run: `GOOS=windows go vet ./... && just ci`
Expected: PASS.

```bash
git add internal/cli/env.go internal/cli/env_test.go internal/testprog/tree/main.go process_group_test.go internal/child/ internal/cli/run.go
git commit -m "fix: give every top-level run's child its own process group

Since v0.3.0 run set INCODA_HELD on its own environment before the
supervisor read it to decide Setpgid, so no child got its own group and
incoda kill left grandchildren running while the lane read free. incoda
now captures its start environment once, passes the child's environment
explicitly and never calls os.Setenv on itself."
```

---

### Task 2: One terminal-safe escaper

**Files:**
- Create: `internal/textsafe/textsafe.go`
- Create: `internal/textsafe/textsafe_test.go`
- Modify: `internal/lane/ticket.go` (`attribution`, `quoteIfNeeded`)
- Modify: `internal/lane/queue.go:416`, `:480` (enqueue and acquire log lines)
- Modify: `internal/cli/run.go` (reenter log line, busy and holder lines)
- Modify: `internal/cli/config.go` (reject control characters on write, escape on echo)
- Test: `config_test.go` (root), `internal/lane/lane_test.go`

**Interfaces:**
- Produces: `textsafe.Escape(s string) string`; `textsafe.Unsafe(s string) bool`; `textsafe.CheckWrite(field, s string) error` (error text `bad-text: <field> contains control characters` or `bad-text: <field> is longer than 200 characters`); `textsafe.LogValue(s string) string`.

- [ ] **Step 1: Write the failing unit tests**

`internal/textsafe/textsafe_test.go`:

```go
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
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/textsafe/`
Expected: FAIL, package has no Go files.

- [ ] **Step 3: Implement the escaper**

`internal/textsafe/textsafe.go`:

```go
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
```

- [ ] **Step 4: Run to verify the unit tests pass**

Run: `go test ./internal/textsafe/`
Expected: PASS.

- [ ] **Step 5: Write the failing integration tests**

Add to `config_test.go` (root package):

```go
// TestConfigRefusesControlCharacters: a description is shown in status and
// in refusals, so it must not carry anything that repaints a terminal.
func TestConfigRefusesControlCharacters(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	out, code := runIncoda(t, incoda, state, "config", "badtext", "--description", "red\x1b[31m")
	if code != 120 || !strings.Contains(out, "incoda: bad-text: description contains control characters") {
		t.Fatalf("want exit 120 and a bad-text refusal, got %d:\n%s", code, out)
	}
	out, code = runIncoda(t, incoda, state, "config", "badtext", "--close", "two\nlines")
	if code != 120 || !strings.Contains(out, "bad-text: closed contains control characters") {
		t.Fatalf("want exit 120 for --close, got %d:\n%s", code, out)
	}
}

// TestLogLineStaysOneLine: a command word with a newline must not split a
// lane.log event into two lines.
func TestLogLineStaysOneLine(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	marker := filepath.Join(t.TempDir(), "m.txt")
	if out, code := runIncoda(t, incoda, state, "run", "--queue", "oneline", "--quiet", "--", stamp, marker, "a\nb", "1"); code != 0 {
		t.Fatalf("run: exit %d\n%s", code, out)
	}
	b, err := os.ReadFile(filepath.Join(state, "queues", "oneline", "lane.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if !strings.Contains(line, "queue=oneline event=") {
			t.Fatalf("a log event was split across lines:\n%s", b)
		}
	}
}
```

Make sure `config_test.go` imports `os` and `path/filepath` (add them to its import block if missing).

- [ ] **Step 6: Run to verify failure**

Run: `go test . -run 'TestConfigRefusesControlCharacters|TestLogLineStaysOneLine' -v`
Expected: FAIL (config accepts the text; the log line splits).

- [ ] **Step 7: Use the escaper in log lines**

In `internal/lane/ticket.go`, replace `attribution` and `quoteIfNeeded`:

```go
// attribution is the k=v block every lifecycle line (enqueue/acquire/release)
// carries: WHERE the job ran (dir, the launch cwd), and when set WHY (reason)
// and WHOSE (owner). Values go through textsafe.LogValue so a space quotes
// them and a control character can never split the event across lines.
// Readers that do not know the fields ignore them, and old lines lack them.
func (t Ticket) attribution() string {
	s := ""
	if t.Dir != "" {
		s += " dir=" + textsafe.LogValue(t.Dir)
	}
	if t.Reason != "" {
		s += " reason=" + strconv.Quote(textsafe.Escape(t.Reason))
	}
	if t.Owner != "" {
		s += " owner=" + strconv.Quote(textsafe.Escape(t.Owner))
	}
	return s
}
```

Delete `quoteIfNeeded`. Add `"github.com/deblasis/incoda/internal/textsafe"` to the imports of `ticket.go`. Keep `strconv` (still used).

In `internal/lane/queue.go`, on the enqueue line (around :416) and the acquire line (around :480), replace `en.ticket.CommandString()` / `e.ticket.CommandString()` in the `cmd=%s` argument with `textsafe.LogValue(en.ticket.CommandString())` / `textsafe.LogValue(e.ticket.CommandString())`, and add the textsafe import.

In `internal/cli/run.go`, the reenter log line becomes:

```go
			q.Logf("queue=%s event=reenter pid=%d cmd=%s", key, os.Getpid(), textsafe.LogValue(lane.Ticket{Command: argv}.CommandString()))
```

and the holder line inside `OnWait` becomes:

```go
					fmt.Fprintf(stderr, "%s   %s\n", p.Dim("incoda:"),
						p.Dim(fmt.Sprintf("holder pid %d in %s: %s", e.Ticket.PID, textsafe.Escape(e.Ticket.Dir), textsafe.Escape(e.Ticket.CommandString()))))
```

Add the textsafe import to `run.go`.

- [ ] **Step 8: Check and escape config text**

In `internal/cli/config.go`, after the `--close`/`--open` contradiction check and before resolving the key, add:

```go
	for _, c := range []struct{ name, flag string }{{"description", "description"}, {"closed", "close"}} {
		set := false
		fs.Visit(func(f *flag.Flag) { set = set || f.Name == c.flag })
		if !set {
			continue
		}
		v := *desc
		if c.flag == "close" {
			v = *closeMsg
		}
		if err := textsafe.CheckWrite(c.name, v); err != nil {
			return usagef("%v", err)
		}
	}
```

In the echo section of `cmdConfig`, print `textsafe.Escape(cfg.Description)` and `textsafe.Escape(cfg.Closed)` instead of the raw values, and in the `event=config` log line replace `closed=%q` with `closed=%s` and the argument with `textsafe.LogValue(cfg.Closed)`. Add the textsafe import.

- [ ] **Step 9: Run to verify the tests pass**

Run: `go test ./... -run 'TestConfig|TestLogLine|TestEscape|TestUnsafe|TestCheckWrite|TestLogValue|TestReleaseRecordsJobStats' && just ci`
Expected: PASS. `TestReleaseRecordsJobStats` still finds `reason="nightly matrix"` and `owner="test-session"`.

- [ ] **Step 10: Commit**

```bash
git add internal/textsafe/ internal/lane/ticket.go internal/lane/queue.go internal/cli/run.go internal/cli/config.go config_test.go
git commit -m "feat: one escaper for every string incoda prints or logs

Control characters, bidi overrides and invalid UTF-8 from argv, stored
config or another process are escaped on display, lane.log values are
quoted so one event stays one line, and config refuses descriptions and
closed texts that carry control characters."
```

---

### Task 3: Config schema 2, locked read-modify-write, unknown fields kept

**Files:**
- Modify: `internal/lane/config.go`
- Modify: `internal/lane/config_test.go` (struct comparisons, new tests)
- Modify: `internal/cli/config.go` (use `UpdateConfig`)
- Modify: `internal/cli/run.go` (newer-schema message)

**Interfaces:**
- Produces: `lane.ConfigSchema = 2`; `lane.Config` fields `Schema int`, `Pools []string`, `QuietMachine bool` plus preserved unknown fields; `(*lane.Queue).UpdateConfig(fn func(*Config) error) (Config, error)`; `lane.NewerSchemaError{Path string; Schema int}` with `Error()` = `<path> was written by a newer incoda (schema N); upgrade this one`; `lane.EqualConfig(a, b Config) bool`.

- [ ] **Step 1: Write the failing tests**

In `internal/lane/config_test.go`, change the two struct comparisons in `TestConfigRoundTripAndDefaults` to use the new helper (the struct holds a slice and is no longer comparable):

```go
	if !EqualConfig(cfg, Config{}) {
		t.Fatalf("missing config should be zero, got %+v", cfg)
	}
```

```go
	if !EqualConfig(got, want) {
		t.Fatalf("config did not round-trip: got %+v want %+v", got, want)
	}
```

Then add:

```go
func TestConfigKeepsUnknownFieldsAndStampsSchema(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	path := filepath.Join(q.Dir, configName)
	if err := os.WriteFile(path, []byte(`{"slots":2,"from_the_future":{"x":1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpdateConfig(func(c *Config) error { c.Description = "d"; return nil }); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["from_the_future"]) != `{"x":1}` {
		t.Fatalf("unknown field lost: %s", b)
	}
	if string(raw["schema"]) != "2" || string(raw["slots"]) != "2" || string(raw["description"]) != `"d"` {
		t.Fatalf("unexpected file: %s", b)
	}
}

func TestConfigNewerSchemaFailsClosed(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if err := os.WriteFile(filepath.Join(q.Dir, configName), []byte(`{"schema":3}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = q.LoadConfig()
	var ns *NewerSchemaError
	if !errors.As(err, &ns) || ns.Schema != 3 {
		t.Fatalf("want NewerSchemaError, got %v", err)
	}
	if _, err := q.UpdateConfig(func(*Config) error { return nil }); !errors.As(err, &ns) {
		t.Fatalf("a write must refuse too, got %v", err)
	}
}

func TestConfigPoolsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if _, err := q.UpdateConfig(func(c *Config) error { c.Pools = []string{"tests", "builds"}; c.QuietMachine = true; return nil }); err != nil {
		t.Fatal(err)
	}
	got, err := q.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Pools) != 2 || got.Pools[0] != "tests" || !got.QuietMachine || got.Schema != ConfigSchema {
		t.Fatalf("got %+v", got)
	}
}

// TestConfigUpdatesDoNotLoseWrites: two processes' worth of concurrent
// updates on different fields must both land, which an unlocked load then
// store cannot guarantee.
func TestConfigUpdatesDoNotLoseWrites(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q, err := Open(dir, "unit")
			if err != nil {
				t.Error(err)
				return
			}
			defer q.Close()
			_, err = q.UpdateConfig(func(c *Config) error {
				c.Pools = append(c.Pools, fmt.Sprintf("p%02d", i))
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	q, _ := Open(dir, "unit")
	defer q.Close()
	got, err := q.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Pools) != 20 {
		t.Fatalf("lost updates: %d of 20 landed: %v", len(got.Pools), got.Pools)
	}
}
```

Add `"fmt"` and `"sync"` to the test file's imports.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/lane/ -run TestConfig`
Expected: compile errors (`EqualConfig`, `UpdateConfig`, `NewerSchemaError`, `ConfigSchema`, `Pools` undefined).

- [ ] **Step 3: Implement schema 2 in `internal/lane/config.go`**

Add to the `Config` struct, after `Closed`:

```go
	// Pools is a project lane's link: the machine-wide pools its runs take.
	// Absent means unlinked. Pools never carry it.
	Pools []string `json:"pools,omitempty"`
	// QuietMachine makes every run on this project lane take quiet-machine.
	QuietMachine bool `json:"quiet_machine,omitempty"`
	// Schema is the file format version; a file without it is schema 1 and
	// is upgraded on its next write.
	Schema int `json:"schema,omitempty"`

	// extra holds fields this binary does not know, so a rewrite by an older
	// binary never drops what a newer one wrote.
	extra map[string]json.RawMessage
```

Add below the struct:

```go
// ConfigSchema is the config.json format this binary writes.
const ConfigSchema = 2

// NewerSchemaError is returned when config.json was written by a newer
// incoda. Reading on would mean ignoring rules this binary does not know,
// so every run through the lane and every write to it fails closed.
type NewerSchemaError struct {
	Path   string
	Schema int
}

func (e *NewerSchemaError) Error() string {
	return fmt.Sprintf("%s was written by a newer incoda (schema %d); upgrade this one", e.Path, e.Schema)
}

// configKnown lists the JSON names Config owns; everything else is extra.
var configKnown = map[string]bool{
	"slots": true, "description": true, "require_reason": true, "closed": true,
	"pools": true, "quiet_machine": true, "schema": true,
}

type configJSON struct {
	Slots         int      `json:"slots,omitempty"`
	Description   string   `json:"description,omitempty"`
	RequireReason bool     `json:"require_reason,omitempty"`
	Closed        string   `json:"closed,omitempty"`
	Pools         []string `json:"pools,omitempty"`
	QuietMachine  bool     `json:"quiet_machine,omitempty"`
	Schema        int      `json:"schema,omitempty"`
}

// UnmarshalJSON reads the known fields and keeps the rest in extra.
func (c *Config) UnmarshalJSON(b []byte) error {
	var known configJSON
	if err := json.Unmarshal(b, &known); err != nil {
		return err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return err
	}
	*c = Config{
		Slots: known.Slots, Description: known.Description, RequireReason: known.RequireReason,
		Closed: known.Closed, Pools: known.Pools, QuietMachine: known.QuietMachine, Schema: known.Schema,
	}
	for k, v := range all {
		if !configKnown[k] {
			if c.extra == nil {
				c.extra = map[string]json.RawMessage{}
			}
			c.extra[k] = v
		}
	}
	return nil
}

// MarshalJSON writes the known fields plus every preserved unknown field.
func (c Config) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(configJSON{
		Slots: c.Slots, Description: c.Description, RequireReason: c.RequireReason,
		Closed: c.Closed, Pools: c.Pools, QuietMachine: c.QuietMachine, Schema: c.Schema,
	})
	if err != nil || len(c.extra) == 0 {
		return b, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range c.extra {
		if _, ok := m[k]; !ok {
			m[k] = v
		}
	}
	return json.Marshal(m)
}

// EqualConfig compares the fields this binary knows.
func EqualConfig(a, b Config) bool {
	if a.Slots != b.Slots || a.Description != b.Description || a.RequireReason != b.RequireReason ||
		a.Closed != b.Closed || a.QuietMachine != b.QuietMachine || a.Schema != b.Schema || len(a.Pools) != len(b.Pools) {
		return false
	}
	for i := range a.Pools {
		if a.Pools[i] != b.Pools[i] {
			return false
		}
	}
	return true
}
```

In `LoadConfig`, after a successful `json.Unmarshal`, add:

```go
	if c.Schema > ConfigSchema {
		return Config{}, &NewerSchemaError{Path: filepath.Join(q.Dir, configName), Schema: c.Schema}
	}
```

Replace `SaveConfig` with `UpdateConfig` plus a `SaveConfig` built on it:

```go
// UpdateConfig loads the config, applies fn and stores the result, all
// inside one hold of the registry lock, so two writers changing different
// fields cannot lose each other's change. The stored file is stamped with
// ConfigSchema and keeps fields this binary does not know. A config written
// by a newer incoda is refused, never rewritten.
func (q *Queue) UpdateConfig(fn func(*Config) error) (Config, error) {
	var out Config
	err := q.withRegistry(func() error {
		c, err := q.LoadConfig()
		if err != nil {
			return err
		}
		if err := fn(&c); err != nil {
			return err
		}
		c.Schema = ConfigSchema
		b, err := json.MarshalIndent(c, "", "  ")
		if err != nil {
			return err
		}
		path := filepath.Join(q.Dir, configName)
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
			return err
		}
		if err := renameRetry(tmp, path); err != nil {
			return err
		}
		out = c
		return nil
	})
	return out, err
}

// SaveConfig replaces the known fields with c and keeps unknown ones.
func (q *Queue) SaveConfig(c Config) error {
	_, err := q.UpdateConfig(func(cur *Config) error {
		extra := cur.extra
		*cur = c
		cur.extra = extra
		return nil
	})
	return err
}

// renameRetry renames, retrying on Windows where a reader holding the file
// open makes the rename fail for a moment.
func renameRetry(from, to string) error {
	var err error
	for i := 0; i < 10; i++ {
		if err = os.Rename(from, to); err == nil || runtime.GOOS != "windows" {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
	return err
}
```

Add `"runtime"` and `"time"` to the imports of `config.go`.

- [ ] **Step 4: Run to verify the lane tests pass**

Run: `go test -race ./internal/lane/`
Expected: PASS. If `internal/tui/model_test.go` or another package compares `lane.Config` with `==`, `go vet ./...` reports it; replace each with `lane.EqualConfig`.

- [ ] **Step 5: Use `UpdateConfig` in the config command**

In `internal/cli/config.go`, replace the block from `cfg, err := q.LoadConfig()` through the end of the `if changed { ... }` block with:

```go
	apply := func(cfg *lane.Config) bool {
		changed := false
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "slots":
				cfg.Slots = *slots
			case "description":
				cfg.Description = *desc
			case "require-reason":
				cfg.RequireReason = *requireReason
			case "close":
				cfg.Closed = *closeMsg
			case "open":
				cfg.Closed = ""
			default:
				return
			}
			changed = true
		})
		return changed
	}
	var cfg lane.Config
	if apply(&lane.Config{}) {
		cfg, err = q.UpdateConfig(func(c *lane.Config) error { apply(c); return nil })
		if err == nil {
			q.Logf("queue=%s event=config pid=%d slots=%d require_reason=%v closed=%s", key, os.Getpid(), cfg.Slots, cfg.RequireReason, textsafe.LogValue(cfg.Closed))
		}
	} else {
		cfg, err = q.LoadConfig()
	}
	var ns *lane.NewerSchemaError
	if errors.As(err, &ns) {
		return exitWith(ExitState, "machine-state: %v", err)
	}
	if err != nil {
		return exitWith(ExitState, "queue %q: %v", key, err)
	}
```

Add `"errors"` to the imports if missing.

- [ ] **Step 6: Report a newer schema from `run` with the `machine-state:` prefix**

In `internal/cli/run.go`, replace:

```go
		cfg, err := q.LoadConfig()
		if err != nil {
			return exitWith(ExitState, "queue %q: %v", key, err)
		}
```

with:

```go
		cfg, err := q.LoadConfig()
		var ns *lane.NewerSchemaError
		if errors.As(err, &ns) {
			return exitWith(ExitState, "machine-state: %v", err)
		}
		if err != nil {
			return exitWith(ExitState, "queue %q: %v", key, err)
		}
```

- [ ] **Step 7: Add an integration test for the newer schema**

Append to `config_test.go`:

```go
// TestNewerConfigSchemaRefusesRuns: a config written by a newer incoda may
// carry rules this binary does not know, so it fails closed.
func TestNewerConfigSchemaRefusesRuns(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "newer", "--slots", "1"); code != 0 {
		t.Fatalf("config: %d\n%s", code, out)
	}
	path := filepath.Join(state, "queues", "newer", "config.json")
	if err := os.WriteFile(path, []byte(`{"schema":9,"slots":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runIncoda(t, incoda, state, "run", "--queue", "newer", "--", stamp, filepath.Join(t.TempDir(), "x"), "x", "1")
	if code != 122 || !strings.Contains(out, "incoda: machine-state:") || !strings.Contains(out, "newer incoda") {
		t.Fatalf("want exit 122 machine-state, got %d:\n%s", code, out)
	}
}
```

- [ ] **Step 8: Run all tests and gates**

Run: `GOOS=windows go vet ./... && just ci`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/lane/config.go internal/lane/config_test.go internal/cli/config.go internal/cli/run.go config_test.go
git commit -m "feat: config schema 2 with locked read-modify-write

config.json gains schema, pools and quiet_machine. Every write loads,
modifies and stores inside one hold of the registry lock, so concurrent
writers cannot lose each other's change, and fields this binary does not
know survive a rewrite. A config from a newer incoda fails closed with
machine-state."
```

---

### Task 4: `KEY=TICKET` held entries

**Files:**
- Create: `internal/held/held.go`
- Create: `internal/held/held_test.go`
- Modify: `internal/lane/ticket.go` (exported name helpers)
- Modify: `internal/lane/queue.go` (`Enrollment.Name`, path helpers)

**Interfaces:**
- Consumes: `lane.ValidateKey`.
- Produces: `lane.ValidTicketName(name string) bool`; `lane.TicketNamePID(name string) (int, bool)`; `lane.RegistryLockPath(queueDir string) string`; `lane.TicketFilePath(queueDir, name string) string`; `(*lane.Enrollment).Name() string`; package `held`: `type Entry struct{ Key, Ticket string }`, `func (Entry) String() string`, `type Bad struct{ Raw string }`, `func Parse(raw string) ([]Entry, []Bad)`, `func Format(entries []Entry) string`, `func Merge(a, b []Entry) []Entry`.

- [ ] **Step 1: Write the failing tests**

`internal/held/held_test.go`:

```go
package held

import "testing"

const t1 = "00000000000000000001-4711.ticket"
const t2 = "00000000000000000002-4711.ticket"

func TestParse(t *testing.T) {
	got, bad := Parse("cap-gate=" + t1 + ", tests=" + t2 + ",,")
	if len(got) != 2 || got[0] != (Entry{"cap-gate", t1}) || got[1] != (Entry{"tests", t2}) || len(bad) != 0 {
		t.Fatalf("got %v bad %v", got, bad)
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	for _, raw := range []string{
		"cap-gate",                    // the pre-0.7 bare key
		"cap-gate=",                   // no ticket
		"=" + t1,                      // no key
		"../x=" + t1,                  // key fails ValidateKey
		"k=../00000000000000000001-1.ticket",
		"k=00000000000000000001-1.txt",
		"k=-12-3.ticket",              // signed number
		"k=00000000000000000001-x.ticket",
	} {
		got, bad := Parse(raw)
		if len(got) != 0 || len(bad) != 1 || bad[0].Raw != raw {
			t.Fatalf("Parse(%q) = %v, bad %v; want one malformed entry", raw, got, bad)
		}
	}
}

func TestParseKeepsFirstPerKey(t *testing.T) {
	got, _ := Parse("k=" + t1 + ",k=" + t2)
	if len(got) != 1 || got[0].Ticket != t1 {
		t.Fatalf("got %v", got)
	}
}

func TestFormatAndMerge(t *testing.T) {
	a := []Entry{{"tests", t2}, {"cap-gate", t1}}
	if got := Format(a); got != "cap-gate="+t1+",tests="+t2 {
		t.Fatalf("got %q", got)
	}
	m := Merge([]Entry{{"k", t1}, {"x", t1}}, []Entry{{"k", t2}})
	if Format(m) != "k="+t2+",x="+t1 {
		t.Fatalf("Merge must prefer the second list per key, got %v", m)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/held/`
Expected: FAIL, no Go files.

- [ ] **Step 3: Add the lane helpers**

In `internal/lane/ticket.go`, below `parseTicketName`, add:

```go
// ValidTicketName reports whether name is exactly a ticket file name as
// ticketName writes it: digits, a dash, digits, ".ticket", nothing else. It
// is checked before a name taken from the environment is joined into a path.
func ValidTicketName(name string) bool {
	if _, ok := parseTicketName(name); !ok {
		return false
	}
	base := strings.TrimSuffix(name, ticketExt)
	for _, r := range base {
		if (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return strings.Count(base, "-") == 1
}

// TicketNamePID returns the pid embedded in a valid ticket name.
func TicketNamePID(name string) (int, bool) {
	if !ValidTicketName(name) {
		return 0, false
	}
	ord, _ := parseTicketName(name)
	return ord.pid, true
}

// TicketFilePath is the path of a ticket file inside a queue directory.
func TicketFilePath(queueDir, name string) string { return ticketPath(queueDir, name) }
```

In `internal/lane/queue.go`, add:

```go
// RegistryLockPath is the path of a queue directory's registry lock.
func RegistryLockPath(queueDir string) string { return filepath.Join(queueDir, registryLockName) }

// Name is the enrolled ticket's file name, which INCODA_HELD carries so a
// nested run can probe the exact ticket.
func (e *Enrollment) Name() string { return e.name }
```

- [ ] **Step 4: Implement the held package**

`internal/held/held.go`:

```go
// Package held reads and writes INCODA_HELD, the lanes an ancestor incoda
// holds on a process's behalf. Each entry names the exact ticket
// (KEY=TICKET), so a nested run can check that the ticket is alive and that
// its holder is really an ancestor before it trusts the entry.
package held

import (
	"sort"
	"strings"

	"github.com/deblasis/incoda/internal/lane"
)

// Entry is one held lane: its key and the holder's ticket file name.
type Entry struct {
	Key    string
	Ticket string
}

func (e Entry) String() string { return e.Key + "=" + e.Ticket }

// Bad is an entry that does not parse: a pre-0.7 bare key, a missing part,
// a key that fails ValidateKey, or a ticket that is not a ticket file name.
type Bad struct{ Raw string }

// Parse splits raw into entries. Both parts are validated before anything
// touches the filesystem. The first entry for a key wins.
func Parse(raw string) ([]Entry, []Bad) {
	var out []Entry
	var bad []Bad
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, t, ok := strings.Cut(part, "=")
		if !ok || lane.ValidateKey(k) != nil || !lane.ValidTicketName(t) {
			bad = append(bad, Bad{Raw: part})
			continue
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, Entry{Key: k, Ticket: t})
	}
	return out, bad
}

// Format renders entries sorted by key, the value a child receives.
func Format(entries []Entry) string {
	s := append([]Entry(nil), entries...)
	sort.Slice(s, func(i, j int) bool { return s[i].Key < s[j].Key })
	parts := make([]string, len(s))
	for i, e := range s {
		parts[i] = e.String()
	}
	return strings.Join(parts, ",")
}

// Merge returns a plus b, one entry per key, b winning on a shared key.
func Merge(a, b []Entry) []Entry {
	byKey := map[string]Entry{}
	for _, e := range a {
		byKey[e.Key] = e
	}
	for _, e := range b {
		byKey[e.Key] = e
	}
	out := make([]Entry, 0, len(byKey))
	for _, e := range byKey {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
```

- [ ] **Step 5: Run to verify the tests pass**

Run: `go test ./internal/held/ ./internal/lane/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/held/ internal/lane/ticket.go internal/lane/queue.go
git commit -m "feat: INCODA_HELD entries name the exact ticket (KEY=TICKET)

A held entry now carries the holder's ticket file name, validated against
the ticket-name grammar before it becomes a path, so a nested run can
probe the exact ticket instead of trusting a bare key."
```

---

### Task 5: Verify held entries (liveness and ancestry)

**Files:**
- Create: `internal/procinfo/procinfo.go`
- Create: `internal/procinfo/procinfo_darwin.go`
- Create: `internal/procinfo/procinfo_linux.go`
- Create: `internal/procinfo/procinfo_other.go`
- Create: `internal/procinfo/procinfo_test.go`
- Modify: `internal/lockfile/lockfile.go`, `lockfile_unix.go`, `lockfile_windows.go` (`OpenExisting`)
- Create: `internal/lockfile/lockfile_test.go`
- Create: `internal/held/verify.go`
- Create: `internal/held/verify_test.go`

**Interfaces:**
- Consumes: Task 4's `held.Entry`, `held.Parse`, `lane.TicketNamePID`, `lane.RegistryLockPath`, `lane.TicketFilePath`, `lane.QueueDir`.
- Produces: `lockfile.OpenExisting(path string) (*File, error)` (error satisfies `errors.Is(err, os.ErrNotExist)` when the file is missing); `procinfo.ParentPID(pid int) (int, error)`; `type procinfo.Chain struct{ PIDs []int; Err error; Skip bool }`; `procinfo.ParentChain() Chain`; `(Chain).Contains(pid int) bool`; package `held`: `type Why string` with constants `Dead`, `Malformed`, `NotAncestor`, `Unverifiable`; `type Dropped struct{ Raw, Key, Ticket string; Why Why; Live bool }`; `type Result struct{ L, P []Entry; Dropped []Dropped }`; `func Verify(stateDir, raw string, chain procinfo.Chain) Result`; `func (Result) PassKeys() map[string]bool`; `func (Result) LiveKeys() map[string]bool`.

- [ ] **Step 1: Write the failing lockfile test**

`internal/lockfile/lockfile_test.go`:

```go
package lockfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenExistingNeverCreates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent")
	if _, err := OpenExisting(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("OpenExisting created the file")
	}
	held, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if ok, _ := held.TryLock(); !ok {
		t.Fatal("lock")
	}
	probe, err := OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	if ok, err := probe.TryLock(); ok || err != nil {
		t.Fatalf("a held lock must refuse a second handle: ok=%v err=%v", ok, err)
	}
}
```

- [ ] **Step 2: Implement `OpenExisting`**

In `internal/lockfile/lockfile.go`, below `Open`:

```go
// OpenExisting opens path for locking without ever creating it. A probe of
// another process's ticket uses it, so a probe of a ticket that has just
// been released cannot recreate the file and make it look alive. A missing
// file yields an error that satisfies errors.Is(err, os.ErrNotExist).
func OpenExisting(path string) (*File, error) {
	f, err := openExistingLockable(path)
	if err != nil {
		return nil, err
	}
	return &File{f: f}, nil
}
```

In `lockfile_unix.go`:

```go
func openExistingLockable(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR, 0)
}
```

In `lockfile_windows.go`, a copy of `openLockable` with `OPEN_EXISTING`:

```go
func openExistingLockable(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(
		p,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}
```

Run: `go test ./internal/lockfile/ && GOOS=windows go vet ./internal/lockfile/`
Expected: PASS.

- [ ] **Step 3: Write the failing procinfo test**

`internal/procinfo/procinfo_test.go`:

```go
package procinfo

import (
	"os"
	"runtime"
	"testing"
)

func TestParentChainStartsAtParent(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no ancestry walk on " + runtime.GOOS)
	}
	c := ParentChain()
	if c.Err != nil {
		t.Fatal(c.Err)
	}
	if len(c.PIDs) == 0 || c.PIDs[0] != os.Getppid() {
		t.Fatalf("chain %v should start at getppid %d", c.PIDs, os.Getppid())
	}
	if !c.Contains(os.Getppid()) || c.Contains(os.Getpid()) {
		t.Fatalf("Contains is wrong for %v", c.PIDs)
	}
	if pp, err := ParentPID(os.Getpid()); err != nil || pp != os.Getppid() {
		t.Fatalf("ParentPID(self) = %d, %v", pp, err)
	}
}

func TestParentPIDOfMissingProcess(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip()
	}
	if _, err := ParentPID(1 << 30); err == nil {
		t.Fatal("a missing pid must be an error")
	}
}
```

- [ ] **Step 4: Implement procinfo**

`internal/procinfo/procinfo.go`:

```go
// Package procinfo reads the parent of a process, so a nested incoda can
// check that a held ticket belongs to one of its ancestors.
package procinfo

import (
	"errors"
	"os"
)

// maxHops bounds the walk up the parent chain.
const maxHops = 64

// ErrUnsupported is returned where the platform has no parent lookup.
var ErrUnsupported = errors.New("parent lookup not supported on this platform")

// Chain is this process's ancestors, nearest first, stopping before pid 1.
// Err is set when a lookup failed partway; PIDs then holds what was found.
// Skip is set where no walk is needed (Windows: the Job Object ends every
// descendant with the holder, so a live held ticket is always an ancestor's).
type Chain struct {
	PIDs []int
	Err  error
	Skip bool
}

// Contains reports whether pid is in the chain.
func (c Chain) Contains(pid int) bool {
	for _, p := range c.PIDs {
		if p == pid {
			return true
		}
	}
	return false
}

// ParentChain walks from getppid up to (not including) pid 1, at most
// maxHops steps.
func ParentChain() Chain {
	if skipWalk {
		return Chain{Skip: true}
	}
	var c Chain
	p := os.Getppid()
	for i := 0; i < maxHops && p > 1; i++ {
		c.PIDs = append(c.PIDs, p)
		next, err := ParentPID(p)
		if err != nil {
			c.Err = err
			return c
		}
		p = next
	}
	return c
}
```

`internal/procinfo/procinfo_darwin.go`:

```go
//go:build darwin

package procinfo

import (
	"fmt"

	"golang.org/x/sys/unix"
)

const skipWalk = false

// ParentPID reads e_ppid from sysctl kern.proc.pid.
func ParentPID(pid int) (int, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	if int(kp.Proc.P_pid) != pid {
		return 0, fmt.Errorf("no process %d", pid)
	}
	return int(kp.Eproc.Ppid), nil
}
```

`internal/procinfo/procinfo_linux.go`:

```go
//go:build linux

package procinfo

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const skipWalk = false

// ParentPID reads field 4 of /proc/<pid>/stat. The command name in field 2
// may contain spaces and parentheses, so fields are counted after the last ')'.
func ParentPID(pid int) (int, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, fmt.Errorf("unparseable /proc/%d/stat", pid)
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0, fmt.Errorf("unparseable /proc/%d/stat", pid)
	}
	return strconv.Atoi(f[1])
}
```

`internal/procinfo/procinfo_other.go`:

```go
//go:build !darwin && !linux

package procinfo

import "runtime"

const skipWalk = runtime.GOOS == "windows"

// ParentPID is not available here; held entries are then unverifiable.
func ParentPID(int) (int, error) { return 0, ErrUnsupported }
```

Run: `go test ./internal/procinfo/ && GOOS=windows go vet ./internal/procinfo/ && GOOS=freebsd go vet ./internal/procinfo/`
Expected: PASS.

Note: `const skipWalk = runtime.GOOS == "windows"` is a valid constant expression because `runtime.GOOS` is a constant.

- [ ] **Step 5: Write the failing Verify tests**

`internal/held/verify_test.go`:

```go
package held

import (
	"os"
	"testing"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/procinfo"
)

// enroll creates a live ticket owned by this test process.
func enroll(t *testing.T, state, key string) (*lane.Queue, *lane.Enrollment) {
	t.Helper()
	q, err := lane.Open(state, key)
	if err != nil {
		t.Fatal(err)
	}
	en, err := q.Enroll(lane.Ticket{Command: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	return q, en
}

func TestVerifyLiveAncestorPassesThrough(t *testing.T) {
	state := t.TempDir()
	q, en := enroll(t, state, "anc")
	defer q.Close()
	defer en.Release(0)
	// The ticket's pid is this process; pretend it is our parent.
	chain := procinfo.Chain{PIDs: []int{os.Getpid()}}
	r := Verify(state, "anc="+en.Name(), chain)
	if len(r.L) != 1 || len(r.P) != 1 || len(r.Dropped) != 0 {
		t.Fatalf("got %+v", r)
	}
	if !r.PassKeys()["anc"] || !r.LiveKeys()["anc"] {
		t.Fatalf("key sets wrong: %+v", r)
	}
}

func TestVerifyLiveNonAncestorCountsButDoesNotPass(t *testing.T) {
	state := t.TempDir()
	q, en := enroll(t, state, "na")
	defer q.Close()
	defer en.Release(0)
	r := Verify(state, "na="+en.Name(), procinfo.Chain{PIDs: []int{1}})
	if len(r.L) != 1 || len(r.P) != 0 || len(r.Dropped) != 1 || r.Dropped[0].Why != NotAncestor || !r.Dropped[0].Live {
		t.Fatalf("got %+v", r)
	}
}

func TestVerifyUnverifiableWhenChainBroken(t *testing.T) {
	state := t.TempDir()
	q, en := enroll(t, state, "uv")
	defer q.Close()
	defer en.Release(0)
	r := Verify(state, "uv="+en.Name(), procinfo.Chain{Err: procinfo.ErrUnsupported})
	if len(r.L) != 1 || len(r.P) != 0 || r.Dropped[0].Why != Unverifiable {
		t.Fatalf("got %+v", r)
	}
}

func TestVerifySkipTrustsLiveEntries(t *testing.T) {
	state := t.TempDir()
	q, en := enroll(t, state, "win")
	defer q.Close()
	defer en.Release(0)
	r := Verify(state, "win="+en.Name(), procinfo.Chain{Skip: true})
	if len(r.P) != 1 {
		t.Fatalf("got %+v", r)
	}
}

func TestVerifyDeadAndMalformedAreDropped(t *testing.T) {
	state := t.TempDir()
	q, en := enroll(t, state, "dead")
	name := en.Name()
	en.Release(0)
	q.Close()
	raw := "dead=" + name + ",ghost=00000000000000000001-1.ticket,bare"
	r := Verify(state, raw, procinfo.Chain{PIDs: []int{os.Getpid()}})
	if len(r.L) != 0 || len(r.P) != 0 || len(r.Dropped) != 3 {
		t.Fatalf("got %+v", r)
	}
	whys := map[string]Why{}
	for _, d := range r.Dropped {
		whys[d.Raw] = d.Why
	}
	if whys["dead="+name] != Dead || whys["ghost=00000000000000000001-1.ticket"] != Dead || whys["bare"] != Malformed {
		t.Fatalf("whys %v", whys)
	}
	// A probe of a missing lane must not create it.
	if lane.Exists(state, "ghost") {
		t.Fatal("Verify created the ghost lane")
	}
	// A probe of a released ticket must not recreate it.
	if _, err := os.Stat(lane.TicketFilePath(lane.QueueDir(state, "dead"), name)); !os.IsNotExist(err) {
		t.Fatal("Verify recreated a released ticket")
	}
}
```

- [ ] **Step 6: Implement Verify**

`internal/held/verify.go`:

```go
package held

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/lockfile"
	"github.com/deblasis/incoda/internal/procinfo"
)

// Why says why an entry was not passed through.
type Why string

const (
	Dead         Why = "dead"
	Malformed    Why = "malformed"
	NotAncestor  Why = "not-ancestor"
	Unverifiable Why = "unverifiable"
)

// Dropped is an entry that is not passed through. Live entries (not-ancestor,
// unverifiable) still count for ordering and the process group.
type Dropped struct {
	Raw    string
	Key    string
	Ticket string
	Why    Why
	Live   bool
}

// Result splits the inherited entries: L is every live entry, P the live
// entries whose holder is a verified ancestor. L decides ordering and the
// process group; P decides pass-through.
type Result struct {
	L       []Entry
	P       []Entry
	Dropped []Dropped
}

// PassKeys is the set of keys in P.
func (r Result) PassKeys() map[string]bool { return keys(r.P) }

// LiveKeys is the set of keys in L.
func (r Result) LiveKeys() map[string]bool { return keys(r.L) }

func keys(es []Entry) map[string]bool {
	m := map[string]bool{}
	for _, e := range es {
		m[e.Key] = true
	}
	return m
}

// Verify parses raw and checks each entry: liveness by probing its ticket
// lock, then ancestry against chain. The probe takes the lane's registry
// lock (the one Enroll and Release hold) and opens files without ever
// creating them, so it can neither race a ticket into existence nor make a
// released ticket look alive.
func Verify(stateDir, raw string, chain procinfo.Chain) Result {
	var r Result
	entries, bad := Parse(raw)
	for _, b := range bad {
		r.Dropped = append(r.Dropped, Dropped{Raw: b.Raw, Why: Malformed})
	}
	for _, e := range entries {
		live, payloadPID, perr := probe(stateDir, e)
		d := Dropped{Raw: e.String(), Key: e.Key, Ticket: e.Ticket}
		if !live {
			d.Why = Dead
			r.Dropped = append(r.Dropped, d)
			continue
		}
		r.L = append(r.L, e)
		if chain.Skip {
			r.P = append(r.P, e)
			continue
		}
		namePID, _ := lane.TicketNamePID(e.Ticket)
		switch {
		case perr != nil:
			d.Why, d.Live = Unverifiable, true
		case payloadPID != namePID:
			d.Why, d.Live = NotAncestor, true
		case chain.Contains(namePID):
			r.P = append(r.P, e)
			continue
		case chain.Err != nil:
			d.Why, d.Live = Unverifiable, true
		default:
			d.Why, d.Live = NotAncestor, true
		}
		r.Dropped = append(r.Dropped, d)
	}
	return r
}

// probe reports whether e's ticket is held by a live process, and the pid
// recorded in its payload. perr reports a payload that could not be read.
func probe(stateDir string, e Entry) (live bool, payloadPID int, perr error) {
	dir := lane.QueueDir(stateDir, e.Key)
	reg, err := lockfile.OpenExisting(lane.RegistryLockPath(dir))
	if err != nil {
		return false, 0, nil
	}
	defer reg.Close()
	if err := reg.Lock(); err != nil {
		return false, 0, nil
	}
	path := lane.TicketFilePath(dir, e.Ticket)
	tf, err := lockfile.OpenExisting(path)
	if err != nil {
		return false, 0, nil
	}
	free, err := tf.TryLock()
	tf.Close()
	if err != nil || free {
		return false, 0, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return true, 0, err
	}
	var t lane.Ticket
	if err := json.Unmarshal(b, &t); err != nil {
		return true, 0, err
	}
	if t.PID == 0 {
		return true, 0, errors.New("ticket payload has no pid")
	}
	return true, t.PID, nil
}
```

- [ ] **Step 7: Run to verify the tests pass**

Run: `go test -race ./internal/held/ ./internal/procinfo/ ./internal/lockfile/ && GOOS=windows go vet ./...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/procinfo/ internal/lockfile/ internal/held/verify.go internal/held/verify_test.go
git commit -m "feat: verify inherited held entries for liveness and ancestry

A nested run probes each KEY=TICKET entry under the lane's registry lock
without creating files, and checks on Unix that the holder is on its own
parent chain. Live entries count for ordering; only verified ancestors
are passed through."
```

---

### Task 6: `run` uses verified held entries

**Files:**
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/cli.go` (help text on INCODA_HELD)
- Create: `held_test.go` (root, integration)
- Modify: `process_group_test.go` (nested group test)

**Interfaces:**
- Consumes: `held.Verify`, `held.Result.PassKeys`, `held.Result.LiveKeys`, `held.Format`, `held.Merge`, `held.Entry`, `procinfo.ParentChain`, `(*lane.Enrollment).Name`, `textsafe.Escape`, `textsafe.LogValue`, Task 1's `childEnv`, `startEnv`, `startGetenv`.
- Produces: the child receives `INCODA_HELD` = `held.Format(held.Merge(L, own tickets))`; the child gets its own process group iff L is empty; `held-dropped:` lines and `event=held-dropped` log lines.

- [ ] **Step 1: Write the failing integration tests**

`held_test.go`:

```go
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runWithHeld runs incoda with INCODA_HELD set to held.
func runWithHeld(t *testing.T, incoda, state, held string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(incoda, args...)
	cmd.Env = append(laneEnv(state), "INCODA_HELD="+held)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		code = exitCodeOf(err)
	}
	return string(out), code
}

// TestDeadHeldEntryIsDropped: a stale INCODA_HELD (a shell profile, a
// leftover export) must not let a run skip its lane.
func TestDeadHeldEntryIsDropped(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	marker := filepath.Join(t.TempDir(), "m.txt")
	// The lane must exist for the drop to be logged in it: a bogus key in
	// the environment never creates a lane directory.
	if out, code := runIncoda(t, incoda, state, "config", "hd", "--slots", "1"); code != 0 {
		t.Fatalf("config: %d\n%s", code, out)
	}
	out, code := runWithHeld(t, incoda, state, "hd=00000000000000000001-1.ticket",
		"run", "--queue", "hd", "--", stamp, marker, "m", "1")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "incoda: held-dropped: hd (dead)") {
		t.Fatalf("missing held-dropped line:\n%s", out)
	}
	if strings.Contains(out, "already held") {
		t.Fatalf("a dead entry must not pass through:\n%s", out)
	}
	log, _ := os.ReadFile(filepath.Join(state, "queues", "hd", "lane.log"))
	if !strings.Contains(string(log), "event=enqueue") || !strings.Contains(string(log), "event=held-dropped") {
		t.Fatalf("the run should enroll and log the drop:\n%s", log)
	}
}

// TestBareHeldKeyIsMalformed: the pre-0.7 bare-key form is not trusted.
func TestBareHeldKeyIsMalformed(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	out, code := runWithHeld(t, incoda, state, "hk",
		"run", "--queue", "hk", "--", stamp, filepath.Join(t.TempDir(), "m"), "m", "1")
	if code != 0 || !strings.Contains(out, "incoda: held-dropped: hk (malformed)") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// TestLiveNonAncestorEntryDoesNotPassThrough: a live ticket held by a
// process that is not this run's ancestor (a leaked variable) must not let
// the run share that lane.
func TestLiveNonAncestorEntryDoesNotPassThrough(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows trusts live entries: the Job Object ends every descendant with the holder")
	}
	incoda, stamp := binaries(t)
	state := t.TempDir()
	holder, _ := startHolder(t, incoda, stamp, state, "na", "holder", 20000, "50ms")
	defer func() { _ = holder.Process.Kill(); _ = holder.Wait() }()
	waitFor(t, incoda, state, "na", func(q queueReport) bool { return len(q.Holders) == 1 })

	var ticket string
	entries, _ := os.ReadDir(filepath.Join(state, "queues", "na"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".ticket") {
			ticket = e.Name()
		}
	}
	out, code := runWithHeld(t, incoda, state, "na="+ticket,
		"run", "--queue", "na", "--wait", "1", "--poll", "50ms", "--", stamp, filepath.Join(t.TempDir(), "m"), "m", "1")
	if code != 121 {
		t.Fatalf("a non-ancestor entry must queue (and time out here), got %d\n%s", code, out)
	}
	if !strings.Contains(out, "held-dropped: na (not-ancestor; still counts for ordering)") {
		t.Fatalf("missing not-ancestor line:\n%s", out)
	}
}
```

Append to `process_group_test.go`:

```go
// TestNestedChildStaysInOuterGroup: a nested run whose outer incoda is alive
// keeps its child in the outer group, so the outer tree kill reaches it.
func TestNestedChildStaysInOuterGroup(t *testing.T) {
	incoda, _ := binaries(t)
	tree := treeBinary(t)
	state := t.TempDir()
	out := filepath.Join(t.TempDir(), "tree.txt")
	cmd := exec.Command(incoda, "run", "--queue", "outer", "--quiet", "--",
		incoda, "run", "--queue", "inner", "--quiet", "--", tree, out)
	cmd.Env = laneEnv(state)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	ti := readTree(t, out)
	defer func() { _ = syscall.Kill(-ti.pgid, syscall.SIGKILL) }()
	// The inner incoda is tree's parent and leads the group the outer run
	// opened; tree must sit in that group, not in one of its own.
	if ti.pgid != ti.ppid {
		t.Fatalf("nested child should stay in the outer group (pgid %d) but has pgid %d", ti.ppid, ti.pgid)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test . -run 'TestDeadHeldEntry|TestBareHeldKey|TestLiveNonAncestor|TestNestedChildStays|TestReentrant' -v`
Expected: the three held tests FAIL (no `held-dropped` lines; the bare key passes through). `TestNestedChildStaysInOuterGroup` and `TestReentrantRunPassesThrough` already PASS under Task 1's interim rule and must keep passing after this task.

- [ ] **Step 3: Verify held entries at the start of `cmdRun`**

In `internal/cli/run.go`:

1. Replace `held := heldKeys()` with:

```go
	// Inherited lanes come from the environment incoda was started with.
	// Each entry is probed: dead and malformed ones are dropped, live ones
	// count for ordering and the process group (L), and only those held by
	// a verified ancestor are passed through (P).
	inherited := held.Verify(dir, startGetenv("INCODA_HELD"), procinfo.ParentChain())
	reportDropped(dir, inherited, *quiet, stderr, p)
	pass := inherited.PassKeys()
	live := inherited.LiveKeys()
```

2. In the key loop, replace `if held[key] {` with `if pass[key] {`.

3. In the out-of-order warning loop, replace `for h := range held {` with `for h := range live {`.

4. In the all-passed-through branch, replace the `child.Run` call with:

```go
		res, runErr := child.Run(argv, os.Stdin, os.Stdout, os.Stderr, nil, child.Options{
			Env:      childEnv(startEnv, held.Format(inherited.L)),
			OwnGroup: len(inherited.L) == 0,
		})
```

5. Replace the main `child.Run` call with:

```go
	// The child's INCODA_HELD is every live inherited entry plus this run's
	// own tickets, set only in the child's environment.
	own := make([]held.Entry, 0, len(toTake))
	for _, pt := range toTake {
		own = append(own, held.Entry{Key: pt.key, Ticket: pt.en.Name()})
	}
	res, runErr := child.Run(argv, os.Stdin, os.Stdout, os.Stderr, abort, child.Options{
		Env:      childEnv(startEnv, held.Format(held.Merge(inherited.L, own))),
		OwnGroup: len(inherited.L) == 0,
	})
```

6. Delete `heldKeys` and `joinHeld`.

7. Add the drop reporter at the end of the file:

```go
// reportDropped prints and logs every inherited entry that is not passed
// through. A log line is written only to a lane that already exists, so a
// bogus key in the environment never creates a directory.
func reportDropped(dir string, r held.Result, quiet bool, stderr io.Writer, p colorize.Palette) {
	for _, d := range r.Dropped {
		name := d.Key
		if name == "" {
			name = d.Raw
		}
		why := string(d.Why)
		if d.Live {
			why += "; still counts for ordering"
		}
		if !quiet {
			fmt.Fprintf(stderr, "%s %s\n", p.Dim("incoda:"),
				p.Yellow(fmt.Sprintf("held-dropped: %s (%s)", textsafe.Escape(name), why)))
		}
		if d.Key == "" || !lane.Exists(dir, d.Key) {
			continue
		}
		q, err := lane.Open(dir, d.Key)
		if err != nil {
			continue
		}
		q.Logf("queue=%s event=held-dropped pid=%d ticket=%s why=%s", d.Key, os.Getpid(), textsafe.LogValue(d.Ticket), d.Why)
		q.Close()
	}
}
```

`paletteFor` returns `colorize.Palette` (internal/cli/status.go:52). Add imports `github.com/deblasis/incoda/internal/held` and `github.com/deblasis/incoda/internal/procinfo`; make sure `io` is imported.

- [ ] **Step 4: Update the help text**

In `internal/cli/cli.go` `rootUsage`, replace:

```
named, taken in sorted order. A run exports INCODA_HELD to its child;
a nested run on a key listed there passes through instead of queueing behind
its own parent. INCODA_OWNER is the default for --owner.
```

with:

```
named, taken in sorted order. A run gives its child INCODA_HELD, the
KEY=TICKET entries of the lanes it holds; a nested run on a key listed there
passes through instead of queueing behind its own parent, once it has checked
that the ticket is alive and held by its ancestor. INCODA_OWNER is the
default for --owner.
```

- [ ] **Step 5: Run to verify the tests pass**

Run: `go test . -run 'TestDeadHeldEntry|TestBareHeldKey|TestLiveNonAncestor|TestNestedChildStays|TestReentrant|TestTopLevelChild|TestKillReaches' -v`
Expected: PASS.

- [ ] **Step 6: Run the full gates**

Run: `GOOS=windows go vet ./... && just ci`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/cli/run.go internal/cli/cli.go held_test.go process_group_test.go
git commit -m "feat: run trusts only verified held entries

A nested run passes through only lanes whose ticket is alive and held by
its ancestor. Dead and malformed entries are dropped and logged; live
entries from a non-ancestor still count for ordering. The child opens its
own process group exactly when no live outer incoda holds a lane for it."
```

---

## Self-review against the spec

- 2.6 "Process group, Unix": Task 1 (start env, `cmd.Env`, no `os.Setenv`), Task 6 (group iff L empty). The `pgid` comparison clause ("or this process's group differs from the `pgid` recorded in every live L entry") needs the `pgid` ticket field, which plan 4 adds with `root`; until then a live L means "stay in the inherited group", which is today's behaviour for nested runs. Tests from section 9 covered here: pgid equals pid, grandchild dies, INCODA_HELD not in own environment, all-dead gives own group (TestDeadHeldEntryIsDropped runs top-level), nested stays in outer group. The `set -m` case is plan 4.
- 2.6 verification steps 1 to 3, `held-dropped` texts and log line: Tasks 4 to 6. The ordering rule, non-blocking acquisition, self-wait and held-lost are plan 4.
- 4.6: Task 2 covers the escaper, write rejection for config text and lane.log quoting; status, watch, refusal and doctor rendering adopt it in plans 2, 3 and 5 as those outputs are written.
- 2.2 and 4.4: Task 3 covers schema 2, unknown fields, newer-schema refusal, locked read-modify-write and the Windows rename retry. The same retry for `machine.json` and `migration.json` is plan 2.
- Layout paths: everything here still uses `lane.QueueDir` (today's `queues/`); plan 2 moves it to `lanes/` in one place.
