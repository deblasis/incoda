package cli

import (
	"fmt"
	"strings"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/textsafe"
)

// poolsValue parses a comma-separated set of keys (--pool and its alias
// --pools, --add-pool, --remove-pool): order does not matter and
// duplicates are dropped, so the result is sorted.
type poolsValue struct {
	keys []string
	set  bool
}

func (v *poolsValue) String() string {
	if v == nil {
		return ""
	}
	return strings.Join(v.keys, ",")
}

func (v *poolsValue) Set(s string) error {
	var keys []string
	for _, k := range strings.Split(s, ",") {
		k = strings.TrimSpace(k)
		if k == "" {
			return fmt.Errorf("empty key in the list %q", s)
		}
		if err := lane.ValidateKey(k); err != nil {
			return err
		}
		keys = append(keys, k)
	}
	v.keys, v.set = machine.SortedSet(keys), true
	return nil
}

// linkEdit is what config's link flags ask for (spec 4.3).
type linkEdit struct {
	set, add, remove []string // nil when the flag is absent
	replace, unlink  bool
	quiet            *bool // nil when --quiet-machine is absent
}

// touchesPools reports whether the edit changes the pools of the link.
func (e linkEdit) touchesPools() bool {
	return e.set != nil || e.add != nil || e.remove != nil || e.unlink
}

// any reports whether the edit is a link write at all.
func (e linkEdit) any() bool { return e.touchesPools() || e.quiet != nil }

// check refuses flag combinations that contradict each other.
func (e linkEdit) check() error {
	switch {
	case e.set != nil && (e.add != nil || e.remove != nil || e.unlink):
		return usagef("--pool sets the whole link; it does not combine with --add-pool, --remove-pool or --unlink")
	case e.unlink && (e.add != nil || e.remove != nil):
		return usagef("--unlink removes the link; it does not combine with --add-pool or --remove-pool")
	case e.replace && e.set == nil:
		return usagef("--replace goes with --pool")
	}
	return nil
}

// named is every pool the edit adds to a link; each must be registered.
func (e linkEdit) named() []string { return append(append([]string(nil), e.set...), e.add...) }

// apply computes the link the edit leaves on a lane whose current config
// is cur: --pool on an unlinked lane sets it, and on a linked one only with
// --replace (else link-exists), unless it names the same set; --add-pool
// on an unlinked lane is --pool; --remove-pool never leaves an empty link.
func (e linkEdit) apply(key string, cur lane.Config) ([]string, bool, error) {
	pools := machine.SortedSet(cur.Pools)
	linked := len(pools) > 0
	switch {
	case e.set != nil:
		if linked && !machine.SameSet(pools, e.set) && !e.replace {
			return nil, false, &machine.Refusal{Msg: fmt.Sprintf("link-exists: %q is linked to %s; changing a link is the user's call: ask them", key, textsafe.Escape(machine.SetText(pools)))}
		}
		pools = machine.SortedSet(e.set)
	case e.unlink:
		pools = nil
	default:
		if e.add != nil {
			pools = machine.SortedSet(append(pools, e.add...))
		}
		if e.remove != nil && linked {
			gone := map[string]bool{}
			for _, k := range e.remove {
				gone[k] = true
			}
			var left []string
			for _, k := range pools {
				if !gone[k] {
					left = append(left, k)
				}
			}
			if len(left) == 0 {
				return nil, false, &machine.Refusal{Msg: fmt.Sprintf("--remove-pool would leave %q linked to no pool; use --unlink to remove the link", key)}
			}
			pools = left
		}
	}
	quiet := cur.QuietMachine
	if e.quiet != nil {
		quiet = *e.quiet
	}
	return pools, quiet, nil
}
