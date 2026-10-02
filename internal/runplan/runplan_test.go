package runplan

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/fixline"
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
		{[]string{"builds"}, "builds(pool)"},
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
	var rf *machine.Refusal
	if _, err := Make(state, reg, Request{Named: []string{"typo-key"}}); !errors.As(err, &rf) || !strings.HasPrefix(rf.Msg, "unlinked: typo-key\n") {
		t.Fatalf("an unlinked key is refused: %v", err)
	}
	if _, err := os.Stat(lane.LaneDir(state, "typo-key")); !os.IsNotExist(err) {
		t.Fatal("planning must not create a lane")
	}
}

// spec4Pools is the pool configs of the spec 4.1 example.
var spec4Pools = map[string]string{
	"builds":       `{"schema":2,"slots":1,"description":"heavy compiler/toolchain builds (LLVM links, toolchain rebuilds)"}`,
	"computer-use": `{"schema":2,"slots":1,"description":"drives the desktop, a browser or a dev server"}`,
	"tests":        `{"schema":2,"slots":1,"description":"test suites and gates"}`,
	"vm":           `{"schema":2,"slots":1,"description":"the VM host"}`,
}

const spec4Rows = `incoda: pools on this machine:
incoda:   builds        1 slot   heavy compiler/toolchain builds (LLVM links, toolchain re...
incoda:   computer-use  1 slot   drives the desktop, a browser or a dev server
incoda:   tests         1 slot   test suites and gates
incoda:   vm            1 slot   the VM host
`

