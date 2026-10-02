package lane

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/deblasis/incoda/internal/atomicfile"
	"github.com/deblasis/incoda/internal/textsafe"
)

const configName = "config.json"

// Config is a queue's standing configuration, kept beside its tickets. It
// exists so that callers do not have to agree on --slots by hand: with a
// dozen scratch scripts wrapping the same key, one that forgets the flag
// silently drags the queue down to a single slot. The queue itself is the
// right place for the number.
//
// Every field is optional and the zero Config means "one slot, no rules",
// which is exactly how a never-configured queue always behaved.
type Config struct {
	// Slots is the queue's width. A ticket that does not ask for a count is
	// stamped with it at enrollment; a ticket that asks for a different
	// count is refused (see NewSlotsDisagreement), so the number every
	// participant carries is the same one.
	Slots int `json:"slots,omitempty"`
	// Description says what the queue guards, for status and watch.
	Description string `json:"description,omitempty"`
	// RequireReason refuses a run with no --reason. On a shared machine an
	// unexplained holder is the first thing everyone asks about.
	RequireReason bool `json:"require_reason,omitempty"`
	// Closed, when set, refuses every run with this message. It is how a
	// retired key points at its replacements instead of quietly going on
	// serialising work nobody meant to put there.
	Closed string `json:"closed,omitempty"`
	// Pools is a project lane's link: the machine-wide pools its runs take.
	// Absent means unlinked. Pools never carry it.
	Pools []string `json:"pools,omitempty"`
	// QuietMachine makes every run on this project lane take quiet-machine.
	QuietMachine bool `json:"quiet_machine,omitempty"`
	// Schema is the file format version; a file without it is schema 1 and
	// is upgraded on its next write.
	Schema int `json:"schema,omitempty"`

	// extra holds fields this binary does not know, so a rewrite by an older
	// binary never drops what a newer one wrote.
	extra map[string]json.RawMessage
}

// ConfigSchema is the config.json format this binary writes.
const ConfigSchema = 2

// NewerSchemaError is returned when config.json was written by a newer
// incoda. Reading on would mean ignoring rules this binary does not know,
// so every run through the lane and every write to it fails closed.
type NewerSchemaError struct {
	Path   string
	Schema int
}

func (e *NewerSchemaError) Error() string {
	return fmt.Sprintf("%s was written by a newer incoda (schema %d); upgrade this one", e.Path, e.Schema)
}

// configKnown lists the JSON names Config owns; everything else is extra.
var configKnown = map[string]bool{
	"slots": true, "description": true, "require_reason": true, "closed": true,
	"pools": true, "quiet_machine": true, "schema": true,
}

type configJSON struct {
	Slots         int      `json:"slots,omitempty"`
	Description   string   `json:"description,omitempty"`
	RequireReason bool     `json:"require_reason,omitempty"`
	Closed        string   `json:"closed,omitempty"`
	Pools         []string `json:"pools,omitempty"`
	QuietMachine  bool     `json:"quiet_machine,omitempty"`
	Schema        int      `json:"schema,omitempty"`
}

// UnmarshalJSON reads the known fields and keeps the rest in extra.
func (c *Config) UnmarshalJSON(b []byte) error {
	var known configJSON
	if err := json.Unmarshal(b, &known); err != nil {
		return err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return err
	}
	*c = Config{
		Slots: known.Slots, Description: known.Description, RequireReason: known.RequireReason,
		Closed: known.Closed, Pools: known.Pools, QuietMachine: known.QuietMachine, Schema: known.Schema,
	}
	for k, v := range all {
		if !configKnown[k] {
			if c.extra == nil {
				c.extra = map[string]json.RawMessage{}
			}
			c.extra[k] = v
		}
	}
	return nil
}

// MarshalJSON writes the known fields plus every preserved unknown field.
func (c Config) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(configJSON{
		Slots: c.Slots, Description: c.Description, RequireReason: c.RequireReason,
		Closed: c.Closed, Pools: c.Pools, QuietMachine: c.QuietMachine, Schema: c.Schema,
	})
	if err != nil || len(c.extra) == 0 {
		return b, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range c.extra {
		if _, ok := m[k]; !ok {
			m[k] = v
		}
	}
	return json.Marshal(m)
}

// EqualConfig compares the fields this binary knows, except Schema, which
// is the file format: it is stamped on every write (see UpdateConfig), so
// comparing it would make a freshly-saved Config never equal to the values
// it was built from. A caller that cares about the stamped schema checks
// Schema directly.
func EqualConfig(a, b Config) bool {
	if a.Slots != b.Slots || a.Description != b.Description || a.RequireReason != b.RequireReason ||
		a.Closed != b.Closed || a.QuietMachine != b.QuietMachine || len(a.Pools) != len(b.Pools) {
		return false
	}
	for i := range a.Pools {
		if a.Pools[i] != b.Pools[i] {
			return false
		}
	}
	return true
}

