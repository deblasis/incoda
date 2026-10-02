package machine

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/deblasis/incoda/internal/lane"
)

// SortedSet returns names sorted with duplicates dropped; nil stays nil.
func SortedSet(names []string) []string {
	if names == nil {
		return nil
	}
	seen := map[string]bool{}
	out := []string{}
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// SameSet reports whether a and b name the same keys, in any order.
func SameSet(a, b []string) bool {
	a, b = SortedSet(a), SortedSet(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Subset reports whether every key of a is in b.
func Subset(a, b []string) bool {
	in := map[string]bool{}
	for _, k := range b {
		in[k] = true
	}
	for _, k := range a {
		if !in[k] {
			return false
		}
	}
	return true
}

// SetText renders a set of pools the way links are printed: sorted and
// comma-separated, or "(none)".
func SetText(names []string) string {
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(SortedSet(names), ",")
}

// NotPools returns the names in names that are not registered pools,
// sorted.
func (r *Registry) NotPools(names []string) []string {
	var out []string
	for _, n := range SortedSet(names) {
		if !r.IsPool(n) {
			out = append(out, n)
		}
	}
	return out
}

// NotAPool is the refusal for a name that is not a registered pool where
// one is required (a link, a --pool set).
func NotAPool(r *Registry, names []string) *Refusal {
	what := fmt.Sprintf("%q is not a pool", names[0])
	if len(names) > 1 {
		quoted := make([]string, len(names))
		for i, n := range names {
			quoted[i] = fmt.Sprintf("%q", n)
		}
		what = strings.Join(quoted, ", ") + " are not pools"
	}
	return &Refusal{Msg: fmt.Sprintf("pool-mismatch: %s on this machine (pools: %s)", what, strings.Join(r.Pools, ", "))}
}

// LinkResult is what a link write found and left.
type LinkResult struct {
	// Old and New are the lane's config before and after; they are equal
	// when nothing was written.
	Old, New lane.Config
	// Changed is set when the config was written.
	Changed bool
	// Registry is machine.json as read under machine.lock.
	Registry *Registry
}

// WriteLink changes key's link (its pools and quiet_machine, and any other
// field fn sets in the same step) as spec 4.4 orders it: machine.lock
// first, then key's registry lock, one load-modify-store inside that hold.
// Under machine.lock it re-reads machine.json and refuses a key that is a
// pool (pools never link and carry no quiet_machine). fn gets the registry
// and the config read inside the hold, so a compare-and-set sees the value
// a concurrent writer left; it returns lane.ErrNoChange to write nothing,
// or a refusal. When the pools change, event=link by=<by> old=<pools>
// new=<pools> goes to the lane's lane.log.
//
// The lane is created when missing (a link on a fresh key is a project
// lane's first config). Registry lock waits stay inside o's --wait budget.
func WriteLink(stateDir, key, by string, o Options, fn func(reg *Registry, c *lane.Config) error) (LinkResult, error) {
	lk, err := AcquireLock(stateDir, o.lockOptions("link"))
	if err != nil {
		return LinkResult{}, err
	}
	defer lk.Release()
	reg, err := ReadRegistry(stateDir)
	if err != nil {
		return LinkResult{}, err
	}
	return writeLinkHeld(stateDir, key, by, o, reg, fn)
}

// writeLinkHeld is WriteLink once machine.lock is held and machine.json
// read: it refuses a pool key, then does the load-modify-store under key's
// registry lock and logs event=link when the pools change.
func writeLinkHeld(stateDir, key, by string, o Options, reg *Registry, fn func(reg *Registry, c *lane.Config) error) (LinkResult, error) {
	if reg.IsPool(key) {
		return LinkResult{}, &Refusal{Msg: fmt.Sprintf("pool-mismatch: %q is a pool; a pool never links other pools and carries no quiet_machine", key)}
	}
	q, err := lane.Open(stateDir, key)
	if err != nil {
		return LinkResult{}, stateErrorf("cannot open queue %q: %s", key, esc(err))
	}
	defer q.Close()
	q.SetBudget(o.Start, o.Wait)
	res := LinkResult{Registry: reg}
	cfg, err := q.UpdateConfig(func(c *lane.Config) error {
		res.Old = *c
		res.Old.Pools = append([]string(nil), c.Pools...)
		return fn(reg, c)
	})
	if errors.Is(err, lane.ErrNoChange) {
		res.New = cfg
		return res, nil
	}
	if err != nil {
		var rf *Refusal
		var ns *lane.NewerSchemaError
		switch {
		case errors.As(err, &rf):
			return res, err
		case errors.As(err, &ns):
			return res, stateErrorf("%s", esc(err))
		}
		return res, &StateError{Msg: fmt.Sprintf("queue %q: %s", key, esc(err))}
	}
	res.New, res.Changed = cfg, true
	if !SameSet(res.Old.Pools, cfg.Pools) {
		q.Logf("queue=%s event=link pid=%d by=%s old=%s new=%s", key, os.Getpid(), by,
			strings.Join(SortedSet(res.Old.Pools), ","), strings.Join(SortedSet(cfg.Pools), ","))
	}
	return res, nil
}

// FirstLink is a link a run writes on an unlinked key: Key gets Pools,
// and quiet_machine when Quiet is set (spec 4.2).
type FirstLink struct {
	Key   string
	Pools []string
	Quiet bool
}

// WriteFirstLinks writes every first link of one run under a single hold
// of machine.lock, so either all of them are written or none (spec 4.2: if
// any key would be refused, the whole run is refused before anything is
// written). Under that hold it re-reads machine.json, refusing a key that
// became a pool or a link naming a pool no longer registered, and reads
// every key's config: links change only under machine.lock, so what it
// reads cannot move until the hold ends. A key already linked to a
// different set refuses the whole run with link-conflict, naming the
// winner by the pid of the last event=link line in its lane.log, and
// nothing is written. Then each key still unlinked is written in key
// order, each under its own registry lock (lock order: machine.lock, then
// one registry lock at a time), and logged event=link by=<by>. A key
// already linked to the same set is left as it is (another run won the
// race to the same link). The results are in key order.
func WriteFirstLinks(stateDir, by string, o Options, links []FirstLink) ([]LinkResult, error) {
	links = append([]FirstLink(nil), links...)
	sort.Slice(links, func(i, j int) bool { return links[i].Key < links[j].Key })
	lk, err := AcquireLock(stateDir, o.lockOptions("link"))
	if err != nil {
		return nil, err
	}
	defer lk.Release()
	reg, err := ReadRegistry(stateDir)
	if err != nil {
		return nil, err
	}
	for _, fl := range links {
		if reg.IsPool(fl.Key) {
			return nil, &Refusal{Msg: fmt.Sprintf("pool-mismatch: %q is a pool; a pool never links other pools and carries no quiet_machine", fl.Key)}
		}
		if bad := reg.NotPools(fl.Pools); len(bad) > 0 {
			return nil, &StateError{Msg: fmt.Sprintf("machine-state: queue %q links %q: it is not a pool on this machine", fl.Key, bad[0])}
		}
		cfg, err := lane.ReadConfig(lane.LaneDir(stateDir, fl.Key))
		var ns *lane.NewerSchemaError
		switch {
		case errors.As(err, &ns):
			return nil, stateErrorf("%s", esc(err))
		case err != nil:
			return nil, &StateError{Msg: fmt.Sprintf("queue %q: %s", fl.Key, esc(err))}
		}
		if len(cfg.Pools) > 0 && !SameSet(cfg.Pools, fl.Pools) {
			return nil, linkConflict(stateDir, fl.Key, cfg.Pools)
		}
	}
	out := make([]LinkResult, 0, len(links))
	for _, fl := range links {
		res, err := writeLinkHeld(stateDir, fl.Key, by, o, reg, func(_ *Registry, c *lane.Config) error {
			switch {
			case len(c.Pools) == 0:
				c.Pools, c.QuietMachine = SortedSet(fl.Pools), c.QuietMachine || fl.Quiet
				return nil
			case SameSet(c.Pools, fl.Pools):
				return lane.ErrNoChange
			}
			// Not reached while every link writer takes machine.lock
			// first; kept so a compare-and-set never overwrites a link.
			return linkConflict(stateDir, fl.Key, c.Pools)
		})
		if err != nil {
			return out, err
		}
		out = append(out, res)
	}
	return out, nil
}

// linkConflict is the refusal of a first link that lost its race to a
// different set (spec 4.2).
func linkConflict(stateDir, key string, now []string) *Refusal {
	by := "another process"
	if pid, ok := LastLinker(stateDir, key); ok {
		by = fmt.Sprintf("pid %d", pid)
	}
	return &Refusal{Msg: fmt.Sprintf("link-conflict: %q was just linked to %s by %s; rerun without --pool", key, SetText(now), by)}
}

// LastLinker reads the pid of the last event=link line in a lane's log, so
// a lost compare-and-set can name the process that won it. It reports
// false when the log has none.
func LastLinker(stateDir, key string) (int, bool) {
	b, err := os.ReadFile(lane.LogPath(lane.LaneDir(stateDir, key)))
	if err != nil {
		return 0, false
	}
	lines := strings.Split(string(b), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if !strings.Contains(lines[i], " event=link ") {
			continue
		}
		for _, f := range strings.Fields(lines[i]) {
			if v, ok := strings.CutPrefix(f, "pid="); ok {
				var pid int
				if _, err := fmt.Sscanf(v, "%d", &pid); err == nil && pid > 0 {
					return pid, true
				}
			}
		}
	}
	return 0, false
}

// LinkedLine is the informational line of a link a run or init wrote:
// "<key> -> <pools>", plus ", quiet_machine" when that is set too.
func LinkedLine(key string, pools []string, quiet bool) string {
	s := key + " -> " + SetText(pools)
	if quiet {
		s += ", quiet_machine"
	}
	return s
}
