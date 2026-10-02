package machine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/textsafe"
)

const machineLogName = "machine.log"

// MachineLogPath is <state>/machine.log, the log of machine-level events
// that belong to no lane (event=refence).
func MachineLogPath(stateDir string) string { return filepath.Join(stateDir, machineLogName) }

func appendMachineLog(stateDir, format string, args ...any) {
	f, err := os.OpenFile(MachineLogPath(stateDir), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

// Ensure makes stateDir ready for a command that takes a ticket or writes
// config (run, config; later link, init, pools). On a migrated layout with
// the fence in place and no leftover migration.json it reads machine.json
// and returns, taking no lock. Otherwise it repairs (re-fence, row 8) or
// migrates under machine.lock, before the caller holds any ticket.
func Ensure(stateDir string, o Options) (*Registry, error) {
	reg, err := ReadRegistry(stateDir)
	if err == nil {
		if FencePlaced(stateDir) && kindOf(planPath(stateDir)) == absent {
			return reg, nil
		}
		return repair(stateDir, o)
	}
	if !errors.Is(err, ErrNoRegistry) {
		return nil, err
	}
	if scanLayout(stateDir).row() == RowRegistryLost {
		// A lost registry is no migration: no M0 PATH warning and no
		// op=migrate note. repair re-places a missing fence and fails
		// closed (or finds machine.json after all: a commit can land
		// between ReadRegistry and scanLayout).
		return repair(stateDir, o)
	}
	return migrate(stateDir, o)
}

// repair takes machine.lock to re-place a missing fence (spec 2.3, only on
// a migrated layout) or to delete a migration.json left after the commit
// (row 8). runMigration re-checks both under the lock: another process may
// have done it already.
func repair(stateDir string, o Options) (*Registry, error) {
	op := "recover"
	if !FencePlaced(stateDir) {
		op = "refence"
	}
	lk, err := AcquireLock(stateDir, o.lockOptions(op))
	if err != nil {
		return nil, err
	}
	defer lk.Release()
	return runMigration(stateDir, lk, o)
}

// migrate runs the transaction of spec 3.3: M0 before machine.lock, then
// everything else under it.
func migrate(stateDir string, o Options) (*Registry, error) {
	checkPath(o)
	lk, err := AcquireLock(stateDir, o.lockOptions("migrate"))
	if err != nil {
		return nil, err
	}
	defer lk.Release()
	crashpoint("locked")
	return runMigration(stateDir, lk, o)
}

// runMigration classifies the layout and resumes at the step the recovery
// table names. The caller holds machine.lock.
func runMigration(stateDir string, lk *Lock, o Options) (*Registry, error) {
	var w notIdleWait
	for {
		reg, err := ReadRegistry(stateDir)
		if err == nil {
			// M1: another process won, or this is row 8, or a re-fence.
			if err := os.Remove(planPath(stateDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, stateErrorf("cannot remove migration.json: %s", esc(err))
			}
			if !FencePlaced(stateDir) {
				if err := refenceWaiting(stateDir, lk, o, &w); err != nil {
					return nil, err
				}
			}
			return reg, nil
		}
		if !errors.Is(err, ErrNoRegistry) {
			return nil, err
		}
		st := scanLayout(stateDir)
		var stepErr error
		switch st.row() {
		case RowRegistryLost:
			if !FencePlaced(stateDir) {
				if err := refenceWaiting(stateDir, lk, o, &w); err != nil {
					return nil, err
				}
			}
			return nil, registryLostError()
		case RowSwapped:
			if err := renameDir(fenceNewPath(stateDir), lane.LanesDir(stateDir)); err != nil {
				return nil, stateErrorf("cannot rename queues.new to lanes: %s", esc(err))
			}
		case RowFenceMissing:
			_, stepErr = placeFence(stateDir)
		case RowFenceNoLanes:
			if err := os.Mkdir(lane.LanesDir(stateDir), 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return nil, stateErrorf("cannot create lanes/: %s", esc(err))
			}
		case RowResume:
		default:
			stepErr = beginMigration(stateDir, lk, o, st)
		}
		if errors.Is(stepErr, errNotIdle) {
			if err := w.wait(stateDir, lk, o); err != nil {
				return nil, err
			}
			continue
		}
		if stepErr != nil {
			return nil, stepErr
		}
		return finishMigration(stateDir, lk, o)
	}
}

// notIdleWait is the wait after a directory rename was refused because
// something still has a file open inside (Windows: a sharing violation or
// access denied). It remembers whether it has printed its line.
type notIdleWait struct{ printed bool }

// wait probes the old queues/ (when it is a directory) for the older runs
// that keep files open there, writes them into the machine.lock note, prints
// one upgrade-wait block (the blockers, or the open-file line when none is
// found), then sleeps one poll, or gives up when the --wait budget is
// spent.
func (w *notIdleWait) wait(stateDir string, lk *Lock, o Options) error {
	var bs []Blocker
	if kindOf(lane.QueuesDir(stateDir)) == aDir {
		bs, _ = findBlockers(stateDir, phaseM2)
		_ = lk.SetBlockers(bs)
	}
	if !w.printed {
		w.printed = true
		if len(bs) > 0 {
			fmt.Fprintf(o.stderr(), "incoda: %s\n", joinLines(upgradeWaitLines(bs, phaseM2)))
		} else {
			fmt.Fprintf(o.stderr(), "incoda: %s\n", notIdleLine)
		}
	}
	if dl := budgetDeadline(o.Start, o.Wait); !dl.IsZero() && !time.Now().Before(dl) {
		if len(bs) > 0 {
			return &Timeout{Msg: joinLines(upgradeTimeoutLines(bs, phaseM2, o.Wait))}
		}
		return &Timeout{Msg: joinLines([]string{
			"upgrade-timeout: a directory the upgrade must move still has a file open inside it (an older incoda or another program)",
			"upgrade the older incoda on PATH; see incoda doctor",
		})}
	}
	time.Sleep(o.poll())
	return nil
}

// notIdleLine is printed once when a rename is refused and no older run is
// found holding a ticket: something else has a file open in the directory.
const notIdleLine = "upgrade-wait: a directory the upgrade must move still has a file open inside it (an older incoda or another program); waiting"

// refenceWaiting re-places the fence, waiting (within the --wait budget)
// while Windows refuses to move a queues/ directory that still has an open
// file inside.
func refenceWaiting(stateDir string, lk *Lock, o Options, w *notIdleWait) error {
	for {
		err := refence(stateDir)
		if !errors.Is(err, errNotIdle) {
			return err
		}
		if err := w.wait(stateDir, lk, o); err != nil {
			return err
		}
	}
}

// beginMigration is M1 (leftover queues.new), M2, M3 and M4.
func beginMigration(stateDir string, lk *Lock, o Options, st layoutState) error {
	if st.Queues == aDir && st.QueuesNew == aFile {
		if err := os.Remove(fenceNewPath(stateDir)); err != nil {
			return stateErrorf("cannot remove a leftover queues.new: %s", esc(err))
		}
	}
	if st.Queues == aDir {
		if err := waitIdle(stateDir, lk, o, phaseM2); err != nil {
			return err
		}
	}
	crashpoint("M2")
	if err := writePlan(stateDir); err != nil {
		return err
	}
	crashpoint("M3")
	return fenceMigration(stateDir)
}

// fenceMigration is M4. With the atomic exchange no instant is unfenced;
// with the fallback, or on an empty state directory, the fence is placed
// with the race rule. On Windows a refused rename of queues/ means an older
// run still has a file open in it: errNotIdle sends the caller back to M2.
func fenceMigration(stateDir string) error {
	if err := writeFenceNew(stateDir); err != nil {
		return err
	}
	crashpoint("M4-new")
	q, lanes := lane.QueuesDir(stateDir), lane.LanesDir(stateDir)
	switch kindOf(q) {
	case aDir:
		err := exchangeFn(fenceNewPath(stateDir), q)
		if err == nil {
			crashpoint("M4-swapped")
			if err := renameDir(fenceNewPath(stateDir), lanes); err != nil {
				return stateErrorf("cannot rename queues.new to lanes: %s", esc(err))
			}
			crashpoint("M4")
			return nil
		}
		if !errors.Is(err, errNoExchange) {
			return stateErrorf("cannot swap the queues fence in: %s", esc(err))
		}
		if err := renameDir(q, lanes); err != nil {
			if isNotIdle(err) {
				return errNotIdle
			}
			return stateErrorf("cannot rename queues to lanes: %s", esc(err))
		}
		crashpoint("M4-moved")
	case absent:
		if err := os.Mkdir(lanes, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return stateErrorf("cannot create lanes/: %s", esc(err))
		}
		crashpoint("M4-lanes")
	default:
		return stateErrorf("%s is neither a directory nor absent; move it away and rerun", textsafe.Escape(q))
	}
	if _, err := placeFence(stateDir); err != nil {
		return err
	}
	crashpoint("M4")
	return nil
}

// maxCommitRefences bounds how often M8 finds the fence gone and goes back
// to M5, so a fence that keeps vanishing cannot spin even with no --wait
// budget.
const maxCommitRefences = 100

// Seam for tests; production never changes it.
var beforeCommitCheck = func() {}

// finishMigration is M5 to M8. Right before the commit it checks the fence
// again (spec 2.3): if it was removed during a long M5 wait, an older
// binary may have recreated queues/ and started a run there, which M5
// never probes. The fence is then re-placed with the race rule (that
// queues/ goes to strays/) and the migration goes back to M5, which probes
// strays/ and waits for the run, all within the one --wait budget.
func finishMigration(stateDir string, lk *Lock, o Options) (*Registry, error) {
	var w notIdleWait
	// refences counts fences actually re-placed; waits for a directory
	// Windows will not move yet do not count against the cap.
	refences := 0
	for {
		if err := waitIdle(stateDir, lk, o, phaseM5); err != nil {
			return nil, err
		}
		crashpoint("M5")
		if err := mergeStrays(stateDir); err != nil {
			return nil, err
		}
		crashpoint("M6")
		if err := applyBootstrap(stateDir); err != nil {
			return nil, err
		}
		crashpoint("M7")
		beforeCommitCheck()
		if FencePlaced(stateDir) {
			break
		}
		if refences >= maxCommitRefences {
			return nil, stateErrorf("the queues fence keeps disappearing; machine.json was not written: see incoda doctor")
		}
		if err := refenceWaiting(stateDir, lk, o, &w); err != nil {
			return nil, err
		}
		refences++
	}
	reg := &Registry{Schema: RegistrySchema, Layout: Layout, Generation: 1, Pools: BootstrapPools(),
		MigratedBy: o.By, MigratedAt: time.Now().UTC().Format(time.RFC3339)}
	// Logged before the commit, so a crash right after it (row 8) still
	// leaves the history; a crash in between logs twice, which is harmless.
	logMigrate(stateDir, reg)
	if err := writeRegistry(stateDir, lk, reg); err != nil {
		return nil, err
	}
	crashpoint("M8-registry")
	if err := os.Remove(planPath(stateDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, stateErrorf("cannot remove migration.json: %s", esc(err))
	}
	for _, line := range migratedLines(reg, summarize(stateDir, reg)) {
		fmt.Fprintln(o.stderr(), line)
	}
	return reg, nil
}

// refence re-places the fence with the race rule and logs event=refence to
// machine.log, naming the strays it made. Counting and cleaning strays is
// plan 2b.
func refence(stateDir string) error {
	moved, err := placeFence(stateDir)
	if err != nil {
		return err
	}
	names := make([]string, len(moved))
	for i, m := range moved {
		names[i] = filepath.Base(m)
	}
	appendMachineLog(stateDir, "event=refence pid=%d strays=%s", os.Getpid(), textsafe.LogValue(strings.Join(names, ",")))
	return nil
}
