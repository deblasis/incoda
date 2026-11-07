package cli

import (
	"bytes"
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
