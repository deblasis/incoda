//go:build windows

package child

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) Bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.b.Bytes()...)
}

// The reply must never reach the job's output whatever mode its console is
// in: a cooked-mode reader (readline, input(), fgets) has every injected key
// echoed by its console, and the echo lands in the tee, the job's log, and
// the run's stdout. The injector answers with the console's echo off, so a
// readline job still gets its reply as input while the passthrough stream
// stays exactly what the job wrote.
func TestReplyDoesNotLeakIntoACookedModeJobsOutput(t *testing.T) {
	py, err := exec.LookPath("python")
	if err != nil {
		t.Skip("no python on PATH")
	}
	script := filepath.Join(t.TempDir(), "c.py")
	src := `
import sys, threading, os, time
def wd():
    time.sleep(8); os._exit(0)
threading.Thread(target=wd, daemon=True).start()
sys.stdout.write("\x1b]7501;?\x07"); sys.stdout.flush()
time.sleep(0.5)
sys.stdout.write("PROMPT> "); sys.stdout.flush()
sys.stdin.readline()
`
	if err := os.WriteFile(script, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	var tee lockedBuffer
	if _, err := Run([]string{py, script}, nil, nil, nil, nil, Options{PTY: &PTYOptions{Tee: &tee}}); err != nil {
		t.Fatal(err)
	}
	out := tee.Bytes()
	if bytes.Contains(out, []byte("states=idle")) {
		t.Fatalf("support reply leaked into the job output (console echo):\n%q", out)
	}
	// The readline itself never returns: a terminal's reply carries no
	// Enter, so it sits at the head of the line the job is reading, which
	// is what a real terminal's answer does there too.
	if !bytes.Contains(out, []byte("PROMPT> ")) {
		t.Fatalf("the job's own output never made it through:\n%q", out)
	}
}

// The injector helper is this very binary in a test, and a test binary
// handed "__coninj pid b64" would run the suite instead of the injection,
// so TestMain routes the hidden entry to the injector the way cli.Main does
// in the real binary.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == ConinjCommand {
		InjectConsoleInput(os.Args[2:])
		os.Exit(0)
	}
	os.Exit(m.Run())
}
