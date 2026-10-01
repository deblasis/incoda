package machine

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/textsafe"
)

// Rebuild is incoda doctor --rebuild-registry (spec 3.6), a human act. It
// writes a new machine.json naming exactly pools, under machine.lock, in
// this order: every name must pass ValidateKey; a named key whose
// config.json carries pools is refused; then every lane's registry lock is
// taken in key order and any live ticket refuses the rebuild (it may change
// any lane's kind, and runs that acquired before the loss may still be
// running); each lane's resulting kind is reported; the fence is placed if
// it is missing; machine.json is written with generation set to the
// current Unix time in nanoseconds, so it differs from any earlier value.
// It refuses a newer machine.json, a state directory without lanes/, and an
// unfinished migration.
func Rebuild(stateDir string, pools []string, o Options, report func(key, kind string)) (*Registry, error) {
	set := map[string]bool{}
	for _, p := range pools {
		if err := lane.ValidateKey(p); err != nil {
			return nil, &Refusal{Msg: "rebuild-registry: " + err.Error()}
		}
		set[p] = true
	}
	if len(set) == 0 {
		return nil, &Refusal{Msg: "rebuild-registry: name at least one pool, for example builds,computer-use,tests,vm"}
	}
	names := make([]string, 0, len(set))
	for p := range set {
		names = append(names, p)
	}
	sort.Strings(names)

	lk, err := AcquireLock(stateDir, o.lockOptions("rebuild-registry"))
	if err != nil {
		return nil, err
	}
	defer lk.Release()

	old, err := ReadRegistry(stateDir)
	var se *StateError
	if errors.As(err, &se) && se.newer {
		return nil, err
	}
	if err != nil {
		old = nil
	}
	st := scanLayout(stateDir)
	if !st.Lanes {
		return nil, stateErrorf("nothing to rebuild: %s has no lanes/; the next mutating incoda command upgrades this state directory", textsafe.Escape(stateDir))
	}
	if st.Plan {
		return nil, stateErrorf("a migration is unfinished; run any mutating incoda command to resume it before rebuilding the registry")
	}
	for _, p := range names {
		if cfg, err := lane.ReadConfig(lane.LaneDir(stateDir, p)); err == nil && len(cfg.Pools) > 0 {
			return nil, &Refusal{Msg: fmt.Sprintf("kind-busy: %q links pools", p)}
		}
	}

	keys, err := lane.ListQueues(stateDir)
	if err != nil {
		return nil, stateErrorf("cannot list lanes/: %s", esc(err))
	}
	sort.Strings(keys)
	var qs []*lane.Queue
	defer func() {
		for _, q := range qs {
			q.Close()
		}
	}()
	for _, k := range keys {
		q, err := lane.OpenIn(lane.LanesDir(stateDir), k, lane.Existing)
		if err != nil {
			return nil, stateErrorf("cannot open lane %q: %s", k, esc(err))
		}
		qs = append(qs, q)
	}
	unlock, err := lane.LockAll(qs)
	if err != nil {
		return nil, stateErrorf("%s", esc(err))
	}
	defer unlock()
	for _, q := range qs {
		live, err := q.LiveLocked()
		if err != nil {
			return nil, stateErrorf("cannot scan lane %q: %s", q.Key, esc(err))
		}
		if len(live) > 0 {
			return nil, &Refusal{Msg: fmt.Sprintf("kind-busy: %q has live tickets", q.Key)}
		}
	}

	inLanes := map[string]bool{}
	for _, k := range keys {
		inLanes[k] = true
		kind := "project"
		if set[k] {
			kind = "pool"
		}
		report(k, kind)
	}
	for _, p := range names {
		if !inLanes[p] {
			report(p, "pool (no lane yet)")
		}
	}
	if !FencePlaced(stateDir) {
		if err := refence(stateDir); errors.Is(err, errNotIdle) {
			return nil, stateErrorf("cannot place the queues fence: the directory at queues still has a file open inside")
		} else if err != nil {
			return nil, err
		}
	}
	now := time.Now()
	reg := &Registry{Schema: RegistrySchema, Layout: Layout, Generation: now.UnixNano(), Pools: names,
		MigratedBy: o.By, MigratedAt: now.UTC().Format(time.RFC3339)}
	if old != nil {
		reg.extra, reg.MigratedBy, reg.MigratedAt = old.extra, old.MigratedBy, old.MigratedAt
	}
	if err := writeRegistry(stateDir, lk, reg); err != nil {
		return nil, err
	}
	return reg, nil
}
