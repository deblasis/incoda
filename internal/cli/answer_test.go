package cli

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/child"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/report"
	"github.com/deblasis/incoda/internal/textsafe"
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

func answerFixture(t *testing.T) (*lane.Queue, *lane.Enrollment, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("INCODA_DIR", dir)
	t.Setenv("INCODA_OWNER", "")
	q, err := lane.Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	writeRegistry(t, dir)
	en, err := q.Enroll(lane.Ticket{Command: []string{"zig", "build"}, PTY: true, Owner: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	return q, en, dir
}

// The answer command is gated before anything reaches the run: an
// answerer who is not the job's owner is refused with attach's own words
// and the request is withdrawn, so the run never sees a request it would
// only refuse. A missing identity, an unknown pid and a job with no
// console are each refused by name.
func TestAnswerRefusesByGateBeforeTheRunSeesAnything(t *testing.T) {
	_, en, _ := answerFixture(t)
	pid := itoa(en.Ticket().PID)

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"not the owner", []string{"--queue", "unit", "--pid", pid, "--text", "y", "--owner", "mallory"}, "attach refused"},
		{"no identity", []string{"--queue", "unit", "--pid", pid, "--text", "y"}, "attach needs --owner"},
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

// A job with no pseudo-console can never receive typed input, and the
// command says so by name instead of letting the request ride to a run
// that would only delete it on release.
func TestAnswerRefusesNonPtyJobsByName(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INCODA_DIR", dir)
	t.Setenv("INCODA_OWNER", "")
	q, err := lane.Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	writeRegistry(t, dir)
	en, err := q.Enroll(lane.Ticket{Command: []string{"zig", "build"}, Owner: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	err = cmdAnswer([]string{"--queue", "unit", "--pid", itoa(en.Ticket().PID),
		"--text", "y", "--owner", "alice", "--wait", "300ms"}, &out, &errb)
	if err == nil || !strings.Contains(err.Error(), "not a pty job") {
		t.Fatalf("want the named pty refusal, got %v (%s)", err, errb.String())
	}
	if _, ok := en.AnswerRequested(); ok {
		t.Fatal("the refused request must be withdrawn")
	}
}

// The completing CR is the CLI's default and --raw is its opt-out; the
// request the holder receives is the contract.
func TestAnswerTextCarriesTheCompletingCR(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"cr by default", nil, "yes\r"},
		{"raw sends exactly", []string{"--raw"}, "yes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, en, _ := answerFixture(t)
			acked := make(chan lane.AnswerRequest, 1)
			go func() {
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					if req, ok := en.AnswerRequested(); ok {
						acked <- req
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
			}()
			args := append([]string{"--queue", "unit", "--pid", itoa(en.Ticket().PID),
				"--text", "yes", "--owner", "alice", "--wait", "4s"}, c.args...)
			var out, errb bytes.Buffer
			// The command ends pending - the verdict never comes - but the
			// request on disk is what this test reads.
			_ = cmdAnswer(args, &out, &errb)
			select {
			case req := <-acked:
				if req.Text != c.want {
					t.Fatalf("request text = %q, want %q", req.Text, c.want)
				}
			default:
				t.Fatal("no request was ever written")
			}
			_ = en.AnswerDelivered(req2id(t, en))
		})
	}
}

func req2id(t *testing.T, en *lane.Enrollment) string {
	t.Helper()
	req, ok := en.AnswerRequested()
	if !ok {
		t.Fatal("no request to clean up")
	}
	return req.ID
}

// A delivered verdict ends the command with success, but only the one
// carrying THIS request's id; a holder that stays silent past --wait is
// reported pending, never success.
func TestAnswerWaitsForTheHoldersAck(t *testing.T) {
	_, en, _ := answerFixture(t)
	acked := make(chan struct{})
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if req, ok := en.AnswerRequested(); ok {
				_ = en.AnswerDelivered(req.ID)
				close(acked)
				return
			}
			time.Sleep(10 * time.Millisecond)
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

	// Nobody acks this one: the command must end pending.
	err := cmdAnswer([]string{"--queue", "unit", "--pid", itoa(en.Ticket().PID),
		"--text", "yes", "--owner", "alice", "--wait", "300ms"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("an unacknowledged answer must not report success")
	}
	if !strings.Contains(err.Error(), "pending") {
		t.Fatalf("want a pending verdict, got %v", err)
	}
	if req, ok := en.AnswerRequested(); !ok || req.Verdict != "" {
		t.Fatal("a pending answer must stay addressed to the ticket, un-answered")
	}
	_ = en.AnswerDelivered(req2id(t, en))
}

// A refusal comes back with the holder's own words.
func TestAnswerReportsTheHoldersRefusal(t *testing.T) {
	_, en, _ := answerFixture(t)
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if req, ok := en.AnswerRequested(); ok {
				_ = en.AnswerRefused(req.ID, "job pid 1 is not reporting blocked")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	var out, errb bytes.Buffer
	err := cmdAnswer([]string{"--queue", "unit", "--pid", itoa(en.Ticket().PID),
		"--text", "yes", "--owner", "alice", "--wait", "4s"}, &out, &errb)
	if err == nil || !strings.Contains(err.Error(), "not reporting blocked") {
		t.Fatalf("want the holder's refusal, got %v (%s)", err, errb.String())
	}
}

// A request superseded by a newer answer is reported as superseded, never
// as delivered: the newer request owns the door file now.
func TestAnswerReportsSupersededRequests(t *testing.T) {
	q, en, _ := answerFixture(t)
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, ok := en.AnswerRequested(); ok {
				// A second answerer replaces the request mid-flight.
				if _, err := q.RequestAnswer(en.Ticket().PID, lane.AnswerRequest{ID: "later", By: "alice", Text: "mine"}); err == nil {
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	var out, errb bytes.Buffer
	err := cmdAnswer([]string{"--queue", "unit", "--pid", itoa(en.Ticket().PID),
		"--text", "yes", "--owner", "alice", "--wait", "1s"}, &out, &errb)
	if err == nil || !strings.Contains(err.Error(), "superseded") {
		t.Fatalf("want the superseded verdict, got %v (%s)", err, errb.String())
	}
	_ = en.AnswerDelivered("later")
}

// A closed console is a refusal, not a delivered-with-audit: whether the
// dying job read the answer cannot be known, and exit 0 must never be
// claimed for an unverifiable delivery.
func TestDeliverAnswerRefusesOnConsoleClose(t *testing.T) {
	_, en, dir := answerFixture(t)
	laneDir := filepath.Join(dir, "lanes", "unit")
	jobs := filepath.Join(laneDir, "jobs")
	if err := os.MkdirAll(jobs, 0o755); err != nil {
		t.Fatal(err)
	}
	msg := base64.StdEncoding.EncodeToString([]byte("proceed?"))
	log := "\x1b]7501;state=blocked:kind=question:id=ask:msg=" + msg + "\x07"
	if err := os.WriteFile(filepath.Join(jobs, stripTicket(en.Name())+".out"), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	q := laneQueue(t, dir)
	pt := &lanePart{key: "unit", q: q, en: en}
	stub := &stubDoor{err: child.ErrConsoleClosedAnswer}
	req := lane.AnswerRequest{ID: "cc", By: "alice", Text: "yes\r"}
	if _, err := q.RequestAnswer(en.Ticket().PID, req); err != nil {
		t.Fatal(err)
	}
	if err := deliverAnswer(pt, req, stub); !errors.Is(err, child.ErrConsoleClosedAnswer) {
		t.Fatalf("a closed console must refuse, got %v", err)
	}
	_ = en.AnswerRefused("cc", child.ErrConsoleClosedAnswer.Error())
}

// The log is the durable verdict: a delivered answer whose verdict file
// was renamed over by a newer request must still be reported delivered -
// the file saying "foreign id" is superseded only when the log holds no
// verdict of ours.
func TestDeliveredVerdictSurvivesSuperseding(t *testing.T) {
	q, en, _ := answerFixture(t)
	pid := en.Ticket().PID
	if _, err := q.RequestAnswer(pid, lane.AnswerRequest{ID: "mine", By: "alice", Text: "yes\r"}); err != nil {
		t.Fatal(err)
	}
	if err := en.AnswerDelivered("mine"); err != nil {
		t.Fatal(err)
	}
	q.Logf("queue=unit event=answer-delivered pid=%d id=%s", pid, "mine")
	// A newer answerer renames over the verdict file before our poll.
	if _, err := q.RequestAnswer(pid, lane.AnswerRequest{ID: "later", By: "alice", Text: "no\r"}); err != nil {
		t.Fatal(err)
	}
	got, reason := pollAnswerVerdict(
		filepath.Join(q.Dir, "no-such-ticket"+lane.AnswerExt),
		lane.LogPath(q.Dir), "mine", time.Now().Add(2*time.Second))
	if got != "delivered" || reason != "" {
		t.Fatalf("a logged delivered verdict must survive the file being renamed over: got %q/%q", got, reason)
	}
	_ = en.AnswerDelivered("later")
}

// The CLI's verdict contract, pinned where it lives: the lane log is the
// durable verdict (a swept file cannot un-deliver an answer), a vanished
// file with no verdict anywhere is pending (never delivered), and an id
// only matches as a whole field.
func TestPollAnswerVerdictContract(t *testing.T) {
	q, en, _ := answerFixture(t)
	pid := en.Ticket().PID
	logPath := lane.LogPath(q.Dir)
	answerPath := filepath.Join(q.Dir, "gone"+lane.AnswerExt)

	// Log verdict, file gone: delivered.
	q.Logf("queue=unit event=answer-delivered pid=%d id=%s", pid, "logdel")
	if v, r := pollAnswerVerdict(answerPath, logPath, "logdel", time.Now().Add(1*time.Second)); v != "delivered" || r != "" {
		t.Fatalf("log verdict: got %q/%q", v, r)
	}
	// Log refusal with a quoted reason: refused, unquoted for display.
	q.Logf(`queue=unit event=answer-refused pid=%d id=%s reason="the job: did not read"`, pid, "logref")
	v, r := pollAnswerVerdict(answerPath, logPath, "logref", time.Now().Add(1*time.Second))
	if v != "refused" || r != "the job: did not read" {
		t.Fatalf("log refusal: got %q/%q", v, r)
	}
	// No verdict anywhere: pending, never delivered.
	if v, _ := pollAnswerVerdict(answerPath, logPath, "nobody", time.Now().Add(200*time.Millisecond)); v != "" {
		t.Fatalf("absence must not be an ack, got %q", v)
	}
	// An id that is a prefix of another request's id claims nothing.
	q.Logf("queue=unit event=answer-delivered pid=%d id=%s", pid, "1-23")
	if v, _ := pollAnswerVerdict(answerPath, logPath, "1-2", time.Now().Add(200*time.Millisecond)); v != "" {
		t.Fatalf("a prefix id must not claim a verdict, got %q", v)
	}
}

// The log-line shape is a cross-package contract: the run writes
// `id=<id>` through textsafe.LogValue (unquoted for our minted ids) and
// the CLI matches it as a whole field. Pin the pair together.
func TestAnswerVerdictLogLineShape(t *testing.T) {
	q, en, _ := answerFixture(t)
	id := "17000-1234567890"
	q.Logf("queue=unit event=answer-delivered pid=%d id=%s", en.Ticket().PID, textsafe.LogValue(id))
	if !lineCarriesID("queue=unit event=answer-delivered pid=1 id="+id, id) {
		t.Fatal("a minted id must match its logged form")
	}
	tail, err := readLogTail(lane.LogPath(q.Dir), 64<<10)
	if err != nil || !lineCarriesID(tail, id) {
		t.Fatalf("the written line must satisfy the CLI's matcher: %v %q", err, tail)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func stripTicket(name string) string {
	return strings.TrimSuffix(name, ".ticket")
}

func laneQueue(t *testing.T, dir string) *lane.Queue {
	t.Helper()
	q, err := lane.OpenIn(lane.LanesDir(dir), "unit", lane.Existing)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	return q
}

type stubDoor struct{ err error }

func (s *stubDoor) Deliver(string) error { return s.err }

// fillProgramStatus is the --json half of the owner gate: the record
// reaches the report for the owner of a pty job whose log carries one,
// a pipe job and a broken payload never get one, and nobody else gets so
// much as an absent field rendered.
func TestFillProgramStatusIsOwnerGatedAndAdditive(t *testing.T) {
	laneDir := t.TempDir()
	jobs := filepath.Join(laneDir, "jobs")
	if err := os.MkdirAll(jobs, 0o755); err != nil {
		t.Fatal(err)
	}
	msg := base64.StdEncoding.EncodeToString([]byte("ship it?"))
	log := "\x1b]7501;state=blocked:kind=question:id=build:msg=" + msg + "\x07"
	for _, name := range []string{"01794028541589289100-161600", "01794028541589289102-161900", "01794028541589289103-162000", "01794028541589289101-161700"} {
		if err := os.WriteFile(filepath.Join(jobs, name+".out"), []byte(log), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const withLog = "01794028541589289100-161600.ticket"
	rep := &report.Report{Schema: 1, Queues: []report.Queue{{
		Key: "builds", Exists: true, Dir: laneDir,
		Holders: []lane.Entry{
			{Holding: true, File: withLog, Ticket: lane.Ticket{PID: 111, PTY: true, Owner: "alice", Command: []string{"just", "merge"}}},
			{Holding: true, File: "01794028541589289102-161900.ticket", Ticket: lane.Ticket{PID: 333, Owner: "alice", Command: []string{"pipe", "job"}}},
			{Holding: true, File: "01794028541589289103-162000.ticket", PayloadError: "parse: boom", Ticket: lane.Ticket{PID: 444, PTY: true, Owner: "alice"}},
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
	if pipe := rep.Queues[0].Holders[1]; pipe.ProgramStatus != nil {
		t.Fatalf("a pipe job must carry no record, got %+v", pipe.ProgramStatus)
	}
	if broken := rep.Queues[0].Holders[2]; broken.ProgramStatus != nil {
		t.Fatalf("a broken payload must carry no record, got %+v", broken.ProgramStatus)
	}
	if w := rep.Queues[0].Waiting[0]; w.ProgramStatus != nil {
		t.Fatalf("another owner's participant must carry no record, got %+v", w.ProgramStatus)
	}

	fillProgramStatus(rep, "mallory")
	if h := rep.Queues[0].Holders[0]; h.ProgramStatus != nil {
		t.Fatalf("a non-owner must get no record, got %+v", h.ProgramStatus)
	}
	if rep.Schema != 1 {
		t.Fatalf("the schema version must stay 1, got %d", rep.Schema)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
