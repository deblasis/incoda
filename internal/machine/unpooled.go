package machine

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/textsafe"
)

// Unpooled is a holder no pool admitted (spec 2.3): a live ticket of an
// older incoda under strays/<n>/<K>/, or under queues/<K>/ while the fence
// is missing, or an orphan record whose tree still runs. New-binary
// acquisitions count each one as a held slot (ChargedPools).
type Unpooled struct {
	Key string
	PID int
	// Command is escaped for display.
	Command string
	// Where is "strays/<n>", "queues" or "orphans".
	Where string
	// Unknown marks a lane whose registry lock another process kept past
	// the probe's deadline (lane.ErrRegistryBusy). It counts as one held
	// slot, the safe direction, and has no pid.
	Unknown bool
}

// Line is how busy lines and status name it.
func (u Unpooled) Line() string {
	if u.Unknown {
		return fmt.Sprintf("unpooled lane of an older incoda: %s/%s: %s", u.Where, u.Key, lane.ErrRegistryBusy)
	}
	return fmt.Sprintf("unpooled run by an older incoda: pid %d, key %s", u.PID, u.Key)
}

// UpgradeBlocked is the refusal for a run whose own ancestor is an older
// incoda it would wait for: an idle-check blocker (spec 3.3 M2) or a
// counted unpooled holder (spec 2.3, 2.6 self-wait).
func UpgradeBlocked(pid int, key string) *Refusal { return upgradeBlocked(pid, key) }

// ScanUnpooled lists the live unpooled holders, sorted by key then pid, one
// per key and pid. Every ticket probe is the create-free probe of spec 2.6
// step 2 under that directory's registry.lock. Its cost is one ReadDir of
// strays/ and one of orphans/ (plus an lstat of queues and the probes of
// whatever stray lanes exist).
//
// With clean set (an acquisition poll, a re-fence, doctor) it also deletes
// every strays/<n>/<K>/ whose tickets are all dead, appending its lane.log
// to lanes/<K>/lane.log when that lane exists, deletes stray batches left
// empty, and deletes orphan records whose tree is empty. It never deletes
// strays/ itself (a re-fence may be moving a directory into it).
//
// Every registry lock it takes (probe and cleanup) is waited for only
// until deadline; a lane whose lock is still held then is an Unknown
// holder and is not cleaned.
//
// Call it only on a migrated layout: during a migration strays/ belongs to
// M5 and M6.
func ScanUnpooled(stateDir string, clean bool, deadline time.Time) ([]Unpooled, error) {
	var out []Unpooled
	batches, err := os.ReadDir(StraysDir(stateDir))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("strays/: %w", err)
	}
	for _, b := range batches {
		if !b.IsDir() {
			continue
		}
		batch := filepath.Join(StraysDir(stateDir), b.Name())
		found, err := probeRoot(batch, "strays/"+b.Name(), deadline)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
		if clean {
			cleanBatch(stateDir, batch, deadline)
		}
	}
	if kindOf(lane.QueuesDir(stateDir)) == aDir {
		found, err := probeRoot(lane.QueuesDir(stateDir), "queues", deadline)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	orphans, err := LiveOrphans(stateDir, clean)
	if err != nil {
		return nil, fmt.Errorf("orphans/: %w", err)
	}
	for _, o := range orphans {
		out = append(out, Unpooled{Key: o.Key, PID: o.PID, Command: textsafe.Escape(o.CommandString()), Where: "orphans"})
	}
	return dedupeUnpooled(out), nil
}

// probeRoot probes every lane directory in root. A directory deleted by a
// concurrent cleaner between the listing and the probe holds nothing.
func probeRoot(root, where string, deadline time.Time) ([]Unpooled, error) {
	keys, err := lane.ListIn(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	var out []Unpooled
	for _, k := range keys {
		live, err := lane.ProbeLane(filepath.Join(root, k), deadline)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", where, k, err)
		}
		for _, p := range live {
			if p.CannotTell() {
				out = append(out, Unpooled{Key: k, Command: lane.ErrRegistryBusy.Error(), Where: where, Unknown: true})
				continue
			}
			cmd := "(unreadable ticket)"
			if p.PayloadErr == nil && p.ProbeErr == nil {
				cmd = p.Ticket.CommandString()
			}
			out = append(out, Unpooled{Key: k, PID: p.PID(), Command: textsafe.Escape(cmd), Where: where})
		}
	}
	return out, nil
}

