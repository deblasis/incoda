package child

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// AnswerDoor carries an owner's typed reply to a blocked job's console.
// The pty runner arms it after spawn (setInjector, the console injector
// bound to the job's pid); everything else may hold the zero value, whose
// Deliver refuses by name - a plain-pipe run has no console to type into.
//

// lockedInjector serializes every __coninj helper on one console: the
// helper's verdict watches the console's shared input buffer and its
// finish flushes whatever is unread, so two helpers at once could destroy
// each other's payload and fabricate a read. Replies and answers share it.
type lockedInjector struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedInjector) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(b)
}

// Deliveries serialize inside the door, and - through the shared
// lockedInjector the pty runner hands both writers - against the
// replier's helpers too: one __coninj at a time is the whole story of a
// console's input buffer, whoever asked for the typing.
//
// stop never waits for an in-flight delivery (its helper caps itself): the
// door goes dead instantly at teardown, and a delivery still running fails
// on its own when the console it addresses is gone.
type AnswerDoor struct {
	mu   sync.Mutex
	inj  io.Writer
	dead atomic.Bool
}

// ErrNoPtyAnswer names the refusal a run without a pseudo-console gives.
var ErrNoPtyAnswer = errors.New("answer needs a pty job: plain-pipe runs have no console to type into")

// ErrConsoleClosedAnswer is what a helper reports when the job's console
// closed while it was delivering: the write's fate is unknowable from
// outside, so this is a refusal - the job's own outcome is where the
// answerer looks next, and exit 0 is never claimed for a delivery that
// cannot be verified.
var ErrConsoleClosedAnswer = errors.New("the job's console closed while the answer was being delivered")

// setInjector arms the door. Called by the pty runner once the job's pid
// exists, before any answer can be delivered.
func (d *AnswerDoor) setInjector(w io.Writer) {
	d.mu.Lock()
	d.inj = w
	d.mu.Unlock()
}

// stop disarms the door for good: after the job is gone there is no
// console to answer into, and a pid's console is not the next pid's.
func (d *AnswerDoor) stop() { d.dead.Store(true) }

// Deliver types text at the job's console input and reports the injector's
// verdict: the helper's failure is the answerer's failure, not a silent
// no-op the run would have to guess about.
func (d *AnswerDoor) Deliver(text string) error {
	if d.dead.Load() {
		return ErrNoPtyAnswer
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dead.Load() || d.inj == nil {
		return ErrNoPtyAnswer
	}
	_, err := d.inj.Write([]byte(text))
	return err
}

// verdictError maps the injector helper's exit code to the error an
// answerer reads: 3, written but never read (the honest
// nobody-was-listening); STATUS_CONTROL_C_EXIT (0xC000013A), the console
// closed under the helper - a refusal, because the write's fate is
// unknowable; anything else, the generic failure.
func verdictError(exitCode int) error {
	switch exitCode {
	case 3:
		return errors.New("the job did not read what was typed at its console")
	case 0xC000013A:
		return ErrConsoleClosedAnswer
	default:
		return fmt.Errorf("console input injection failed: exit status %d", exitCode)
	}
}
