package lane

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// killExt is the companion file a kill request leaves beside a ticket.
const killExt = ".kill"

// KillRequest is what a killer leaves for a participant: who asked, why, and
// when. The reason is mandatory at the CLI because the killed job's owner
// is going to read it on their own stderr, and "killed" with no reason is
// the message that starts an argument.
type KillRequest struct {
	By     string `json:"by"`
	ByPID  int    `json:"by_pid,omitempty"`
	Reason string `json:"reason"`
	At     string `json:"at"`
}

// KilledError is returned by Acquire when a waiting participant is killed.
type KilledError struct{ Request KillRequest }

func (e *KilledError) Error() string {
	return fmt.Sprintf("killed by %s: %s", e.Request.By, e.Request.Reason)
}

// ErrNoParticipant is wrapped by RequestKill when the pid is not in the queue.
var ErrNoParticipant = errors.New("no live participant with that pid")

// RequestKill addresses a kill to the live participant with pid. The request
// is a file next to the ticket, which is the only channel every participant
// already watches: a holder polls its own kill file the way a waiter polls
// its position, so no signal, pipe or port is involved and the same
// mechanism works on every platform. The killer does not stop anything
// itself; the participant does, so it can say on its own stderr who killed
// it and why before it goes.
//
// The request is written under the registry lock so it cannot land between
// a scan that found the ticket and a release that removed it, and through a
// rename so a reader never sees half a file.
func (q *Queue) RequestKill(pid int, req KillRequest) (Entry, error) {
	var found Entry
	err := q.withRegistry(func() error {
		live, err := q.scanLocked(time.Now())
		if err != nil {
			return err
		}
		for _, e := range live {
			if e.Ticket.PID == pid {
				found = e
				return q.writeKill(e.File, req)
			}
		}
		return fmt.Errorf("queue %q has no live participant with pid %d: %w", q.Key, pid, ErrNoParticipant)
	})
	if err != nil {
		return Entry{}, err
	}
	q.Logf("queue=%s event=kill-request pid=%d by=%s reason=%q", q.Key, pid, req.By, req.Reason)
	return found, nil
}

// requestKillFile addresses a request to one ticket by file name. It exists
// for tests, where two tickets of one process share a pid.
func (q *Queue) requestKillFile(ticketFile string, req KillRequest) error {
	return q.withRegistry(func() error { return q.writeKill(ticketFile, req) })
}

func (q *Queue) writeKill(ticketFile string, req KillRequest) error {
	if req.At == "" {
		req.At = time.Now().Format(time.RFC3339Nano)
	}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	path := ticketPath(q.Dir, ticketFile+killExt)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// KillRequested reports whether a kill has been addressed to this ticket. An
// unreadable request still counts: the intent to stop is clear even when
// the reason did not survive, and stopping is the safe direction.
func (e *Enrollment) KillRequested() (KillRequest, bool) {
	b, err := os.ReadFile(e.path + killExt)
	if err != nil {
		return KillRequest{}, false
	}
	var r KillRequest
	if err := json.Unmarshal(b, &r); err != nil {
		return KillRequest{By: "(unreadable request)", Reason: "(unreadable request)"}, true
	}
	return r, true
}

// WaitGone polls until no live participant has pid, or wait elapses. It is
// how a killer learns whether the request was honoured: the ticket vanishes
// when the participant releases, and the kernel makes it reapable if the
// participant simply died.
func (q *Queue) WaitGone(pid int, wait, poll time.Duration) (bool, error) {
	if poll <= 0 {
		poll = 100 * time.Millisecond
	}
	deadline := time.Now().Add(wait)
	for {
		var present bool
		err := q.withRegistry(func() error {
			live, err := q.scanLocked(time.Now())
			if err != nil {
				return err
			}
			for _, e := range live {
				if e.Ticket.PID == pid {
					present = true
				}
			}
			return nil
		})
		if err != nil {
			return false, err
		}
		if !present {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		time.Sleep(poll)
	}
}

// reapKillFiles deletes kill requests whose ticket is gone. The caller must
// hold the registry lock. A request outlives its ticket when the participant
// died before reading it or was reaped by someone else's scan.
func (q *Queue) reapKillFiles(names []os.DirEntry) {
	for _, de := range names {
		if de.IsDir() || !strings.HasSuffix(de.Name(), killExt) {
			continue
		}
		ticket := strings.TrimSuffix(de.Name(), killExt)
		if _, err := os.Stat(filepath.Join(q.Dir, ticket)); errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(filepath.Join(q.Dir, de.Name()))
		}
	}
}
