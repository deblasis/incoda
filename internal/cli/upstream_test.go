package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/lockfile"
)

// TestUpstreamWaitingShowsOnPoolViews: the pool row of `queues` and the
// pool section of `status` state the depth of the lanes that link to them.
// A pool only ever holds forwarded runs, so its own waiting list is empty
// while the lane queues work; reading "0 waiting" alone was a lie of scope.
func TestUpstreamWaitingShowsOnPoolViews(t *testing.T) {
	state := t.TempDir()
	t.Setenv("INCODA_DIR", state)
	if err := os.MkdirAll(lane.LanesDir(state), 0o755); err != nil {
		t.Fatal(err)
	}
	reg, _ := json.Marshal(map[string]any{"schema": 1, "layout": 2, "generation": 1, "pools": []string{"builds"}})
	if err := os.WriteFile(filepath.Join(state, "machine.json"), reg, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "queues"), []byte("fence"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"builds", "proj"} {
		if err := os.MkdirAll(lane.LaneDir(state, k), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(lane.LaneDir(state, "builds"), "config.json"), []byte(`{"schema":2,"slots":4}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lane.LaneDir(state, "proj"), "config.json"), []byte(`{"schema":2,"slots":2,"pools":["builds"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// The pool is busy with a forwarded run, which is the case the pool row
	// has to state correctly: one holder, its own waiting empty, one lane
	// queued behind it.
	base := time.Now().UnixNano()
	held := lane.Ticket{
		PID: 9001, Queue: "builds", Slots: 1,
		ArrivalNano: base, Arrival: time.Now().Format(time.RFC3339),
		AcquireNano: base + 1, Acquired: time.Now().Format(time.RFC3339),
		Command: []string{"zig", "build"}, Hostname: "test",
	}
	holdTicketForTest(t, lane.LaneDir(state, "builds"), fmt.Sprintf("%020d-%d.ticket", held.ArrivalNano, held.PID), held)

	dir := lane.LaneDir(state, "proj")
	for i := range 3 {
		tk := lane.Ticket{
			PID: 9100 + i, Queue: "proj", Slots: 2,
			ArrivalNano: base + int64(i), Arrival: time.Now().Format(time.RFC3339),
			Command: []string{"zig", "build"}, Hostname: "test",
		}
		if i < 2 {
			tk.AcquireNano = base + int64(i) + 1
			tk.Acquired = time.Now().Format(time.RFC3339)
		}
		holdTicketForTest(t, dir, fmt.Sprintf("%020d-%d.ticket", tk.ArrivalNano, tk.PID), tk)
	}

	var out, errb bytes.Buffer
	if err := cmdQueues([]string{"--no-color"}, &out, &errb); err != nil {
		t.Fatalf("queues: %v\n%s", err, errb.String())
	}
	row := outputLine(out.String(), "builds")
	if row == "" {
		t.Fatalf("no builds row in:\n%s", out.String())
	}
	if !strings.Contains(row, "1/4 held, 0 waiting") || !strings.Contains(row, ", 1 upstream (proj)") {
		t.Fatalf("the pool row must state its own holders and its upstream depth, got %q", row)
	}
	laneRow := outputLine(out.String(), "proj")
	if !strings.Contains(laneRow, "2/2 held, 1 waiting") {
		t.Fatalf("the lane row keeps its own waiting count, got %q", laneRow)
	}

	out.Reset()
	errb.Reset()
	if err := cmdStatus([]string{"--queue", "builds", "--no-color"}, &out, &errb); err != nil {
		t.Fatalf("status: %v\n%s", err, errb.String())
	}
	if !strings.Contains(out.String(), "upstream: 1 waiting on proj") {
		t.Fatalf("the pool section must state the lane depth, got:\n%s", out.String())
	}
}

// outputLine returns the output line whose first field is key.
func outputLine(out, key string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), key+" ") {
			return l
		}
	}
	return ""
}

// holdTicketForTest writes one ticket and holds its kernel lock until the
// test ends: liveness is the lock, not the pid.
func holdTicketForTest(t *testing.T, dir, name string, tk lane.Ticket) {
	t.Helper()
	b, err := json.Marshal(tk)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	lf, err := lockfile.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := lf.Lock(); err != nil {
		lf.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = lf.Unlock()
		_ = lf.Close()
	})
}
