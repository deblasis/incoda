package lane

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/deblasis/incoda/internal/lockfile"
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
}

// Open prepares the on-disk state for key and opens the registry lock file. It
// does not take any lock.
func Open(stateDir, key string) (*Queue, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	dir := QueueDir(stateDir, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create queue directory: %w", err)
	}
	reg, err := lockfile.Open(filepath.Join(dir, registryLockName))
	if err != nil {
		return nil, fmt.Errorf("open registry lock: %w", err)
	}
	return &Queue{Key: key, Dir: dir, registry: reg}, nil
}

// Close releases the registry handle. It does not release tickets.
func (q *Queue) Close() error {
	if q == nil {
		return nil
	}
	return q.registry.Close()
}

func (q *Queue) withRegistry(fn func() error) error {
	if err := q.registry.Lock(); err != nil {
		return fmt.Errorf("registry lock: %w", err)
	}
	defer q.registry.Unlock()
	return fn()
}

// Logf appends one line to the queue's handoff log. Log failures are never
// fatal: the log is history for humans, not state the algorithm reads.
func (q *Queue) Logf(format string, args ...any) {
	f, err := os.OpenFile(filepath.Join(q.Dir, logName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

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

// scanLocked lists live tickets in arrival order and reaps dead ones. The
// caller must hold the registry lock.
//
// Reaping is the whole staleness story: a ticket whose exclusive lock can be
// taken has no living owner, because the kernel drops that lock on process
// death however the process died.
func (q *Queue) scanLocked(now time.Time) ([]Entry, error) {
	names, err := os.ReadDir(q.Dir)
	if err != nil {
		return nil, err
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
		free, err := lockfile.IsFree(path)
		if err != nil {
			// A ticket we cannot even open is not something we can reason
			// about; treat it as live so we fail safe (wait) rather than
			// running concurrently with something we mis-read.
			free = false
		}
		if free {
			// The only record of how a hard-killed holder ended. Without
			// this line the log shows an enqueue with no ending, and a
			// history that cannot say how a job finished cannot be used to
			// size a queue.
			_ = os.Remove(path)
			q.Logf("queue=%s event=reaped pid=%d", q.Key, ord.pid)
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
	slots := effectiveSlots(live)
	for i := range live {
		// A participant that already acquired is holding whatever the count
		// says now: an exclusive arrival or a smaller --slots narrows the
		// queue for newcomers but never revokes a running job, and status
		// must not show that job as waiting.
		live[i].Holding = i < slots || live[i].Ticket.AcquireNano != 0
		live[i].fill(now)
	}
	return live, nil
}

// effectiveSlots resolves the slot count for the current ticket set as the
// minimum requested by any live participant, floored at 1, and 1 outright
// while an exclusive participant is live.
//
// Mixing --slots values on one queue is a configuration error; taking the
// minimum makes the *most restrictive* caller win, which is the safe direction.
// It is not a full guarantee: a participant already running is never revoked,
// so a late arrival with a smaller --slots can observe more holders than its own
// number. `incoda run` warns when it sees a disagreement. An exclusive ticket
// is the same rule used on purpose: it rides the minimum down to 1 and back
// up when it leaves, and because it is the ticket's own request rather than a
// mismatch, it is not a disagreement.
func effectiveSlots(live []Entry) int {
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
	return slots
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

// Observe scans the queue without joining it.
func (q *Queue) Observe(logLines int) (*Snapshot, error) {
	var live []Entry
	err := q.withRegistry(func() error {
		var e error
		live, e = q.scanLocked(time.Now())
		return e
	})
	if err != nil {
		return nil, err
	}
	cfg, cfgErr := q.LoadConfig()
	slots := effectiveSlots(live)
	if len(live) == 0 && cfg.Slots > 0 {
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

// ErrTimeout is returned by Acquire when --wait elapses without a free slot.
var ErrTimeout = errors.New("timed out waiting for a slot")

// Enroll creates this process's ticket and takes its lifetime lock. After
// Enroll the process is in the queue, in arrival order, whether or not it holds
// a slot yet.
func (q *Queue) Enroll(t Ticket) (*Enrollment, error) {
	var en *Enrollment
	err := q.withRegistry(func() error {
		// The stamp is taken while holding the registry lock so that stamp
		// order and file-visibility order cannot disagree.
		now := time.Now()
		t.Queue = q.Key
		t.PID = os.Getpid()
		t.ArrivalNano = now.UnixNano()
		t.Arrival = now.Format(time.RFC3339Nano)
		// The configured count is both the default for a ticket that did
		// not ask and the ceiling for one that asked for more: a queue that
		// says 2 means 2, and a caller passing --slots 4 is not allowed to
		// widen it. Asking for fewer still narrows it through the minimum
		// rule. A missing or broken config leaves an unset count at 1, the
		// safe direction.
		cfg, cfgErr := q.LoadConfig()
		switch {
		case t.Slots < 1 && cfgErr == nil && cfg.Slots > 0:
			t.Slots = cfg.Slots
		case t.Slots < 1:
			t.Slots = 1
		case cfgErr == nil && cfg.Slots > 0 && t.Slots > cfg.Slots:
			t.Slots = cfg.Slots
		}
		name := ticketName(t.ArrivalNano, t.PID)
		path := ticketPath(q.Dir, name)
		lf, err := lockfile.Open(path)
		if err != nil {
			return err
		}
		ok, err := lf.TryLock()
		if err != nil {
			lf.Close()
			return err
		}
		if !ok {
			// Same nanosecond and same pid as a live ticket is impossible for
			// two distinct processes; this means a leftover we cannot own.
			lf.Close()
			return fmt.Errorf("ticket %s is already locked", name)
		}
		b, _ := json.Marshal(t)
		if err := lf.Truncate(b); err != nil {
			lf.Close()
			_ = os.Remove(path)
			return err
		}
		en = &Enrollment{q: q, name: name, path: path, lock: lf, ticket: t}
		return nil
	})
	if err != nil {
		return nil, err
	}
	extra := ""
	if en.ticket.Exclusive {
		extra += " exclusive=true"
	}
	if en.ticket.Owner != "" {
		extra += " owner=" + en.ticket.Owner
	}
	q.Logf("queue=%s event=enqueue pid=%d slots=%d%s cmd=%s", q.Key, en.ticket.PID, en.ticket.Slots, extra, en.ticket.CommandString())
	return en, nil
}

// Release drops the ticket. It is safe to call more than once.
func (e *Enrollment) Release(rc int) {
	if e == nil || e.lock == nil {
		return
	}
	_ = e.q.withRegistry(func() error {
		// Close before unlink: on Windows the handle must go away for the
		// delete to take effect promptly even with share-delete.
		_ = e.lock.Close()
		_ = os.Remove(e.path)
		return nil
	})
	e.lock = nil
	e.q.Logf("queue=%s event=release pid=%d rc=%d%s", e.q.Key, e.ticket.PID, rc, e.Stats.logFields())
}

// Position reports this enrollment's 0-based place in the live queue plus the
// current effective slot count.
func (e *Enrollment) Position() (idx, slots int, live []Entry, err error) {
	err = e.q.withRegistry(func() error {
		var scanErr error
		live, scanErr = e.q.scanLocked(time.Now())
		return scanErr
	})
	if err != nil {
		return -1, 0, nil, err
	}
	slots = effectiveSlots(live)
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
	e.q.Logf("queue=%s event=acquire pid=%d cmd=%s", e.q.Key, e.ticket.PID, e.ticket.CommandString())
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
		live, err := q.scanLocked(time.Now())
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

// ListQueues returns the keys that have state on this machine.
func ListQueues(stateDir string) ([]string, error) {
	entries, err := os.ReadDir(QueuesDir(stateDir))
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

// Exists reports whether a queue key has any state on this machine.
func Exists(stateDir, key string) bool {
	fi, err := os.Stat(QueueDir(stateDir, key))
	return err == nil && fi.IsDir()
}
