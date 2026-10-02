package lane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/lockfile"
	"github.com/deblasis/incoda/internal/textsafe"
)

const (
	registryLockName = "registry.lock"
	logName          = "lane.log"
)

// Queue is a handle on one named queue's state directory.
//
// Two locks are in play and they are not the same thing:
//
//   - The registry lock is held for microseconds at a time and serialises
//     *mutation and inspection of the ticket set*. It exists to close the
//     window between "a ticket file exists" and "its owner has locked it": a
//     scanner that saw a ticket in that window would find the lock free,
//     conclude the owner was dead, and delete a living participant's ticket.
//     Holding the registry lock across create+lock, across release, and across
//     every scan removes that window and also makes the arrival stamp order
//     agree with the order in which tickets become visible.
//
//   - A ticket lock is held for the whole lifetime of a participant and is the
//     actual liveness signal. Nothing ever blocks on a ticket lock; it is only
//     ever probed non-blockingly.
//
// The lock order is registry-then-ticket, and ticket acquisition is always
// non-blocking, so the pair cannot deadlock.
type Queue struct {
	Key      string
	Dir      string
	registry *lockfile.File
	readOnly bool
	// budget bounds every wait for the registry lock (SetBudget).
	budget budget
}

// budget is a caller's --wait budget: it runs from start for wait, and a
// negative wait never ends. The zero budget is unset.
type budget struct {
	start time.Time
	wait  time.Duration
	set   bool
}

// Registry lock bounds for this binary's own lane operations. A registry
// hold lasts microseconds; only a holder stopped inside one (Ctrl-Z, a
// debugger, a kill window never resumed) keeps it longer, and such a holder
// must cost a run, a kill or a config at most its own --wait, never hang it.
// Every wait polls TryLock (LockBy); none blocks in the kernel except a
// handle whose budget never ends (a negative --wait).
var (
	// defaultRegistryWait bounds a registry lock wait of a handle that has
	// no budget: force-release, the watch screen's killer, tests.
	defaultRegistryWait = 5 * time.Second
	// releaseRegistryWait bounds Release's wait. Past it Release goes on
	// without the lock (see Release).
	releaseRegistryWait = 2 * time.Second
	// registryFloor is the least any bounded wait gets, so a run whose
	// budget is spent still takes a lock an ordinary Enroll or Release holds
	// for an instant. Tests shorten it.
	registryFloor = 2 * time.Second
)

// SetBudget bounds every registry lock wait of this handle by the caller's
// --wait budget, which runs from start; a negative wait has no end. Each
// wait still gets registryFloor. Position caps each poll's wait at
// PollProbeWait raised to registryFloor (so at most registryFloor while
// the budget lasts), and a waiting run checks its interrupt and kill file
// between polls. A wait that runs out fails with ErrRegistryBusy.
func (q *Queue) SetBudget(start time.Time, wait time.Duration) {
	q.budget = budget{start: start, wait: wait, set: true}
}

// registryDeadline is when one registry lock wait gives up: the end of the
// budget (defaultRegistryWait from now without one), at most limit from now
// when limit is positive, and never less than registryFloor from now. The
// zero time means no deadline.
func (q *Queue) registryDeadline(limit time.Duration) time.Time {
	now := time.Now()
	var d time.Time
	switch {
	case !q.budget.set:
		d = now.Add(defaultRegistryWait)
	case q.budget.wait >= 0:
		d = q.budget.start.Add(q.budget.wait)
	}
	if limit > 0 && (d.IsZero() || now.Add(limit).Before(d)) {
		d = now.Add(limit)
	}
	if d.IsZero() {
		return d
	}
	if floor := now.Add(registryFloor); d.Before(floor) {
		d = floor
	}
	return d
}

// lockRegistry takes the registry lock within registryDeadline(limit).
func (q *Queue) lockRegistry(limit time.Duration) error {
	var err error
	if dl := q.registryDeadline(limit); dl.IsZero() {
		err = q.registry.Lock()
	} else {
		err = LockBy(q.registry, dl)
	}
	if err != nil {
		return fmt.Errorf("registry lock: %w", err)
	}
	return nil
}

