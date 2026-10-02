// Package runplan works out what a run takes (spec 2.4, 2.5): its lane
// set, which is the named keys plus each named project key's linked pools,
// deduplicated; the one total order every run joins them in (project lanes
// sorted by key, then pools sorted by key); and the rules every lane of the
// set is checked against before any ticket exists.
//
// It reads machine.json and the lanes' config.json files and nothing
// else: it never opens, creates or locks a lane, so a refused run leaves no
// directory, ticket or log behind (spec 4.1). Its refusals are
// *machine.Refusal (exit 120) and *machine.StateError (exit 122).
package runplan

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/deblasis/incoda/internal/fixline"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/textsafe"
)

// Request is what a run asks for.
type Request struct {
	// Named are the --queue keys, validated.
	Named []string
	// Slots is --slots (0 when not given), Exclusive --exclusive and
	// Reason --reason.
	Slots     int
	Exclusive bool
	Reason    string
	// Held is the set of keys a verified ancestor holds (P, spec 2.6). A
	// pool in it that the run does not name is passed through and not
	// re-checked: the ancestor's ticket was admitted under its rules
	// (spec 4.5).
	Held map[string]bool
	// Fix is the caller's own run line for printed fix lines: every flag
	// it gave except --queue and --pool, its argv, and its directory as
	// Dir and Here. Queue and Pool are set by each refusal. WaitGiven says
	// whether --wait was among the flags.
	Fix       fixline.Run
	WaitGiven bool
}

// Lane is one lane of a plan.
type Lane struct {
	Key string
	// Pool is the lane's kind, from machine.json.
	Pool bool
	// Named is set when --queue names the lane.
	Named bool
	// Via are, for a pool, the named project keys whose link brings it
	// into the run, sorted; empty when the pool is only named directly.
	Via []string
	// Cfg is the lane's config as read at plan time.
	Cfg lane.Config
}

// Role says how a pool lane is reached, for busy, holder, timeout and
// refusal lines (spec 5.1): "pool, via cap-gate", "pool" for direct use,
// and "" for a project lane, whose lines stay as they were.
func (l Lane) Role() string {
	switch {
	case !l.Pool:
		return ""
	case len(l.Via) == 0:
		return "pool"
	}
	return "pool, via " + strings.Join(l.Via, ",")
}

// StatusKey is the key a timeout line sends the caller to: the run's own
// project key for a pool reached through a link, else the lane itself.
func (l Lane) StatusKey() string {
	if len(l.Via) > 0 {
		return l.Via[0]
	}
	return l.Key
}

// Plan is a run's lane set in total order and what it was computed from.
type Plan struct {
	Lanes []Lane
	// Generation is machine.json's generation at plan time.
	Generation int64
	// Links is each named project key's link (its pools, sorted) as read
	// at plan time; an unlinked key maps to nil.
	Links map[string][]string
}

// Less is the total order of spec 2.4: project lanes before pools, each
// group by key in byte order.
func Less(a, b Lane) bool {
	if a.Pool != b.Pool {
		return !a.Pool
	}
	return a.Key < b.Key
}

func sortLanes(ls []Lane) { sort.Slice(ls, func(i, j int) bool { return Less(ls[i], ls[j]) }) }

