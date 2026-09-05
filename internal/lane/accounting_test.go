package lane

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func lastLog(t *testing.T, q *Queue) string {
	t.Helper()
	lines := q.TailLog(1)
	if len(lines) == 0 {
		t.Fatal("lane.log is empty")
	}
	return lines[0]
}

func TestReapIsLogged(t *testing.T) {
	// A holder killed from Task Manager leaves a ticket that the next scan
	// deletes. Until this event existed the log showed an enqueue with no
	// ending, so four days of history could not say how nine jobs finished.
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	orphan := filepath.Join(q.Dir, ticketName(time.Now().UnixNano()-1_000_000, 424242))
	if err := os.WriteFile(orphan, []byte(`{"pid":424242,"slots":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Observe(0); err != nil {
		t.Fatal(err)
	}
	if got := lastLog(t, q); !strings.Contains(got, "event=reaped pid=424242") {
		t.Fatalf("reaping must be logged, last line: %q", got)
	}
}

func TestReleaseLogsJobStats(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	en, err := q.Enroll(Ticket{Command: []string{"zig", "build"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := en.Acquire(context.Background(), AcquireOptions{Wait: 0}); err != nil {
		t.Fatal(err)
	}
	en.Stats = Stats{PeakBytes: 3 << 30, HavePeak: true, CPU: 90 * time.Second, HaveCPU: true}
	en.Release(0)

	got := lastLog(t, q)
	for _, want := range []string{"event=release", "rc=0", "peak_mem=3.0 GB", "cpu=1m30s"} {
		if !strings.Contains(got, want) {
			t.Fatalf("release line missing %q: %q", want, got)
		}
	}

	// Without accounting the line must not invent numbers.
	en2, err := q.Enroll(Ticket{Command: []string{"noop"}})
	if err != nil {
		t.Fatal(err)
	}
	en2.Release(3)
	if got := lastLog(t, q); strings.Contains(got, "peak_mem=") || strings.Contains(got, "cpu=") {
		t.Fatalf("no stats means no stats fields, got %q", got)
	}
}

func TestOwnerRoundTrips(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	en, err := q.Enroll(Ticket{Command: []string{"x"}, Owner: "session-7 in C:/temp/wt-a"})
	if err != nil {
		t.Fatal(err)
	}
	defer en.Release(0)
	snap, err := q.Observe(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Holders) != 1 || snap.Holders[0].Ticket.Owner != "session-7 in C:/temp/wt-a" {
		t.Fatalf("owner did not round-trip: %+v", snap.Holders)
	}
}

func TestStatsString(t *testing.T) {
	cases := []struct {
		s    Stats
		want string
	}{
		{Stats{}, ""},
		{Stats{PeakBytes: 512 << 20, HavePeak: true}, " peak_mem=512.0 MB"},
		{Stats{CPU: 2 * time.Second, HaveCPU: true}, " cpu=2s"},
		{Stats{PeakBytes: 1 << 30, HavePeak: true, CPU: time.Minute, HaveCPU: true}, " peak_mem=1.0 GB cpu=1m0s"},
	}
	for _, c := range cases {
		if got := c.s.logFields(); got != c.want {
			t.Errorf("%+v.logFields() = %q, want %q", c.s, got, c.want)
		}
	}
}