// Mode says what a Queue handle may create or change.
type Mode int

const (
	// Create makes the lane directory and its registry lock when missing.
	// Only commands that have migrated the state directory use it, so it
	// never runs against the old queues/ root.
	Create Mode = iota
	// Existing opens a lane that already exists and never creates a
	// directory, so it cannot recreate a root the migration has just moved
	// away. It may create the registry lock inside an existing lane
	// directory, and scans reap dead tickets as usual.
	Existing
	// ReadOnly opens only files that already exist and changes nothing:
	// scans skip dead tickets without removing them and Logf writes
	// nothing. Read-only commands use it on a layout not upgraded yet.
	ReadOnly
)

// Open prepares key's lane under <stateDir>/lanes and opens its registry
// lock file. It does not take any lock.
func Open(stateDir, key string) (*Queue, error) {
	return OpenIn(LanesDir(stateDir), key, Create)
}

// OpenIn opens key's lane directory inside root (lanes/ on layout 2, the old
// queues/ for a read-only view of a layout not upgraded yet) in mode.
func OpenIn(root, key string, mode Mode) (*Queue, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	dir := filepath.Join(root, key)
	var reg *lockfile.File
	var err error
	switch mode {
	case Create:
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create queue directory: %w", err)
		}
		reg, err = lockfile.Open(RegistryLockPath(dir))
	case Existing:
		if fi, serr := os.Stat(dir); serr != nil || !fi.IsDir() {
			return nil, fmt.Errorf("queue %q has no state in %s: %w", key, root, os.ErrNotExist)
		}
		reg, err = lockfile.Open(RegistryLockPath(dir))
	case ReadOnly:
		reg, err = lockfile.OpenExisting(RegistryLockPath(dir))
	default:
		return nil, fmt.Errorf("unknown open mode %d", mode)
	}
	if err != nil {
		return nil, fmt.Errorf("open registry lock: %w", err)
	}
	return &Queue{Key: key, Dir: dir, registry: reg, readOnly: mode == ReadOnly}, nil
}

// Close releases the registry handle. It does not release tickets.
func (q *Queue) Close() error {
	if q == nil {
		return nil
	}
	return q.registry.Close()
}

// RegistryLockPath is the path of a queue directory's registry lock.
func RegistryLockPath(queueDir string) string { return filepath.Join(queueDir, registryLockName) }

// withRegistry runs fn inside one hold of the registry lock, waiting for it
// only as long as the handle's budget allows (registryDeadline).
func (q *Queue) withRegistry(fn func() error) error { return q.withRegistryLimit(0, fn) }

// withRegistryLimit is withRegistry with each wait also capped at limit.
func (q *Queue) withRegistryLimit(limit time.Duration, fn func() error) error {
	if err := q.lockRegistry(limit); err != nil {
		return err
	}
	defer q.registry.Unlock()
	return fn()
}

// withRegistryBy is withRegistry for a read-only view: it waits for the
// registry lock only until deadline and fails with ErrRegistryBusy past it.
func (q *Queue) withRegistryBy(deadline time.Time, fn func() error) error {
	if err := LockBy(q.registry, deadline); err != nil {
		return fmt.Errorf("registry lock: %w", err)
	}
	defer q.registry.Unlock()
	return fn()
}

// Logf appends one line to the queue's handoff log. Log failures are never
// fatal: the log is history for humans, not state the algorithm reads. A
// read-only handle writes nothing.
func (q *Queue) Logf(format string, args ...any) {
	if q.readOnly {
		return
	}
	AppendLog(q.Dir, format, args...)
}

