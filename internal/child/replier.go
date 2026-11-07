package child

import (
	"io"
	"sync"
	"time"

	"github.com/deblasis/incoda/internal/osc7501"
)

// serialWriter is the one door to a pty's input. Everything bound for the
// job's console input (the Ctrl-C the supervisor forwards, the OSC 7501
// support reply) goes through Write, and Write never blocks on the pty: it
// appends to a queue that one pump goroutine drains in order, so two
// writers can never interleave their bytes mid-sequence, and a write parked
// on a full input pipe parks the pump alone — never the supervisor's Ctrl-C,
// never the output reader. A queue past its bound drops the write: the
// caller's bytes are few and repeatable (a 0x03, a reply), and stalling the
// caller behind a job that will not read its input is the one outcome the
// door exists to prevent. Close discards what is queued and ends the pump;
// a write already in flight is the caller's to abandon (CancelIoEx before
// closing the handle beneath it, as CloseInput does).
type serialWriter struct {
	start   sync.Once
	mu      sync.Mutex
	w       io.Writer
	q       [][]byte
	closing bool
	wake    chan struct{}
	done    chan struct{}
}

// maxQueuedWrites bounds the pump's queue: the door must not become a
// second unbounded buffer in front of a job that will not read.
const maxQueuedWrites = 64

func (s *serialWriter) startPump() {
	s.wake = make(chan struct{}, 1)
	s.done = make(chan struct{})
	go s.pump()
}

func (s *serialWriter) Write(b []byte) (int, error) {
	s.start.Do(s.startPump)
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	if len(s.q) < maxQueuedWrites {
		s.q = append(s.q, append([]byte(nil), b...))
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return len(b), nil
}

func (s *serialWriter) pump() {
	defer close(s.done)
	for {
		s.mu.Lock()
		if s.closing {
			s.mu.Unlock()
			return
		}
		if len(s.q) == 0 {
			s.mu.Unlock()
			<-s.wake
			continue
		}
		b := s.q[0]
		s.q = s.q[1:]
		s.mu.Unlock()
		_, _ = s.w.Write(b)
	}
}

// Close stops the pump. Queued writes are discarded, and Close does not
// wait: a write in flight is in flight regardless, and the caller cancels
// it through the handle (see Pty.CloseInput).
func (s *serialWriter) Close() {
	s.start.Do(s.startPump)
	s.mu.Lock()
	s.closing = true
	s.q = nil
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// replyGap is the least time between two reply writes. A program flooding
// queries gets at most one reply per gap however many it sends, which keeps
// what lands in conhost's input queue bounded; a program asking at a saner
// pace still gets exactly one reply per query, because the gap only delays
// writes, never drops the queries behind them (the owed queue below).
const replyGap = 100 * time.Millisecond

// modeWait is how long a reply waits for the job's console to stop echoing
// before it is dropped. A program that asks and then starts reading cooked
// input (readline) never gets an answer from this terminal; one that asks
// and then switches its console to raw input (every program that can parse
// a reply has to) gets it within this window.
const modeWait = 750 * time.Millisecond

// maxOwedReplies bounds the replies owed to queries the writer has not
// answered yet: one per complete query, but never an unbounded pile in
// front of a job that will not read its input. Past the bound the query is
// dropped, which is the answer a terminal that stayed silent would have
// given.
const maxOwedReplies = 64

// replier answers OSC 7501 support queries without ever blocking the
// output reader. The reader only enqueues (reply, through Write, never
// blocks); a single goroutine owns the actual write to the input side,
// one reply owed per query, writes at least replyGap apart. The writer is
// the console injector (coninj_windows.go), which delivers the reply
// without echo whatever mode the job's console is in.
type replier struct {
	w        io.Writer
	gap      time.Duration
	stopWait time.Duration

	mu        sync.Mutex
	closed    bool
	abandoned bool
	owed      [][]byte
	last      time.Time
	wake      chan struct{}
	done      chan struct{}
}

func newReplier(w io.Writer) *replier {
	r := &replier{w: w, gap: replyGap, stopWait: time.Second, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go r.loop()
	return r
}

// Write is the scanner's Respond sink: one call per complete query, the
// bytes the reply must consist of. It only enqueues.
func (r *replier) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || len(r.owed) >= maxOwedReplies {
		return len(b), nil
	}
	r.owed = append(r.owed, append([]byte(nil), b...))
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return len(b), nil
}

// loop is the only goroutine that writes replies.
func (r *replier) loop() {
	defer close(r.done)
	for {
		r.mu.Lock()
		if len(r.owed) == 0 {
			closed := r.closed
			r.mu.Unlock()
			if closed {
				return
			}
			select {
			case <-r.wake:
			case <-time.After(r.gap):
			}
			continue
		}
		if r.abandoned {
			r.owed = nil
			r.mu.Unlock()
			return
		}
		b := r.owed[0]
		gap, last := r.gap, r.last
		r.mu.Unlock()

		// One reply per gap, however dense the queries.
		if d := time.Since(last); d < gap {
			time.Sleep(gap - d)
			continue
		}
		_, _ = r.w.Write(b)
		r.mu.Lock()
		if len(r.owed) > 0 {
			r.owed = r.owed[1:]
		}
		r.last = time.Now()
		closed := r.closed && len(r.owed) == 0
		r.mu.Unlock()
		if closed {
			return
		}
	}
}

// stop ends the loop, waiting at most stopWait: a write wedged on an
// unread input pipe is abandoned (the caller cancels it through the
// handle), and replies still owed are discarded with it.
func (r *replier) stop() {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
	r.mu.Unlock()
	t := time.NewTimer(r.stopWait)
	defer t.Stop()
	select {
	case <-r.done:
	case <-t.C:
		r.mu.Lock()
		r.abandoned = true
		r.mu.Unlock()
	}
}

// statusTap sits in front of the tee. The bytes go to next exactly as read
// (the result and error are next's, so a tee failure still tears the job
// down); the scanner only looks.
type statusTap struct {
	next io.Writer
	sc   osc7501.Scanner
}

func newStatusTap(next io.Writer, r *replier) *statusTap {
	t := &statusTap{next: next}
	t.sc.Respond(r)
	return t
}

func (t *statusTap) Write(p []byte) (int, error) {
	n, err := t.next.Write(p)
	t.sc.Scan(p, nil)
	return n, err
}
