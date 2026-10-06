package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/deblasis/incoda/internal/lane"
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
