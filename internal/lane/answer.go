package lane

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/deblasis/incoda/internal/textsafe"
)

// answerIDShape is the one shape a request id may take: the minted
// "<pid>-<nanotime>". The door file is writable by any local process, and
// a planted id could otherwise smuggle log-shaped bytes past the CLI's
// verdict matcher; an id that is not this shape is not a request at all.
var answerIDShape = regexp.MustCompile(`^[0-9]+-[0-9]+$`)

// AnswerExt is the companion file an answer request leaves beside a
// ticket, the same door a kill request uses: a file next to the ticket is
// the only channel every participant already watches.
const AnswerExt = ".answer"

// AnswerFileMax bounds what the door will read and write: the payload a
// request may carry is capped at the CLI, but the door file is writable by
// any local process, and the reader is the trusted run - a planted file of
// any size must cost one stat, never a whole read.
const AnswerFileMax = 16 << 10

// AnswerRequest is what an answerer leaves for a participant: who asked,
// the text to type at the job's console, and an identity token minted by
// RequestAnswer. The holder answers only to that token: the verdict is a
// rewrite of the same file (Delivered or Refused), never a deletion, so
// the file's absence can never be mistaken for an ack and a second
// answerer's request can never be destroyed by the first one's verdict.
type AnswerRequest struct {
	ID      string `json:"id"`
	By      string `json:"by"`
	ByPID   int    `json:"by_pid,omitempty"`
	Text    string `json:"text,omitempty"`
	At      string `json:"at"`
	Refused string `json:"refused,omitempty"`
	Verdict string `json:"verdict,omitempty"` // delivered | refused, written by the holder only
}

// RequestAnswer addresses an answer to the live participant with pid, under
// the registry lock and through a rename, exactly like RequestKill. The
// gate is the holder's to enforce: the holder reads the request on its own
// poll, checks the owner against the ticket, and either types the text at
// the job or answers with a refusal.
func (q *Queue) RequestAnswer(pid int, req AnswerRequest) (Entry, error) {
	if req.ID == "" {
		req.ID = fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	}
	if !answerIDShape.MatchString(req.ID) {
		return Entry{}, fmt.Errorf("answer id %q is not the minted <pid>-<nanotime> shape", textsafe.Escape(req.ID))
	}
	var found Entry
	err := q.withRegistry(func() error {
		live, _, err := q.scanLocked(time.Now())
		if err != nil {
			return err
		}
		for _, e := range live {
			if e.Ticket.PID == pid {
				found = e
				return q.writeAnswer(e.File, req)
			}
		}
		return fmt.Errorf("queue %q has no live participant with pid %d: %w", q.Key, pid, ErrNoParticipant)
	})
	if err != nil {
		return Entry{}, err
	}
	q.Logf("queue=%s event=answer-request pid=%d by=%s", q.Key, pid, textsafe.LogValue(req.By))
	return found, nil
}

// WithdrawAnswer takes a request back before the holder acts on it: the
// CLI uses it when its own gate refuses, so the run never sees a request
// it would only refuse itself. Only the request with that id is taken.
func (q *Queue) WithdrawAnswer(pid int, id string) error {
	return q.withRegistry(func() error {
		live, _, err := q.scanLocked(time.Now())
		if err != nil {
			return err
		}
		for _, e := range live {
			if e.Ticket.PID == pid {
				req, ok := readAnswer(ticketPath(q.Dir, e.File+AnswerExt))
				if !ok || req.ID != id {
					return nil // someone else's request: not ours to take
				}
				return os.Remove(ticketPath(q.Dir, e.File+AnswerExt))
			}
		}
		return nil
	})
}

func (q *Queue) writeAnswer(ticketFile string, req AnswerRequest) error {
	if req.At == "" {
		req.At = time.Now().Format(time.RFC3339Nano)
	}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	path := ticketPath(q.Dir, ticketFile+AnswerExt)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readAnswer reads the request addressed to a ticket, bounded: a file past
// AnswerFileMax, unreadable, or unparseable is no request at all.
func readAnswer(path string) (AnswerRequest, bool) {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > AnswerFileMax {
		return AnswerRequest{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return AnswerRequest{}, false
	}
	var r AnswerRequest
	if err := json.Unmarshal(b, &r); err != nil {
		return AnswerRequest{}, false
	}
	if !answerIDShape.MatchString(r.ID) {
		return AnswerRequest{}, false
	}
	return r, true
}

// AnswerRequested reports the answer addressed to this ticket, if any. A
// request the holder has already answered comes back with its Verdict set,
// which is what the answerer polls for.
func (e *Enrollment) AnswerRequested() (AnswerRequest, bool) {
	return readAnswer(e.path + AnswerExt)
}

// AnswerDelivered is the holder's success ack for the request with id: the
// file is rewritten with the verdict (never deleted - absence must not be
// readable as an ack), under the registry lock and re-read so it can only
// ever answer the request it delivered, never a newer one that landed in
// flight. The verdict is swept with the ticket, and by nothing else.
func (e *Enrollment) AnswerDelivered(id string) error {
	return e.answerVerdict(id, "delivered", "")
}

// AnswerRefused is the holder's refusal ack for the request with id. The
// rewrite is atomic (temp file and rename) and drops the payload: a
// refusal that echoed the typed text back to disk would keep the secret
// the answerer tried to deliver sitting in the state directory.
func (e *Enrollment) AnswerRefused(id, reason string) error {
	return e.answerVerdict(id, "refused", reason)
}

func (e *Enrollment) answerVerdict(id, verdict, reason string) error {
	if e.q == nil {
		return nil
	}
	return e.q.withRegistry(func() error {
		req, ok := readAnswer(e.path + AnswerExt)
		if !ok || req.ID != id {
			return nil // a different request owns the file now: leave it to its own poll
		}
		req.Text = ""
		req.Verdict = verdict
		req.Refused = reason
		b, err := json.Marshal(req)
		if err != nil {
			return err
		}
		path := e.path + AnswerExt
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, b, 0o644); err != nil {
			return err
		}
		return os.Rename(tmp, path)
	})
}
