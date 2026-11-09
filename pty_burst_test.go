//go:build windows

package main

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// tick is one OSC 7501 status record as a reporter on a pty lane emits it:
// the shape of the burst a suite of chatty processes produces when several of
// them report progress through the same console.
func tick(id string, n int) string {
	rec := "state=working:progress=" + strconv.Itoa(n)
	if id != "" {
		rec = "state=working:id=" + id + ":progress=" + strconv.Itoa(n)
	}
	return "\x1b]7501;" + rec + "\x07"
}

// burstPayload interleaves n status ticks with n plain output lines, the way a
// real reporter ticks between the lines of its own output. The plain lines are
// the control: coalescing the ticks must never cost one of them.
func burstPayload(id string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(tick(id, i))
		b.WriteString("BURST-L" + strconv.Itoa(i) + "\r\n")
	}
	return b.String()
}

func jobFileOf(t *testing.T, state, key string) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(state, "lanes", key, "jobs", "*.out"))
	if err != nil || len(files) != 1 {
		t.Fatalf("want exactly one job file, got %v (%v)", files, err)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestPTYStatusTickBurstIsCoalesced: a job that reports status through the
// console in a burst must not be able to degrade the lane that runs it. Sixty
// ticks land in one write (the shape of several processes reporting at once);
// the capture may carry a handful of them, never the whole burst, while every
// plain line between them arrives verbatim and the newest tick survives.
func TestPTYStatusTickBurstIsCoalesced(t *testing.T) {
	bin, tty := ptyBinaries(t)
	state := t.TempDir()
	linkTestKeys(t, bin, state, "pty-burst")
	const n = 60
	out, errb, code := ptyRunIncoda(t, ownerEnv(state, "alice"),
		"run", "--queue", "pty-burst", "--owner", "alice", "--reason", "test", "--pty", "--",
		tty, "write", "hex:"+hex.EncodeToString([]byte(burstPayload("", n))))
	if code != 0 {
		t.Fatalf("run exited %d:\n%s\n%s", code, out, errb)
	}
	capture := jobFileOf(t, state, "pty-burst")
	for _, stream := range []struct{ name, body string }{{"stdout", out}, {"capture", capture}} {
		ticks := strings.Count(stream.body, "\x1b]7501;")
		if ticks > 8 {
			t.Fatalf("%s carried %d of %d status ticks: a burst must collapse to the handful the window lets through, not the whole storm:\n%q",
				stream.name, ticks, n, stream.body)
		}
		if !strings.Contains(stream.body, tick("", n-1)) {
			t.Fatalf("%s lost the newest status tick:\n%q", stream.name, stream.body)
		}
		for i := 0; i < n; i++ {
			if want := "BURST-L" + strconv.Itoa(i); !strings.Contains(stream.body, want) {
				t.Fatalf("%s lost the plain line %q to tick coalescing:\n%q", stream.name, want, stream.body)
			}
		}
	}
}

// TestPTYStatusTickBurstKeepsEachID: coalescing collapses a reporter's own
// superseded reports, not the other ids reporting beside it. Three ids tick in
// a round-robin burst; each id's newest report survives and each id keeps only
// the handful the window lets through.
func TestPTYStatusTickBurstKeepsEachID(t *testing.T) {
	bin, tty := ptyBinaries(t)
	state := t.TempDir()
	linkTestKeys(t, bin, state, "pty-burst-id")
	const n = 20
	var b strings.Builder
	for i := 0; i < n; i++ {
		for _, id := range []string{"w1", "w2", "w3"} {
			b.WriteString(tick(id, i))
		}
		b.WriteString("ROUND-L" + strconv.Itoa(i) + "\r\n")
	}
	out, errb, code := ptyRunIncoda(t, ownerEnv(state, "alice"),
		"run", "--queue", "pty-burst-id", "--owner", "alice", "--reason", "test", "--pty", "--",
		tty, "write", "hex:"+hex.EncodeToString([]byte(b.String())))
	if code != 0 {
		t.Fatalf("run exited %d:\n%s\n%s", code, out, errb)
	}
	capture := jobFileOf(t, state, "pty-burst-id")
	for _, stream := range []struct{ name, body string }{{"stdout", out}, {"capture", capture}} {
		for _, id := range []string{"w1", "w2", "w3"} {
			if !strings.Contains(stream.body, tick(id, n-1)) {
				t.Fatalf("%s lost the newest report of id %s:\n%q", stream.name, id, stream.body)
			}
			per := strings.Count(stream.body, "id="+id)
			if per > 6 {
				t.Fatalf("%s carried %d reports for id %s: a burst must collapse to that id's newest, not carry every round:\n%q",
					stream.name, per, id, stream.body)
			}
		}
		for i := 0; i < n; i++ {
			if want := "ROUND-L" + strconv.Itoa(i); !strings.Contains(stream.body, want) {
				t.Fatalf("%s lost the plain line %q:\n%q", stream.name, want, stream.body)
			}
		}
	}
}
