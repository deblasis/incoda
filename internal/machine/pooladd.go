package machine

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/textsafe"
)

// PoolSpec is the standing configuration a new pool starts with. A zero
// Slots leaves the pool at the default of one slot.
type PoolSpec struct {
	Slots       int
	Description string
}

// PoolAdded is what AddPool changed.
type PoolAdded struct {
	// Registry is machine.json as written.
	Registry *Registry
	// OldGeneration is the generation before the write.
	OldGeneration int64
	// Config is the pool's config.json as written.
	Config lane.Config
}

// AddPool registers one new pool (incoda pool add). It is the additive
// counterpart of Rebuild: it never lists, rewrites or reorders any other
// pool, and it touches no other lane.
//
// Under machine.lock it re-reads machine.json, then in this order: refuses a
// name that fails ValidateKey or is already a pool; refuses a lane of that
// name that links pools or has a live ticket (it would change kind under a
// running job); writes the pool's config.json (slots, description) by temp
// file plus rename; writes machine.json by temp file plus rename. The lane
// config goes first on purpose: a crash between the two leaves an ordinary
// project lane with a slot count, and the same command can be run again,
// whereas the other order would leave a pool the command then refuses to
// configure. machine.json is therefore the commit point, and until it is
// renamed over, the old registry is what every reader sees.
func AddPool(stateDir, name string, spec PoolSpec, by string, o Options) (*PoolAdded, error) {
	if err := lane.ValidateKey(name); err != nil {
		return nil, &Refusal{Msg: "pool add: " + err.Error()}
	}
	if spec.Slots < 0 {
		return nil, &Refusal{Msg: fmt.Sprintf("pool add: --slots must be at least 1, got %d", spec.Slots)}
	}
	if err := textsafe.CheckWrite("description", spec.Description); err != nil {
		return nil, &Refusal{Msg: "pool add: " + err.Error()}
	}

	lk, err := AcquireLock(stateDir, o.lockOptions("pool-add"))
	if err != nil {
		return nil, err
	}
	defer lk.Release()

	reg, err := ReadRegistry(stateDir)
	if err != nil {
		return nil, err
	}
	if reg.IsPool(name) {
		return nil, &Refusal{Msg: fmt.Sprintf("pool-exists: %q is already a pool; change it with: incoda config %s --slots N", name, name)}
	}
	if st := scanLayout(stateDir); !st.Lanes {
		return nil, stateErrorf("%s has no lanes/; run any mutating incoda command to upgrade it first", textsafe.Escape(stateDir))
	} else if st.Plan {
		return nil, stateErrorf("a migration is unfinished; run any mutating incoda command to resume it before adding a pool")
	}

	if lane.Exists(stateDir, name) {
		cfg, err := lane.ReadConfig(lane.LaneDir(stateDir, name))
		if err != nil {
			return nil, &Refusal{Msg: fmt.Sprintf("kind-busy: %q has an unreadable config.json; fix or delete it first", name)}
		}
		if len(cfg.Pools) > 0 || cfg.QuietMachine {
			return nil, &Refusal{Msg: fmt.Sprintf("kind-busy: %q is a project lane that links pools; unlink it first or choose another name", name)}
		}
		eq, err := lane.OpenIn(lane.LanesDir(stateDir), name, lane.Existing)
		if err != nil {
			return nil, stateErrorf("cannot open lane %q: %s", name, esc(err))
		}
		unlock, err := lane.LockAll([]*lane.Queue{eq}, lockDeadline(o.Start, o.Wait, time.Now()))
		if err != nil {
			eq.Close()
			return nil, stateErrorf("%s", esc(err))
		}
		live, err := eq.LiveLocked()
		unlock()
		eq.Close()
		if err != nil {
			return nil, stateErrorf("cannot scan lane %q: %s", name, esc(err))
		}
		if len(live) > 0 {
			return nil, &Refusal{Msg: fmt.Sprintf("kind-busy: %q has live tickets", name)}
		}
	}

	q, err := lane.Open(stateDir, name)
	if err != nil {
		return nil, stateErrorf("cannot open queue %q: %s", name, esc(err))
	}
	defer q.Close()
	q.SetBudget(o.Start, o.Wait)
	cfg, err := q.UpdateConfig(func(c *lane.Config) error {
		changed := false
		if spec.Slots > 0 && c.Slots != spec.Slots {
			c.Slots, changed = spec.Slots, true
		}
		if spec.Description != "" && c.Description != spec.Description {
			c.Description, changed = spec.Description, true
		}
		if !changed {
			return lane.ErrNoChange
		}
		return nil
	})
	if err != nil && !errors.Is(err, lane.ErrNoChange) {
		var ns *lane.NewerSchemaError
		if errors.As(err, &ns) {
			return nil, stateErrorf("%s", esc(err))
		}
		return nil, stateErrorf("queue %q: %s", name, esc(err))
	}
	crashpoint("pool-add-config")

	old := reg.Generation
	reg.Pools = sortedWith(reg.Pools, name)
	reg.Generation++
	if err := writeRegistry(stateDir, lk, reg); err != nil {
		return nil, err
	}
	q.Logf("queue=%s event=pool-add pid=%d by=%s slots=%d generation=%d", name, os.Getpid(), by, max(cfg.Slots, 1), reg.Generation)
	return &PoolAdded{Registry: reg, OldGeneration: old, Config: cfg}, nil
}

func sortedWith(pools []string, name string) []string {
	out := append(append([]string(nil), pools...), name)
	sort.Strings(out)
	return out
}