// CleanStrays deletes every fully dead lane directory under strays/ (see
// ScanUnpooled), without listing anything. A re-fence and doctor use it on
// a migrated layout. Registry locks are waited for only until deadline.
// Its only error is a strays/ it cannot list; a lane it cannot delete is
// logged (cleanBatch) and skipped.
func CleanStrays(stateDir string, deadline time.Time) error {
	batches, err := os.ReadDir(StraysDir(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, b := range batches {
		if b.IsDir() {
			cleanBatch(stateDir, filepath.Join(StraysDir(stateDir), b.Name()), deadline)
		}
	}
	return nil
}

// cleanBatch deletes every fully dead lane directory of one strays batch,
// passing its log fragment to lanes/<K>/lane.log when that lane exists,
// and then the batch itself if nothing is left in it.
//
// It is housekeeping, so it is best effort: a failure (a lane or batch it
// cannot list or delete) is logged to machine.log as event=cleanup-failed
// and skipped, never handed to the run, config or doctor that called it.
// Counting is not affected: the probes that decide what holds a slot fail
// closed on their own.
func cleanBatch(stateDir, batch string, deadline time.Time) {
	keys, err := lane.ListIn(batch)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			logCleanupFailed(stateDir, batch, err)
		}
		return
	}
	for _, k := range keys {
		dir := filepath.Join(batch, k)
		_, err := removeIfIdleFn(dir, deadline, func(logPath string) {
			if lane.Exists(stateDir, k) {
				appendFragment(logPath, lane.LogPath(lane.LaneDir(stateDir, k)))
			}
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			logCleanupFailed(stateDir, dir, err)
		}
	}
	if left, err := os.ReadDir(batch); err == nil && len(left) == 0 {
		if err := os.Remove(batch); err != nil && !errors.Is(err, fs.ErrNotExist) {
			logCleanupFailed(stateDir, batch, err)
		}
	}
}

// removeIfIdleFn is the stray lane delete; tests replace it to make it
// fail.
var removeIfIdleFn = lane.RemoveIfIdle

// logCleanupFailed records a housekeeping failure in machine.log, one line
// with escaped values.
func logCleanupFailed(stateDir, path string, err error) {
	appendMachineLog(stateDir, "event=cleanup-failed pid=%d path=%s err=%s", os.Getpid(), textsafe.LogValue(path), textsafe.LogValue(err.Error()))
}

func dedupeUnpooled(us []Unpooled) []Unpooled {
	sort.SliceStable(us, func(i, j int) bool {
		if us[i].Key != us[j].Key {
			return us[i].Key < us[j].Key
		}
		return us[i].PID < us[j].PID
	})
	var out []Unpooled
	for i, u := range us {
		// Unknown lanes (pid 0) are kept apart: two batches holding the
		// same key are two slots.
		if i > 0 && !u.Unknown && u.Key == us[i-1].Key && u.PID == us[i-1].PID {
			continue
		}
		out = append(out, u)
	}
	return out
}

// ChargedPools is where an unpooled holder on key counts as one held slot
// (spec 2.3): the pools linked from lanes/<key> if key is a linked project
// lane, key itself if it is a pool, and every pool otherwise (an unknown or
// unlinked key, a config that cannot be read, or a link naming no
// registered pool: the safe direction).
func ChargedPools(stateDir string, reg *Registry, key string) []string {
	if reg.IsPool(key) {
		return []string{key}
	}
	if cfg, err := lane.ReadConfig(lane.LaneDir(stateDir, key)); err == nil {
		var linked []string
		for _, p := range cfg.Pools {
			if reg.IsPool(p) {
				linked = append(linked, p)
			}
		}
		if len(linked) > 0 {
			sort.Strings(linked)
			return linked
		}
	}
	return append([]string(nil), reg.Pools...)
}

// ChargedTo returns the holders of us that count on lane key. On a pool
// they are the holders ChargedPools charges to it (spec 2.3). On a project
// lane they are the holders on that very key: an older incoda that held
// project key K used K's own width as well as its pools, so a new run on K
// counts it against K's slots too, before it reaches the pools (the safe
// direction; spec 2.3 names only the pools).
func ChargedTo(stateDir string, reg *Registry, key string, us []Unpooled) []Unpooled {
	var out []Unpooled
	for _, u := range us {
		if !reg.IsPool(key) {
			if u.Key == key {
				out = append(out, u)
			}
			continue
		}
		for _, p := range ChargedPools(stateDir, reg, u.Key) {
			if p == key {
				out = append(out, u)
				break
			}
		}
	}
	return out
}
