package machine

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/deblasis/incoda/internal/atomicfile"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/textsafe"
)

const (
	// RegistrySchema is the machine.json format this binary reads and writes.
	RegistrySchema = 1
	// Layout is the state layout this binary runs on: lanes/ plus the
	// queues fence.
	Layout = 2

	registryName = "machine.json"
)

// bootstrapPools are the pools a migration registers (spec 3.5).
var bootstrapPools = []string{"builds", "computer-use", "tests", "vm"}

// BootstrapPools returns the pools a migration registers, sorted.
func BootstrapPools() []string { return append([]string(nil), bootstrapPools...) }

// Registry is machine.json: the sole record of which keys are pools and of
// the layout version (spec 2.1). Pool existence and kind are never derived
// from scanning lane configs.
type Registry struct {
	Schema     int
	Layout     int
	Generation int64
	Pools      []string
	MigratedBy string
	MigratedAt string

	// extra keeps fields this binary does not know, so a rewrite never
	// drops what a newer incoda wrote.
	extra map[string]json.RawMessage
}

type registryJSON struct {
	Schema     int      `json:"schema"`
	Layout     int      `json:"layout"`
	Generation int64    `json:"generation"`
	Pools      []string `json:"pools"`
	MigratedBy string   `json:"migrated_by,omitempty"`
	MigratedAt string   `json:"migrated_at,omitempty"`
}

var registryKnown = map[string]bool{
	"schema": true, "layout": true, "generation": true, "pools": true,
	"migrated_by": true, "migrated_at": true,
}

// UnmarshalJSON reads the known fields and keeps the rest.
func (r *Registry) UnmarshalJSON(b []byte) error {
	var k registryJSON
	if err := json.Unmarshal(b, &k); err != nil {
		return err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return err
	}
	*r = Registry{Schema: k.Schema, Layout: k.Layout, Generation: k.Generation, Pools: k.Pools,
		MigratedBy: k.MigratedBy, MigratedAt: k.MigratedAt}
	for key, v := range all {
		if !registryKnown[key] {
			if r.extra == nil {
				r.extra = map[string]json.RawMessage{}
			}
			r.extra[key] = v
		}
	}
	return nil
}

// MarshalJSON writes the known fields plus every preserved unknown one.
func (r Registry) MarshalJSON() ([]byte, error) {
	pools := r.Pools
	if pools == nil {
		pools = []string{}
	}
	b, err := json.Marshal(registryJSON{Schema: r.Schema, Layout: r.Layout, Generation: r.Generation,
		Pools: pools, MigratedBy: r.MigratedBy, MigratedAt: r.MigratedAt})
	if err != nil || len(r.extra) == 0 {
		return b, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range r.extra {
		if _, ok := m[k]; !ok {
			m[k] = v
		}
	}
	return json.Marshal(m)
}

// IsPool reports whether key is a registered pool.
func (r *Registry) IsPool(key string) bool {
	for _, p := range r.Pools {
		if p == key {
			return true
		}
	}
	return false
}

// ErrNoRegistry means machine.json does not exist. Whether that is "not
// migrated yet" or "registry lost" depends on the rest of the layout
// (Inspect, Ensure).
var ErrNoRegistry = errors.New("machine.json does not exist")

// RegistryPath is <state>/machine.json.
func RegistryPath(stateDir string) string { return filepath.Join(stateDir, registryName) }

// ReadRegistry reads machine.json without a lock. A missing file is
// ErrNoRegistry; an unreadable or malformed one, or one written by a newer
// incoda, is a *StateError that fails closed (spec 2.1, 3.6).
func ReadRegistry(stateDir string) (*Registry, error) {
	b, err := os.ReadFile(RegistryPath(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoRegistry
	}
	if err != nil {
		return nil, stateErrorf("machine.json: %s; run incoda doctor", textsafe.Escape(err.Error()))
	}
	var r Registry
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, stateErrorf("machine.json: %s; run incoda doctor", textsafe.Escape(err.Error()))
	}
	if r.Schema > RegistrySchema || r.Layout > Layout {
		return nil, newerError()
	}
	if r.Schema < 1 || r.Layout != Layout {
		return nil, stateErrorf("machine.json: schema %d, layout %d is not a registry this incoda reads; run incoda doctor", r.Schema, r.Layout)
	}
	for _, p := range r.Pools {
		if lane.ValidateKey(p) != nil {
			return nil, stateErrorf("machine.json: pool %q is not a valid key; run incoda doctor", textsafe.Escape(p))
		}
	}
	return &r, nil
}

// newerError is the refusal for a machine.json whose schema or layout is
// newer than this binary knows. It names this binary, the one to upgrade.
func newerError() *StateError {
	self, err := os.Executable()
	if err != nil {
		self = "this incoda"
	}
	e := stateErrorf("machine.json was written by a newer incoda; upgrade this one (%s)", textsafe.Escape(self))
	e.newer = true
	return e
}

// writeRegistry writes machine.json by temp file plus rename. It takes the
// held machine.lock as proof that the caller is the only writer.
func writeRegistry(stateDir string, lk *Lock, r *Registry) error {
	if lk == nil || lk.f == nil {
		return stateErrorf("machine.json is written only under machine.lock")
	}
	pools := map[string]bool{}
	var sorted []string
	for _, p := range r.Pools {
		if !pools[p] {
			pools[p] = true
			sorted = append(sorted, p)
		}
	}
	sort.Strings(sorted)
	r.Pools = sorted
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return stateErrorf("cannot encode machine.json: %v", err)
	}
	if err := atomicfile.Write(RegistryPath(stateDir), append(b, '\n'), 0o644); err != nil {
		return stateErrorf("cannot write machine.json: %s", textsafe.Escape(err.Error()))
	}
	return nil
}

// UpdateRegistry rewrites machine.json under machine.lock: read it, apply
// fn, increment generation, write it by temp file plus rename. Fields this
// binary does not know are kept (spec 2.1).
func UpdateRegistry(stateDir string, lk *Lock, fn func(*Registry) error) (*Registry, error) {
	r, err := ReadRegistry(stateDir)
	if err != nil {
		return nil, err
	}
	if err := fn(r); err != nil {
		return nil, err
	}
	r.Generation++
	if err := writeRegistry(stateDir, lk, r); err != nil {
		return nil, err
	}
	return r, nil
}
