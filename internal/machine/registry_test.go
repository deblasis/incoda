package machine

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReadRegistryMissing(t *testing.T) {
	if _, err := ReadRegistry(t.TempDir()); !errors.Is(err, ErrNoRegistry) {
		t.Fatalf("want ErrNoRegistry, got %v", err)
	}
}

func TestUpdateRegistryBumpsGenerationAndKeepsUnknownFields(t *testing.T) {
	state := t.TempDir()
	body := `{"schema":1,"layout":2,"generation":7,"pools":["vm","builds"],"migrated_by":"incoda 0.7.0","future":{"x":1}}`
	if err := os.WriteFile(RegistryPath(state), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := ReadRegistry(state)
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsPool("vm") || r.IsPool("cap-gate") {
		t.Fatalf("IsPool: %+v", r)
	}
	lk, err := AcquireLock(state, LockOptions{Op: "test", Start: time.Now(), Wait: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Release()
	r, err = UpdateRegistry(state, lk, func(r *Registry) error { r.Pools = append(r.Pools, "tests"); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if r.Generation != 8 {
		t.Fatalf("generation %d, want 8", r.Generation)
	}
	b, _ := os.ReadFile(RegistryPath(state))
	if !strings.Contains(string(b), `"future"`) || !strings.Contains(string(b), `"pools": [`+"\n"+`    "builds",`) {
		t.Fatalf("unknown field lost or pools not sorted:\n%s", b)
	}
	if err := writeRegistry(state, nil, r); err == nil {
		t.Fatal("writing machine.json without machine.lock must fail")
	}
}

func TestReadRegistryFailsClosed(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"{", "machine-state: machine.json: unexpected end of JSON input; run incoda doctor"},
		{`{"schema":1,"layout":1,"generation":1,"pools":[]}`, "machine-state: machine.json: schema 1, layout 1 is not a registry this incoda reads; run incoda doctor"},
		{`{"schema":1,"layout":2,"generation":1,"pools":["a/b"]}`, `machine-state: machine.json: pool "a/b" is not a valid key; run incoda doctor`},
		{`{"schema":2,"layout":2,"generation":1,"pools":[]}`, "machine-state: machine.json was written by a newer incoda; upgrade this one ("},
		{`{"schema":1,"layout":3,"generation":1,"pools":[]}`, "machine-state: machine.json was written by a newer incoda; upgrade this one ("},
	} {
		state := t.TempDir()
		if err := os.WriteFile(RegistryPath(state), []byte(tc.body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := ReadRegistry(state)
		var se *StateError
		if !errors.As(err, &se) || !strings.HasPrefix(se.Msg, tc.want) {
			t.Fatalf("%s: got %v, want prefix %q", tc.body, err, tc.want)
		}
	}
	state := t.TempDir()
	if err := os.Mkdir(RegistryPath(state), 0o755); err != nil {
		t.Fatal(err)
	}
	var se *StateError
	if _, err := ReadRegistry(state); !errors.As(err, &se) || !strings.HasPrefix(se.Msg, "machine-state: machine.json: ") || !strings.HasSuffix(se.Msg, "; run incoda doctor") {
		t.Fatalf("an unreadable machine.json fails closed, got %v", err)
	}
}

func TestBootstrapPools(t *testing.T) {
	got := strings.Join(BootstrapPools(), ",")
	if got != "builds,computer-use,tests,vm" {
		t.Fatalf("bootstrap pools %s", got)
	}
}
