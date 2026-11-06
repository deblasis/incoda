//go:build windows

// PTY-mode integration tests: a real incoda binary, a real pseudo-console,
// real conhost processes. The conhost accounting is the point of these: a
// pseudo-console that outlives its job is a handle leak, and nothing short
// of real processes can show it. ConPTY only exists on Windows, so this
// file does not build anywhere else; the off-Windows refusal has its own
// test in internal/cli.
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
	"regexp"
	"strconv"
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
		ttyBin = filepath.Join(binDir, "ttyecho.exe") // this file only builds on Windows
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

func ownerEnv(stateDir, owner string) []string {
	return append(laneEnv(stateDir), "INCODA_OWNER="+owner)
}

// joblogCutMarker is the distinctive phrase of the drain-cut marker line:
// a clean exit must never carry it, because a clean exit ends its tee by
// EOF from a closed pseudo-console, not by the grace window cutting it.
const joblogCutMarker = "job output cut"

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
	// Each payload piece must arrive as its exact bytes: anything in the
	// tee path that parsed, re-encoded or rewrote line endings breaks one
	// of these. Contiguity is NOT asserted: conhost reorders the stream
	// (it hoisted the OSC ahead of the screen clear in the run that shaped
	// this test), so byte-exactness holds per sequence, not per ordering.
	for _, want := range []string{"P7K-line1\r\n", osc, "P7K-end\r\n"} {
		if !bytes.Contains(b, []byte(want)) {
			t.Fatalf("job file lost the exact bytes %q; got:\n%q", want, b)
		}
	}
	// ConPTY wraps the stream in its own prologue, so the capture starts
	// with conhost's sequences, not the job's first byte: an escape sequence
	// sits before the payload.
	esc := bytes.Index(b, []byte("\x1b["))
	pay := bytes.Index(b, []byte("P7K-line1"))
	if esc < 0 || pay < 0 || esc > pay {
		t.Fatalf("capture has no conhost prologue ahead of the payload (esc=%d payload=%d):\n%q", esc, pay, b)
	}
	// A normal exit must not carry the drain-cut marker: the run closes the
	// pseudo-console once the job is gone, so EOF, not the grace window,
	// ends the tee.
	if bytes.Contains(b, []byte(joblogCutMarker)) {
		t.Fatalf("a clean exit was cut: %q appears in the capture", joblogCutMarker)
	}
}

