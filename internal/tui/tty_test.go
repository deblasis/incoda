package tui

import (
	"strings"
	"testing"
)

func TestWatchTTYRestoreSequence(t *testing.T) {
	for _, want := range []string{"\x1b[?1049l", "\x1b[?25h", "\x1b[?1000l", "\x1b[?1002l", "\x1b[?1003l", "\x1b[?1006l"} {
		if !strings.Contains(watchTTYRestoreSeq, want) {
			t.Fatalf("restore seq missing %q: %q", want, watchTTYRestoreSeq)
		}
	}
}

func TestRestoreWatchTTYNoPanic(t *testing.T) {
	// /dev/tty is absent in some CI sandboxes; skip rather than fail the build.
	restoreWatchTTY()
}
