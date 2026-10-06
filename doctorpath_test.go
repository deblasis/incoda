//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestDoctorProbesEveryIncodaOnPath: doctor runs `<path> version` for
// every incoda on PATH (never one from the PATH the tests run with: these
// are fake scripts on a temp PATH). It flags one older than 0.7, gives an
// attention line for a dev, an unparseable and a silent one, kills a probe
// that does not answer within 5s with its whole group, and is not held up
// by a probe whose child keeps its output open.
func TestDoctorProbesEveryIncodaOnPath(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "hang.pid")
	scripts := []struct{ name, body string }{
		{"old", `echo "incoda v0.6.0"; echo "commit: abc"`},
		{"new", `read x; echo "incoda v0.7.1"`},
		{"dev", `echo "incoda dev"`},
		{"holdsout", `( /bin/sleep 30 ) & echo "incoda v0.7.0"`},
		{"hang", `echo $$ > "` + pidFile + `"; exec /bin/sleep 30`},
		{"junk", `echo "hello there"`},
	}
	var dirs []string
	for _, sc := range scripts {
		d := filepath.Join(t.TempDir(), sc.name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "incoda"), []byte("#!/bin/sh\n"+sc.body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, d)
	}
	start := time.Now()
	out, code := doctorWithPath(t, incoda, state, strings.Join(dirs, string(os.PathListSeparator)))
	if code != 0 {
		t.Fatalf("doctor exit %d\n%s", code, out)
	}
	p := func(name string) string {
		return filepath.Join(dirs[map[string]int{"old": 0, "new": 1, "dev": 2, "holdsout": 3, "hang": 4, "junk": 5}[name]], "incoda")
	}
	mustContain(t, out,
		"on PATH:   "+p("old")+" v0.6.0 (older than 0.7)\n",
		"on PATH:   "+p("new")+" v0.7.1\n",
		"on PATH:   "+p("dev")+" dev\n",
		"on PATH:   "+p("holdsout")+" v0.7.0\n",
		"on PATH:   "+p("hang")+" (no answer)\n",
		"on PATH:   "+p("junk")+" (version unknown)\n",
		"attention: "+p("old")+" is incoda v0.6.0, older than 0.7: it stops with \"not a directory\" (exit 122) on this state directory and runs nothing; upgrade it",
		"attention: "+p("dev")+" reports version \"dev\": cannot tell whether it is older than 0.7",
		"attention: "+p("hang")+" did not answer \"version\" within 5s",
		"attention: "+p("junk")+": cannot read its version (unexpected output \"hello there\")")
	if el := time.Since(start); el > 15*time.Second {
		t.Fatalf("doctor took %s: every probe is bounded", el)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	hang, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	waitGone(t, "the probe that did not answer", hang)
}
