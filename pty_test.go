// PTY-mode integration tests: a real incoda binary, a real pseudo-console,
// real conhost processes. The conhost accounting is the point of these: a
// pseudo-console that outlives its job is exactly the leak the job-object
// adoption exists to prevent, and nothing short of real processes can show
// it.
package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/pty"
)

var (
	ttyOnce sync.Once
	ttyBin  string
	ttyErr  error
)

// ptyBinaries returns the incoda binary and the ttyecho fixture, building
// both once per test binary run.
func ptyBinaries(t *testing.T) (string, string) {
	t.Helper()
	bin, _ := binaries(t)
	ttyOnce.Do(func() {
		ext := ""
		if runtime.GOOS == "windows" {
			ext = ".exe"
		}
		ttyBin = filepath.Join(binDir, "ttyecho"+ext)
		cmd := exec.Command("go", "build", "-o", ttyBin, "./internal/testprog/ttyecho")
		cmd.Env = append(os.Environ(), "GOTOOLCHAIN=auto")
		if out, err := cmd.CombinedOutput(); err != nil {
			ttyErr = fmt.Errorf("build ttyecho: %v\n%s", err, out)
		}
	})
	if ttyErr != nil {
		t.Fatal(ttyErr)
	}
	return bin, ttyBin
}

func requireWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("pty mode is Windows ConPTY")
	}
}

func ownerEnv(stateDir, owner string) []string {
	return append(laneEnv(stateDir), "INCODA_OWNER="+owner)
}

// runIncoda runs one incoda command to completion and returns its streams
// separately: pty output is a byte stream on stdout, and mixing it with
// stderr would blur every assertion here.
func ptyRunIncoda(t *testing.T, env []string, args ...string) (string, string, int) {
	t.Helper()
	bin, _ := ptyBinaries(t)
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	code := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run %v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	return out.String(), errb.String(), code
}

type holderView struct {
	File string `json:"file"`
	Tick struct {
		PID   int    `json:"pid"`
		PTY   bool   `json:"pty"`
		Owner string `json:"owner"`
	} `json:"ticket"`
}

func statusHolders(t *testing.T, stateDir, key string) ([]holderView, error) {
	t.Helper()
	bin, _ := ptyBinaries(t)
	cmd := exec.Command(bin, "status", "--queue", key, "--json")
	cmd.Env = laneEnv(stateDir)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	var snap struct {
		Queues []struct {
			Key     string       `json:"key"`
			Holders []holderView `json:"holders"`
		} `json:"queues"`
	}
	if err := json.Unmarshal(out.Bytes(), &snap); err != nil {
		return nil, fmt.Errorf("status --json: %v\n%s", err, out.String())
	}
	for _, q := range snap.Queues {
		if q.Key == key {
			return q.Holders, nil
		}
	}
	return nil, fmt.Errorf("status --json: no queue %q in output:\n%s", key, out.String())
}

// waitHolder polls until the queue reports the run as holding, then returns
// its view. The pid in it is the run process's, which is what kill and
// attach both address.
func waitHolder(t *testing.T, stateDir, key string) holderView {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		hs, err := statusHolders(t, stateDir, key)
		if err == nil && len(hs) == 1 {
			return hs[0]
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no holder appeared on %s within 20s", key)
	return holderView{}
}

// conhostGone polls until no conhost process is parented to pid any more.
func conhostGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		pids, err := pty.ConhostPIDs(pid)
		if err == nil && len(pids) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("conhost(s) %v still alive under pid %d after 10s", pids, pid)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// TestPTYCaptureAndPassthrough: what a pty-mode job writes reaches the run's
// stdout AND the per-job file, with an OSC 7501 sequence intact in both. The
// sequence is what program-status emission rides on, and any step in the tee
// path that parsed or rewrote bytes would break it.
func TestPTYCaptureAndPassthrough(t *testing.T) {
	requireWindows(t)
	bin, tty := ptyBinaries(t)
	state := t.TempDir()
	linkTestKeys(t, bin, state, "pty-cap")
	osc := "\x1b]7501;state=run\x07"
	payload := "P7K-line1\r\n" + osc + "P7K-end\r\n"
	out, errb, code := ptyRunIncoda(t, ownerEnv(state, "alice"),
		"run", "--queue", "pty-cap", "--owner", "alice", "--reason", "test", "--pty", "--",
		tty, "write", "hex:"+hex.EncodeToString([]byte(payload)))
	if code != 0 {
		t.Fatalf("run exited %d:\n%s", code, errb)
	}
	for _, want := range []string{"P7K-line1", "P7K-end", osc} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout lost %q; got:\n%q", want, out)
		}
	}
	files, err := filepath.Glob(filepath.Join(state, "lanes", "pty-cap", "jobs", "*.out"))
	if err != nil || len(files) != 1 {
		t.Fatalf("want exactly one job file, got %v (%v)", files, err)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"P7K-line1", "P7K-end", osc} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("job file lost %q; got:\n%q", want, b)
		}
	}
}

