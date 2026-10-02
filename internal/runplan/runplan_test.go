package runplan

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
)

// machineDir writes a migrated-looking state directory: machine.json with
// the four bootstrap pools at generation 3, and a config.json per lane in
// configs (a nil value writes no file).
func machineDir(t *testing.T, configs map[string]string) (string, *machine.Registry) {
	t.Helper()
	state := t.TempDir()
	reg := `{"schema":1,"layout":2,"generation":3,"pools":["builds","computer-use","tests","vm"]}`
	if err := os.WriteFile(machine.RegistryPath(state), []byte(reg), 0o644); err != nil {
		t.Fatal(err)
	}
	for k, body := range configs {
		dir := lane.LaneDir(state, k)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := machine.ReadRegistry(state)
	if err != nil {
		t.Fatal(err)
	}
	return state, r
}

func keysOf(p *Plan) string {
	var out []string
	for _, l := range p.Lanes {
		s := l.Key
		if r := l.Role(); r != "" {
			s += "(" + r + ")"
		}
		out = append(out, s)
	}
	return strings.Join(out, " ")
}

// TestLaneSetAndTotalOrder: the named keys plus each named project's
// linked pools, deduplicated, project lanes first then pools, each sorted
// (spec 2.4).
func TestLaneSetAndTotalOrder(t *testing.T) {
	state, reg := machineDir(t, map[string]string{
		"kungfoo-gate": `{"schema":2,"pools":["tests"]}`,
		"cap-e2e":      `{"schema":2,"pools":["tests","computer-use"]}`,
		"builds":       `{"schema":2,"slots":1}`,
	})
	for _, c := range []struct {
		named []string
		want  string
	}{
		{[]string{"kungfoo-gate"}, "kungfoo-gate tests(pool, via kungfoo-gate)"},
		{[]string{"kungfoo-gate", "builds"}, "kungfoo-gate builds(pool) tests(pool, via kungfoo-gate)"},
		{[]string{"kungfoo-gate", "cap-e2e"}, "cap-e2e kungfoo-gate computer-use(pool, via cap-e2e) tests(pool, via cap-e2e,kungfoo-gate)"},
		{[]string{"tests", "kungfoo-gate"}, "kungfoo-gate tests(pool, via kungfoo-gate)"},
		{[]string{"plain"}, "plain"},
	} {
		p, err := Make(state, reg, Request{Named: c.named})
		if err != nil {
			t.Fatalf("%v: %v", c.named, err)
		}
		if got := keysOf(p); got != c.want {
			t.Errorf("%v:\n got %s\nwant %s", c.named, got, c.want)
		}
		if p.Generation != 3 {
			t.Errorf("generation %d", p.Generation)
		}
	}
	p, _ := Make(state, reg, Request{Named: []string{"tests", "kungfoo-gate"}})
	if l := p.Lanes[1]; !l.Named || !l.Pool || l.StatusKey() != "kungfoo-gate" {
		t.Fatalf("a pool named and linked is both: %+v", l)
	}
	if strings.Join(p.Links["kungfoo-gate"], ",") != "tests" {
		t.Fatalf("links: %v", p.Links)
	}
}

// TestMakeRules: closed, require_reason and --slots, with the pool path in
// the text; a pool an ancestor holds is not re-checked; links that do not
// resolve and newer configs fail closed; nothing is created.
func TestMakeRules(t *testing.T) {
	state, reg := machineDir(t, map[string]string{
		"cap-e2e":       `{"schema":2,"pools":["computer-use","vm"]}`,
		"vm":            `{"schema":2,"slots":1,"closed":"maintenance"}`,
		"builds":        `{"schema":2,"slots":1,"require_reason":true}`,
		"kungfoo-build": `{"schema":2,"pools":["builds"],"slots":2}`,
		"dangling":      `{"schema":2,"pools":["printer"]}`,
		"broken-link":   `{"schema":2,"pools":["tests"]}`,
		"tests":         `{`,
		"newer":         `{"schema":9}`,
		"shut":          `{"schema":2,"closed":"use cap-e2e","pools":["vm"]}`,
	})
	for _, c := range []struct {
		req   Request
		state bool
		want  string
	}{
		{Request{Named: []string{"cap-e2e"}}, false, `queue "vm" is closed: maintenance (pool, via cap-e2e)`},
		{Request{Named: []string{"vm"}}, false, `queue "vm" is closed: maintenance (pool)`},
		{Request{Named: []string{"shut"}}, false, `queue "shut" is closed: use cap-e2e`},
		{Request{Named: []string{"kungfoo-build"}}, false, `queue "builds" requires --reason (pool, via kungfoo-build): say what this job is so status can answer "whose is that and why"`},
		{Request{Named: []string{"builds"}, Reason: "x", Slots: 2}, false, `queue "builds" is configured for 1 slot(s); --slots 2 is not allowed to disagree.`},
		{Request{Named: []string{"kungfoo-build"}, Reason: "x", Slots: 1}, false, `queue "kungfoo-build" is configured for 2 slot(s); --slots 1 is not allowed to disagree.`},
		{Request{Named: []string{"dangling"}}, true, `machine-state: queue "dangling" links "printer": it is not a pool on this machine`},
		{Request{Named: []string{"broken-link"}}, true, `machine-state: queue "broken-link" links "tests": config `},
		{Request{Named: []string{"newer"}}, true, "machine-state: "},
	} {
		_, err := Make(state, reg, c.req)
		var rf *machine.Refusal
		var se *machine.StateError
		switch {
		case c.state && errors.As(err, &se) && strings.HasPrefix(se.Msg, c.want):
		case !c.state && errors.As(err, &rf) && strings.HasPrefix(rf.Msg, c.want):
		default:
			t.Errorf("%v: got %v, want %q", c.req.Named, err, c.want)
		}
	}
	if _, err := Make(state, reg, Request{Named: []string{"kungfoo-build"}, Reason: "x", Slots: 2}); err != nil {
		t.Fatalf("--slots agreeing with the project lane is fine and never checked against its pools: %v", err)
	}
	if _, err := Make(state, reg, Request{Named: []string{"cap-e2e"}, Held: map[string]bool{"vm": true}}); err != nil {
		t.Fatalf("a pool an ancestor holds is passed through, not re-checked: %v", err)
	}
	if _, err := Make(state, reg, Request{Named: []string{"vm"}, Held: map[string]bool{"vm": true}}); err == nil {
		t.Fatal("a pool the run names is checked, held or not")
	}
	if _, err := Make(state, reg, Request{Named: []string{"typo-key"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lane.LaneDir(state, "typo-key")); !os.IsNotExist(err) {
		t.Fatal("planning must not create a lane")
	}
}
