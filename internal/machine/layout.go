package machine

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/procinfo"
)

type entryKind int

const (
	absent entryKind = iota
	aDir
	aFile
	other
)

func kindOf(path string) entryKind {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return absent
	case err != nil:
		return other
	case fi.IsDir():
		return aDir
	case fi.Mode().IsRegular():
		return aFile
	default:
		return other
	}
}

// layoutState is what lstat finds at the five names the migration uses.
type layoutState struct {
	Queues, QueuesNew     entryKind
	Lanes, Plan, Registry bool
}

func scanLayout(stateDir string) layoutState {
	return layoutState{
		Queues:    kindOf(lane.QueuesDir(stateDir)),
		QueuesNew: kindOf(fenceNewPath(stateDir)),
		Lanes:     kindOf(lane.LanesDir(stateDir)) == aDir,
		Plan:      kindOf(planPath(stateDir)) == aFile,
		Registry:  kindOf(RegistryPath(stateDir)) != absent,
	}
}

// Row is a row of the crash-recovery table of spec 3.3.
type Row int

const (
	RowNotStarted Row = iota + 1
	RowPlanned
	RowSwapped
	RowFenceMissing
	RowEmptyPlanned
	RowFenceNoLanes
	RowResume
	RowCommitted
	RowRegistryLost
)

var rowMeaning = map[Row]string{
	RowNotStarted:   "not started",
	RowPlanned:      "stopped in M3 or M4 before the fence",
	RowSwapped:      "stopped between the swap and the rename",
	RowFenceMissing: "the fallback or empty-dir path stopped before the fence was placed",
	RowEmptyPlanned: "the empty-dir path stopped after M3",
	RowFenceNoLanes: "a fence without lanes/, which no step order produces",
	RowResume:       "stopped in M5 to M7",
	RowCommitted:    "stopped after the commit",
	RowRegistryLost: "registry lost",
}

// String is the "meaning" column of the recovery table; doctor prints it.
func (r Row) String() string {
	if s, ok := rowMeaning[r]; ok {
		return s
	}
	return fmt.Sprintf("row %d", int(r))
}

// row classifies a state directory whose machine.json is absent.
// RowCommitted needs a valid machine.json and is handled by the caller.
func (s layoutState) row() Row {
	switch {
	case s.Queues == aFile && s.QueuesNew == aDir:
		return RowSwapped
	case s.Queues == aFile && !s.Lanes:
		return RowFenceNoLanes
	case s.Queues == aFile && s.Lanes && s.Plan:
		return RowResume
	case s.Lanes && !s.Plan:
		return RowRegistryLost
	case s.Lanes && s.Plan:
		return RowFenceMissing
	case s.Plan && s.Queues == absent:
		return RowEmptyPlanned
	case s.Plan:
		return RowPlanned
	default:
		return RowNotStarted
	}
}

// View is what a read-only command needs to know about the layout.
type View struct {
	// Root holds one directory per lane: lanes/ once migrated; the old
	// queues/ before (lanes/ when a migration has already moved it).
	Root     string
	Migrated bool
	Registry *Registry
	// Banner is set on a layout not migrated yet (spec 3.2).
	Banner string
	// FenceMissing is set on a migrated layout whose <state>/queues is not
	// a regular file; only the next mutating command re-places it.
	FenceMissing bool
}

// Inspect reads the layout for status, watch, queues, kill and
// force-release. It never writes, never re-fences and never takes
// machine.lock. A migrated layout with a broken or lost registry fails
// closed with a *StateError (spec 2.1, 3.6).
func Inspect(stateDir string) (View, error) {
	for attempt := 0; ; attempt++ {
		reg, err := ReadRegistry(stateDir)
		if err == nil {
			return View{Root: lane.LanesDir(stateDir), Migrated: true, Registry: reg, FenceMissing: !FencePlaced(stateDir)}, nil
		}
		if !errors.Is(err, ErrNoRegistry) {
			return View{}, err
		}
		st := scanLayout(stateDir)
		if st.row() == RowRegistryLost {
			// M8 writes machine.json and then deletes migration.json; a
			// read between our two looks can see neither. Look once more
			// before failing closed.
			if attempt == 0 {
				continue
			}
			return View{}, registryLostError()
		}
		v := View{Root: lane.QueuesDir(stateDir), Banner: banner(stateDir)}
		if st.Queues != aDir && st.Lanes {
			v.Root = lane.LanesDir(stateDir)
		}
		return v, nil
	}
}

func registryLostError() *StateError {
	return stateErrorf("machine.json: missing while lanes/ exists; run incoda doctor")
}

// migrationNote returns the note of a live migration holder, read from
// machine.lock without taking it. A note left by a holder that died does
// not count.
func migrationNote(stateDir string) (Note, bool) {
	n, ok := ReadNote(stateDir)
	if !ok || n.Op != "migrate" || !procinfo.Alive(n.PID) {
		return Note{}, false
	}
	return n, true
}

// banner is the read-only banner of spec 3.2.
func banner(stateDir string) string {
	if n, ok := migrationNote(stateDir); ok {
		s := fmt.Sprintf("state upgrade in progress by pid %d since %s", n.PID, n.Since.UTC().Format(time.RFC3339))
		if len(n.Blockers) > 0 {
			s += "; waiting for older runs: " + blockerList(n.Blockers)
		}
		return s
	}
	return "state not upgraded yet: the next mutating incoda command upgrades it"
}
