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
		t.Fatalf("answer exited %d:\n%s\n%s", code, out, errb)
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
