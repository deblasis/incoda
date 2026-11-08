package lane

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/deblasis/incoda/internal/textsafe"
)

// AnswerExt is the companion file an answer request leaves beside a
// ticket, the same door a kill request uses: a file next to the ticket is
// the only channel every participant already watches.
const AnswerExt = ".answer"

// AnswerRequest is what an answerer leaves for a participant: who asked,
// and the text to type at the job's console. Refused is empty on the way
// in; the holder that refuses a request rewrites the file with its reason
// there, so the answerer learns the verdict instead of timing out.
type AnswerRequest struct {
	By      string `json:"by"`
	ByPID   int    `json:"by_pid,omitempty"`
	Text    string `json:"text"`
	At      string `json:"at"`
	Refused string `json:"refused,omitempty"`
}

// RequestAnswer addresses an answer to the live participant with pid, under
// the registry lock and through a rename, exactly like RequestKill. The
// gate is the holder's to enforce: the holder reads the request on its own
// poll, checks the owner against the ticket, and either types the text at
// the job or rewrites the file with its refusal.
func (q *Queue) RequestAnswer(pid int, req AnswerRequest) (Entry, error) {
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
// it would only refuse itself.
func (q *Queue) WithdrawAnswer(pid int) error {
	return q.withRegistry(func() error {
		live, _, err := q.scanLocked(time.Now())
		if err != nil {
			return err
		}
		for _, e := range live {
			if e.Ticket.PID == pid {
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

// AnswerRequested reports the answer addressed to this ticket, if any. A
// request the holder has already refused comes back with Refused set,
// which is the verdict the answerer polls for.
func (e *Enrollment) AnswerRequested() (AnswerRequest, bool) {
	b, err := os.ReadFile(e.path + AnswerExt)
	if err != nil {
		return AnswerRequest{}, false
	}
	var r AnswerRequest
	if err := json.Unmarshal(b, &r); err != nil {
		return AnswerRequest{}, false
	}
	return r, true
}

// AnswerDelivered is the holder's success ack: the request file's removal
// is what the answerer waits for.
func (e *Enrollment) AnswerDelivered() error {
	err := os.Remove(e.path + AnswerExt)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// AnswerRefused is the holder's refusal ack: the request file is rewritten
// with the reason, and the answerer reads it through the same door. The
// rewrite is atomic (temp file and rename) so a reader never sees half.
func (e *Enrollment) AnswerRefused(reason string) error {
	req, ok := e.AnswerRequested()
	if !ok {
		return nil
	}
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
}