// SlotsDisagreement is the refusal a configured queue gives a ticket whose
// explicit --slots does not match. It is a typed error so every entry point
// (run's pre-check and Enroll itself, which a config change between the two
// can still reach) reports the same message and the same exit code.
type SlotsDisagreement struct {
	Key        string
	Configured int
	Asked      int
	// Exclusive records whether the refused ticket also passed --exclusive,
	// which changes the advice: telling an exclusive caller to "pass
	// --exclusive" would be nonsense.
	Exclusive bool
}

// NewSlotsDisagreement builds the refusal for a ticket that asked for Asked
// slots on a queue configured for Configured.
func NewSlotsDisagreement(key string, configured, asked int, exclusive bool) *SlotsDisagreement {
	return &SlotsDisagreement{Key: key, Configured: configured, Asked: asked, Exclusive: exclusive}
}

func (e *SlotsDisagreement) Error() string {
	advice := fmt.Sprintf("Drop --slots to take the configured count, change it with `incoda config %s --slots N`, or pass --exclusive if the job needs the queue alone", e.Key)
	if e.Exclusive {
		advice = "Drop --slots: --exclusive already holds the queue alone, whatever the count says"
	}
	return fmt.Sprintf("queue %q is configured for %d slot(s); --slots %d is not allowed to disagree. %s",
		e.Key, e.Configured, e.Asked, advice)
}

// ClosedError is Enroll's refusal on a closed lane. Enroll checks under the
// registry lock, so a lane closed between a run's plan and its enrollment
// still refuses it (spec 2.5).
type ClosedError struct{ Key, Text string }

func (e *ClosedError) Error() string {
	return fmt.Sprintf("queue %q is closed: %s", e.Key, textsafe.Escape(e.Text))
}

// ReasonRequiredError is Enroll's refusal of a ticket with no reason on a
// lane that requires one (spec 4.5).
type ReasonRequiredError struct{ Key string }

func (e *ReasonRequiredError) Error() string {
	return fmt.Sprintf("queue %q requires --reason: say what this job is so status can answer \"whose is that and why\"", e.Key)
}

// LoadConfig reads the queue's config. A missing file is the zero Config
// and no error; a file that cannot be parsed is an error, because a queue
// that silently forgot it was closed would let the old key back in.
func (q *Queue) LoadConfig() (Config, error) { return ReadConfig(q.Dir) }

// ReadConfig is LoadConfig for a lane directory that is not open. It takes
// no lock; the migration and doctor use it on lanes nobody can enroll on at
// that moment, and its reads are atomic because every write is a rename.
func ReadConfig(dir string) (Config, error) {
	path := filepath.Join(dir, configName)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("config %s is not valid JSON: %w", path, err)
	}
	if c.Schema > ConfigSchema {
		return Config{}, &NewerSchemaError{Path: path, Schema: c.Schema}
	}
	return c, nil
}

// ErrNoChange, returned by an UpdateConfig callback, means "nothing to
// write": UpdateConfig stores nothing and returns the config it read with
// ErrNoChange, so a compare-and-set that finds the value already in place
// rewrites nothing.
var ErrNoChange = errors.New("no change")

// UpdateConfig loads the config, applies fn and stores the result, all
// inside one hold of the registry lock, so two writers changing different
// fields cannot lose each other's change. The write goes through a temp
// file and a rename, so a reader never sees half a file. The stored file is
// stamped with ConfigSchema and keeps fields this binary does not know. A
// config written by a newer incoda is refused, never rewritten.
func (q *Queue) UpdateConfig(fn func(*Config) error) (Config, error) {
	var out Config
	err := q.withRegistry(func() error {
		c, err := q.LoadConfig()
		if err != nil {
			return err
		}
		if err := fn(&c); err != nil {
			if errors.Is(err, ErrNoChange) {
				out = c
			}
			return err
		}
		c.Schema = ConfigSchema
		b, err := json.MarshalIndent(c, "", "  ")
		if err != nil {
			return err
		}
		if err := atomicfile.Write(filepath.Join(q.Dir, configName), append(b, '\n'), 0o644); err != nil {
			return err
		}
		out = c
		return nil
	})
	return out, err
}

// SaveConfig replaces the known fields with c and keeps unknown ones.
func (q *Queue) SaveConfig(c Config) error {
	_, err := q.UpdateConfig(func(cur *Config) error {
		extra := cur.extra
		*cur = c
		cur.extra = extra
		return nil
	})
	return err
}
