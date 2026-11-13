package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/lockfile"
)

// TestBuildReportsPoolKind: the kind of a key comes from machine.json, not
// from the lane's own config, and reaches the report so watch can group on
// it. A lane config that happens to name pools is not a pool; a registered
// pool is, whatever its config says.
func TestBuildReportsPoolKind(t *testing.T) {
	state := t.TempDir()
	if err := os.MkdirAll(lane.LanesDir(state), 0o755); err != nil {
		t.Fatal(err)
	}
	reg := map[string]any{"schema": 1, "layout": 2, "generation": 1, "pools": []string{"builds", "tests"}}
	b, _ := json.Marshal(reg)
	if err := os.WriteFile(filepath.Join(state, "machine.json"), b, 0o644); err != nil {
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
	// proj links to builds; builds links to nothing (pools never do).
	if err := os.WriteFile(filepath.Join(lane.LaneDir(state, "proj"), "config.json"),
		[]byte(`{"schema":2,"pools":["builds"]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := Build(state, "test", nil, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]Queue{}
	for _, q := range rep.Queues {
		byKey[q.Key] = q
	}
	if !byKey["builds"].IsPool {
		t.Fatalf("a registered pool must report is_pool: %+v", byKey["builds"])
	}
	if byKey["proj"].IsPool {
		t.Fatalf("a project lane is not a pool, whatever its link says: %+v", byKey["proj"])
	}
	if got := byKey["proj"].Config.Pools; len(got) != 1 || got[0] != "builds" {
		t.Fatalf("the lane's link must reach the report: %v", got)
	}
}

// TestBuildReportsUpstreamWaitingOnPools: a pool's own Waiting is empty by
// construction (a forwarded run is a holder from the moment it enters), so a
// pool-scoped view would read "0 waiting" while the lane that links to it
// queues work. UpstreamWaiting carries that depth instead of a silent zero,
// and a project lane carries none.
func TestBuildReportsUpstreamWaitingOnPools(t *testing.T) {
	state := t.TempDir()
	if err := os.MkdirAll(lane.LanesDir(state), 0o755); err != nil {
		t.Fatal(err)
	}
	reg := map[string]any{"schema": 1, "layout": 2, "generation": 1, "pools": []string{"builds"}}
	b, _ := json.Marshal(reg)
	if err := os.WriteFile(filepath.Join(state, "machine.json"), b, 0o644); err != nil {
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
	if err := os.WriteFile(filepath.Join(lane.LaneDir(state, "builds"), "config.json"),
		[]byte(`{"schema":2,"slots":4}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lane.LaneDir(state, "proj"), "config.json"),
		[]byte(`{"schema":2,"slots":2,"pools":["builds"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Two holders and one waiter on proj: the waiter is the queued work the
	// pool reader is asking about. Every ticket is locked, because liveness
	// is the kernel lock, not the pid.
	dir := lane.LaneDir(state, "proj")
	base := time.Now().UnixNano()
	for i := range 3 {
		tk := lane.Ticket{
			PID:         9000 + i,
			Queue:       "proj",
			Slots:       2,
			ArrivalNano: base + int64(i),
			Arrival:     time.Now().Format(time.RFC3339),
			Command:     []string{"zig", "build"},
			Hostname:    "test",
		}
		if i < 2 {
			tk.AcquireNano = base + int64(i) + 1
			tk.Acquired = time.Now().Format(time.RFC3339)
		}
		holdTicket(t, dir, fmt.Sprintf("%020d-%d.ticket", tk.ArrivalNano, tk.PID), tk)
	}

	rep, err := Build(state, "test", nil, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]Queue{}
	for _, q := range rep.Queues {
		byKey[q.Key] = q
	}
	if got := len(byKey["proj"].Waiting); got != 1 {
		t.Fatalf("fixture: proj must show one waiter, got %d", got)
	}
	if got := byKey["builds"].Waiting; len(got) != 0 {
		t.Fatalf("a pool's own waiting list stays empty: %d", len(got))
	}
	up := byKey["builds"].UpstreamWaiting
	if len(up) != 1 || up[0].Lane != "proj" || up[0].Waiting != 1 {
		t.Fatalf("the pool must carry proj's queued depth, got %+v", up)
	}
	if len(byKey["proj"].UpstreamWaiting) != 0 {
		t.Fatalf("a project lane has no upstream, got %+v", byKey["proj"].UpstreamWaiting)
	}
}

// holdTicket writes one ticket and holds its kernel lock for the test's
// lifetime, which is what makes it live to a scan.
func holdTicket(t *testing.T, dir, name string, tk lane.Ticket) {
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
