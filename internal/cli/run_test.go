package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
)

// TestFirstLinkConflict: a run whose first link loses the compare-and-set
// to a different link refuses with link-conflict, naming the winner, and
// takes nothing (spec 4.2).
func TestFirstLinkConflict(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /usr/bin/true")
	}
	dir := t.TempDir()
	t.Setenv("INCODA_DIR", dir)
	if code := Main([]string{"config", "seed"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("migrate: %d", code)
	}
	saved := beforeFirstLink
	defer func() { beforeFirstLink = saved }()
	beforeFirstLink = func(dir, key string) {
		_, err := machine.WriteLink(dir, key, "test", machine.Options{Start: time.Now(), Wait: 5 * time.Second}, func(_ *machine.Registry, c *lane.Config) error {
			c.Pools = []string{"builds"}
			return nil
		})
		if err != nil {
			t.Error(err)
		}
	}
	var stderr bytes.Buffer
	code := Main([]string{"run", "--queue", "race-gate", "--pool", "tests", "--", "true"}, io.Discard, &stderr)
	want := fmt.Sprintf(`incoda: link-conflict: "race-gate" was just linked to builds by pid %d; rerun without --pool`, os.Getpid())
	if code != ExitUsage || !strings.Contains(stderr.String(), want) {
		t.Fatalf("want 120 and %q, got %d:\n%s", want, code, stderr.String())
	}
	cfg, err := lane.ReadConfig(lane.LaneDir(dir, "race-gate"))
	if err != nil || machine.SetText(cfg.Pools) != "builds" {
		t.Fatalf("the winner's link stays: %+v %v", cfg, err)
	}
	for _, k := range []string{"race-gate", "builds", "tests"} {
		entries, _ := os.ReadDir(lane.LaneDir(dir, k))
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".ticket") {
				t.Fatalf("%s holds a ticket after the refusal", k)
			}
		}
	}
}

// TestFirstLinksAreAllOrNothing: a run making first links on two keys,
// whose second key loses its race to a different set, refuses with
// link-conflict and leaves the first key unlinked (spec 4.2: if any key
// would be refused, nothing is written).
func TestFirstLinksAreAllOrNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /usr/bin/true")
	}
	dir := t.TempDir()
	t.Setenv("INCODA_DIR", dir)
	if code := Main([]string{"config", "seed"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("migrate: %d", code)
	}
	saved := beforeFirstLink
	defer func() { beforeFirstLink = saved }()
	beforeFirstLink = func(dir, key string) {
		if key != "race-b-gate" {
			return
		}
		_, err := machine.WriteLink(dir, key, "test", machine.Options{Start: time.Now(), Wait: 5 * time.Second}, func(_ *machine.Registry, c *lane.Config) error {
			c.Pools = []string{"builds"}
			return nil
		})
		if err != nil {
			t.Error(err)
		}
	}
	var stderr bytes.Buffer
	code := Main([]string{"run", "--queue", "race-a-gate,race-b-gate", "--pool", "tests", "--", "true"}, io.Discard, &stderr)
	want := fmt.Sprintf(`incoda: link-conflict: "race-b-gate" was just linked to builds by pid %d; rerun without --pool`, os.Getpid())
	if code != ExitUsage || !strings.Contains(stderr.String(), want) {
		t.Fatalf("want 120 and %q, got %d:\n%s", want, code, stderr.String())
	}
	if strings.Contains(stderr.String(), "linked: ") {
		t.Fatalf("a refused run announces no link:\n%s", stderr.String())
	}
	cfg, err := lane.ReadConfig(lane.LaneDir(dir, "race-a-gate"))
	if err != nil || len(cfg.Pools) != 0 {
		t.Fatalf("the first key stays unlinked: %+v %v", cfg, err)
	}
	if _, err := os.Stat(lane.LaneDir(dir, "race-a-gate")); !os.IsNotExist(err) {
		t.Fatalf("the first key's lane was created: %v", err)
	}
}
