package cli

import (
	"strings"
	"testing"
	"time"
)

// Console self-hide: incoda hides its OWN console window only when nobody
// else is attached and its output is captured, so no human can be reading
// or typing at it. These cases are the design's predicate matrix; the
// Windows fact-gathering lives in consolehide_windows.go.

func TestConsoleHidePredicate(t *testing.T) {
	cases := []struct {
		name string
		f    consoleFacts
		cmd  string
		want bool
	}{
		{
			name: "fresh console, captured output, alone: hide",
			f:    consoleFacts{HasWindow: true, Visible: true, Attached: 1, StdoutCon: false, StderrCon: false},
			cmd:  "run",
			want: true,
		},
		{
			name: "stdout on the console: keep (human can read)",
			f:    consoleFacts{HasWindow: true, Visible: true, Attached: 1, StdoutCon: true, StderrCon: false},
			cmd:  "run",
			want: false,
		},
		{
			name: "stderr on the console: keep (human can read)",
			f:    consoleFacts{HasWindow: true, Visible: true, Attached: 1, StdoutCon: false, StderrCon: true},
			cmd:  "run",
			want: false,
		},
		{
			name: "bare stdio (both console): keep (only copy of output)",
			f:    consoleFacts{HasWindow: true, Visible: true, Attached: 1, StdoutCon: true, StderrCon: true},
			cmd:  "run",
			want: false,
		},
		{
			name: "shared console: never hide",
			f:    consoleFacts{HasWindow: true, Visible: true, Attached: 2, StdoutCon: false, StderrCon: false},
			cmd:  "run",
			want: false,
		},
		{
			name: "already hidden: nothing to do",
			f:    consoleFacts{HasWindow: true, Visible: false, Attached: 1, StdoutCon: false, StderrCon: false},
			cmd:  "run",
			want: false,
		},
		{
			name: "no console at all",
			f:    consoleFacts{HasWindow: false, Visible: false, Attached: 0, StdoutCon: false, StderrCon: false},
			cmd:  "run",
			want: false,
		},
		{name: "watch is interactive", f: consoleFacts{HasWindow: true, Visible: true, Attached: 1}, cmd: "watch", want: false},
		{name: "attach is interactive", f: consoleFacts{HasWindow: true, Visible: true, Attached: 1}, cmd: "attach", want: false},
		{name: "answer is interactive", f: consoleFacts{HasWindow: true, Visible: true, Attached: 1}, cmd: "answer", want: false},
		{name: "status is not interactive", f: consoleFacts{HasWindow: true, Visible: true, Attached: 1}, cmd: "status", want: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldHide(c.f, c.cmd); got != c.want {
				t.Fatalf("shouldHide(%+v, %q) = %v, want %v", c.f, c.cmd, got, c.want)
			}
		})
	}
}

func TestConsoleFactsSummaryNamesTheDecision(t *testing.T) {
	line := consoleFactsSummary()
	for _, want := range []string{"window=", "attached=", "hide-would-fire="} {
		if !strings.Contains(line, want) {
			t.Fatalf("doctor console line lacks %q: %s", want, line)
		}
	}
}

func TestSettleStateRestartsTheQuietWindow(t *testing.T) {
	for i, tc := range []struct {
		visible       bool
		sinceLastHide time.Duration
		sinceFirst    time.Duration
		needHide      bool
		done          bool
	}{
		{true, 100 * time.Millisecond, 100 * time.Millisecond, true, false},
		{true, 2 * time.Second, 2 * time.Second, true, false},
		{false, 2 * time.Second, 2 * time.Second, false, false},          // quiet window not over
		{false, hideRetryWindow, hideRetryWindow, false, true},           // quiet to the end
		{true, hideRetryWindow, hideRetryWindow, true, false},            // visible: window restarts
		{false, 10 * time.Millisecond, 2 * hideRetryWindow, false, true}, // cap, quiet
		{true, 10 * time.Millisecond, 2 * hideRetryWindow, true, true},   // cap, still visible
	} {
		needHide, done := settleState(tc.visible, tc.sinceLastHide, tc.sinceFirst)
		if needHide != tc.needHide || done != tc.done {
			t.Fatalf("case %d: got (%v,%v), want (%v,%v)", i, needHide, done, tc.needHide, tc.done)
		}
	}
}
