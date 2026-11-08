//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// childBlocked is the job under test: it reports itself blocked on the
// user over OSC 7501 (kind=question, a message only its owner may read),
// waits for a line of input, and prints exactly what it read between
// markers before reporting itself done.
const childBlocked = `
import base64, sys, time
def report(body):
    sys.stdout.write(body); sys.stdout.flush()
report("\x1b]7501;state=blocked:kind=question:id=e2e/ask:msg=" + base64.b64encode(b"proceed with the destructive step?").decode() + "\x07")
time.sleep(0.2)
line = sys.stdin.readline()
report("\r\nREAD<" + repr(line) + ">END\r\n")
report("\x1b]7501;state=done:id=e2e/ask\x07")
`

type statusJSONShape struct {
	Queues []struct {
		Key     string `json:"key"`
		Holders []struct {
			File string `json:"file"`
			Tick struct {
				PID int `json:"pid"`
			} `json:"ticket"`
			ProgramStatus *struct {
				State string `json:"state"`
				Kind  string `json:"kind"`
				Msg   string `json:"msg"`
				ID    string `json:"id"`
			} `json:"program_status"`
		} `json:"holders"`
	} `json:"queues"`
}

// The whole answer loop, end to end on a real lane: the job blocks and
// says so over OSC 7501; status --json carries the record to the job's
// owner (and to nobody else); incoda answer types the reply into the job's
// console without echoing it into the job's log; the job reads the reply
// and finishes, and its record flips to done. The one occurrence of the
// answer text in the job's log is the job's own READ print - the console
// echo would be the second.
func TestAnswerClosesTheLoopOnABlockedJob(t *testing.T) {
	py, err := exec.LookPath("python")
	if err != nil {
		t.Skip("no python on PATH for the answer fixture")
	}
	bin, _ := ptyBinaries(t)
	state := t.TempDir()
	linkTestKeys(t, bin, state, "pty-ans")
	script := filepath.Join(t.TempDir(), "blocked.py")
	if err := os.WriteFile(script, []byte(childBlocked), 0o600); err != nil {
		t.Fatal(err)
	}

	env := ownerEnv(state, "alice")
	run := exec.Command(bin, "run", "--queue", "pty-ans", "--owner", "alice",
		"--reason", "test", "--pty", "--", py, script)
	run.Env = env
	var runOut, runErr bytes.Buffer
	run.Stdout = &runOut
	run.Stderr = &runErr
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = run.Process.Wait() }()

	// The record reaches the owner through status --json, and never
	// reaches anyone else.
	pid := 0
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		out, errb, code := ptyRunIncoda(t, env, "status", "--queue", "pty-ans", "--json", "--owner", "alice")
		if code != 0 {
			t.Fatalf("status --json exited %d:\n%s\n%s", code, out, errb)
		}
		var rep statusJSONShape
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatalf("status --json is not the shape it was: %v\n%s", err, out)
		}
		for _, q := range rep.Queues {
			for _, h := range q.Holders {
				if h.ProgramStatus != nil && h.ProgramStatus.State == "blocked" {
					pid = h.Tick.PID
					if h.ProgramStatus.Kind != "question" || h.ProgramStatus.Msg != "proceed with the destructive step?" || h.ProgramStatus.ID != "e2e/ask" {
						t.Fatalf("the record lost its fields: %+v", h.ProgramStatus)
					}
				}
			}
		}
		if pid != 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if pid == 0 {
		run.Process.Kill()
		t.Fatalf("the blocked record never reached status --json; run output so far:\n%s\n%s", runOut.String(), runErr.String())
	}

	// A stranger's answer is refused with the owner gate's own words and
	// never reaches the job.
	_, errb, code := ptyRunIncoda(t, ownerEnv(state, "mallory"), "answer",
		"--queue", "pty-ans", "--pid", itoa(pid), "--text", "no", "--wait", "2s")
	if code == 0 || !strings.Contains(errb, "attach refused") {
		t.Fatalf("a stranger's answer must be refused by name, got exit %d:\n%s", code, errb)
	}

	// The owner's answer is typed into the job, the job reads it, and the
	// run finishes clean.
	out, errb, code := ptyRunIncoda(t, env, "answer",
		"--queue", "pty-ans", "--pid", itoa(pid), "--text", "yes", "--owner", "alice", "--wait", "30s")
	if code != 0 {
		t.Fatalf("answer exited %d:\n%s\n%s\nrun so far:\n%s\n%s", code, out, errb, runOut.String(), runErr.String())
	}
	done := make(chan error)
	go func() { done <- run.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the run failed after its answer: %v\n%s\n%s", err, runOut.String(), runErr.String())
		}
	case <-time.After(60 * time.Second):
		run.Process.Kill()
		t.Fatal("the job never finished after the answer")
	}
	// The cooked read hands the job \n for the completing CR.
	if !strings.Contains(runOut.String(), "READ<'yes\\n'>END") {
		t.Fatalf("the job did not read the answer text with its CR:\n%q", runOut.String())
	}

	// The job's log carries the job's own READ print of the answer and no
	// console echo of it.
	files, err := filepath.Glob(filepath.Join(state, "lanes", "pty-ans", "jobs", "*.out"))
	if err != nil || len(files) != 1 {
		t.Fatalf("want one job file, got %v (%v)", files, err)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(b), "yes"); got != 1 {
		t.Fatalf("the answer echoed into the job log (%d occurrences):\n%q", got, b)
	}
	if !strings.Contains(string(b), `state=done`) {
		t.Fatalf("the job's final record never landed in the log:\n%q", b)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

// The run's own gates, not the CLI's, are the ones that make the answer
// channel safe to leave open: a job that never reported blocked cannot be
// typed into, and a job with no console at all is refused by name rather
// than left to swallow the request silently at release.
func TestAnswerRefusedWhenTheJobIsNotBlocked(t *testing.T) {
	py, err := exec.LookPath("python")
	if err != nil {
		t.Skip("no python on PATH for the answer fixture")
	}
	bin, _ := ptyBinaries(t)
	state := t.TempDir()
	linkTestKeys(t, bin, state, "pty-ans2")
	script := filepath.Join(t.TempDir(), "busy.py")
	src := "\nimport sys, time\nsys.stdout.write(\"\\x1b]7501;state=working:id=e2e/busy:progress=10\\x07\"); sys.stdout.flush()\ntime.sleep(30)\n"
	if err := os.WriteFile(script, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	env := ownerEnv(state, "alice")
	run := exec.Command(bin, "run", "--queue", "pty-ans2", "--owner", "alice",
		"--reason", "test", "--pty", "--", py, script)
	run.Env = env
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run.Process.Kill(); _, _ = run.Process.Wait() }()
	pid := waitForHolder(t, env, "pty-ans2")
	_, errb, code := ptyRunIncoda(t, env, "answer", "--queue", "pty-ans2",
		"--pid", itoa(pid), "--text", "yes", "--owner", "alice", "--wait", "15s")
	if code == 0 || !strings.Contains(errb, "not reporting blocked") {
		t.Fatalf("a working job must be refused by name, got exit %d:\n%s", code, errb)
	}
}

func TestAnswerRefusedForNonPtyJobs(t *testing.T) {
	py, err := exec.LookPath("python")
	if err != nil {
		t.Skip("no python on PATH for the answer fixture")
	}
	bin, _ := ptyBinaries(t)
	state := t.TempDir()
	linkTestKeys(t, bin, state, "pty-ans3")
	script := filepath.Join(t.TempDir(), "sleep.py")
	if err := os.WriteFile(script, []byte("import time\ntime.sleep(30)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := ownerEnv(state, "alice")
	run := exec.Command(bin, "run", "--queue", "pty-ans3", "--owner", "alice",
		"--reason", "test", "--", py, script)
	run.Env = env
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run.Process.Kill(); _, _ = run.Process.Wait() }()
	pid := waitForHolder(t, env, "pty-ans3")
	_, errb, code := ptyRunIncoda(t, env, "answer", "--queue", "pty-ans3",
		"--pid", itoa(pid), "--text", "yes", "--owner", "alice", "--wait", "15s")
	if code == 0 || !strings.Contains(errb, "not a pty job") {
		t.Fatalf("a pipe job must be refused by name, got exit %d:\n%s", code, errb)
	}
}

// waitForHolder polls status --json for the queue's holder pid.
func waitForHolder(t *testing.T, env []string, key string) int {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		out, _, _ := ptyRunIncoda(t, env, "status", "--queue", key, "--json", "--owner", "alice")
		var rep statusJSONShape
		if json.Unmarshal([]byte(out), &rep) == nil {
			for _, q := range rep.Queues {
				for _, h := range q.Holders {
					return h.Tick.PID
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("the job never appeared")
	return 0
}

// A request planted straight onto the door file - no CLI, no gates - is
// answered by the run itself: a pipe run refuses it by name (it has no
// console to type into), and a request still pending when a run is
// killed is answered with the run-ended refusal, never swept into
// silence. The run-side gates are the ones that make the channel safe to
// leave open; these plant the file the way a foreign process would.
func TestPlantedAnswerOnPipeRunIsRefused(t *testing.T) {
	py, err := exec.LookPath("python")
	if err != nil {
		t.Skip("no python on PATH for the answer fixture")
	}
	bin, _ := ptyBinaries(t)
	state := t.TempDir()
	linkTestKeys(t, bin, state, "pty-ans4")
	script := filepath.Join(t.TempDir(), "sleep4.py")
	if err := os.WriteFile(script, []byte("import time\ntime.sleep(30)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := ownerEnv(state, "alice")
	run := exec.Command(bin, "run", "--queue", "pty-ans4", "--owner", "alice",
		"--reason", "test", "--", py, script)
	run.Env = env
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run.Process.Kill(); _, _ = run.Process.Wait() }()
	file := waitForHolderFile(t, env, "pty-ans4")
	answerPath := filepath.Join(state, "lanes", "pty-ans4", file+".answer")
	plant := `{"id":"333-1","by":"alice","text":"yes\r"}`
	if err := os.WriteFile(answerPath, []byte(plant), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(answerPath)
		if err == nil && strings.Contains(string(b), `"verdict":"refused"`) && strings.Contains(string(b), "not a pty job") {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	b, _ := os.ReadFile(answerPath)
	t.Fatalf("the pipe run never refused the planted answer; door: %q", b)
}

func TestPendingAnswerRefusedAtRunEnd(t *testing.T) {
	py, err := exec.LookPath("python")
	if err != nil {
		t.Skip("no python on PATH for the answer fixture")
	}
	bin, _ := ptyBinaries(t)
	state := t.TempDir()
	linkTestKeys(t, bin, state, "pty-ans5")
	script := filepath.Join(t.TempDir(), "blocked5.py")
	src := "\nimport base64, sys, time\nsys.stdout.write(\"\\x1b]7501;state=blocked:kind=question:id=e2e/ask:msg=\" + base64.b64encode(b\"q?\").decode() + \"\\x07\"); sys.stdout.flush()\ntime.sleep(30)\n"
	if err := os.WriteFile(script, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	env := ownerEnv(state, "alice")
	run := exec.Command(bin, "run", "--queue", "pty-ans5", "--owner", "alice",
		"--reason", "test", "--pty", "--", py, script)
	run.Env = env
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run.Process.Kill(); _, _ = run.Process.Wait() }()
	file := waitForHolderFile(t, env, "pty-ans5")
	answerPath := filepath.Join(state, "lanes", "pty-ans5", file+".answer")
	plant := `{"id":"333-2","by":"alice","text":"yes\r"}`
	if err := os.WriteFile(answerPath, []byte(plant), 0o644); err != nil {
		t.Fatal(err)
	}
	// Kill the run with the request pending: the run must answer it.
	_, errb, code := ptyRunIncoda(t, env, "kill", "--queue", "pty-ans5",
		"--pid", holderPid(t, env, "pty-ans5"), "--reason", "test end", "--wait", "60s")
	if code != 0 {
		t.Fatalf("kill exited %d: %s", code, errb)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		tail, err := readLaneLog(t, filepath.Join(state, "lanes", "pty-ans5", "lane.log"))
		if err == nil && strings.Contains(tail, " id=333-2 ") && strings.Contains(tail, "event=answer-refused") {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	tail, _ := readLaneLog(t, filepath.Join(state, "lanes", "pty-ans5", "lane.log"))
	t.Fatalf("the run never refused the pending answer at its end; log tail:\n%s", tail)
}

func waitForHolderFile(t *testing.T, env []string, key string) string {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		out, _, _ := ptyRunIncoda(t, env, "status", "--queue", key, "--json", "--owner", "alice")
		var rep statusJSONShape
		if json.Unmarshal([]byte(out), &rep) == nil {
			for _, q := range rep.Queues {
				for _, h := range q.Holders {
					if h.File != "" {
						return h.File
					}
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("no holder file ever appeared")
	return ""
}

func holderPid(t *testing.T, env []string, key string) string {
	t.Helper()
	out, _, _ := ptyRunIncoda(t, env, "status", "--queue", key, "--json", "--owner", "alice")
	var rep statusJSONShape
	if json.Unmarshal([]byte(out), &rep) != nil {
		t.Fatal("status parse")
	}
	for _, q := range rep.Queues {
		for _, h := range q.Holders {
			return itoa(h.Tick.PID)
		}
	}
	t.Fatal("no holder")
	return ""
}

func readLaneLog(t *testing.T, path string) (string, error) {
	t.Helper()
	b, err := os.ReadFile(path)
	return string(b), err
}