// AppendLog appends one timestamped line to the lane.log of a lane
// directory. The migration uses it for lanes it touches without opening
// them.
func AppendLog(dir, format string, args ...any) {
	f, err := os.OpenFile(LogPath(dir), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

// LogPath is the lane.log of a lane directory.
func LogPath(dir string) string { return filepath.Join(dir, logName) }

// TailLog returns the last n log lines, oldest first.
func (q *Queue) TailLog(n int) []string {
	b, err := os.ReadFile(filepath.Join(q.Dir, logName))
	if err != nil {
		return nil
	}
	var lines []string
	start := 0
	for i := 0; i < len(b); i++ {
		if b[i] == '\n' {
			line := string(b[start:i])
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			if line != "" {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	if start < len(b) {
		lines = append(lines, string(b[start:]))
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// scanLocked lists live tickets in arrival order, reaps dead ones, and
// resolves the effective slot count under the same lock hold, so the Holding
// flags and the count can never disagree. The caller must hold the registry
// lock.
//
// Reaping is the whole staleness story: a ticket whose exclusive lock can be
// taken has no living owner, because the kernel drops that lock on process
// death however the process died.
func (q *Queue) scanLocked(now time.Time) ([]Entry, int, error) {
	names, err := os.ReadDir(q.Dir)
	if err != nil {
		return nil, 0, err
	}
	var live []Entry
	for _, de := range names {
		if de.IsDir() {
			continue
		}
		ord, ok := parseTicketName(de.Name())
		if !ok {
			continue
		}
		path := ticketPath(q.Dir, de.Name())
		// The ticket is opened without O_CREATE: one a Release unlinked
		// after the ReadDir above (Release may run without the registry
		// lock, see Release) is simply gone, not a dead holder to recreate,
		// reap and log.
		lf, err := lockfile.OpenExisting(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		var free bool
		if err == nil {
			free, err = lf.TryLock()
			lf.Close()
		}
		if err != nil {
			// A ticket we cannot even open is not something we can reason
			// about; treat it as live so we fail safe (wait) rather than
			// running concurrently with something we mis-read.
			free = false
		}
		if free {
			if q.readOnly {
				continue
			}
			// The only record of how a hard-killed holder ended. Without
			// this line the log shows an enqueue with no ending, and a
			// history that cannot say how a job finished cannot be used to
			// size a queue.
			// Only the scan that removed the file logs it, so a ticket a
			// lockless Release removed meanwhile is not logged as reaped.
			removed := os.Remove(path) == nil
			_ = os.Remove(path + killExt)
			if removed {
				q.Logf("queue=%s event=reaped pid=%d", q.Key, ord.pid)
			}
			continue
		}
		e := Entry{File: de.Name(), order: ord}
		if b, err := os.ReadFile(path); err != nil {
			e.PayloadError = "read: " + err.Error()
		} else if len(b) == 0 {
			e.PayloadError = "payload is empty"
		} else if err := json.Unmarshal(b, &e.Ticket); err != nil {
			e.PayloadError = "parse: " + err.Error()
		}
		if e.Ticket.ArrivalNano == 0 {
			e.Ticket.ArrivalNano = ord.arrivalNano
		}
		if e.Ticket.PID == 0 {
			e.Ticket.PID = ord.pid
		}
		live = append(live, e)
	}
	sortTickets(live)
	slots := q.effectiveSlotsLocked(live)
	for i := range live {
		// A participant that already acquired is holding whatever the count
		// says now: an exclusive arrival or a smaller --slots narrows the
		// queue for newcomers but never revokes a running job, and status
		// must not show that job as waiting.
		live[i].Holding = i < slots || live[i].Ticket.AcquireNano != 0
		live[i].fill(now)
	}
	if !q.readOnly {
		q.reapKillFiles(names)
	}
	return live, slots, nil
}

// effectiveSlots resolves the slot count for the current ticket set.
//
// On a queue whose config sets slots, that number is the width in both
// directions: the result is clamped to it, so nobody can narrow the queue by
// asking for less, and nobody can widen it either. Tickets that carry a
// different count still occur (a stale binary that predates per-queue config,
// a ticket written during a rolling upgrade) and they ride at the configured
// width rather than dragging everyone down to one or lifting the queue past
// what it was sized for.
//
// A queue with no configured count keeps the original rule: the minimum
// requested by any live participant, floored at 1. Mixing --slots values
// there is a configuration error; the minimum is the safe direction, and
// `incoda run` warns when it sees a disagreement.
//
// An exclusive participant overrides both: while one is live the count is 1,
// whatever the queue or anyone else asked for. That is the one narrowing that
// survives, because it is the ticket's explicit ask for the machine alone, and
// it is not counted as a disagreement.
func effectiveSlots(live []Entry, cfgSlots int) int {
	slots := 0
	for _, e := range live {
		if e.Ticket.Exclusive {
			return 1
		}
		s := e.Ticket.Slots
		if s < 1 {
			s = 1
		}
		if slots == 0 || s < slots {
			slots = s
		}
	}
	if slots < 1 {
		slots = 1
	}
	if cfgSlots > 0 {
		// The configured count is a clamp on both ends, not a floor: a live
		// ticket carrying a larger count (written before the config was
		// narrowed) must not admit more holders than the queue now allows.
		if slots > cfgSlots {
			slots = cfgSlots
		}
		if slots < cfgSlots {
			slots = cfgSlots
		}
	}
	return slots
}

// effectiveSlotsLocked is effectiveSlots with the queue's own config applied.
// The caller must hold the registry lock. A config that cannot be read must
// not widen the queue, so the resolution falls back to the ticket-set minimum
// with no configured floor, exactly the pre-config behavior.
func (q *Queue) effectiveSlotsLocked(live []Entry) int {
	cfg, err := q.LoadConfig()
	if err != nil {
		return effectiveSlots(live, 0)
	}
	return effectiveSlots(live, cfg.Slots)
}

// SlotsDisagree reports whether live participants asked for different slot
// counts.
func SlotsDisagree(live []Entry) bool {
	seen := 0
	for _, e := range live {
		if e.Ticket.Exclusive {
			continue
		}
		s := e.Ticket.Slots
		if s < 1 {
			s = 1
		}
		if seen == 0 {
			seen = s
		} else if seen != s {
			return true
		}
	}
	return false
}

// Snapshot is the observer's view of a queue.
type Snapshot struct {
	Key            string   `json:"key"`
	Dir            string   `json:"dir"`
	Exists         bool     `json:"exists"`
	EffectiveSlots int      `json:"effective_slots"`
	Config         Config   `json:"config"`
	ConfigError    string   `json:"config_error,omitempty"`
	Holders        []Entry  `json:"holders"`
	Waiting        []Entry  `json:"waiting"`
	Log            []Entry  `json:"-"`
	RecentEvents   []string `json:"recent_events"`
}

// Observe scans the queue without joining it. The ticket set and the config
// are read inside one registry lock hold (SaveConfig writes under the same
// lock), so the snapshot can never pair one era's tickets with another era's
// count.
func (q *Queue) Observe(logLines int) (*Snapshot, error) {
	return q.observe(logLines, q.withRegistry)
}

// ObserveBy is Observe for status, watch and queues: it waits for the
// registry lock only until deadline, so a lock another process keeps for
// ever (a stopped incoda) fails the observation with ErrRegistryBusy
// instead of hanging the view.
func (q *Queue) ObserveBy(logLines int, deadline time.Time) (*Snapshot, error) {
	return q.observe(logLines, func(fn func() error) error { return q.withRegistryBy(deadline, fn) })
}

func (q *Queue) observe(logLines int, with func(func() error) error) (*Snapshot, error) {
	var live []Entry
	var slots int
	var cfg Config
	var cfgErr error
	err := with(func() error {
		var e error
		live, slots, e = q.scanLocked(time.Now())
		if e != nil {
			return e
		}
		cfg, cfgErr = q.LoadConfig()
		return nil
	})
	if err != nil {
		return nil, err
	}
	if cfgErr == nil && len(live) == 0 && cfg.Slots > 0 {
		// Nobody is enrolled to carry the number, so the config is the
		// only thing that can say how wide an empty queue is.
		slots = cfg.Slots
	}
	s := &Snapshot{
		Key:            q.Key,
		Dir:            q.Dir,
		Exists:         true,
		EffectiveSlots: slots,
		Config:         cfg,
		Holders:        []Entry{},
		Waiting:        []Entry{},
		RecentEvents:   q.TailLog(logLines),
	}
	if cfgErr != nil {
		s.ConfigError = cfgErr.Error()
	}
	for _, e := range live {
		if e.Holding {
			s.Holders = append(s.Holders, e)
		} else {
			s.Waiting = append(s.Waiting, e)
		}
	}
	return s, nil
}

// Enrollment is a ticket this process owns. Its OS lock is held until Release
// or process death.
type Enrollment struct {
	q      *Queue
	name   string
	path   string
	lock   *lockfile.File
	ticket Ticket
	// Stats is set by the caller once the command has finished and is
	// written on the release line. Zero means "nothing measured".
	Stats Stats
}

// Ticket returns a copy of the enrolled ticket payload.
func (e *Enrollment) Ticket() Ticket { return e.ticket }

// Name is the enrolled ticket's file name, which INCODA_HELD carries so a
// nested run can probe the exact ticket.
func (e *Enrollment) Name() string { return e.name }

// ErrTimeout is returned by Acquire when --wait elapses without a free slot.
var ErrTimeout = errors.New("timed out waiting for a slot")

// Enroll creates this process's ticket and takes its lifetime lock. After
// Enroll the process is in the queue, in arrival order, whether or not it holds
// a slot yet.
func (q *Queue) Enroll(t Ticket) (*Enrollment, error) {
	return q.EnrollContext(context.Background(), t, nil)
}

// EnrollContext is Enroll for a run: its wait for the registry lock (within
// the handle's budget) also ends when ctx does, with ctx's error and no
// ticket written, so an interrupt is never held up by a registry lock
// another process keeps. busy, when not nil, is called once if that wait
// lasts longer than PollProbeWait, so the caller can say why it waits.
func (q *Queue) EnrollContext(ctx context.Context, t Ticket, busy func()) (*Enrollment, error) {
	if err := lockByContext(ctx, q.registry, q.registryDeadline(0), PollProbeWait, busy); err != nil {
		return nil, fmt.Errorf("registry lock: %w", err)
	}
	en, err := q.enrollLocked(t)
	_ = q.registry.Unlock()
	if err != nil {
		return nil, err
	}
	extra := ""
	if en.ticket.Exclusive {
		extra += " exclusive=true"
	}
	q.Logf("queue=%s event=enqueue pid=%d slots=%d%s%s cmd=%s", q.Key, en.ticket.PID, en.ticket.Slots, extra, en.ticket.attribution(), textsafe.LogValue(en.ticket.CommandString()))
	return en, nil
}

// enrollLocked writes this process's ticket. The caller holds the registry
// lock.
func (q *Queue) enrollLocked(t Ticket) (*Enrollment, error) {
	// The stamp is taken while holding the registry lock so that stamp
	// order and file-visibility order cannot disagree.
	now := time.Now()
	t.Queue = q.Key
	t.PID = os.Getpid()
	t.ArrivalNano = now.UnixNano()
	t.Arrival = now.Format(time.RFC3339Nano)
	// The configured count is the queue's width, in full. A ticket that
	// does not ask is stamped with it; a ticket that asks for a
	// different number is refused rather than silently clamped in either
	// direction. Narrowing used to be allowed through the minimum rule
	// and one stray "--slots 1" dragged a five-slot queue down to one
	// for everyone; a job that genuinely needs the queue alone says
	// --exclusive, which is explicit, visible in status, and still
	// narrows to 1 on purpose. A missing or broken config leaves an
	// unset count at 1, the safe direction.
	//
	// The rules are checked here, under the registry lock, as well as
	// before: a lane closed, or made to require a reason, after the
	// run read its config still refuses it (spec 2.5, 4.5). A config
	// written by a newer incoda may carry rules this binary does not
	// know, so it fails closed.
	cfg, cfgErr := q.LoadConfig()
	var ns *NewerSchemaError
	switch {
	case errors.As(cfgErr, &ns):
		return nil, cfgErr
	case cfgErr == nil && cfg.Closed != "":
		return nil, &ClosedError{Key: q.Key, Text: cfg.Closed}
	case cfgErr == nil && cfg.RequireReason && strings.TrimSpace(t.Reason) == "":
		return nil, &ReasonRequiredError{Key: q.Key}
	}
	switch {
	case cfgErr == nil && cfg.Slots > 0 && t.Slots >= 1 && t.Slots != cfg.Slots:
		return nil, NewSlotsDisagreement(q.Key, cfg.Slots, t.Slots, t.Exclusive)
	case t.Slots < 1 && cfgErr == nil && cfg.Slots > 0:
		t.Slots = cfg.Slots
	case t.Slots < 1:
		t.Slots = 1
	}
	name := ticketName(t.ArrivalNano, t.PID)
	path := ticketPath(q.Dir, name)
	lf, err := lockfile.Open(path)
	if err != nil {
		return nil, err
	}
	ok, err := lf.TryLock()
	if err != nil {
		lf.Close()
		return nil, err
	}
	if !ok {
		// Same nanosecond and same pid as a live ticket is impossible for
		// two distinct processes; this means a leftover we cannot own.
		lf.Close()
		return nil, fmt.Errorf("ticket %s is already locked", name)
	}
	b, _ := json.Marshal(t)
	if err := lf.Truncate(b); err != nil {
		lf.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return &Enrollment{q: q, name: name, path: path, lock: lf, ticket: t}, nil
}

// Release drops the ticket. It is safe to call more than once.
func (e *Enrollment) Release(rc int) {
	if e == nil || e.lock == nil {
		return
	}
	// Release never waits for ever: past releaseRegistryWait it goes on
	// without the registry lock. That is safe for a release: the slot is
	// freed by closing the ticket lock, and a scanner that listed the name
	// meanwhile finds the file gone or unlocked and reaps it.
	locked := LockBy(e.q.registry, time.Now().Add(releaseRegistryWait)) == nil
	// Close before unlink: on Windows the handle must go away for the
	// delete to take effect promptly even with share-delete.
	_ = e.lock.Close()
	_ = os.Remove(e.path)
	_ = os.Remove(e.path + killExt)
	if locked {
		_ = e.q.registry.Unlock()
	}
	e.lock = nil
	// dur is the wall time from enqueue to release (the arrival nano is the
	// enqueue instant): time in the lane, wait included; cpu= already covers
	// processor time. ArrivalNano is a wall stamp, so a backward clock step
	// (NTP resync) could go negative - clamped like every other duration
	// this repo renders.
	dur := ""
	if e.ticket.ArrivalNano > 0 {
		d := time.Since(time.Unix(0, e.ticket.ArrivalNano)).Round(time.Second)
		if d < 0 {
			d = 0
		}
		dur = " dur=" + d.String()
	}
	e.q.Logf("queue=%s event=release pid=%d rc=%d%s%s%s", e.q.Key, e.ticket.PID, rc, e.Stats.logFields(), e.ticket.attribution(), dur)
}

// Position reports this enrollment's 0-based place in the live queue plus the
// current effective slot count. Both come from one scan under the registry
// lock, so the place and the count describe the same instant. It waits for
// the registry lock at most PollProbeWait raised to registryFloor (and,
// past the floor, never past the budget); past that it fails with
// ErrRegistryBusy, which Acquire reads as "not admitted on this poll".
func (e *Enrollment) Position() (idx, slots int, live []Entry, err error) {
	err = e.q.withRegistryLimit(PollProbeWait, func() error {
		var scanErr error
		live, slots, scanErr = e.q.scanLocked(time.Now())
		return scanErr
	})
	if err != nil {
		return -1, 0, nil, err
	}
	idx = -1
	for i, x := range live {
		if x.File == e.name {
			idx = i
			break
		}
	}
	return idx, slots, live, nil
}

// MarkAcquired records the acquisition time in the ticket payload so that
// status can distinguish "waiting since" from "holding since".
func (e *Enrollment) MarkAcquired() {
	now := time.Now()
	e.ticket.AcquireNano = now.UnixNano()
	e.ticket.Acquired = now.Format(time.RFC3339Nano)
	b, _ := json.Marshal(e.ticket)
	_ = e.q.withRegistry(func() error { return e.lock.Truncate(b) })
	e.q.Logf("queue=%s event=acquire pid=%d%s cmd=%s", e.q.Key, e.ticket.PID, e.ticket.attribution(), textsafe.LogValue(e.ticket.CommandString()))
}

// ForceRelease deletes every ticket in the queue. It refuses while any live
// participant exists unless live is true.
//
// The refusal is deliberate and is carried over from build-lane, where a blind
// force-release once let two heavy builds run at the same time and caused a real
// collision. Deleting a live participant's ticket does not stop that process; it
// only removes the record that was keeping the next caller out of the way.
func (q *Queue) ForceRelease(allowLive bool) (removed int, err error) {
	err = q.withRegistry(func() error {
		live, _, err := q.scanLocked(time.Now())
		if err != nil {
			return err
		}
		if len(live) > 0 && !allowLive {
			pids := make([]int, 0, len(live))
			for _, e := range live {
				pids = append(pids, e.Ticket.PID)
			}
			return fmt.Errorf("queue %q has %d LIVE participant(s) %v; breaking their tickets is how two heavy jobs end up running at once (a blind force-release caused exactly that collision in the tool this replaces). "+
				"Their locks are released automatically when those processes die, so you almost never need this. If you are sure: incoda force-release --queue %s --live",
				q.Key, len(live), pids, q.Key)
		}
		entries, err := os.ReadDir(q.Dir)
		if err != nil {
			return err
		}
		for _, de := range entries {
			if de.IsDir() {
				continue
			}
			if _, ok := parseTicketName(de.Name()); !ok {
				continue
			}
			if os.Remove(ticketPath(q.Dir, de.Name())) == nil {
				removed++
			}
		}
		return nil
	})
	return removed, err
}

// LockAll takes the registry lock of every queue in the order given;
// callers pass them sorted by key, the lock order of spec 3.1. It returns
// the function that releases them in reverse order. A registry rebuild
// uses it to hold every lane still while it checks for tickets and
// rewrites machine.json. Each lock is waited for only until deadline (the
// zero time: as long as it takes); past it LockAll releases what it took
// and fails with ErrRegistryBusy.
func LockAll(qs []*Queue, deadline time.Time) (func(), error) {
	var held []*Queue
	unlock := func() {
		for i := len(held) - 1; i >= 0; i-- {
			_ = held[i].registry.Unlock()
		}
	}
	for _, q := range qs {
		var err error
		if deadline.IsZero() {
			err = q.registry.Lock()
		} else {
			err = LockBy(q.registry, deadline)
		}
		if err != nil {
			unlock()
			return nil, fmt.Errorf("registry lock of %q: %w", q.Key, err)
		}
		held = append(held, q)
	}
	return unlock, nil
}

// LiveLocked lists the live tickets, reaping dead ones as any scan does.
// The caller holds this queue's registry lock (LockAll).
func (q *Queue) LiveLocked() ([]Entry, error) {
	live, _, err := q.scanLocked(time.Now())
	return live, err
}

// ListQueues returns the keys that have state under <stateDir>/lanes.
func ListQueues(stateDir string) ([]string, error) { return ListIn(LanesDir(stateDir)) }

// ListIn returns the keys that have a lane directory inside root. A missing
// root has none.
func ListIn(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, de := range entries {
		if de.IsDir() && ValidateKey(de.Name()) == nil {
			keys = append(keys, de.Name())
		}
	}
	return keys, nil
}

// Exists reports whether a queue key has state under <stateDir>/lanes.
func Exists(stateDir, key string) bool { return ExistsIn(LanesDir(stateDir), key) }

// ExistsIn reports whether root holds a lane directory for key.
func ExistsIn(root, key string) bool {
	fi, err := os.Stat(filepath.Join(root, key))
	return err == nil && fi.IsDir()
}
