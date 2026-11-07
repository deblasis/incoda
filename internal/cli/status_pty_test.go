package cli

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	renderQueue(&buf, colorize.Plain, qr)
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
	renderQueue(&buf, colorize.Plain, qr)
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
			{Holding: true, HeldSeconds: 5, File: withLog, Ticket: lane.Ticket{PID: 111, PTY: true, Command: []string{"just", "merge"}}},
			{Holding: true, HeldSeconds: 5, File: noLog, Ticket: lane.Ticket{PID: 222, PTY: true, Command: []string{"zig", "build"}}},
		},
	}
	var buf bytes.Buffer
	renderQueue(&buf, colorize.Plain, qr)
	out := buf.String()
	if !strings.Contains(out, "[working 55%] linking") {
		t.Fatalf("holder with records must show the root record, got:\n%s", out)
	}
	if strings.Contains(out, "[done]") || strings.Contains(out, "sub") {
		t.Fatalf("a nested record must not speak for the job, got:\n%s", out)
	}
	if strings.Count(out, "[") != 1 {
		t.Fatalf("only the participant with records gets a status, got:\n%s", out)
	}
	// Pipe-mode job: no status even with a log file that holds records.
	qr.Holders[0].Ticket.PTY = false
	buf.Reset()
	renderQueue(&buf, colorize.Plain, qr)
	if strings.Contains(buf.String(), "[working") {
		t.Fatalf("a non-pty job shows no status, got:\n%s", buf.String())
	}
}
