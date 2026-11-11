package cli

import (
	"bytes"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/colorize"
	"github.com/deblasis/incoda/internal/lane"
)

// The mark is the one-glance answer to "which jobs carry a live capture":
// status paints it next to every pty participant, holder and waiter alike,
// and paints nothing next to a pipe-mode job, so the mark cannot be read as
// decoration on everything.
func TestStatusMarksPtyParticipants(t *testing.T) {
	qr := QueueReport{
		Key: "builds", Exists: true, EffectiveSlots: 2,
		Holders: []lane.Entry{
			{Holding: true, HeldSeconds: 5, File: "01794028541589289100-161600.ticket",
				Ticket: lane.Ticket{PID: 111, PTY: true, Command: []string{"zig", "build"}}},
		},
		Waiting: []lane.Entry{
			{WaitingSeconds: 3, File: "01794028541589289101-161700.ticket",
				Ticket: lane.Ticket{PID: 222, PTY: true, Command: []string{"dotnet", "test"}}},
		},
	}
	var buf bytes.Buffer
	renderQueue(&buf, colorize.Plain, qr, nil)
	out := buf.String()
	if !strings.Contains(out, "HOLDER 🖥️") {
		t.Fatalf("a pty holder must carry the mark next to it, got:\n%s", out)
	}
	if !strings.Contains(out, "1. 🖥️") {
		t.Fatalf("a pty waiter must carry the mark next to it, got:\n%s", out)
	}

	qr.Holders[0].Ticket.PTY = false
	qr.Waiting[0].Ticket.PTY = false
	buf.Reset()
	renderQueue(&buf, colorize.Plain, qr, nil)
	if strings.Contains(buf.String(), "🖥️") {
		t.Fatalf("pipe-mode participants must carry no mark, got:\n%s", buf.String())
	}
}

// A pty participant whose log carries OSC 7501 records shows the root
// record's status on its line; one with no records, and a pipe-mode job,
// stay exactly as they were.
func TestStatusShowsProgramStatusFromJobLog(t *testing.T) {
	laneDir := t.TempDir()
	jobs := filepath.Join(laneDir, "jobs")
	if err := os.MkdirAll(jobs, 0o755); err != nil {
		t.Fatal(err)
	}
	msg := base64.StdEncoding.EncodeToString([]byte("linking"))
	sub := base64.StdEncoding.EncodeToString([]byte("sub"))
	log := "noise\r\n" +
		"\x1b]7501;state=working:id=build/step:msg=" + sub + "\x07" +
		"\x1b]7501;state=working:id=build:progress=55:msg=" + msg + "\x1b\\" +
		"\x1b]7501;state=done:id=build/step\x07"
	const withLog, noLog = "01794028541589289100-161600.ticket", "01794028541589289101-161700.ticket"
	if err := os.WriteFile(filepath.Join(jobs, "01794028541589289100-161600.out"), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobs, "01794028541589289101-161700.out"), []byte("plain\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	qr := QueueReport{
		Key: "builds", Exists: true, EffectiveSlots: 2, Dir: laneDir,
		Holders: []lane.Entry{
			{Holding: true, HeldSeconds: 5, File: withLog, Ticket: lane.Ticket{PID: 111, PTY: true, Owner: "alice", Command: []string{"just", "merge"}}},
			{Holding: true, HeldSeconds: 5, File: noLog, Ticket: lane.Ticket{PID: 222, PTY: true, Owner: "alice", Command: []string{"zig", "build"}}},
		},
	}
	var buf bytes.Buffer
	renderQueue(&buf, colorize.Plain, qr, nil)
	out := buf.String()
	if !strings.Contains(out, "🖥️ 🔄 55%") {
		t.Fatalf("a working record must badge the mark with its progress, got:\n%s", out)
	}
	if !strings.Contains(out, "linking") {
		t.Fatalf("the record's msg must still end the line, got:\n%s", out)
	}
	if strings.Contains(out, "[") || strings.Contains(out, "sub") {
		t.Fatalf("no bracketed head and no nested record must appear, got:\n%s", out)
	}
	if strings.Count(out, "🔄") != 1 {
		t.Fatalf("only the participant with records gets a badge, got:\n%s", out)
	}
	// Pipe-mode job: no status even with a log file that holds records.
	qr.Holders[0].Ticket.PTY = false
	buf.Reset()
	renderQueue(&buf, colorize.Plain, qr, nil)
	if strings.Contains(buf.String(), "🔄") {
		t.Fatalf("a non-pty job shows no status, got:\n%s", buf.String())
	}
}