// TestUnlinkedRefusal: the texts of spec 4.1, byte for byte, as the CLI
// prints them ("incoda: " before the message). POSIX quoting.
func TestUnlinkedRefusal(t *testing.T) {
	if fixline.Native() != fixline.POSIX {
		t.Skip("the expected lines are POSIX sh")
	}
	configs := map[string]string{
		"linked-gate": `{"schema":2,"pools":["tests"]}`,
		"strict-gate": `{"schema":2,"require_reason":true}`,
	}
	for k, v := range spec4Pools {
		configs[k] = v
	}
	state, reg := machineDir(t, configs)
	fix := func(argv ...string) fixline.Run {
		return fixline.Run{Flags: []fixline.Flag{{Name: "reason", Value: "wintty gate"}}, Argv: argv, Dir: "/src", Here: "/src"}
	}
	for _, c := range []struct {
		name string
		req  Request
		want string
	}{
		{"suggestion", Request{Named: []string{"wintty-gate"}, Reason: "wintty gate", Fix: fix("just", "gate")}, `incoda: unlinked: wintty-gate
incoda: queue "wintty-gate" is not linked to any pool; every project queue names the machine-wide pools its jobs use.
` + spec4Rows + `incoda: suggested: tests (name matches *-gate)
incoda: to link it to the suggestion (stored; every later run on this queue takes these pools):
incoda:   incoda run --queue wintty-gate --pool tests --reason 'wintty gate' -- 'just' 'gate'
incoda: if the suggestion does not fit, ask the user; they run: incoda link wintty-gate
`},
		{"no suggestion", Request{Named: []string{"polymatto"}, Fix: fix("pnpm", "build")}, `incoda: unlinked: polymatto
incoda: queue "polymatto" is not linked to any pool; every project queue names the machine-wide pools its jobs use.
` + spec4Rows + `incoda: suggested: none (no name pattern matches)
incoda: ask the user which pools this queue's jobs use; they run: incoda link polymatto
incoda: (incoda init links every queue in one pass)
incoda: do not pick a pool yourself, and never because it is free.
`},
		{"quiet suggestion adds --wait 5m", Request{Named: []string{"kungfoo-measure"}, Reason: "wintty gate", Fix: fix("just", "measure")}, `incoda: unlinked: kungfoo-measure
incoda: queue "kungfoo-measure" is not linked to any pool; every project queue names the machine-wide pools its jobs use.
` + spec4Rows + `incoda: suggested: tests, quiet_machine (name matches *-measure)
incoda: to link it to the suggestion (stored; every later run on this queue takes these pools and quiet-machine):
incoda:   incoda run --queue kungfoo-measure --pool tests --reason 'wintty gate' --wait '5m' -- 'just' 'measure'
incoda: (--wait 5m added: quiet-machine holds every pool it has drained while it waits for the rest)
incoda: if the suggestion does not fit, ask the user; they run: incoda link kungfoo-measure
`},
		{"several keys", Request{Named: []string{"cap-gate", "cap-e2e", "builds"}, Reason: "wintty gate", Fix: fix("just", "e2e")}, `incoda: unlinked: cap-e2e, cap-gate
incoda: queues "cap-e2e", "cap-gate" are not linked to any pool; every project queue names the machine-wide pools its jobs use.
` + spec4Rows + `incoda: suggested: cap-e2e -> computer-use,tests (name matches *-e2e); cap-gate -> tests (name matches *-gate)
incoda: to link them to the suggestions (stored; every later run on these queues takes these pools), then run:
incoda:   incoda config cap-e2e --pool computer-use,tests
incoda:   incoda config cap-gate --pool tests
incoda:   incoda run --queue cap-e2e,cap-gate,builds --reason 'wintty gate' -- 'just' 'e2e'
incoda: if a suggestion does not fit, ask the user; they run: incoda link cap-e2e, incoda link cap-gate
`},
		{"several keys, one without a suggestion", Request{Named: []string{"cap-gate", "polymatto"}, Fix: fix("x")}, `incoda: unlinked: cap-gate, polymatto
incoda: queues "cap-gate", "polymatto" are not linked to any pool; every project queue names the machine-wide pools its jobs use.
` + spec4Rows + `incoda: suggested: cap-gate -> tests (name matches *-gate); polymatto: none (no name pattern matches)
incoda: ask the user which pools these queues' jobs use; they run: incoda link cap-gate, incoda link polymatto
incoda: (incoda init links every queue in one pass)
incoda: do not pick a pool yourself, and never because it is free.
`},
		{"one unlinked key next to a linked one", Request{Named: []string{"linked-gate", "new-gate"}, Reason: "wintty gate", Fix: fix("x")}, `incoda: unlinked: new-gate
incoda: queue "new-gate" is not linked to any pool; every project queue names the machine-wide pools its jobs use.
` + spec4Rows + `incoda: suggested: tests (name matches *-gate)
incoda: to link it to the suggestion (stored; every later run on this queue takes these pools), then run:
incoda:   incoda config new-gate --pool tests
incoda:   incoda run --queue linked-gate,new-gate --reason 'wintty gate' -- 'x'
incoda: if the suggestion does not fit, ask the user; they run: incoda link new-gate
`},
		{"a lane requires a reason the run lacks", Request{Named: []string{"strict-gate"}, Fix: fixline.Run{Argv: []string{"x"}, Dir: "/src", Here: "/src"}}, `incoda: unlinked: strict-gate
incoda: queue "strict-gate" is not linked to any pool; every project queue names the machine-wide pools its jobs use.
` + spec4Rows + `incoda: suggested: tests (name matches *-gate)
incoda: to link it to the suggestion (stored; every later run on this queue takes these pools):
incoda: no runnable command (queue "strict-gate" requires --reason and this run has none); run it with these fields:
incoda:   queue: strict-gate
incoda:   pool: tests
incoda:   flags: (none)
incoda:   cwd: /src
incoda:   command: "x"
incoda: if the suggestion does not fit, ask the user; they run: incoda link strict-gate
`},
	} {
		_, err := Make(state, reg, c.req)
		var rf *machine.Refusal
		if !errors.As(err, &rf) {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got := "incoda: " + rf.Msg + "\n"; got != c.want {
			t.Errorf("%s:\n got:\n%s\nwant:\n%s", c.name, got, c.want)
		}
	}
}

func TestSuggest(t *testing.T) {
	_, reg := machineDir(t, nil)
	if s := Suggest(reg, "cap-e2e"); !s.Usable || s.Text() != "computer-use,tests" {
		t.Fatalf("%+v", s)
	}
	if s := Suggest(reg, "kf-measure"); !s.Usable || s.Text() != "tests, quiet_machine" {
		t.Fatalf("%+v", s)
	}
	if s := Suggest(reg, "polymatto"); s.Usable || s.Why != "no name pattern matches" {
		t.Fatalf("%+v", s)
	}
	reg.Pools = []string{"builds", "tests"}
	if s := Suggest(reg, "cap-e2e"); s.Usable || s.Why != "the suggested pools computer-use,tests are not all pools on this machine" {
		t.Fatalf("a suggestion naming a missing pool is not usable: %+v", s)
	}
}

// TestPoolFlag: --pool takes a subset of each named project key's link;
// on an unlinked key only the suggestion becomes a first link; anything
// else is refused before anything is written (spec 4.2).
func TestPoolFlag(t *testing.T) {
	if fixline.Native() != fixline.POSIX {
		t.Skip("the expected lines are POSIX sh")
	}
	configs := map[string]string{
		"polymatto": `{"schema":2,"pools":["builds","computer-use","tests"]}`,
		"kf-gate":   `{"schema":2,"pools":["tests"]}`,
	}
	for k, v := range spec4Pools {
		configs[k] = v
	}
	state, reg := machineDir(t, configs)
	fix := fixline.Run{Flags: []fixline.Flag{{Name: "reason", Value: "prod build"}}, Argv: []string{"pnpm", "build"}, Dir: "/src", Here: "/src"}
	req := func(named []string, pool ...string) Request {
		return Request{Named: named, Pool: pool, Reason: "prod build", Fix: fix}
	}
	p, err := Make(state, reg, req([]string{"polymatto"}, "tests"))
	if err != nil || keysOf(p) != "polymatto tests(pool, via polymatto)" || len(p.FirstLinks) != 0 {
		t.Fatalf("a subset of the link: %v %v", keysOf(p), err)
	}
	p, err = Make(state, reg, req([]string{"polymatto", "builds"}, "tests"))
	if err != nil || keysOf(p) != "polymatto builds(pool) tests(pool, via polymatto)" || strings.Join(p.Notes, "|") != `--pool ignored for pool key "builds"` {
		t.Fatalf("a named pool next to a project key ignores --pool: %v %v %v", keysOf(p), p.Notes, err)
	}
	p, err = Make(state, reg, req([]string{"cap-e2e"}, "tests", "computer-use"))
	if err != nil || len(p.FirstLinks) != 1 || p.FirstLinks[0].Key != "cap-e2e" || strings.Join(p.FirstLinks[0].Pools, ",") != "computer-use,tests" ||
		keysOf(p) != "cap-e2e computer-use(pool, via cap-e2e) tests(pool, via cap-e2e)" {
		t.Fatalf("a first link equal to the suggestion: %v %+v %v", keysOf(p), p.FirstLinks, err)
	}
	if p, err := Make(state, reg, req([]string{"kf-measure"}, "tests")); err != nil || !p.FirstLinks[0].Quiet {
		t.Fatalf("the first link carries quiet_machine with its suggestion: %+v %v", p, err)
	}
	for _, c := range []struct {
		name string
		req  Request
		want string
	}{
		{"not part of the link", req([]string{"polymatto"}, "vm"), `incoda: pool-mismatch: "polymatto" is linked to builds,computer-use,tests; --pool vm is not part of it
incoda: to also hold vm for this run only, name it next to the queue (no link change):
incoda:   incoda run --queue polymatto,vm --reason 'prod build' -- 'pnpm' 'build'
incoda: changing the link is the user's call; ask them.
`},
		{"partly part of the link", req([]string{"polymatto"}, "tests", "vm"), `incoda: pool-mismatch: "polymatto" is linked to builds,computer-use,tests; --pool vm is not part of it
incoda: to also hold vm for this run only, name it next to the queue (no link change):
incoda:   incoda run --queue polymatto,vm --pool tests --reason 'prod build' -- 'pnpm' 'build'
incoda: changing the link is the user's call; ask them.
`},
		{"a pool named in --queue and in --pool prints once", Request{Named: []string{"polymatto", "vm"}, Pool: []string{"vm"}, Reason: "prod build", Fix: fix}, `incoda: pool-mismatch: "polymatto" is linked to builds,computer-use,tests; --pool vm is not part of it
incoda: to also hold vm for this run only, name it next to the queue (no link change):
incoda:   incoda run --queue polymatto,vm --reason 'prod build' -- 'pnpm' 'build'
incoda: changing the link is the user's call; ask them.
`},
		{"no project key", req([]string{"builds"}, "tests"), `incoda: pool-mismatch: --pool needs a project key; "builds" is a pool
incoda: rerun without --pool:
incoda:   incoda run --queue builds --reason 'prod build' -- 'pnpm' 'build'
`},
		{"not a pool", req([]string{"polymatto"}, "printer"), `incoda: pool-mismatch: "printer" is not a pool on this machine (pools: builds, computer-use, tests, vm)
`},
		{"first link other than the suggestion", req([]string{"cap-e2e"}, "tests"), `incoda: link-needs-user: "cap-e2e" suggests computer-use,tests; a first link from run must equal it
incoda: run it with the suggestion instead (stored; every later run on this queue takes these pools):
incoda:   incoda run --queue cap-e2e --pool computer-use,tests --reason 'prod build' -- 'pnpm' 'build'
incoda: ask the user for anything else; they run: incoda link cap-e2e
`},
		{"first link without a suggestion", req([]string{"polymatto-x"}, "tests"), `incoda: link-needs-user: "polymatto-x" has no suggested pools; ask the user; they run: incoda link polymatto-x
`},
		{"one key refused refuses the run", req([]string{"cap-gate", "polymatto"}, "vm"), `incoda: link-needs-user: "cap-gate" suggests tests; a first link from run must equal it
incoda: link it to the suggestion instead (stored; every later run on this queue takes these pools), then run without --pool, which applies to every named project queue:
incoda:   incoda config cap-gate --pool tests
incoda:   incoda run --queue cap-gate,polymatto --reason 'prod build' -- 'pnpm' 'build'
incoda: ask the user for anything else; they run: incoda link cap-gate
`},
		// Two unlinked keys with different suggestions: a printed --pool
		// would be refused for one of them, so each gets its config line
		// and the run line has no --pool.
		{"several unlinked keys", req([]string{"cap-gate", "cap-e2e"}, "tests"), `incoda: link-needs-user: "cap-e2e" suggests computer-use,tests; a first link from run must equal it
incoda: link them to the suggestions instead (stored; every later run on these queues takes these pools), then run without --pool, which applies to every named project queue:
incoda:   incoda config cap-e2e --pool computer-use,tests
incoda:   incoda config cap-gate --pool tests
incoda:   incoda run --queue cap-e2e,cap-gate --reason 'prod build' -- 'pnpm' 'build'
incoda: ask the user for anything else; they run: incoda link cap-e2e
`},
		{"several unlinked keys, one without a suggestion", req([]string{"cap-e2e", "polymatto-x"}, "tests"), `incoda: link-needs-user: "cap-e2e" suggests computer-use,tests; a first link from run must equal it
incoda: no runnable line: "polymatto-x" has no usable suggestion (no name pattern matches); ask the user; they run: incoda link cap-e2e, incoda link polymatto-x
`},
		{"pool-mismatch with another project key", req([]string{"polymatto", "kf-gate"}, "vm"), `incoda: pool-mismatch: "kf-gate" is linked to tests; --pool vm is not part of it
incoda: to also hold vm for this run only, name it next to the queue (no link change; without --pool, which applies to every named project queue):
incoda:   incoda run --queue kf-gate,polymatto,vm --reason 'prod build' -- 'pnpm' 'build'
incoda: changing the link is the user's call; ask them.
`},
		{"pool-mismatch next to an unlinked key", req([]string{"kf-gate", "wintty-gate"}, "vm"), `incoda: pool-mismatch: "kf-gate" is linked to tests; --pool vm is not part of it
incoda: link wintty-gate to its suggestion first (stored; every later run on this queue takes these pools):
incoda:   incoda config wintty-gate --pool tests
incoda: to also hold vm for this run only, name it next to the queue (no link change; without --pool, which applies to every named project queue):
incoda:   incoda run --queue kf-gate,wintty-gate,vm --reason 'prod build' -- 'pnpm' 'build'
incoda: changing the link is the user's call; ask them.
`},
	} {
		_, err := Make(state, reg, c.req)
		var rf *machine.Refusal
		if !errors.As(err, &rf) {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got := "incoda: " + rf.Msg + "\n"; got != c.want {
			t.Errorf("%s:\n got:\n%s\nwant:\n%s", c.name, got, c.want)
		}
	}
}

// TestChanged: the replan triggers of spec 2.5 and nothing else.
func TestChanged(t *testing.T) {
	state, reg := machineDir(t, map[string]string{
		"p-gate": `{"schema":2,"pools":["builds","tests"]}`,
	})
	plan := func(pool ...string) *Plan {
		t.Helper()
		p, err := Make(state, reg, Request{Named: []string{"p-gate"}, Pool: pool})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	setReg := func(gen int, pools string) {
		body := fmt.Sprintf(`{"schema":1,"layout":2,"generation":%d,"pools":[%s]}`, gen, pools)
		if err := os.WriteFile(machine.RegistryPath(state), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	setLink := func(body string) {
		if err := os.WriteFile(filepath.Join(lane.LaneDir(state, "p-gate"), "config.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	changed := func(p *Plan) string {
		t.Helper()
		why, err := p.Changed(state)
		if err != nil {
			t.Fatal(err)
		}
		return why
	}
	p, sub := plan(), plan("tests")
	if why := changed(p); why != "" {
		t.Fatalf("nothing changed: %q", why)
	}
	setReg(4, `"builds","computer-use","tests","vm","printer"`)
	if why := changed(p); why != "" {
		t.Fatalf("a new generation that leaves the plan's kinds alone is no trigger: %q", why)
	}
	setReg(5, `"builds","computer-use","tests","p-gate"`)
	if why := changed(p); why != `queue "p-gate" became a pool` {
		t.Fatalf("kind change: %q", why)
	}
	setReg(6, `"computer-use","tests","vm"`)
	if why := changed(p); why != `pool "builds" left the registry` {
		t.Fatalf("pool removed: %q", why)
	}
	setReg(3, `"builds","computer-use","tests","vm"`)
	setLink(`{"schema":2,"pools":["tests","vm"]}`)
	if why := changed(p); why != `the link of "p-gate" changed: builds,tests -> tests,vm` {
		t.Fatalf("link change: %q", why)
	}
	if why := changed(sub); why != "" {
		t.Fatalf("a link change that keeps the --pool subset is no trigger: %q", why)
	}
	setLink(`{"schema":2,"pools":["vm"]}`)
	if why := changed(sub); why != `the link of "p-gate" changed: builds,tests -> vm` {
		t.Fatalf("a link change beyond the --pool subset: %q", why)
	}
	setLink(`{"schema":2,"pools":["builds","tests"]}`)
	if err := os.MkdirAll(lane.LaneDir(state, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lane.LaneDir(state, "tests"), "config.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	var se *machine.StateError
	if _, err := p.Changed(state); !errors.As(err, &se) || !strings.HasPrefix(se.Msg, `machine-state: queue "p-gate" links "tests": `) {
		t.Fatalf("a linked pool that stops resolving fails closed: %v", err)
	}
}
