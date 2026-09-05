package lane

import (
	"context"
	"time"
)

// AcquireOptions controls the polling wait.
type AcquireOptions struct {
	// Wait is the maximum time to spend queueing. Zero means "fail immediately
	// if a slot is not already free"; a negative value means wait forever.
	Wait time.Duration
	// Poll is the interval between position checks.
	Poll time.Duration
	// OnWait is called the first time the caller finds itself queued and then
	// once every Notify interval, with its 0-based position and the current
	// live set.
	OnWait func(pos int, slots int, live []Entry, waited time.Duration)
	// Notify is how often OnWait repeats. Zero disables the repeats.
	Notify time.Duration
	// Killed, when set, is consulted on every poll in addition to this
	// ticket's own kill file. A multi-key run passes a check over the keys
	// it already holds: a kill addressed to one of those must end the wait
	// on the next one, not sit unread until every key is held.
	Killed func() (KillRequest, bool)
}

// Acquire blocks until this enrollment holds a slot, the wait budget runs out,
// or ctx is cancelled.
//
// A participant holds a slot when its ticket is among the N lowest-ordered LIVE
// tickets, where N is the effective slot count. Because ticket order is fixed at
// enrollment and every participant computes it from the same filenames, service
// is first-in-first-out: a later arrival can never overtake an earlier one that
// is still waiting.
//
// The cost of that fairness is that a freed slot can sit idle for up to one poll
// interval, because the next-in-line has to notice. That is the deliberate
// trade: no signalling, no cron, nothing to go stale.
func (e *Enrollment) Acquire(ctx context.Context, opt AcquireOptions) error {
	if opt.Poll <= 0 {
		opt.Poll = 500 * time.Millisecond
	}
	start := time.Now()
	notified := false
	lastNotify := time.Time{}

	for {
		// A waiter is killed by the same file a holder is: checked before
		// the position so a request that landed during the sleep cannot
		// be overtaken by a slot that freed at the same moment.
		if req, ok := e.KillRequested(); ok {
			return &KilledError{Request: req}
		}
		if opt.Killed != nil {
			if req, ok := opt.Killed(); ok {
				return &KilledError{Request: req}
			}
		}
		idx, slots, live, err := e.Position()
		if err != nil {
			return err
		}
		if idx >= 0 && idx < slots {
			e.MarkAcquired()
			return nil
		}

		waited := time.Since(start)
		if opt.OnWait != nil {
			due := !notified ||
				(opt.Notify > 0 && time.Since(lastNotify) >= opt.Notify)
			if due {
				opt.OnWait(idx, slots, live, waited)
				notified = true
				lastNotify = time.Now()
			}
		}

		if opt.Wait == 0 {
			return ErrTimeout
		}
		if opt.Wait > 0 && waited >= opt.Wait {
			return ErrTimeout
		}

		sleep := opt.Poll
		if opt.Wait > 0 {
			if left := opt.Wait - waited; left < sleep {
				sleep = left
			}
		}
		if sleep <= 0 {
			return ErrTimeout
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleep):
		}
	}
}
