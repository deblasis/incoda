package machine

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
)

func TestSets(t *testing.T) {
	if got := SortedSet([]string{"tests", "builds", "tests"}); strings.Join(got, ",") != "builds,tests" {
		t.Fatalf("SortedSet: %v", got)
	}
	if !SameSet([]string{"b", "a"}, []string{"a", "b", "a"}) || SameSet([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("SameSet")
	}
	if !Subset([]string{"a"}, []string{"a", "b"}) || Subset([]string{"c"}, []string{"a"}) || !Subset(nil, nil) {
		t.Fatal("Subset")
	}
	if SetText(nil) != "(none)" || SetText([]string{"vm", "builds"}) != "builds,vm" {
		t.Fatal("SetText")
	}
}

// TestWriteLink: a link is written under machine.lock and the lane's
// registry lock, logged as event=link, refused on a pool, and a callback
// that finds nothing to do writes nothing.
func TestWriteLink(t *testing.T) {
	state := t.TempDir()
	if _, _, err := ensure(t, state); err != nil {
		t.Fatal(err)
	}
	o := Options{Start: time.Now(), Wait: 10 * time.Second, Poll: 20 * time.Millisecond}
	set := func(pools ...string) func(*Registry, *lane.Config) error {
		return func(_ *Registry, c *lane.Config) error {
			if SameSet(c.Pools, pools) {
				return lane.ErrNoChange
			}
			c.Pools = pools
			return nil
		}
	}
	res, err := WriteLink(state, "cap-gate", "test", o, set("tests"))
	if err != nil || !res.Changed || len(res.Old.Pools) != 0 || SetText(res.New.Pools) != "tests" || !res.Registry.IsPool("tests") {
		t.Fatalf("first link: %+v %v", res, err)
	}
	cfg, err := lane.ReadConfig(lane.LaneDir(state, "cap-gate"))
	if err != nil || SetText(cfg.Pools) != "tests" || cfg.Schema != lane.ConfigSchema {
		t.Fatalf("stored config: %+v %v", cfg, err)
	}
	res, err = WriteLink(state, "cap-gate", "test", o, set("tests"))
	if err != nil || res.Changed {
		t.Fatalf("the same link again writes nothing: %+v %v", res, err)
	}
	b, _ := os.ReadFile(lane.LogPath(lane.LaneDir(state, "cap-gate")))
	if n := strings.Count(string(b), " event=link "); n != 1 || !strings.Contains(string(b), " by=test old= new=tests") {
		t.Fatalf("one event=link line, got %d:\n%s", n, b)
	}
	if pid, ok := LastLinker(state, "cap-gate"); !ok || pid != os.Getpid() {
		t.Fatalf("LastLinker = %d %v", pid, ok)
	}
	var rf *Refusal
	if _, err := WriteLink(state, "builds", "test", o, set("tests")); !errors.As(err, &rf) || !strings.HasPrefix(rf.Msg, `pool-mismatch: "builds" is a pool`) {
		t.Fatalf("a pool never links: %v", err)
	}
	if err := os.WriteFile(lane.LaneDir(state, "cap-gate")+"/config.json", []byte(`{"schema":9}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var se *StateError
	if _, err := WriteLink(state, "cap-gate", "test", o, set("vm")); !errors.As(err, &se) || !strings.Contains(se.Msg, "newer incoda") {
		t.Fatalf("a newer config fails closed: %v", err)
	}
}

// TestConcurrentFirstLinksToOneSetNeverEscalate: several writers making
// the same first link at once: one writes, the others find it in place.
func TestConcurrentFirstLinksToOneSetNeverEscalate(t *testing.T) {
	state := t.TempDir()
	if _, _, err := ensure(t, state); err != nil {
		t.Fatal(err)
	}
	o := Options{Start: time.Now(), Wait: 10 * time.Second, Poll: 5 * time.Millisecond}
	var wg sync.WaitGroup
	var mu sync.Mutex
	changed := 0
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := WriteLink(state, "kf-gate", "test", o, func(_ *Registry, c *lane.Config) error {
				if len(c.Pools) > 0 {
					return lane.ErrNoChange
				}
				c.Pools = []string{"tests"}
				return nil
			})
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			if res.Changed {
				changed++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if changed != 1 {
		t.Fatalf("%d writers changed the link, want exactly 1", changed)
	}
}
