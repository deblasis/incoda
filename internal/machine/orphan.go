package machine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/atomicfile"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/procinfo"
)

const (
	orphansName = "orphans"
	orphanExt   = ".orphan"
)

// OrphansDir is <state>/orphans: the records an old-holder kill writes
// before it sends any terminating signal (spec 3.2). Older binaries never
// read it.
func OrphansDir(stateDir string) string { return filepath.Join(stateDir, orphansName) }

// OrphanProc is one recorded descendant: its pid and the start time that
// identifies that incarnation of the pid (procinfo.Proc.Start).
type OrphanProc struct {
	PID   int    `json:"pid"`
	Start uint64 `json:"start"`
}

// Orphan is an orphan record: the job of an older incoda that a kill is
// ending. While any recorded descendant still runs with its recorded start
// time, or any recorded process group still has members, the record is a
// live holder on Key: idle checks wait for it and acquisitions count it
// (spec 2.3, 3.2).
type Orphan struct {
	Key         string       `json:"key"`
	PID         int          `json:"pid"`
	Command     []string     `json:"command,omitempty"`
	Descendants []OrphanProc `json:"descendants"`
	Groups      []int        `json:"groups"`
	ByPID       int          `json:"by_pid"`
	At          string       `json:"at"`

	// File is the record's file name inside orphans/.
	File string `json:"-"`
}

// CommandString renders the recorded command the way tickets do.
func (o Orphan) CommandString() string { return lane.Ticket{Command: o.Command}.CommandString() }

// orphanName is <old pid>-<unix-nanos>.orphan.
func orphanName(pid int, at time.Time) string {
	return fmt.Sprintf("%d-%d%s", pid, at.UnixNano(), orphanExt)
}

// writeOrphan writes the record by temp file plus rename, so a reader sees
// all of it or none of it, and returns its path.
func writeOrphan(stateDir string, o *Orphan) (string, error) {
	now := time.Now()
	if o.At == "" {
		o.At = now.UTC().Format(time.RFC3339Nano)
	}
	if o.Descendants == nil {
		o.Descendants = []OrphanProc{}
	}
	if o.Groups == nil {
		o.Groups = []int{}
	}
	if err := os.MkdirAll(OrphansDir(stateDir), 0o755); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return "", err
	}
	o.File = orphanName(o.PID, now)
	path := filepath.Join(OrphansDir(stateDir), o.File)
	if err := atomicfile.Write(path, append(b, '\n'), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// BadOrphan is a file in orphans/ that does not parse as a record. It is
// never counted (nothing says which key or tree it meant) and never
// deleted; doctor names it.
type BadOrphan struct {
	File string
	Err  error
}

// ReadOrphans reads every record in orphans/, sorted by file name. A
// missing directory holds none.
func ReadOrphans(stateDir string) ([]Orphan, []BadOrphan, error) {
	entries, err := os.ReadDir(OrphansDir(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var out []Orphan
	var bad []BadOrphan
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), orphanExt) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(OrphansDir(stateDir), e.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue // deleted by another reader since the listing
		}
		var o Orphan
		if err == nil {
			err = json.Unmarshal(b, &o)
		}
		if err == nil && (lane.ValidateKey(o.Key) != nil || o.PID <= 0) {
			err = fmt.Errorf("no valid key and pid")
		}
		if err != nil {
			bad = append(bad, BadOrphan{File: e.Name(), Err: err})
			continue
		}
		o.File = e.Name()
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out, bad, nil
}

// Live reports whether the recorded tree still has a process: a recorded
// descendant that exists with its recorded start time and is not a zombie,
// or a recorded group for which kill(-G, 0) answers anything but ESRCH
// (EPERM included: macOS answers it for a group of zombies). A lookup that
// fails for another reason counts as live: an unreadable process table
// must never read as "tree empty".
func (o Orphan) Live() bool {
	for _, d := range o.Descendants {
		p, err := procinfo.Lookup(d.PID)
		if errors.Is(err, procinfo.ErrNoProcess) {
			continue
		}
		if err != nil {
			return true
		}
		if p.Start == d.Start && p.State != 'Z' {
			return true
		}
	}
	for _, g := range o.Groups {
		if groupHasMembers(g) {
			return true
		}
	}
	return false
}

// LiveOrphans returns the records whose tree still runs. With sweep set it
// deletes the others, as idle checks, acquisitions and plain force-release
// do; status and doctor only read.
func LiveOrphans(stateDir string, sweep bool) ([]Orphan, error) {
	all, _, err := ReadOrphans(stateDir)
	if err != nil {
		return nil, err
	}
	var live []Orphan
	for _, o := range all {
		if o.Live() {
			live = append(live, o)
			continue
		}
		if sweep {
			_ = os.Remove(filepath.Join(OrphansDir(stateDir), o.File))
		}
	}
	return live, nil
}

// SweepOrphans deletes the records of key whose tree is empty ("" for
// every key) and returns how many it deleted. Plain force-release uses it.
func SweepOrphans(stateDir, key string) (int, error) {
	all, _, err := ReadOrphans(stateDir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, o := range all {
		if (key != "" && o.Key != key) || o.Live() {
			continue
		}
		if os.Remove(filepath.Join(OrphansDir(stateDir), o.File)) == nil {
			n++
		}
	}
	return n, nil
}

// pidList renders the recorded descendant pids, for display.
func (o Orphan) pidList() string {
	parts := make([]string, len(o.Descendants))
	for i, d := range o.Descendants {
		parts[i] = strconv.Itoa(d.PID)
	}
	return strings.Join(parts, ", ")
}
