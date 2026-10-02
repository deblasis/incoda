package machine

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/deblasis/incoda/internal/lane"
)

// TargetKind says how kill ends a participant (spec 3.2).
type TargetKind int

const (
	// TargetNone: no live participant with that pid on that key.
	TargetNone TargetKind = iota
	// TargetLane: a ticket of this binary under lanes/ on a migrated
	// layout; it watches its .kill file and ends its own tree.
	TargetLane
	// TargetOld: an old-layout holder, a ticket of an older incoda under
	// queues/<K> (before the fence, or after a careless rm of it), under
	// strays/, or under lanes/ or queues.new/ while machine.json is absent.
	// It is always ended by the old-holder kill, never by a request: v0.3.0
	// to v0.6.0 acknowledge a request by ending only their direct child,
	// which shares their group, so its descendants would keep running
	// while the lane reads free; behind the fence no request reaches it.
	TargetOld
)

// KillTarget is the participant kill found: where its ticket is and who
// holds it.
type KillTarget struct {
	Kind TargetKind
	Key  string
	// Root holds the lane directory Dir; Ticket is the ticket file name.
	Root, Dir, Ticket string
	PID               int
	Command           []string
}

// Old reports whether the target is an older incoda (an old-layout holder).
func (t KillTarget) Old() bool { return t.Kind == TargetOld }

// FindKillTarget looks for the live participant pid on key wherever the
// layout v allows one: lanes/<key> on a migrated layout, queues/<key> while
// queues/ is a directory, the root a migration has moved the lanes to, and
// every strays/<n>/<key>. Every probe is create-free (spec 2.6 step 2); it
// never writes, migrates or takes machine.lock.
func FindKillTarget(stateDir string, v View, key string, pid int) (KillTarget, error) {
	type place struct {
		root string
		kind TargetKind
	}
	var places []place
	switch {
	case v.Migrated:
		places = append(places, place{lane.LanesDir(stateDir), TargetLane})
		if kindOf(lane.QueuesDir(stateDir)) == aDir {
			places = append(places, place{lane.QueuesDir(stateDir), TargetOld})
		}
	default:
		places = append(places, place{v.Root, TargetOld})
	}
	batches, err := os.ReadDir(StraysDir(stateDir))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return KillTarget{}, fmt.Errorf("strays/: %w", err)
	}
	for _, b := range batches {
		if b.IsDir() {
			places = append(places, place{filepath.Join(StraysDir(stateDir), b.Name()), TargetOld})
		}
	}
	for _, pl := range places {
		dir := filepath.Join(pl.root, key)
		live, err := lane.ProbeLane(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return KillTarget{}, fmt.Errorf("%s: %w", dir, err)
		}
		for _, p := range live {
			if p.PID() == pid {
				return KillTarget{Kind: pl.kind, Key: key, Root: pl.root, Dir: dir, Ticket: p.Name, PID: pid, Command: p.Ticket.Command}, nil
			}
		}
	}
	return KillTarget{Kind: TargetNone, Key: key, PID: pid}, nil
}

// fixWord quotes one value of a printed command (spec 2.6, fix lines):
// one single-quoted word, an embedded quote closed, escaped with a
// backslash and reopened on Unix (POSIX sh), doubled on Windows
// (PowerShell).
func fixWord(s string) string {
	if runtime.GOOS == "windows" {
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// KillLine is a printed stop line: incoda kill --queue K --pid N --reason
// '<reason>', plus --force when force is set (the stopped-holder rerun line
// of spec 3.2 carries it; kill needs no --force for an older incoda). The key is
// validated and prints bare; the reason is always a constant of this
// binary, so the line never needs a placeholder.
func KillLine(key string, pid int, reason string, force bool) string {
	s := fmt.Sprintf("incoda kill --queue %s --pid %d --reason %s", key, pid, fixWord(reason))
	if force {
		s += " --force"
	}
	return s
}
