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

// relink sets key's link to pools, the way a user's config --replace does.
func relink(t *testing.T, dir, key string, pools ...string) {
	t.Helper()
	_, err := machine.WriteLink(dir, key, "test", machine.Options{Start: time.Now(), Wait: 5 * time.Second}, func(_ *machine.Registry, c *lane.Config) error {
		c.Pools = pools
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// laneLog is a lane's lane.log.
func laneLog(dir, key string) string {
	b, _ := os.ReadFile(lane.LogPath(lane.LaneDir(dir, key)))
	return string(b)
}

// TestReplanAtEachVerifyPoint: a link that changes after an enroll, or
// before the final verify, makes the run release everything, say what
// changed, log event=replan and plan again; it then takes the new link's
// pools (spec 2.5).
func TestReplanAtEachVerifyPoint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /usr/bin/true")
	}
	for _, c := range []struct {
		name, at string
	}{
		{"after the enroll on the project lane", "v-gate"},
		{"at the final verify", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("INCODA_DIR", dir)
			if code := Main([]string{"config", "v-gate", "--pool", "tests"}, io.Discard, io.Discard); code != 0 {
				t.Fatalf("link: %d", code)
			}
			saved := atVerify
			defer func() { atVerify = saved }()
			done := false
			atVerify = func(dir, key string) {
				if key == c.at && !done {
					done = true
					relink(t, dir, "v-gate", "builds")
				}
			}
			var stderr bytes.Buffer
			if code := Main([]string{"run", "--queue", "v-gate", "--", "true"}, io.Discard, &stderr); code != 0 {
				t.Fatalf("run: %d\n%s", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), `incoda: replan: the link of "v-gate" changed: tests -> builds`+"\n") {
				t.Fatalf("missing the replan line:\n%s", stderr.String())
			}
			acq := fmt.Sprintf("event=acquire pid=%d", os.Getpid())
			if !strings.Contains(laneLog(dir, "builds"), acq) || !strings.Contains(laneLog(dir, "v-gate"), "event=replan pid=") {
				t.Fatalf("the replanned run takes builds and logs the replan:\nbuilds:\n%s\nv-gate:\n%s", laneLog(dir, "builds"), laneLog(dir, "v-gate"))
			}
		})
	}
}

// TestReplanWhenAPoolLeavesTheRegistry: a linked pool removed from
// machine.json while the run holds its project lane makes it replan, and
// the new plan fails closed: the lane set never shrinks silently.
func TestReplanWhenAPoolLeavesTheRegistry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /usr/bin/true")
	}
	dir := t.TempDir()
	t.Setenv("INCODA_DIR", dir)
	if code := Main([]string{"config", "w-gate", "--pool", "vm"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("link: %d", code)
	}
	saved := atVerify
	defer func() { atVerify = saved }()
	atVerify = func(dir, key string) {
		if key != "w-gate" {
			return
		}
		lk, err := machine.AcquireLock(dir, machine.LockOptions{Op: "test", Start: time.Now(), Wait: 5 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		defer lk.Release()
		if _, err := machine.UpdateRegistry(dir, lk, func(r *machine.Registry) error {
			r.Pools = []string{"builds", "computer-use", "tests"}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	var stderr bytes.Buffer
	code := Main([]string{"run", "--queue", "w-gate", "--", "true"}, io.Discard, &stderr)
	want := "incoda: replan: pool \"vm\" left the registry\nincoda: machine-state: queue \"w-gate\" links \"vm\": it is not a pool on this machine\n"
	if code != ExitState || !strings.HasSuffix(stderr.String(), want) {
		t.Fatalf("want 122 and\n%s\ngot %d:\n%s", want, code, stderr.String())
	}
	if strings.Contains(laneLog(dir, "w-gate"), fmt.Sprintf("event=acquire pid=%d", os.Getpid())) {
		t.Fatal("the run must not have acquired anything")
	}
}

// TestReplanSharesTheWaitBudget: a link that changes at every verify point
// cannot keep a run replanning for ever; the replans share the one --wait
// budget and the run ends with 121 once it is spent (spec 2.5).
func TestReplanSharesTheWaitBudget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /usr/bin/true")
	}
	dir := t.TempDir()
	t.Setenv("INCODA_DIR", dir)
	if code := Main([]string{"config", "f-gate", "--pool", "tests"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("link: %d", code)
	}
	saved := atVerify
	defer func() { atVerify = saved }()
	flip := false
	atVerify = func(dir, key string) {
		if key != "f-gate" {
			return
		}
		flip = !flip
		if flip {
			relink(t, dir, "f-gate", "builds")
		} else {
			relink(t, dir, "f-gate", "tests")
		}
	}
	var stderr bytes.Buffer
	begin := time.Now()
	code := Main([]string{"run", "--queue", "f-gate", "--wait", "1s", "--", "true"}, io.Discard, &stderr)
	if code != ExitTimeout || !strings.Contains(stderr.String(), "incoda: replan: ") || !strings.Contains(stderr.String(), "--wait budget of 1s is spent") {
		t.Fatalf("want 121 after the budget, got %d:\n%s", code, stderr.String())
	}
	if d := time.Since(begin); d > 15*time.Second {
		t.Fatalf("took %s", d)
	}
	if !strings.Contains(laneLog(dir, "f-gate"), "event=replan pid=") {
		t.Fatalf("no replan logged:\n%s", laneLog(dir, "f-gate"))
	}
}
