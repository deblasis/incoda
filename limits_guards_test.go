package main

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// requireStallCounter guards a test that asserts a --stall kill, or the
// absence of a warning around one. The CPU counter it needs is proven on
// windows and linux; on darwin it is not, so the run calibrates, warns and
// never arms, and neither shape of assertion can hold there. The platform
// deferral is the README's, in the Run limits section.
func requireStallCounter(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("the darwin CPU counter is not proven: --stall warns and disarms, so stall kills and clean-warning assertions do not hold here")
	}
}

// requirePty guards a variant that passes --pty=true: pty mode is a Windows
// pseudo-console and the run refuses the flag everywhere else with exit 120.
func requirePty(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("pty mode is Windows-only; the run refuses --pty=true on this platform")
	}
}

// TestPlatformGuardsStayOnTheLimitTests pins each guard to the tests that
// need it, by name. Without this, a rename can drop a guard silently and the
// deterministic red comes back on the linux and darwin CI legs. It fails when
// a guarded test is gone, when one lost its guard, and when a guard count
// changes in either direction, so guards cannot creep onto other tests
// unnoticed either.
func TestPlatformGuardsStayOnTheLimitTests(t *testing.T) {
	src, err := os.ReadFile("limits_test.go")
	if err != nil {
		t.Fatal(err)
	}
	guarded := map[string]string{
		"TestStallKillsAnIdleJob":           "requireStallCounter",
		"TestStallCountsDescendants":        "requireStallCounter",
		"TestLimitLaneDefaultsAndOverrides": "requireStallCounter",
		"TestNestedRunDoesNotKillItsParent": "requireStallCounter",
		"TestLimitsOnPipes":                 "requireStallCounter",
		"TestShortStallWarns":               "requireStallCounter",
		"TestLimitKillLeavesNothingBehind":  "requirePty",
	}
	calls := map[string]int{}
	for name, guard := range guarded {
		start := strings.Index(string(src), "func "+name+"(")
		if start < 0 {
			t.Errorf("%s is gone; move its %s guard to the new name", name, guard)
			continue
		}
		end := strings.Index(string(src)[start+1:], "\nfunc ")
		body := string(src)[start:]
		if end >= 0 {
			body = string(src)[start : start+1+end]
		}
		if !strings.Contains(body, guard+"(t)") {
			t.Errorf("%s lost its %s guard", name, guard)
		}
	}
	for _, g := range []string{"requireStallCounter", "requirePty"} {
		calls[g] = strings.Count(string(src), g+"(t)")
		want := 0
		for _, guard := range guarded {
			if guard == g {
				want++
			}
		}
		if calls[g] != want {
			t.Errorf("%s is called %d times, want exactly %d", g, calls[g], want)
		}
	}
}
