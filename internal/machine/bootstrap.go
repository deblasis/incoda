package machine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/textsafe"
)

func esc(err error) string { return textsafe.Escape(err.Error()) }

// mergeStrays is M6. Every ticket under strays/ is dead by now (M5 waited
// for the live ones). A strays/<n>/<K> whose lanes/<K> does not exist is
// renamed into lanes/; the rest hold only dead tickets and a log fragment,
// which is appended to lanes/<K>/lane.log before the directory is deleted.
// strays/ ends removed.
func mergeStrays(stateDir string) error {
	batches, err := os.ReadDir(StraysDir(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return stateErrorf("cannot read strays/: %s", esc(err))
	}
	for _, b := range batches {
		batch := filepath.Join(StraysDir(stateDir), b.Name())
		if !b.IsDir() {
			if err := os.Remove(batch); err != nil {
				return stateErrorf("cannot remove %s: %s", textsafe.Escape(batch), esc(err))
			}
			continue
		}
		entries, err := os.ReadDir(batch)
		if err != nil {
			return stateErrorf("cannot read %s: %s", textsafe.Escape(batch), esc(err))
		}
		for _, e := range entries {
			src := filepath.Join(batch, e.Name())
			isLane := e.IsDir() && lane.ValidateKey(e.Name()) == nil
			if isLane && !lane.Exists(stateDir, e.Name()) {
				if err := renameDir(src, lane.LaneDir(stateDir, e.Name())); err != nil {
					return stateErrorf("cannot move %s into lanes/: %s", textsafe.Escape(src), esc(err))
				}
				continue
			}
			if isLane {
				appendFragment(lane.LogPath(src), lane.LogPath(lane.LaneDir(stateDir, e.Name())))
			}
			if err := os.RemoveAll(src); err != nil {
				return stateErrorf("cannot remove %s: %s", textsafe.Escape(src), esc(err))
			}
		}
		if err := os.Remove(batch); err != nil {
			return stateErrorf("cannot remove %s: %s", textsafe.Escape(batch), esc(err))
		}
	}
	if err := os.Remove(StraysDir(stateDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return stateErrorf("cannot remove strays/: %s", esc(err))
	}
	return nil
}

// appendFragment appends the log of a stray to its lane's log. Log failures
// are never fatal: the log is history, not state.
func appendFragment(src, dst string) {
	b, err := os.ReadFile(src)
	if err != nil || len(b) == 0 {
		return
	}
	f, err := os.OpenFile(dst, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if b[len(b)-1] != '\n' {
		b = append(b, '\n')
	}
	_, _ = f.Write(b)
}

// applyBootstrap is M7: every bootstrap pool gets a lane with a schema 2
// config, slots 1 unless an existing lane of that name sets slots (spec
// 3.5). An existing pool lane keeps description, slots, closed and
// require_reason as read now (UpdateConfig loads under the registry lock).
// A malformed, unreadable or newer config is never rewritten: the key is
// registered as a pool with the file left in place, so runs through it exit
// 122 naming the file until a human fixes it. The migration never fails on
// a lane config.
func applyBootstrap(stateDir string) error {
	for _, key := range BootstrapPools() {
		q, err := lane.Open(stateDir, key)
		if err != nil {
			return stateErrorf("cannot create pool %q: %s", key, esc(err))
		}
		if _, err := q.LoadConfig(); err != nil {
			q.Close()
			continue
		}
		_, err = q.UpdateConfig(func(c *lane.Config) error {
			if c.Slots < 1 {
				c.Slots = 1
			}
			return nil
		})
		q.Close()
		if err != nil {
			return stateErrorf("cannot write the config of pool %q: %s", key, esc(err))
		}
	}
	return nil
}

// summary is what the M8 lines count, computed from lanes/ at commit time.
type summary struct {
	needLink       int // open, unlinked project lanes with a readable config
	withSuggestion int // of those, the ones the name table suggests pools for
	unreadable     int // lanes (pools included) whose config.json cannot be read
}

func summarize(stateDir string, reg *Registry) summary {
	var s summary
	keys, _ := lane.ListQueues(stateDir)
	for _, k := range keys {
		cfg, err := lane.ReadConfig(lane.LaneDir(stateDir, k))
		if err != nil {
			s.unreadable++
			continue
		}
		if reg.IsPool(k) || cfg.Closed != "" || len(cfg.Pools) > 0 {
			continue
		}
		s.needLink++
		if _, ok := Suggest(k); ok {
			s.withSuggestion++
		}
	}
	return s
}

// migratedLines is the M8 text, printed once to stderr.
func migratedLines(reg *Registry, s summary) []string {
	lines := []string{fmt.Sprintf("incoda: migrated: pools %s; %d queues need a link before they run again", strings.Join(reg.Pools, ", "), s.needLink)}
	if s.needLink > 0 {
		lines = append(lines, fmt.Sprintf("incoda: ask the user to run incoda init: it shows each queue's suggested pools and asks (%d have one)", s.withSuggestion))
	}
	if s.unreadable > 0 {
		lines = append(lines, fmt.Sprintf("incoda: %d queue(s) have an unreadable config.json: see incoda doctor", s.unreadable))
	}
	return lines
}

// logMigrate writes event=migrate into every lane's lane.log.
func logMigrate(stateDir string, reg *Registry) {
	keys, _ := lane.ListQueues(stateDir)
	for _, k := range keys {
		kind := "project"
		if reg.IsPool(k) {
			kind = "pool"
		}
		lane.AppendLog(lane.LaneDir(stateDir, k), "queue=%s event=migrate pid=%d layout=%d kind=%s", k, os.Getpid(), Layout, kind)
	}
}

// Suggestion is the link the name table of spec 3.5 suggests for a key.
type Suggestion struct {
	Pools        []string
	QuietMachine bool
	Pattern      string
}

var suffixTable = []struct {
	suffix string
	pools  []string
	quiet  bool
}{
	{"-gate", []string{"tests"}, false},
	{"-test", []string{"tests"}, false},
	{"-tests", []string{"tests"}, false},
	{"-build", []string{"builds"}, false},
	{"-ui", []string{"computer-use"}, false},
	{"-desktop", []string{"computer-use"}, false},
	{"-e2e", []string{"computer-use", "tests"}, false},
	{"-measure", []string{"tests"}, true},
}

// Suggest returns the suggestion of spec 3.5 for key, or false when no
// pattern matches. A suffix pattern needs a non-empty name before it.
// Plan 3 prints suggestions; the migration only counts them (M8).
func Suggest(key string) (Suggestion, bool) {
	if key == "compiles" {
		return Suggestion{Pools: []string{"builds"}, Pattern: "compiles"}, true
	}
	for _, r := range suffixTable {
		if len(key) > len(r.suffix) && strings.HasSuffix(key, r.suffix) {
			return Suggestion{Pools: append([]string(nil), r.pools...), QuietMachine: r.quiet, Pattern: "*" + r.suffix}, true
		}
	}
	return Suggestion{}, false
}