// Make computes the plan for req against reg and the lanes' configs.
func Make(stateDir string, reg *machine.Registry, req Request) (*Plan, error) {
	p := &Plan{Generation: reg.Generation, Links: map[string][]string{}}
	lanes := map[string]*Lane{}
	named := append([]string(nil), req.Named...)
	sort.Strings(named)

	// Named keys first: a closed lane is refused for being closed before
	// anything else is said about it (spec 4.1).
	for _, k := range named {
		cfg, err := readConfig(stateDir, k)
		if err != nil {
			return nil, err
		}
		l := &Lane{Key: k, Pool: reg.IsPool(k), Named: true, Cfg: cfg}
		if cfg.Closed != "" {
			return nil, closedRefusal(*l)
		}
		lanes[k] = l
	}

	// A project lane with no link refuses the run before any ticket, with
	// its suggestion and the line that makes it (spec 4.1).
	var unlinked []string
	projects := 0
	for _, k := range named {
		if l := lanes[k]; !l.Pool {
			projects++
			if len(l.Cfg.Pools) == 0 {
				unlinked = append(unlinked, k)
			}
		}
	}
	if len(unlinked) > 0 {
		return nil, unlinkedRefusal(stateDir, reg, req, unlinked, projects)
	}

	// Each named project key brings its linked pools. Every one must
	// resolve to a registered pool with a readable config: the lane set
	// never shrinks silently (spec 2.5).
	for _, k := range named {
		l := lanes[k]
		if l.Pool {
			continue
		}
		link := machine.SortedSet(l.Cfg.Pools)
		p.Links[k] = link
		for _, pool := range link {
			pl, err := linkedPool(stateDir, reg, lanes, k, pool)
			if err != nil {
				return nil, err
			}
			pl.Via = append(pl.Via, k)
		}
	}

	for _, l := range lanes {
		p.Lanes = append(p.Lanes, *l)
	}
	sortLanes(p.Lanes)

	for _, l := range p.Lanes {
		if l.Pool && !l.Named && req.Held[l.Key] {
			continue
		}
		if !l.Named && l.Cfg.Closed != "" {
			return nil, closedRefusal(l)
		}
		if l.Cfg.RequireReason && strings.TrimSpace(req.Reason) == "" {
			return nil, reasonRefusal(l)
		}
		if !l.Named {
			continue
		}
		// --slots applies to named keys. A pool ticket always carries the
		// pool's configured count (a pool without one is one slot wide), so
		// a --slots on a named pool is only checked against it, never
		// written (spec 2.4).
		configured := l.Cfg.Slots
		if l.Pool && configured < 1 {
			configured = 1
		}
		if configured > 0 && req.Slots >= 1 && req.Slots != configured {
			return nil, &machine.Refusal{Msg: lane.NewSlotsDisagreement(l.Key, configured, req.Slots, req.Exclusive).Error()}
		}
	}
	return p, nil
}

// linkedPool returns the plan lane of pool, which project key links,
// reading its config the first time.
func linkedPool(stateDir string, reg *machine.Registry, lanes map[string]*Lane, project, pool string) (*Lane, error) {
	if pl := lanes[pool]; pl != nil {
		return pl, nil
	}
	if !reg.IsPool(pool) {
		return nil, linkStateError(project, pool, "it is not a pool on this machine")
	}
	cfg, err := lane.ReadConfig(lane.LaneDir(stateDir, pool))
	if err != nil {
		return nil, linkStateError(project, pool, textsafe.Escape(err.Error()))
	}
	pl := &Lane{Key: pool, Pool: true, Cfg: cfg}
	lanes[pool] = pl
	return pl, nil
}

func linkStateError(project, pool, reason string) *machine.StateError {
	return &machine.StateError{Msg: fmt.Sprintf("machine-state: queue %q links %q: %s", project, pool, reason)}
}

// readConfig reads a named key's config without opening the lane. A
// config written by a newer incoda fails closed with machine-state; any
// other unreadable config fails the run as it always did.
func readConfig(stateDir, key string) (lane.Config, error) {
	cfg, err := lane.ReadConfig(lane.LaneDir(stateDir, key))
	var ns *lane.NewerSchemaError
	switch {
	case errors.As(err, &ns):
		return cfg, &machine.StateError{Msg: "machine-state: " + textsafe.Escape(err.Error())}
	case err != nil:
		return cfg, &machine.StateError{Msg: fmt.Sprintf("queue %q: %s", key, textsafe.Escape(err.Error()))}
	}
	return cfg, nil
}

// withRole appends " (<role>)" for a pool.
func withRole(l Lane) string {
	if r := l.Role(); r != "" {
		return " (" + r + ")"
	}
	return ""
}

// closedRefusal is the closed refusal of spec 4.5: today's text, plus the
// path for a pool.
func closedRefusal(l Lane) *machine.Refusal {
	return &machine.Refusal{Msg: fmt.Sprintf("queue %q is closed: %s%s", l.Key, textsafe.Escape(l.Cfg.Closed), withRole(l))}
}

// reasonRefusal is the require_reason refusal of spec 4.5.
func reasonRefusal(l Lane) *machine.Refusal {
	return &machine.Refusal{Msg: fmt.Sprintf("queue %q requires --reason%s: say what this job is so status can answer \"whose is that and why\"", l.Key, withRole(l))}
}
