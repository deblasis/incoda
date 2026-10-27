package machine

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
)

func addPool(state, name string, spec PoolSpec) (*PoolAdded, error) {
	return AddPool(state, name, spec, "pool-add", Options{Start: time.Now(), Wait: 30 * time.Second, Poll: 20 * time.Millisecond, By: "incoda test"})
}

func freshState(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	if _, _, err := ensure(t, state); err != nil {
		t.Fatal(err)
	}
	return state
}

func compact(b json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return string(b)
	}
	return buf.String()
}

func rawRegistry(t *testing.T, state string) map[string]json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(RegistryPath(state))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAddPoolToEmptyRegistry(t *testing.T) {
	state := freshState(t)
	lk, err := AcquireLock(state, LockOptions{Op: "test", Start: time.Now(), Wait: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateRegistry(state, lk, func(r *Registry) error { r.Pools = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	lk.Release()

	res, err := addPool(state, "merges", PoolSpec{Slots: 1})
	if err != nil {
		t.Fatal(err)
	}
	r, err := ReadRegistry(state)
	if err != nil || strings.Join(r.Pools, ",") != "merges" || !r.IsPool("merges") {
		t.Fatalf("registry %+v %v", r, err)
	}
	if res.Registry.Generation != res.OldGeneration+1 {
		t.Fatalf("generation %d -> %d", res.OldGeneration, res.Registry.Generation)
	}
	cfg, err := lane.ReadConfig(lane.LaneDir(state, "merges"))
	if err != nil || cfg.Slots != 1 {
		t.Fatalf("pool config %+v %v", cfg, err)
	}
}

func TestAddPoolAlongsideExistingChangesNothingElse(t *testing.T) {
	state := freshState(t)
	// A hand-set field this binary does not know, and a configured pool and lane.
	m := rawRegistry(t, state)
	m["future"] = json.RawMessage(`{"x":1}`)
	b, _ := json.Marshal(m)
	if err := os.WriteFile(RegistryPath(state), b, 0o644); err != nil {
		t.Fatal(err)
	}
	bq, _ := lane.Open(state, "builds")
	if err := bq.SaveConfig(lane.Config{Slots: 5, Description: "cpu and ram"}); err != nil {
		t.Fatal(err)
	}
	bq.Close()
	lq, _ := lane.Open(state, "wintty-build")
	if _, err := lq.UpdateConfig(func(c *lane.Config) error { c.Pools = []string{"builds"}; c.Slots = 3; return nil }); err != nil {
		t.Fatal(err)
	}
	lq.Close()

	snap := func() map[string][]byte {
		out := map[string][]byte{}
		_ = filepath.Walk(lane.LanesDir(state), func(p string, fi os.FileInfo, err error) error {
			if err == nil && fi.Name() == "config.json" {
				b, _ := os.ReadFile(p)
				out[p] = b
			}
			return nil
		})
		return out
	}
	before := rawRegistry(t, state)
	beforeCfg := snap()

	if _, err := addPool(state, "merges", PoolSpec{Slots: 1, Description: "serialises merges"}); err != nil {
		t.Fatal(err)
	}

	after := rawRegistry(t, state)
	for k, v := range before {
		switch k {
		case "pools", "generation":
			continue
		}
		if compact(v) != compact(after[k]) {
			t.Fatalf("field %s changed: %s -> %s", k, v, after[k])
		}
	}
	var gb, ga int64
	_ = json.Unmarshal(before["generation"], &gb)
	_ = json.Unmarshal(after["generation"], &ga)
	if ga != gb+1 {
		t.Fatalf("generation %d -> %d", gb, ga)
	}
	var pb, pa []string
	_ = json.Unmarshal(before["pools"], &pb)
	_ = json.Unmarshal(after["pools"], &pa)
	want := append(append([]string(nil), pb...), "merges")
	if strings.Join(pa, ",") != "builds,computer-use,merges,tests,vm" || len(pa) != len(want) {
		t.Fatalf("pools %v -> %v", pb, pa)
	}
	for p, b := range beforeCfg {
		if a, _ := os.ReadFile(p); !bytes.Equal(a, b) {
			t.Fatalf("%s changed:\n%s\n%s", p, b, a)
		}
	}
	if cfg, _ := lane.ReadConfig(lane.LaneDir(state, "builds")); cfg.Slots != 5 {
		t.Fatalf("builds slots %d", cfg.Slots)
	}
}

func TestAddPoolRefusals(t *testing.T) {
	state := freshState(t)
	before, _ := os.ReadFile(RegistryPath(state))
	var rf *Refusal
	for _, tc := range []struct{ name, want string }{
		{"builds", "pool-exists: "},
		{"a/b", "pool add: queue key"},
		{"", "pool add: queue key is empty"},
		{"..", "pool add: queue key"},
		{"CON", "pool add: queue key"},
	} {
		if _, err := addPool(state, tc.name, PoolSpec{}); !errors.As(err, &rf) || !strings.HasPrefix(rf.Msg, tc.want) {
			t.Fatalf("%q: %v", tc.name, err)
		}
	}
	if _, err := addPool(state, "ok", PoolSpec{Slots: -1}); !errors.As(err, &rf) {
		t.Fatalf("negative slots: %v", err)
	}
	if _, err := addPool(state, "ok", PoolSpec{Description: "a\x1b[2Jb"}); !errors.As(err, &rf) {
		t.Fatalf("control characters in description: %v", err)
	}
	if after, _ := os.ReadFile(RegistryPath(state)); !bytes.Equal(before, after) {
		t.Fatal("a refusal rewrote machine.json")
	}
	if lane.Exists(state, "ok") {
		t.Fatal("a refusal created a lane")
	}
}

func TestAddPoolRefusesAKeyThatLinksOrIsRunning(t *testing.T) {
	state := freshState(t)
	q, _ := lane.Open(state, "proj")
	if _, err := q.UpdateConfig(func(c *lane.Config) error { c.Pools = []string{"builds"}; return nil }); err != nil {
		t.Fatal(err)
	}
	q.Close()
	var rf *Refusal
	if _, err := addPool(state, "proj", PoolSpec{}); !errors.As(err, &rf) || !strings.HasPrefix(rf.Msg, "kind-busy: ") {
		t.Fatalf("linked project lane: %v", err)
	}
	if r, _ := ReadRegistry(state); r.IsPool("proj") {
		t.Fatal("registered despite the refusal")
	}
}

func TestAddPoolConcurrentAddsAreSerialized(t *testing.T) {
	state := freshState(t)
	names := []string{"p1", "p2", "p3", "p4", "p5", "p6"}
	var wg sync.WaitGroup
	errs := make([]error, len(names)+2)
	for i, n := range names {
		wg.Add(1)
		go func() { defer wg.Done(); _, errs[i] = addPool(state, n, PoolSpec{Slots: 2}) }()
	}
	// Two racers for the same name: exactly one may win.
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, errs[len(names)+i] = addPool(state, "same", PoolSpec{}) }()
	}
	wg.Wait()
	for i := range names {
		if errs[i] != nil {
			t.Fatalf("%s: %v", names[i], errs[i])
		}
	}
	won := 0
	for _, e := range errs[len(names):] {
		if e == nil {
			won++
		} else if rf := new(Refusal); !errors.As(e, &rf) || !strings.HasPrefix(rf.Msg, "pool-exists") {
			t.Fatalf("loser: %v", e)
		}
	}
	if won != 1 {
		t.Fatalf("%d winners for one name", won)
	}
	r, err := ReadRegistry(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range append(names, "same", "builds", "computer-use", "tests", "vm") {
		if !r.IsPool(n) {
			t.Fatalf("lost update: %s missing from %v", n, r.Pools)
		}
	}
	if len(r.Pools) != len(names)+1+4 {
		t.Fatalf("pools %v", r.Pools)
	}
}

func TestAddPoolFailedWriteLeavesTheOldRegistry(t *testing.T) {
	state := freshState(t)
	before, _ := os.ReadFile(RegistryPath(state))
	// The temp file's name is taken by a directory, so the write cannot start.
	if err := os.Mkdir(RegistryPath(state)+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := addPool(state, "merges", PoolSpec{Slots: 1}); err == nil {
		t.Fatal("expected the write to fail")
	}
	if after, _ := os.ReadFile(RegistryPath(state)); !bytes.Equal(before, after) {
		t.Fatalf("registry changed:\n%s\n%s", before, after)
	}
	if r, err := ReadRegistry(state); err != nil || r.IsPool("merges") {
		t.Fatalf("registry %+v %v", r, err)
	}
	// The same command works once the obstacle is gone.
	if _, err := addPool(state, "merges", PoolSpec{Slots: 1}); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestAddPoolRefusesNamesThatShareADirectory(t *testing.T) {
	state := freshState(t)
	q, _ := lane.Open(state, "Proj")
	q.Close()
	lk, err := AcquireLock(state, LockOptions{Op: "test", Start: time.Now(), Wait: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateRegistry(state, lk, func(r *Registry) error { r.Pools = append(r.Pools, "ghost"); return nil }); err != nil {
		t.Fatal(err)
	}
	lk.Release()
	var rf *Refusal
	cfgBefore, _ := os.ReadFile(filepath.Join(lane.LaneDir(state, "builds"), "config.json"))
	for _, name := range []string{"Builds", "BUILDS", "Ghost", "proj", "x."} {
		before, _ := os.ReadFile(RegistryPath(state))
		if _, err := addPool(state, name, PoolSpec{Slots: 9}); !errors.As(err, &rf) {
			t.Fatalf("%q: %v", name, err)
		}
		if after, _ := os.ReadFile(RegistryPath(state)); !bytes.Equal(before, after) {
			t.Fatalf("%q rewrote machine.json", name)
		}
	}
	if cfgAfter, _ := os.ReadFile(filepath.Join(lane.LaneDir(state, "builds"), "config.json")); !bytes.Equal(cfgBefore, cfgAfter) {
		t.Fatalf("builds config touched:\n%s\n%s", cfgBefore, cfgAfter)
	}
}

func TestAddPoolHoldsTheLaneLockUntilTheRegistryIsWritten(t *testing.T) {
	state := freshState(t)
	q, _ := lane.Open(state, "foo")
	q.Close()
	var held bool
	afterLaneConfig = func() {
		p, err := lane.Open(state, "foo")
		if err != nil {
			t.Error(err)
			return
		}
		defer p.Close()
		unlock, err := lane.LockAll([]*lane.Queue{p}, time.Now().Add(300*time.Millisecond))
		if err == nil {
			unlock()
			return
		}
		held = true
	}
	defer func() { afterLaneConfig = nil }()
	if _, err := addPool(state, "foo", PoolSpec{Slots: 2}); err != nil {
		t.Fatal(err)
	}
	if !held {
		t.Fatal("the lane's registry lock was free while the pool was being registered")
	}
}

func TestAddPoolRefusesAClosedLaneAndAnUnreadableConfig(t *testing.T) {
	state := freshState(t)
	q, _ := lane.Open(state, "shut")
	if _, err := q.UpdateConfig(func(c *lane.Config) error { c.Closed = "retired"; return nil }); err != nil {
		t.Fatal(err)
	}
	q.Close()
	var rf *Refusal
	if _, err := addPool(state, "shut", PoolSpec{}); !errors.As(err, &rf) || !strings.Contains(rf.Msg, "closed lane") {
		t.Fatalf("closed: %v", err)
	}
	q, _ = lane.Open(state, "bad")
	q.Close()
	if err := os.WriteFile(filepath.Join(lane.LaneDir(state, "bad"), "config.json"), []byte(`{"schema":99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var se *StateError
	if _, err := addPool(state, "bad", PoolSpec{}); !errors.As(err, &se) {
		t.Fatalf("unreadable config must be a state error: %v", err)
	}
}

func TestAddPoolLogsAndRefusesAnUnfinishedMigration(t *testing.T) {
	state := freshState(t)
	if _, err := addPool(state, "merges", PoolSpec{Slots: 1}); err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(lane.LogPath(lane.LaneDir(state, "merges")))
	if !strings.Contains(string(log), "queue=merges event=pool-add ") || !strings.Contains(string(log), "by=pool-add") {
		t.Fatalf("lane.log:\n%s", log)
	}
	if err := os.WriteFile(planPath(state), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	var se *StateError
	if _, err := addPool(state, "other", PoolSpec{}); !errors.As(err, &se) || !strings.Contains(se.Msg, "migration is unfinished") {
		t.Fatalf("unfinished migration: %v", err)
	}
}

func TestAddPoolKeepsAnExistingLanesConfig(t *testing.T) {
	state := freshState(t)
	q, _ := lane.Open(state, "merges")
	if _, err := q.UpdateConfig(func(c *lane.Config) error { c.RequireReason = true; c.Description = "old"; c.Slots = 4; return nil }); err != nil {
		t.Fatal(err)
	}
	q.Close()
	res, err := addPool(state, "merges", PoolSpec{Slots: 1})
	if err != nil || !res.LaneExisted {
		t.Fatalf("%+v %v", res, err)
	}
	cfg, _ := lane.ReadConfig(lane.LaneDir(state, "merges"))
	if cfg.Slots != 1 || !cfg.RequireReason || cfg.Description != "old" {
		t.Fatalf("config %+v", cfg)
	}
}

func TestAddPoolRefusesALaneWithALiveTicket(t *testing.T) {
	state := freshState(t)
	q, _ := lane.Open(state, "busy")
	defer q.Close()
	en, err := q.Enroll(lane.Ticket{Command: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	defer en.Release(0)
	var rf *Refusal
	if _, err := addPool(state, "busy", PoolSpec{Slots: 2}); !errors.As(err, &rf) || rf.Msg != `kind-busy: "busy" has live tickets` {
		t.Fatalf("live ticket: %v", err)
	}
	if r, _ := ReadRegistry(state); r.IsPool("busy") {
		t.Fatal("registered despite a live ticket")
	}
	if cfg, _ := lane.ReadConfig(lane.LaneDir(state, "busy")); cfg.Slots != 0 {
		t.Fatalf("config written despite the refusal: %+v", cfg)
	}
}

func TestAddPoolRefusesALaneClosedInTheWindow(t *testing.T) {
	state := freshState(t)
	q, _ := lane.Open(state, "late")
	q.Close()
	beforeLock = func() {
		p, _ := lane.Open(state, "late")
		_, _ = p.UpdateConfig(func(c *lane.Config) error { c.Closed = "retired"; return nil })
		p.Close()
	}
	defer func() { beforeLock = nil }()
	var rf *Refusal
	if _, err := addPool(state, "late", PoolSpec{}); !errors.As(err, &rf) || !strings.Contains(rf.Msg, "changed while") {
		t.Fatalf("closed in the window: %v", err)
	}
	if r, _ := ReadRegistry(state); r.IsPool("late") {
		t.Fatal("registered")
	}
}
