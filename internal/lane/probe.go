package lane

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/deblasis/incoda/internal/lockfile"
)

// Probed is one ticket as the create-free probe saw it.
type Probed struct {
	// Name is the ticket file name.
	Name string
	// Live is set when another process holds the ticket's lock.
	Live bool
	// Ticket is a live ticket's payload; PayloadErr says why it could not
	// be read.
	Ticket     Ticket
	PayloadErr error
	// ProbeErr is set when the probe itself failed: the ticket could not be
	// opened for a reason other than being absent, or a lock call errored.
	// ProbeTicket reports such a ticket as not live (the INCODA_HELD rule of
	// plan 1); ProbeLane reports it as live, because the migration must
	// never read "cannot tell" as idle.
	ProbeErr error
}

// PID is the holder's pid: the payload's, else the one in the ticket name.
func (p Probed) PID() int {
	if p.Ticket.PID != 0 {
		return p.Ticket.PID
	}
	if ord, ok := parseTicketName(p.Name); ok {
		return ord.pid
	}
	return 0
}

// ProbeTicket is the liveness probe of spec 2.6 step 2 for one ticket: it
// opens the lane's registry lock without O_CREATE and takes it (the lock
// Enroll and Release hold, in this binary and in every older one), opens
// the ticket without O_CREATE and tries its lock. A missing lane directory,
// registry lock or ticket is dead. It never creates or removes a file and
// never keeps a lock.
//
// The caller must not hold this lane's registry lock through another
// handle: the blocking Lock here would wait for itself.
func ProbeTicket(laneDir, name string) Probed {
	reg, err := lockfile.OpenExisting(RegistryLockPath(laneDir))
	if err != nil {
		return Probed{Name: name}
	}
	defer reg.Close()
	if err := reg.Lock(); err != nil {
		return Probed{Name: name, ProbeErr: err}
	}
	return probeLocked(laneDir, name)
}

// ProbeLane probes every ticket file in laneDir under one hold of its
// registry lock and returns the live ones in file name order. A missing
// directory or registry lock holds no live ticket (an older incoda creates
// the registry lock before any ticket). The migration uses it for the idle
// checks of spec 3.3 M2 and M5; like ProbeTicket it creates, removes and
// keeps nothing.
func ProbeLane(laneDir string) ([]Probed, error) {
	reg, err := lockfile.OpenExisting(RegistryLockPath(laneDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer reg.Close()
	if err := reg.Lock(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(laneDir)
	if err != nil {
		return nil, err
	}
	var live []Probed
	for _, de := range entries {
		if de.IsDir() {
			continue
		}
		if _, ok := parseTicketName(de.Name()); !ok {
			continue
		}
		p := probeLocked(laneDir, de.Name())
		if p.ProbeErr != nil {
			p.Live = true
		}
		if p.Live {
			live = append(live, p)
		}
	}
	return live, nil
}

// probeLocked probes one ticket. The caller holds the lane's registry lock.
func probeLocked(laneDir, name string) Probed {
	p := Probed{Name: name}
	path := ticketPath(laneDir, name)
	tf, err := lockfile.OpenExisting(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			p.ProbeErr = err
		}
		return p
	}
	free, err := tf.TryLock()
	tf.Close()
	if err != nil {
		p.ProbeErr = err
		return p
	}
	if free {
		return p
	}
	p.Live = true
	b, err := os.ReadFile(path)
	if err != nil {
		p.PayloadErr = err
		return p
	}
	if err := json.Unmarshal(b, &p.Ticket); err != nil {
		p.PayloadErr = err
	}
	return p
}

// RemoveIfIdle deletes laneDir when no ticket in it is live, deciding
// under one hold of its registry lock with the same probe as ProbeLane
// (a ticket whose probe fails counts as live). Before deleting it hands
// the directory's lane.log path to keepLog. It reports whether it deleted
// the directory; a directory that is already gone is not an error.
//
// It is meant for stray lane directories (spec 2.3): nobody enrolls there,
// because older binaries address queues/<K>, which the fence makes
// ENOTDIR, so no ticket can appear between the probe and the delete. The
// registry lock file goes last, after the hold ends, because Windows
// cannot delete a directory while a handle inside it is open.
func RemoveIfIdle(laneDir string, keepLog func(logPath string)) (bool, error) {
	reg, err := lockfile.OpenExisting(RegistryLockPath(laneDir))
	if errors.Is(err, os.ErrNotExist) {
		// No registry lock: older binaries create it before any ticket,
		// so no ticket here was ever live.
		if fi, serr := os.Stat(laneDir); serr != nil || !fi.IsDir() {
			return false, nil
		}
		keepLog(LogPath(laneDir))
		return true, os.RemoveAll(laneDir)
	}
	if err != nil {
		return false, err
	}
	if err := reg.Lock(); err != nil {
		reg.Close()
		return false, err
	}
	entries, err := os.ReadDir(laneDir)
	if err != nil {
		reg.Close()
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	for _, de := range entries {
		if de.IsDir() {
			continue
		}
		if _, ok := parseTicketName(de.Name()); !ok {
			continue
		}
		if p := probeLocked(laneDir, de.Name()); p.Live || p.ProbeErr != nil {
			reg.Close()
			return false, nil
		}
	}
	keepLog(LogPath(laneDir))
	for _, de := range entries {
		if de.Name() != registryLockName {
			_ = os.RemoveAll(filepath.Join(laneDir, de.Name()))
		}
	}
	reg.Close()
	return true, os.RemoveAll(laneDir)
}
