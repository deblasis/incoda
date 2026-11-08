//go:build windows

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// childQuery is the job under test: it asks the terminal whether it speaks
// OSC 7501, waits up to five seconds for console input, and echoes whatever
// arrived (repr'd, so the escape bytes are visible in the capture) between
// markers.
const childQuery = `
import msvcrt, sys, time
sys.stdout.write("\x1b]7501;?\x07")
sys.stdout.flush()
got = ""
end = time.time() + 5
while time.time() < end:
    if msvcrt.kbhit():
        got += msvcrt.getwch()
        if got.endswith("\x1b\\"):
            break
    else:
        time.sleep(0.02)
sys.stdout.write("\r\nREPLY<" + repr(got) + ">END\r\n")
sys.stdout.flush()
`

// TestPTYAnswersOSC7501Query: a job under --pty that asks the terminal for
// program-status support gets the support reply on its console input. The
// round trip is the point: query out through the master read loop, reply back
// through the master input, read by the job as typed input.
func TestPTYAnswersOSC7501Query(t *testing.T) {
	py, err := exec.LookPath("python")
	if err != nil {
		t.Skip("no python on PATH for the query fixture")
	}
	bin, _ := ptyBinaries(t)
	state := t.TempDir()
	linkTestKeys(t, bin, state, "pty-osc")
	script := filepath.Join(t.TempDir(), "query.py")
	if err := os.WriteFile(script, []byte(childQuery), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errb, code := ptyRunIncoda(t, ownerEnv(state, "alice"),
		"run", "--queue", "pty-osc", "--owner", "alice", "--reason", "test", "--pty", "--",
		py, script)
	if code != 0 {
		t.Fatalf("run exited %d:\n%s\n%s", code, out, errb)
	}
	want := `'\x1b]7501;?:states=idle,working,done,blocked,error,clear:kinds=permission,question,auth\x07'`
	if !strings.Contains(out, "REPLY<"+want+">END") {
		if strings.Contains(out, "REPLY<''>END") {
			t.Fatalf("the job asked and got no reply on its input:\n%q", out)
		}
		t.Fatalf("reply did not round-trip:\n%q", out)
	}

	// Passthrough is untouched: the query itself is still in the stream the
	// run's stdout and the job file carry.
	query := []byte("\x1b]7501;?\x07")
	files, err := filepath.Glob(filepath.Join(state, "lanes", "pty-osc", "jobs", "*.out"))
	if err != nil || len(files) != 1 {
		t.Fatalf("want one job file, got %v (%v)", files, err)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, query) {
		t.Fatalf("job file lost the query bytes:\n%q", b)
	}
	if !strings.Contains(out, string(query)) {
		t.Fatalf("stdout lost the query bytes:\n%q", out)
	}
}