// TestPTYTtyVsPipe: the job sees a TTY exactly when pty mode is on, and the
// default stays the pipe path.
func TestPTYTtyVsPipe(t *testing.T) {
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

// TestPTYEnvSurvivesSpawn: the pseudo-console spawn hands the job the whole
// environment. A malformed environment block gives the job a one-variable
// environment and nothing names the loss: most jobs then fail somewhere
// downstream in ways nobody connects to the spawn, so the count and two named
// survivors are pinned here.
func TestPTYEnvSurvivesSpawn(t *testing.T) {
	bin, tty := ptyBinaries(t)
	state := t.TempDir()
	linkTestKeys(t, bin, state, "pty-env")
	env := append(ownerEnv(state, "alice"), "INCODA_PTY_PROBE=marker-7f3c")
	out, errb, code := ptyRunIncoda(t, env,
		"run", "--queue", "pty-env", "--owner", "alice", "--reason", "test", "--pty", "--",
		tty, "env", "INCODA_PTY_PROBE", "LOCALAPPDATA")
	if code != 0 {
		t.Fatalf("run exited %d:\n%s", code, errb)
	}
	m := regexp.MustCompile(`envcount=(\d+)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no envcount line in output:\n%q", out)
	}
	n, _ := strconv.Atoi(m[1])
	if n < 10 {
		t.Fatalf("the job saw %d environment variables; the spawn mangled the block", n)
	}
	if !strings.Contains(out, "env:INCODA_PTY_PROBE=marker-7f3c") {
		t.Fatalf("the probe variable did not survive the spawn:\n%q", out)
	}
	if !strings.Contains(out, "env:LOCALAPPDATA=") || strings.Contains(out, "env:LOCALAPPDATA=UNSET") {
		t.Fatalf("LOCALAPPDATA did not survive the spawn:\n%q", out)
	}
}

// TestPTYConfigDefaultFollowsNamedFirstKey: the pty default comes from the
// config of the key the user named first, not the first key of the plan's
// sorted order. pty-zulu sorts after pty-alpha, and only pty-zulu carries
// the default, so a run reading the sorted-first key's config would spawn
// with pipes here, and the flag-less run below would say pipe.
func TestPTYConfigDefaultFollowsNamedFirstKey(t *testing.T) {
	bin, tty := ptyBinaries(t)
	state := t.TempDir()
	linkTestKeys(t, bin, state, "pty-zulu", "pty-alpha")
	if out, _, code := ptyRunIncoda(t, ownerEnv(state, "alice"), "config", "pty-zulu", "--pty"); code != 0 {
		t.Fatalf("config --pty exited %d:\n%s", code, out)
	}
	out, errb, code := ptyRunIncoda(t, ownerEnv(state, "alice"),
		"run", "--queue", "pty-zulu,pty-alpha", "--owner", "alice", "--reason", "test", "--",
		tty, "tty")
	if code != 0 {
		t.Fatalf("run exited %d:\n%s", code, errb)
	}
	if !strings.Contains(out, "stdout=tty") {
		t.Fatalf("config on the named-first key did not route the run through a pty:\n%q", out)
	}
	// An explicit --pty=false beats the config default.
	out, errb, code = ptyRunIncoda(t, ownerEnv(state, "alice"),
		"run", "--queue", "pty-zulu", "--owner", "alice", "--reason", "test", "--pty=false", "--",
		tty, "tty")
	if code != 0 {
		t.Fatalf("run --pty=false exited %d:\n%s", code, errb)
	}
	if !strings.Contains(out, "stdout=pipe") {
		t.Fatalf("--pty=false did not beat the config default:\n%q", out)
	}
}

// TestPTYMultiKeyRunTeedPerHeldLane: a run holding two lanes tees to one job
// file per lane, so an attach through either key finds the same bytes.
func TestPTYMultiKeyRunTeedPerHeldLane(t *testing.T) {
	bin, tty := ptyBinaries(t)
	state := t.TempDir()
	linkTestKeys(t, bin, state, "pty-ma", "pty-mb")
	payload := "multikey-payload\r\n"
	out, errb, code := ptyRunIncoda(t, ownerEnv(state, "alice"),
		"run", "--queue", "pty-ma,pty-mb", "--owner", "alice", "--reason", "test", "--pty", "--",
		tty, "write", "hex:"+hex.EncodeToString([]byte(payload)))
	if code != 0 {
		t.Fatalf("run exited %d:\n%s", code, errb)
	}
	if !strings.Contains(out, "multikey-payload") {
		t.Fatalf("stdout lost the payload:\n%q", out)
	}
	for _, key := range []string{"pty-ma", "pty-mb"} {
		files, err := filepath.Glob(filepath.Join(state, "lanes", key, "jobs", "*.out"))
		if err != nil || len(files) != 1 {
			t.Fatalf("lane %s: want exactly one job file, got %v (%v)", key, files, err)
		}
		b, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "multikey-payload") {
			t.Fatalf("lane %s job file lost the payload:\n%q", key, b)
		}
	}
}

// TestPTYNestedPassThroughWritesNoJobFile: a nested run riding its parent's
// ticket has no ticket of its own, so it writes no job file; the bytes reach
// its stdout (and, through the parent's pty, the parent's capture) only. If
// the pass-through path ever grew its own tee, every wrapped recipe would
// leave a second file behind per nesting level.
func TestPTYNestedPassThroughWritesNoJobFile(t *testing.T) {
	bin, tty := ptyBinaries(t)
	state := t.TempDir()
	linkTestKeys(t, bin, state, "pty-nest")
	// The inner run rides the parent's ticket with pty mode ON (it reads
	// the same lane config), because a pass-through that is not in pty
	// mode never reaches the pty tee at all and guards nothing.
	if out, _, code := ptyRunIncoda(t, ownerEnv(state, "alice"), "config", "pty-nest", "--pty"); code != 0 {
		t.Fatalf("config --pty exited %d:\n%s", code, out)
	}
	payload := "nested-payload\r\n"
	outer, _, code := ptyRunIncoda(t, ownerEnv(state, "alice"),
		"run", "--queue", "pty-nest", "--owner", "alice", "--reason", "test", "--pty", "--",
		"cmd", "/c", bin, "run", "--queue", "pty-nest", "--owner", "alice", "--reason", "nested", "--",
		tty, "write", "hex:"+hex.EncodeToString([]byte(payload)))
	if code != 0 {
		t.Fatalf("outer run exited %d:\n%s", code, outer)
	}
	files, err := filepath.Glob(filepath.Join(state, "lanes", "pty-nest", "jobs", "*.out"))
	if err != nil || len(files) != 1 {
		t.Fatalf("want exactly the outer run's one job file, got %v (%v)", files, err)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "nested-payload") {
		t.Fatalf("the outer capture lost the nested chain's bytes:\n%q", b)
	}
}

// TestPTYAttachGateStreamKillCleanup is the whole attach story in one lane
// hold: the ticket says pty, the wrong owner is refused by name, the right
// owner sees the stream live, a kill through the lane takes the job down,
// and no conhost survives the teardown.
func TestPTYAttachGateStreamKillCleanup(t *testing.T) {
	bin, tty := ptyBinaries(t)
	state := t.TempDir()
	linkTestKeys(t, bin, state, "pty-live")
	env := ownerEnv(state, "alice")
	run := exec.Command(bin, "run", "--queue", "pty-live", "--owner", "alice", "--reason", "test",
		"--pty", "--wait", "90s", "--", tty, "spin", "m1", "200", "180")
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

	// The right owner attaches and sees the stream LIVE, not a replay: pin
	// the highest marker index the file holds right now, and require the
	// attach stream to move strictly past it. A from-start replay contains
	// those same indices, so only following the file clears this.
	preAttach := -1
	if files, _ := filepath.Glob(filepath.Join(state, "lanes", "pty-live", "jobs", "*.out")); len(files) == 1 {
		if b, rerr := os.ReadFile(files[0]); rerr == nil {
			for _, m := range regexp.MustCompile(`\bm1 (\d+)`).FindAllStringSubmatch(string(b), -1) {
				if i, cerr := strconv.Atoi(m[1]); cerr == nil && i > preAttach {
					preAttach = i
				}
			}
		}
	}
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
	// Two gates, because one is not enough. The spinner writes a marker
	// every 200ms, so an attach that drains the file once at startup and
	// exits still delivers indices past the pre-attach snapshot: fast
	// replay looks live against a static bar. The second gate raises the
	// bar AFTER attach has run for seconds: the file's max marker at that
	// point did not exist when a replay drained, so only a stream that is
	// still following the file can reach it.
	var seenMu sync.Mutex
	seen := -1         // highest index the attach stream has delivered
	raisedTarget := -1 // set by the main test after the first gate
	firstLive := make(chan struct{}, 1)
	reachedRaised := make(chan struct{}, 1)
	go func() {
		buf := make([]byte, 4096)
		var acc bytes.Buffer
		re := regexp.MustCompile(`\bm1 (\d+)`)
		for {
			n, rerr := pipe.Read(buf)
			if n > 0 {
				acc.Write(buf[:n])
				for _, m := range re.FindAllStringSubmatch(acc.String(), -1) {
					if i, cerr := strconv.Atoi(m[1]); cerr == nil {
						seenMu.Lock()
						if i > seen {
							seen = i
						}
						cur, tgt := seen, raisedTarget
						seenMu.Unlock()
						if cur > preAttach {
							select {
							case firstLive <- struct{}{}:
							default:
							}
						}
						if tgt > preAttach && cur >= tgt {
							reachedRaised <- struct{}{}
							return
						}
					}
				}
			}
			if rerr != nil {
				return
			}
		}
	}()
	select {
	case <-firstLive:
	case <-time.After(30 * time.Second):
		t.Fatalf("attach never moved past the pre-attach markers (max %d); it is a replay, not a live stream (stderr: %s)", preAttach, attachErr)
	}
	time.Sleep(3 * time.Second)
	fileMax := -1
	if files, _ := filepath.Glob(filepath.Join(state, "lanes", "pty-live", "jobs", "*.out")); len(files) == 1 {
		if b, rerr := os.ReadFile(files[0]); rerr == nil {
			for _, m := range regexp.MustCompile(`\bm1 (\d+)`).FindAllStringSubmatch(string(b), -1) {
				if i, cerr := strconv.Atoi(m[1]); cerr == nil && i > fileMax {
					fileMax = i
				}
			}
		}
	}
	if fileMax <= preAttach {
		t.Fatalf("spinner did not advance past the pre-attach max %d in 3s (file max %d); the liveness bar cannot be raised", preAttach, fileMax)
	}
	seenMu.Lock()
	raisedTarget = fileMax
	seenMu.Unlock()
	select {
	case <-reachedRaised:
	case <-time.After(30 * time.Second):
		t.Fatalf("attach stopped following: the file reached m1 %d while attach was running and the stream never delivered it (pre-attach max %d); it replayed and quit (stderr: %s)", fileMax, preAttach, attachErr)
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
	bin, tty := ptyBinaries(t)
	state := t.TempDir()
	linkTestKeys(t, bin, state, "pty-force")
	run := exec.Command(bin, "run", "--queue", "pty-force", "--owner", "alice", "--reason", "test",
		"--pty", "--wait", "90s", "--", tty, "spin", "f1", "200", "180")
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
