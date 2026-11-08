package child

import (
	"errors"
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

// Deliveries serialize inside the door: one helper attaches, suppresses
// echo, writes, waits and flushes at a time, so two answers can never
// interleave their console-mode choreography on one console. The replier's
// own path is untouched and stays independent: replies are a terminal's
// answer, not an owner's, and their discipline is the replier's own.
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
// closed while it was delivering: the bytes were written to a console
// whose job had passed the blocked gate moments before, and the close
// means the job ended in the same instant. Whether it read the answer is
// the job's own exit to say, so this is delivered-with-audit, not refused.
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
