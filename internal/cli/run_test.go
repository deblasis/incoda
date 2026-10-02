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
