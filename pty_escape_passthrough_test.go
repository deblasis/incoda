//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/jobstatus"
)

// childEscapeBattery emits the sequence classes incoda must never touch:
// CSI styling/cursor, a window title OSC, a program-status OSC 7501 report
// (what watch renders and answer's door reads), and a done report. The
// console self-hide runs at startup for exactly this shape (piped stdio);
// none of it may edit the bytes on the way through.
const childEscapeBattery = `
import sys, time
seq = []
seq.append("\x1b[31mred\x1b[0m")
seq.append("\x1b]0;pty-title\x07")
seq.append("\x1b]7501;state=working:id=build:progress=40:msg=bGlua2luZw==\x07")
seq.append("\x1b[2Kline-cleared")
seq.append("\x1b]7501;state=done:id=build\x07")
sys.stdout.write("\r\n" + "".join(seq) + "\r\n")
sys.stdout.flush()
time.sleep(0.5)
`

func runEscapeBattery(t *testing.T, pty bool) (string, string, []string, int) {
	t.Helper()
	py, err := exec.LookPath("python")
	if err != nil {
		t.Skip("no python on PATH for the fixture")
	}
	bin, _ := ptyBinaries(t)
	key := "pty-esc"
	state := t.TempDir()
	linkTestKeys(t, bin, state, key)
	script := filepath.Join(t.TempDir(), "escapes.py")
	if err := os.WriteFile(script, []byte(childEscapeBattery), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"run", "--queue", key, "--owner", "alice", "--reason", "test"}
	if pty {
		args = append(args, "--pty")
	} else {
		args = append(args, "--pty=false")
	}
	args = append(args, "--", py, script)
	out, errb, code := ptyRunIncoda(t, ownerEnv(state, "alice"), args...)
	files, _ := filepath.Glob(filepath.Join(state, "lanes", key, "jobs", "*.out"))
	return out, errb, files, code
}

// TestPipePassesEscapesByteForByte: on the pipe path incoda is the only
// writer between the job and the caller, so every byte class the job emits -
// CSI, OSC title, OSC 7501 - must reach stdout and the job capture exactly
// as written. This is the path that proves incoda itself never edits the
// stream; the pty path below proves the status protocol survives the host.
func TestPipePassesEscapesByteForByte(t *testing.T) {
	out, errb, _, code := runEscapeBattery(t, false)
	if code != 0 {
		t.Fatalf("run exited %d:\n%s\n%s", code, out, errb)
	}
	want := []byte("\x1b[31mred\x1b[0m")
	want = append(want, []byte("\x1b]0;pty-title\x07")...)
	want = append(want, []byte("\x1b]7501;state=working:id=build:progress=40:msg=bGlua2luZw==\x07")...)
	want = append(want, []byte("\x1b[2Kline-cleared")...)
	want = append(want, []byte("\x1b]7501;state=done:id=build\x07")...)
	if !strings.Contains(out, string(want)) {
		t.Fatalf("stdout did not carry the battery verbatim:\n%q", out)
	}
	// Pipe-mode runs have no job capture (joblog is the pty path's tee), so
	// stdout is the only surface and it must be byte-exact.
}

// TestPTYKeepsOSC7501VisibleToIncoda: under the default ConPTY the host
// renders the job's screen (that is what a pseudo-console is), but the OSC
// 7501 reports must still reach the capture and be readable by the same
// follower watch uses - the console self-hide must not disturb either.
func TestPTYKeepsOSC7501VisibleToIncoda(t *testing.T) {
	out, errb, files, code := runEscapeBattery(t, true)
	if code != 0 {
		t.Fatalf("run exited %d:\n%s\n%s", code, out, errb)
	}
	for _, want := range []string{
		"\x1b]7501;state=working:id=build:progress=40:msg=bGlua2luZw==\x07",
		"\x1b]7501;state=done:id=build\x07",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout lost the OSC 7501 report %q:\n%q", want, out)
		}
	}
	if len(files) != 1 {
		t.Fatalf("expected one job capture, got %v", files)
	}
	captured, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	// ESC bytes are present: the host's rendering is still a VT stream.
	if !strings.Contains(string(captured), "\x1b") {
		t.Fatalf("capture carries no ESC bytes at all:\n%q", captured)
	}
	var st jobstatus.LogStatus
	rec, ok := st.ReadWhole(files[0])
	if !ok {
		t.Fatalf("the OSC 7501 report in the capture was not readable")
	}
	// ReadWhole returns the NEWEST record, which is the done report.
	if rec.State != "done" || rec.ID != "build" {
		t.Fatalf("parsed record = %+v, want the done report for build", rec)
	}
}
