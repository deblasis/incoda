package machine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
)

func rebuild(state string, pools ...string) (*Registry, []string, error) {
	var kinds []string
	reg, err := Rebuild(state, pools, Options{Start: time.Now(), Wait: 5 * time.Second, By: "incoda test"},
		func(k, kind string) { kinds = append(kinds, k+"="+kind) })
	return reg, kinds, err
}

func TestRebuildRegistry(t *testing.T) {
	state := t.TempDir()
	if _, _, err := ensure(t, state); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(RegistryPath(state)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lane.QueuesDir(state)); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UnixNano()
	reg, kinds, err := rebuild(state, "tests", "builds", "tests", "printer")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(reg.Pools, ",") != "builds,printer,tests" || reg.Generation < before {
		t.Fatalf("registry %+v", reg)
	}
	if got := strings.Join(kinds, ","); got != "builds=pool,computer-use=project,tests=pool,vm=project,printer=pool (no lane yet)" {
		t.Fatalf("kinds %s", got)
	}
	if !FencePlaced(state) {
		t.Fatal("the rebuild places a missing fence")
	}
	if r, err := ReadRegistry(state); err != nil || r.Generation != reg.Generation {
		t.Fatalf("written registry %+v %v", r, err)
	}
}

func TestRebuildRefusals(t *testing.T) {
	var rf *Refusal
	var se *StateError

	fresh := t.TempDir()
	if _, _, err := rebuild(fresh, "builds"); !errors.As(err, &se) || !strings.HasPrefix(se.Msg, "machine-state: nothing to rebuild: ") {
		t.Fatalf("no lanes/: %v", err)
	}

	state := t.TempDir()
	if _, _, err := ensure(t, state); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rebuild(state, "a/b"); !errors.As(err, &rf) || !strings.HasPrefix(rf.Msg, `rebuild-registry: queue key "a/b" contains`) {
		t.Fatalf("invalid key: %v", err)
	}
	if _, _, err := rebuild(state); !errors.As(err, &rf) || !strings.HasPrefix(rf.Msg, "rebuild-registry: name at least one pool") {
		t.Fatalf("empty list: %v", err)
	}

	cfg := filepath.Join(lane.LaneDir(state, "tests"), "config.json")
	if err := os.WriteFile(cfg, []byte(`{"schema":2,"pools":["builds"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rebuild(state, "tests"); !errors.As(err, &rf) || rf.Msg != `kind-busy: "tests" links pools` {
		t.Fatalf("a named key with a link: %v", err)
	}
	if err := os.WriteFile(cfg, []byte(`{"schema":2,"slots":1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	release := holdTicket(t, lane.LanesDir(state), "vm", 4711, "qemu")
	if _, _, err := rebuild(state, "builds"); !errors.As(err, &rf) || rf.Msg != `kind-busy: "vm" has live tickets` {
		t.Fatalf("a live ticket: %v", err)
	}
	release()

	if err := writePlan(state); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rebuild(state, "builds"); !errors.As(err, &se) || !strings.Contains(se.Msg, "a migration is unfinished") {
		t.Fatalf("unfinished migration: %v", err)
	}
	if err := os.Remove(planPath(state)); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(RegistryPath(state), []byte(`{"schema":2,"layout":2,"generation":1,"pools":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rebuild(state, "builds"); !errors.As(err, &se) || !strings.Contains(se.Msg, "written by a newer incoda") {
		t.Fatalf("a newer registry is never overwritten: %v", err)
	}
}