// plainStatus writes a job log holding one record under any amount of later
// output and renders it, one-shot and polling.
func plainStatus(t *testing.T, filler int) (string, *string) {
	t.Helper()
	laneDir := t.TempDir()
	jobs := filepath.Join(laneDir, "jobs")
	if err := os.MkdirAll(jobs, 0o755); err != nil {
		t.Fatal(err)
	}
	log := "\x1b]7501;state=working:progress=10:msg=" + base64.StdEncoding.EncodeToString([]byte("early record")) + "\x07"
	log += strings.Repeat("filler line that carries no record at all\r\n", filler/38)
	path := filepath.Join(jobs, "01794028541589289100-161600.out")
	if err := os.WriteFile(path, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	qr := QueueReport{
		Key: "builds", Exists: true, Dir: laneDir,
		Holders: []lane.Entry{{Holding: true, File: "01794028541589289100-161600.ticket",
			Ticket: lane.Ticket{PID: 111, PTY: true, Owner: "alice", Command: []string{"zig", "build"}}}},
	}
	var buf bytes.Buffer
	renderQueue(&buf, colorize.Plain, qr, nil)
	return buf.String(), &path
}

// A one-shot render reads the whole log: a record stays on the line however
// much ordinary output was written after it.
func TestPlainStatusKeepsADeeplyBuriedRecord(t *testing.T) {
	out, _ := plainStatus(t, 600<<10)
	if !strings.Contains(out, "🔄 10%") || !strings.Contains(out, "early record") {
		t.Fatalf("the record fell out of the plain line:\n%s", out)
	}
}

// A polling render follows the log: the first look reads what is there and
// the next reads only what was appended, even when the first look was huge.
func TestPlainStatusPollReadsOnlyAppendedBytes(t *testing.T) {
	_, path := plainStatus(t, 600<<10)
	laneDir := filepath.Dir(filepath.Dir(*path))
	qr := QueueReport{
		Key: "builds", Exists: true, Dir: laneDir,
		Holders: []lane.Entry{{Holding: true, File: "01794028541589289100-161600.ticket",
			Ticket: lane.Ticket{PID: 111, PTY: true, Owner: "alice", Command: []string{"zig", "build"}}}},
	}
	follow := newStatusFollowers()
	var buf bytes.Buffer
	renderQueue(&buf, colorize.Plain, qr, follow)
	if !strings.Contains(buf.String(), "🔄 10%") || !strings.Contains(buf.String(), "early record") {
		t.Fatalf("the follower lost the record:\n%s", buf.String())
	}
	ls := follow.logs[laneDir+"\x00"+"01794028541589289100-161600.ticket"]
	if ls == nil {
		t.Fatal("no follower was kept")
	}
	before := ls.BytesRead()
	f, err := os.OpenFile(*path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("another filler line\r\n")
	f.Close()
	buf.Reset()
	renderQueue(&buf, colorize.Plain, qr, follow)
	if grew := ls.BytesRead() - before; grew > 4<<10 {
		t.Fatalf("a poll with 19 appended bytes read %d bytes", grew)
	}
}

// A job-written "incoda: ..." last line is not a truncation note: the row
// keeps its status (only incoda's own three texts freeze it).
func TestForgedMarkerDoesNotFreezeTheStatus(t *testing.T) {
	laneDir := t.TempDir()
	jobs := filepath.Join(laneDir, "jobs")
	if err := os.MkdirAll(jobs, 0o755); err != nil {
		t.Fatal(err)
	}
	msg := base64.StdEncoding.EncodeToString([]byte("still going"))
	log := "\x1b]7501;state=working:msg=" + msg + "\x07\r\nincoda: this is my own line, not a truncation\r\n"
	if err := os.WriteFile(filepath.Join(jobs, "01794028541589289100-161600.out"), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	qr := QueueReport{
		Key: "builds", Exists: true, Dir: laneDir,
		Holders: []lane.Entry{{Holding: true, File: "01794028541589289100-161600.ticket",
			Ticket: lane.Ticket{PID: 111, PTY: true, Owner: "alice", Command: []string{"zig", "build"}}}},
	}
	var buf bytes.Buffer
	renderQueue(&buf, colorize.Plain, qr, nil)
	if !strings.Contains(buf.String(), "🔄") || !strings.Contains(buf.String(), "still going") {
		t.Fatalf("a forged marker froze the status row:\n%s", buf.String())
	}
}

// A done report from a live run claims nothing: the mark stays alone until
// the process is gone, because a sub-task's "done, exit 0" is not the job's
// ending while the job keeps running. Once the pid is gone the record is
// believed again.
func TestADoneReportFromALiveRunClaimsNothing(t *testing.T) {
	laneDir := t.TempDir()
	jobs := filepath.Join(laneDir, "jobs")
	if err := os.MkdirAll(jobs, 0o755); err != nil {
		t.Fatal(err)
	}
	msg := base64.StdEncoding.EncodeToString([]byte("exit 0"))
	log := "\x1b]7501;state=working:id=just/materialize-fast:app=just\x1b\\" +
		"\x1b]7501;state=done:id=just/materialize-fast:app=just:msg=" + msg + "\x07" +
		strings.Repeat("build output keeps coming\r\n", 50)
	if err := os.WriteFile(filepath.Join(jobs, "01794404609713789100-11496.out"), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestStatusSleeper$", "-test.v")
	cmd.Env = append(os.Environ(), "GO_INCODA_STATUS_SLEEPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	qr := QueueReport{
		Key: "builds", Exists: true, Dir: laneDir,
		Holders: []lane.Entry{{Holding: true, HeldSeconds: 5, File: "01794404609713789100-11496.ticket",
			Ticket: lane.Ticket{PID: cmd.Process.Pid, PTY: true, Owner: "alice", Command: []string{"pwsh", "release"}}}},
	}
	render := func() string {
		var buf bytes.Buffer
		renderQueue(&buf, colorize.Plain, qr, nil)
		return buf.String()
	}
	if out := render(); strings.Contains(out, "✅") || strings.Contains(out, "exit 0") {
		t.Fatalf("a live run's stale done must not badge or speak, got:\n%s", out)
	}
	if !strings.Contains(render(), "HOLDER 🖥️") {
		t.Fatalf("the mark stays alone on a live run, got:\n%s", render())
	}
	// The JSON report carries the same verdict: no program_status for a
	// live run's stale done, the record once the pid is gone.
	rep := &Report{Schema: 1, Queues: []QueueReport{{
		Key: "builds", Exists: true, Dir: laneDir,
		Holders: []lane.Entry{qr.Holders[0]},
	}}}
	fillProgramStatus(rep)
	if ps := rep.Queues[0].Holders[0].ProgramStatus; ps != nil {
		t.Fatalf("a live run's stale done must not reach the JSON report, got %+v", ps)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if out := render(); !strings.Contains(out, "🖥️ ✅") || !strings.Contains(out, "exit 0") {
		t.Fatalf("a dead run's done is believed, got:\n%s", out)
	}
	fillProgramStatus(rep)
	if ps := rep.Queues[0].Holders[0].ProgramStatus; ps == nil || ps.State != "done" || ps.Msg != "exit 0" {
		t.Fatalf("a dead run's done reaches the JSON report, got %+v", ps)
	}
}

// TestStatusSleeper is the helper process the test above keeps alive: it
// only runs when asked to, and then it just sleeps.
func TestStatusSleeper(t *testing.T) {
	if os.Getenv("GO_INCODA_STATUS_SLEEPER") != "1" {
		t.Skip("helper process")
	}
	time.Sleep(2 * time.Minute)
}
