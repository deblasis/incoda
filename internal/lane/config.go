package lane

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// LoadConfig reads the queue's config. A missing file is the zero Config
// and no error; a file that cannot be parsed is an error, because a queue
// that silently forgot it was closed would let the old key back in.
func (q *Queue) LoadConfig() (Config, error) {
	b, err := os.ReadFile(filepath.Join(q.Dir, configName))
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("config %s is not valid JSON: %w", filepath.Join(q.Dir, configName), err)
	}
	return c, nil
}

// SaveConfig writes the config under the registry lock, through a temp
// file and a rename, so a reader never sees half a file.
func (q *Queue) SaveConfig(c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return q.withRegistry(func() error {
		path := filepath.Join(q.Dir, configName)
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
			return err
		}
		return os.Rename(tmp, path)
	})
}