// TestPTYTtyVsPipe: the job sees a TTY exactly when pty mode is on, and the
// default stays the pipe path.
func TestPTYTtyVsPipe(t *testing.T) {
	requireWindows(t)
	bin, tty := ptyBinaries(t)
	state := t.TempDir()
	linkTestKeys(t, bin, state, "pty-tty")
	for _, on := range []bool{false, true} {
		want := "stdout=pipe"
		args := []string{"run", "--queue", "pty-tty", "--owner", "alice", "--reason", "test", "--"}
		if on {
			want = "stdout=tty"
			args = append(args[:len(args)-1], "--pty", "--")
		}
		args = append(args, tty, "tty")
		out, _, code := ptyRunIncoda(t, ownerEnv(state, "alice"), args...)
		if code != 0 {
			t.Fatalf("run exited %d:\n%s", code, out)
		}
		if !strings.Contains(out, want) {
			t.Fatalf("pty=%v: want %q in output, got:\n%q", on, want, out)
		}
	}
}

// TestPTYAttachGateStreamKillCleanup is the whole attach story in one lane
// hold: the ticket says pty, the wrong owner is refused by name, the right
// owner sees the stream live, a kill through the lane takes the job down,
// and no conhost survives the teardown.
func TestPTYAttachGateStreamKillCleanup(t *testing.T) {
	requireWindows(t)
	bin, tty := ptyBinaries(t)
	state := t.TempDir()
	linkTestKeys(t, bin, state, "pty-live")
	env := ownerEnv(state, "alice")
	run := exec.Command(bin, "run", "--queue", "pty-live", "--owner", "alice", "--reason", "test",
		"--pty", "--wait", "90s", "--", tty, "spin", "m1", "200", "60")
	run.Env = env
	var runOut, runErr bytes.Buffer
	run.Stdout = &runOut
	run.Stderr = &runErr
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if run.Process != nil {
			_ = run.Process.Kill()
			_, _ = run.Process.Wait()
		}
	}()

	h := waitHolder(t, state, "pty-live")
	if h.Tick.PID != run.Process.Pid {
		t.Fatalf("status reports pid %d, run is %d", h.Tick.PID, run.Process.Pid)
	}
	if !h.Tick.PTY || h.Tick.Owner != "alice" {
		t.Fatalf("ticket should say pty by alice, got %+v", h.Tick)
	}

	// Another owner is refused, and the refusal names both sides.
	out, errb, code := ptyRunIncoda(t, ownerEnv(state, "bob"),
		"attach", "--queue", "pty-live", "--pid", fmt.Sprint(h.Tick.PID))
	if code != 120 {
		t.Fatalf("wrong owner attach: want exit 120, got %d\n%s\n%s", code, out, errb)
	}
	for _, want := range []string{"belongs to owner \"alice\"", "you are \"bob\"", "Only the job's owner may attach"} {
		if !strings.Contains(errb, want) && !strings.Contains(out, want) {
			t.Fatalf("refusal missing %q:\n%s%s", want, out, errb)
		}
	}

	// The right owner attaches and sees the stream live: the marker has
	// moved past anything the file held when attach started.
	attach := exec.Command(bin, "attach", "--queue", "pty-live", "--pid", fmt.Sprint(h.Tick.PID))
	attach.Env = ownerEnv(state, "alice")
	attachErr := &bytes.Buffer{}
	attach.Stderr = attachErr
	pipe, err := attach.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := attach.Start(); err != nil {
		t.Fatal(err)
	}
	sawLive := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		var acc bytes.Buffer
		for {
			n, err := pipe.Read(buf)
			if n > 0 {
				acc.Write(buf[:n])
				if strings.Contains(acc.String(), "m1 5") {
					sawLive <- acc.String()
					return
				}
			}
			if err != nil {
				sawLive <- acc.String()
				return
			}
		}
	}()
	select {
	case got := <-sawLive:
		if !strings.Contains(got, "m1 5") {
			t.Fatalf("attach stream ended before live output:\n%q\nstderr: %s", got, attachErr)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("attach never saw live output (stderr: %s)", attachErr)
	}
	_ = attach.Process.Kill()
	_, _ = attach.Process.Wait()

	// Kill through the lane: the run exits 124 and nothing of the
	// pseudo-console is left behind.
	_, _, kcode := ptyRunIncoda(t, ownerEnv(state, "tester"),
		"kill", "--queue", "pty-live", "--pid", fmt.Sprint(h.Tick.PID), "--reason", "pty integration test", "--wait", "20s")
	if kcode != 0 {
		t.Fatalf("kill exited %d", kcode)
	}
	werr := run.Wait()
	ee, ok := werr.(*exec.ExitError)
	if !ok || ee.ExitCode() != 124 {
		t.Fatalf("run should exit 124 after the kill, got %v", werr)
	}
	conhostGone(t, h.Tick.PID)
}

// TestPTYForceKilledRunLeavesNoConhost: force-killing the run process (the
// case no cooperative teardown ever reaches) must still take the
// pseudo-console down. It goes with the run's own handles: process death
// closes the HPCON, which ends conhost, and closes the job object, which
// kills the child.
func TestPTYForceKilledRunLeavesNoConhost(t *testing.T) {
	requireWindows(t)
	bin, tty := ptyBinaries(t)
	state := t.TempDir()
	linkTestKeys(t, bin, state, "pty-force")
	run := exec.Command(bin, "run", "--queue", "pty-force", "--owner", "alice", "--reason", "test",
		"--pty", "--wait", "90s", "--", tty, "spin", "f1", "200", "60")
	run.Env = ownerEnv(state, "alice")
	run.Stdout = io.Discard
	run.Stderr = io.Discard
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	h := waitHolder(t, state, "pty-force")
	if err := run.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = run.Wait()
	conhostGone(t, h.Tick.PID)
}
