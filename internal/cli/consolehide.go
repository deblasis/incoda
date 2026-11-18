package cli

import "time"

// Console self-hide: a console-subsystem program spawned by a parent with
// no console gets a fresh VISIBLE console window from Windows. When that
// happens to incoda and nobody else is attached to the console and the
// job output is captured, the window is pure noise: hide it. When any
// clause is false the window stays, because a human may be reading or
// typing at it.
//
// The predicate is platform-independent so it is testable everywhere; the
// Windows fact-gathering and the ShowWindow call live in
// consolehide_windows.go, and other platforms are a no-op.

// consoleFacts is what the hide decision depends on, sampled once per
// retry step.
type consoleFacts struct {
	HasWindow bool // GetConsoleWindow() != 0
	Visible   bool // IsWindowVisible(window)
	Attached  int  // processes attached to the console (GetConsoleProcessList)
	StdoutCon bool // stdout is a console handle (GetConsoleMode succeeds)
	StderrCon bool // stderr is a console handle
	Window    uintptr
}

const (
	hideRetryWindow = 3 * time.Second
	hideRetryStep   = 50 * time.Millisecond
)

// shouldHide is the whole policy: hide only a window nobody can be reading
// or typing at. stdout/stderr on the console mean the window carries output
// that exists nowhere else (a bare `incoda run` from a console-less harness
// included); Attached != 1 means a human shell shares the console.
func shouldHide(f consoleFacts, subcommand string) bool {
	if !f.HasWindow || !f.Visible || f.Attached != 1 {
		return false
	}
	if f.StdoutCon || f.StderrCon {
		return false
	}
	switch subcommand {
	case "watch", "attach", "answer":
		return false
	default:
		return true
	}
}

// hidableCommand reports whether the subcommand may ever be auto-hidden.
func hidableCommand(subcommand string) bool {
	switch subcommand {
	case "watch", "attach", "answer":
		return false
	default:
		return true
	}
}
