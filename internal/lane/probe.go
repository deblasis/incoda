package lane

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/deblasis/incoda/internal/lockfile"
)

// ErrRegistryBusy is what a bounded probe reports when another process
// kept the lane's registry lock past the probe's deadline: a stopped
// incoda, or one killed inside its kill window and never resumed, can hold
// it for ever. The probe cannot tell which tickets are live, so it reports
// the lane as live (fail safe for admission and idle checks) and read-only
// views print this text.
var ErrRegistryBusy = errors.New("cannot tell: registry lock held by another process")

// Probe bounds. No probe ever waits for a registry lock without a
// deadline.
const (
	// ViewProbeWait bounds the probes of one read-only view (status, watch,
	// doctor, queues): one deadline shared by every probe of the view.
	ViewProbeWait = 500 * time.Millisecond
	// PollProbeWait bounds the probes of one poll of a waiting loop (an
	// acquisition poll, an idle check of the upgrade), so the loop keeps
	// checking its own budget and its interrupt between probes.
	PollProbeWait = time.Second
	// OneShotProbeWait bounds the probes of a command that has no --wait
	// of its own (force-release).
	OneShotProbeWait = 5 * time.Second
	// probeFloor is the least a probe inside a budget gets, so a budget
	// that is nearly spent still reads a registry lock that an ordinary
	// Enroll or Release holds for an instant.
	probeFloor = 50 * time.Millisecond
	// probeRetry is the TryLock poll interval of a bounded probe.
	probeRetry = 10 * time.Millisecond
)

// ProbeDeadline is the deadline of one probe pass inside a --wait budget
// that ends at end (zero: no end): at most limit from now, never past end,
// and never less than a short floor from now.
func ProbeDeadline(end time.Time, limit time.Duration) time.Time {
	now := time.Now()
	d := now.Add(limit)
	if !end.IsZero() && end.Before(d) {
		d = end
	}
	if floor := now.Add(probeFloor); d.Before(floor) {
		d = floor
	}
	return d
}

// LockBy takes reg by polling TryLock until deadline. It never blocks in
// the kernel, so a registry lock another process holds for ever costs the
// caller at most its deadline; past it the error is ErrRegistryBusy. A
// zero or past deadline tries once.
func LockBy(reg *lockfile.File, deadline time.Time) error {
	for {
		ok, err := reg.TryLock()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		left := time.Until(deadline)
		if left <= 0 {
			return ErrRegistryBusy
		}
		time.Sleep(min(probeRetry, left))
	}
}

// CannotTell reports a probe that gave up on a registry lock another
// process holds (ErrRegistryBusy). Such an entry from ProbeLane stands for
// the whole lane: it has no ticket name and no pid.
func (p Probed) CannotTell() bool { return errors.Is(p.ProbeErr, ErrRegistryBusy) }

// AnyCannotTell reports whether a probe result holds a CannotTell entry.
func AnyCannotTell(ps []Probed) bool {
	for _, p := range ps {
		if p.CannotTell() {
			return true
		}
	}
	return false
}

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
// It waits for the registry lock only until deadline; past it the probe
// fails with ErrRegistryBusy. The caller must not hold this lane's
// registry lock through another handle: the probe would wait for itself.
func ProbeTicket(laneDir, name string, deadline time.Time) Probed {
	reg, err := lockfile.OpenExisting(RegistryLockPath(laneDir))
	if err != nil {
		return Probed{Name: name}
	}
	defer reg.Close()
	if err := LockBy(reg, deadline); err != nil {
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
//
// It waits for the registry lock only until deadline. Past it the lane is
// reported live: the result is one CannotTell entry (ProbeErr
// ErrRegistryBusy, no name, no pid) and a nil error, so admission counts
// it as held, idle checks keep waiting and read-only views print it.
func ProbeLane(laneDir string, deadline time.Time) ([]Probed, error) {
	reg, err := lockfile.OpenExisting(RegistryLockPath(laneDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer reg.Close()
	if err := LockBy(reg, deadline); err != nil {
		if errors.Is(err, ErrRegistryBusy) {
			return []Probed{{Live: true, ProbeErr: ErrRegistryBusy}}, nil
		}
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
//
// It waits for the registry lock only until deadline. A lock still held
// then means it cannot tell, so it deletes nothing and reports (false,
// nil): a lane it cannot read is not idle.
func RemoveIfIdle(laneDir string, deadline time.Time, keepLog func(logPath string)) (bool, error) {
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
	if err := LockBy(reg, deadline); err != nil {
		reg.Close()
		if errors.Is(err, ErrRegistryBusy) {
			return false, nil
		}
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
