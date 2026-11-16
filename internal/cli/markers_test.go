package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/colorize"
	"github.com/deblasis/incoda/internal/lane"
)

func statusReportWith(es ...lane.Entry) QueueReport {
	qr := QueueReport{
		Key:            "q",
		Dir:            "C:/state/q",
		Exists:         true,
		EffectiveSlots: 4,
		Holders:        es,
	}
	for _, e := range es {
		if e.Holding && e.Ticket.Exclusive {
			qr.ExclusiveHolder = true
		}
	}
	return qr
}

func holder(pid int, exclusive bool) lane.Entry {
	return lane.Entry{
		File:    "00000000000000000001-1.ticket",
		Holding: true,
		Ticket:  lane.Ticket{PID: pid, Slots: 4, Exclusive: exclusive, Command: []string{"job"}},
	}
}

func unreadableHolder(pid int) lane.Entry {
	return lane.Entry{
		File:         "00000000000000000002-2.ticket",
		PayloadError: "parse: unexpected end of JSON input",
		Ticket:       lane.Ticket{PID: pid},
	}
}

func TestRenderQueueMarksAReadableExclusiveHolder(t *testing.T) {
	cases := []struct {
		name string
		e    lane.Entry
		want string
		bad  string
	}{
		{"exclusive", holder(7, true), "1/4 slot(s) held (exclusive)", ""},
		{"plain", holder(8, false), "1/4 slot(s) held", "(exclusive)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := paletteFor(&buf, true)
			renderQueue(&buf, p, statusReportWith(c.e), nil)
			out := buf.String()
			if !strings.Contains(out, c.want) {
				t.Fatalf("status header missing %q:\n%s", c.want, out)
			}
			if c.bad != "" && strings.Contains(out, c.bad) {
				t.Fatalf("status header carries %q for a plain holder:\n%s", c.bad, out)
			}
		})
	}
}

func TestRenderQueueNamesUnreadableTicketsApartFromStartedHolders(t *testing.T) {
	var buf bytes.Buffer
	p := paletteFor(&buf, true)
	renderQueue(&buf, p, statusReportWith(holder(7, false), unreadableHolder(9)), nil)
	out := buf.String()
	if strings.Contains(out, "2/4 slot(s) held") {
		t.Fatalf("an unreadable ticket must not inflate the started-holder fraction:\n%s", out)
	}
	if !strings.Contains(out, "1/4 slot(s) held, 1 unreadable") {
		t.Fatalf("status header does not name the unreadable ticket:\n%s", out)
	}
	if !strings.Contains(out, "UNREADABLE") || !strings.Contains(out, "not started") {
		t.Fatalf("status row still calls an unreadable ticket a started holder:\n%s", out)
	}
	if !strings.Contains(out, "ticket unreadable") {
		t.Fatalf("status row hides the payload error:\n%s", out)
	}
	if strings.Contains(out, "(none)") {
		t.Fatalf("status row states an unreadable command or directory as (none):\n%s", out)
	}
}

func TestQueuesStateMarksExclusiveAndUnreadable(t *testing.T) {
	cases := []struct {
		name string
		snap *lane.Snapshot
		want string
		bad  string
	}{
		{"exclusive", &lane.Snapshot{EffectiveSlots: 4, Holders: []lane.Entry{holder(7, true)}},
			"1/4 held (exclusive), 0 waiting", ""},
		{"plain", &lane.Snapshot{EffectiveSlots: 4, Holders: []lane.Entry{holder(8, false)}},
			"1/4 held, 0 waiting", "(exclusive)"},
		{"unreadable", &lane.Snapshot{EffectiveSlots: 4, Holders: []lane.Entry{holder(7, false), unreadableHolder(9)}},
			"1/4 held, 1 unreadable, 0 waiting", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := queueStateLine(colorize.Plain, c.snap)
			if !strings.Contains(got, c.want) {
				t.Fatalf("queues state = %q, want %q", got, c.want)
			}
			if c.bad != "" && strings.Contains(got, c.bad) {
				t.Fatalf("queues state carries %q for this set: %q", c.bad, got)
			}
		})
	}
}

func TestHolderLineMarksAReadableExclusiveHolder(t *testing.T) {
	ex := lane.Entry{Ticket: lane.Ticket{PID: 1, Dir: "C:/w", Command: []string{"go", "test"}, Exclusive: true}}
	if got := holderLine(ex); !strings.Contains(got, "(exclusive)") {
		t.Fatalf("holderLine dropped the exclusive marker: %q", got)
	}
	plain := lane.Entry{Ticket: lane.Ticket{PID: 2, Dir: "C:/w", Command: []string{"go", "test"}}}
	if got := holderLine(plain); strings.Contains(got, "(exclusive)") {
		t.Fatalf("holderLine marked a plain holder exclusive: %q", got)
	}
	bad := unreadableHolder(3)
	got := holderLine(bad)
	if !strings.Contains(got, "ticket unreadable") || !strings.Contains(got, bad.PayloadError) {
		t.Fatalf("holderLine hid the payload error: %q", got)
	}
}
