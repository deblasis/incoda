package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
)

func TestPoolsValue(t *testing.T) {
	var v poolsValue
	if err := v.Set("tests, builds,tests"); err != nil || v.String() != "builds,tests" || !v.set {
		t.Fatalf("got %q %v", v.String(), err)
	}
	for _, bad := range []string{"", "a,,b", "bad key", "a/b"} {
		if err := (&poolsValue{}).Set(bad); err == nil {
			t.Errorf("Set(%q) must fail", bad)
		}
	}
}

func TestLinkEdit(t *testing.T) {
	yes := true
	unlinked := lane.Config{}
	linked := lane.Config{Pools: []string{"tests", "builds"}}
	for _, c := range []struct {
		name  string
		e     linkEdit
		cur   lane.Config
		want  string
		quiet bool
		err   string
	}{
		{"pool on unlinked", linkEdit{set: []string{"tests"}}, unlinked, "tests", false, ""},
		{"same pool on linked", linkEdit{set: []string{"builds", "tests"}}, linked, "builds,tests", false, ""},
		{"other pool on linked", linkEdit{set: []string{"vm"}}, linked, "", false, `link-exists: "k" is linked to builds,tests; changing a link is the user's call: ask them`},
		{"replace", linkEdit{set: []string{"vm"}, replace: true}, linked, "vm", false, ""},
		{"add on unlinked", linkEdit{add: []string{"vm"}}, unlinked, "vm", false, ""},
		{"add", linkEdit{add: []string{"vm"}}, linked, "builds,tests,vm", false, ""},
		{"add contained", linkEdit{add: []string{"tests"}}, linked, "builds,tests", false, ""},
		{"remove", linkEdit{remove: []string{"builds"}}, linked, "tests", false, ""},
		{"remove last", linkEdit{remove: []string{"builds", "tests"}}, linked, "", false, `--remove-pool would leave "k" linked to no pool; use --unlink to remove the link`},
		{"remove on unlinked", linkEdit{remove: []string{"vm"}}, unlinked, "(none)", false, ""},
		{"unlink", linkEdit{unlink: true}, linked, "(none)", false, ""},
		{"quiet", linkEdit{quiet: &yes}, linked, "builds,tests", true, ""},
	} {
		pools, quiet, err := c.e.apply("k", c.cur)
		if c.err != "" {
			var rf *machine.Refusal
			if !errors.As(err, &rf) || rf.Msg != c.err {
				t.Errorf("%s: err %v, want %q", c.name, err, c.err)
			}
			continue
		}
		if err != nil || machine.SetText(pools) != c.want || quiet != c.quiet {
			t.Errorf("%s: %v %v %v", c.name, pools, quiet, err)
		}
	}
	for _, e := range []linkEdit{
		{set: []string{"a"}, add: []string{"b"}},
		{set: []string{"a"}, unlink: true},
		{unlink: true, remove: []string{"a"}},
		{replace: true},
	} {
		if err := e.check(); err == nil || !strings.Contains(err.Error(), "--") {
			t.Errorf("%+v must be refused: %v", e, err)
		}
	}
}
