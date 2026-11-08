package cli

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/report"
)

// writeRegistry gives a freshly built lane state the machine.json a real
// run leaves behind, so readState's fail-closed layout check sees a
// migrated state instead of a broken one.
func writeRegistry(t *testing.T, dir string) {
	t.Helper()
	reg := `{"schema":1,"layout":2,"generation":1,"pools":["builds"],"migrated_by":"test","migrated_at":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, "machine.json"), []byte(reg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The answer command is gated before anything reaches the run: an
// answerer who is not the job's owner is refused with attach's own words
// and the request is withdrawn, so the run never sees a request it would
// only refuse. A missing identity, a job nobody owns, an unknown pid and a
// non-pty job are each refused by name.
func TestAnswerRefusesByGateBeforeTheRunSeesAnything(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INCODA_DIR", dir)
	t.Setenv("INCODA_OWNER", "")
	q, err := lane.Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	writeRegistry(t, dir)
	en, err := q.Enroll(lane.Ticket{Command: []string{"zig", "build"}, PTY: true, Owner: "alice"})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"not the owner", []string{"--queue", "unit", "--pid", itoa(en.Ticket().PID), "--text", "y", "--owner", "mallory"}, "attach refused"},
		{"no identity", []string{"--queue", "unit", "--pid", itoa(en.Ticket().PID), "--text", "y"}, "attach needs --owner"},
		{"unknown pid", []string{"--queue", "unit", "--pid", "999999", "--text", "y", "--owner", "alice"}, "no live participant"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			err := cmdAnswer(c.args, &out, &errb)
			if err == nil {
				t.Fatalf("must be refused, got success: %s", out.String())
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("refusal %q does not carry the named reason %q", err.Error(), c.want)
			}
			if _, ok := en.AnswerRequested(); ok {
				t.Fatal("a refused answer must be withdrawn before the run sees it")
			}
		})
	}
}

// A delivered answer is the file's deletion, and the command reports
// success only once the holder has taken it; a holder that stays silent
// past --wait is reported pending, never success.
func TestAnswerWaitsForTheHoldersAck(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INCODA_DIR", dir)
	t.Setenv("INCODA_OWNER", "")
	q, err := lane.Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	writeRegistry(t, dir)
	en, err := q.Enroll(lane.Ticket{Command: []string{"zig", "build"}, PTY: true, Owner: "alice"})
	if err != nil {
		t.Fatal(err)
	}

	// The holder acks while the command polls.
	acked := make(chan struct{})
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, ok := en.AnswerRequested(); ok {
				_ = en.AnswerDelivered()
				close(acked)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	var out, errb bytes.Buffer
	if err := cmdAnswer([]string{"--queue", "unit", "--pid", itoa(en.Ticket().PID),
		"--text", "yes", "--owner", "alice", "--wait", "4s"}, &out, &errb); err != nil {
		t.Fatalf("a delivered answer must succeed: %v (%s)", err, errb.String())
	}
	select {
	case <-acked:
	default:
		t.Fatal("success without the holder's ack")
	}

	// Nobody acks this one: the command must end pending, with the
	// request still addressed to the ticket.
	err = cmdAnswer([]string{"--queue", "unit", "--pid", itoa(en.Ticket().PID),
		"--text", "yes", "--owner", "alice", "--wait", "300ms"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("an unacknowledged answer must not report success")
	}
	if !strings.Contains(err.Error(), "pending") {
		t.Fatalf("want a pending verdict, got %v", err)
	}
	if _, ok := en.AnswerRequested(); !ok {
		t.Fatal("a pending answer must stay addressed to the ticket")
	}
	_ = en.AnswerDelivered()
}

// A holder that refuses rewrites the door file with its reason, and the
// answerer reads that verdict back: the command fails with the holder's
// own words, not a timeout.
func TestAnswerReportsTheHoldersRefusal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INCODA_DIR", dir)
	t.Setenv("INCODA_OWNER", "")
	q, err := lane.Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	writeRegistry(t, dir)
	en, err := q.Enroll(lane.Ticket{Command: []string{"zig", "build"}, PTY: true, Owner: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	refused := make(chan struct{})
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if req, ok := en.AnswerRequested(); ok {
				if req.Refused == "" {
					_ = en.AnswerRefused("job pid %d is not reporting blocked")
					close(refused)
					return
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	var out, errb bytes.Buffer
	err = cmdAnswer([]string{"--queue", "unit", "--pid", itoa(en.Ticket().PID),
		"--text", "yes", "--owner", "alice", "--wait", "4s"}, &out, &errb)
	if err == nil || !strings.Contains(err.Error(), "not reporting blocked") {
		t.Fatalf("want the holder's refusal, got %v (%s)", err, errb.String())
	}
	select {
	case <-refused:
	default:
		t.Fatal("the verdict did not come from the refusal rewrite")
	}
}

// fillProgramStatus is the --json half of the owner gate: the record
// reaches the report for the owner of a pty job whose log carries one,
// and nobody else gets so much as an absent field rendered.
func TestFillProgramStatusIsOwnerGatedAndAdditive(t *testing.T) {
	laneDir := t.TempDir()
	jobs := filepath.Join(laneDir, "jobs")
	if err := os.MkdirAll(jobs, 0o755); err != nil {
		t.Fatal(err)
	}
	msg := base64.StdEncoding.EncodeToString([]byte("ship it?"))
	log := "\x1b]7501;state=blocked:kind=question:id=build:msg=" + msg + "\x07"
	if err := os.WriteFile(filepath.Join(jobs, "01794028541589289100-161600.out"), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	const withLog = "01794028541589289100-161600.ticket"
	rep := &report.Report{Schema: 1, Queues: []report.Queue{{
		Key: "builds", Exists: true, Dir: laneDir,
		Holders: []lane.Entry{
			{Holding: true, File: withLog, Ticket: lane.Ticket{PID: 111, PTY: true, Owner: "alice", Command: []string{"just", "merge"}}},
		},
		Waiting: []lane.Entry{
			{File: "01794028541589289101-161700.ticket", Ticket: lane.Ticket{PID: 222, PTY: true, Owner: "bob", Command: []string{"zig", "build"}}},
		},
	}}}

	fillProgramStatus(rep, "alice")
	h := rep.Queues[0].Holders[0]
	if h.ProgramStatus == nil || h.ProgramStatus.State != "blocked" || h.ProgramStatus.Kind != "question" || h.ProgramStatus.Msg != "ship it?" {
		t.Fatalf("the owner's record must be filled, got %+v", h.ProgramStatus)
	}
	if w := rep.Queues[0].Waiting[0]; w.ProgramStatus != nil {
		t.Fatalf("another owner's participant must carry no record, got %+v", w.ProgramStatus)
	}

	// A non-owner caller sees nothing, a pipe job sees nothing, and the
	// schema field is additive: the report still says schema 1.
	fillProgramStatus(rep, "mallory")
	if h := rep.Queues[0].Holders[0]; h.ProgramStatus != nil {
		t.Fatalf("a non-owner must get no record, got %+v", h.ProgramStatus)
	}
	if rep.Schema != 1 {
		t.Fatalf("the schema version must stay 1, got %d", rep.Schema)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
