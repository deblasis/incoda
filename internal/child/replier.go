package child

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/deblasis/incoda/internal/osc7501"
)

// serialWriter is the one door to a pty's input. Everything bound for the
// job's console input (the Ctrl-C the supervisor forwards, the OSC 7501
// support reply) goes through Write, which holds a lock across the whole
// call, so two writers can never interleave their bytes mid-sequence.
type serialWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *serialWriter) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(b)
}

// replier answers OSC 7501 support queries without ever blocking the output
// reader. The reader only enqueues (reply is non-blocking); a single
// goroutine owns the actual write to the input side, so a job that never
// reads its console input can fill the input pipe and wedge that write
// without stalling the output stream or the tee behind it. A reply that does
// not fit the queue is dropped: the program asked a terminal that did not
// answer, which every program using the protocol already tolerates.
type replier struct {
	w        io.Writer
	q        chan struct{}
	done     chan struct{}
	stopWait time.Duration

	mu        sync.Mutex
	closed    bool
	abandoned bool
}

const replyQueue = 8

func newReplier(w io.Writer) *replier {
	r := &replier{w: w, q: make(chan struct{}, replyQueue), done: make(chan struct{}), stopWait: time.Second}
	go r.loop()
	return r
}

// loop is the only goroutine that writes replies.
func (r *replier) loop() {
	defer close(r.done)
	for range r.q {
		r.mu.Lock()
		skip := r.abandoned
		r.mu.Unlock()
		if skip {
			continue
		}
		_, _ = r.w.Write(osc7501.SupportReply())
	}
}

// reply queues one support reply. Never blocks.
func (r *replier) reply() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	select {
	case r.q <- struct{}{}:
	default:
	}
}

// stop flushes queued replies and ends the loop, waiting at most stopWait:
// a write wedged on an unread input pipe is abandoned (its goroutine ends
// when the pipe is closed by the caller), and replies still queued are
// discarded.
func (r *replier) stop() {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.q)
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

type replyFunc func()

func (f replyFunc) Write(b []byte) (int, error) { f(); return len(b), nil }

// statusTap sits in front of the tee. The bytes go to next exactly as read
// (the result and error are next's, so a tee failure still tears the job
// down); the scanner only looks.
type statusTap struct {
	next io.Writer
	sc   osc7501.Scanner
}

func newStatusTap(next io.Writer, r *replier) *statusTap {
	t := &statusTap{next: next}
	t.sc.Respond(replyFunc(r.reply))
	return t
}

func (t *statusTap) Write(p []byte) (int, error) {
	n, err := io.Writer(t.next).Write(p)
	t.sc.Scan(p, nil)
	return n, err
}

// keyWriter writes bytes to a pty input as win32-input-mode key events
// (CSI Vk;Sc;Uc;Kd;Cs;Rc _), each character a key-down then a key-up, the
// whole run as one underlying Write. ConPTY switches the input to this mode
// itself (it prints ?9001h at the start of every session) and parses a bare
// ESC-led byte string as VT input instead of typed text, so a reply sent raw
// never reaches the job's console input; as key events it arrives as the
// characters the job reads. One Write keeps the events of a reply together
// under the serialWriter lock.
type keyWriter struct{ w io.Writer }

func (k keyWriter) Write(b []byte) (int, error) {
	buf := make([]byte, 0, len(b)*32)
	for _, c := range string(b) {
		buf = fmt.Appendf(buf, "\x1b[0;0;%d;1;0;1_\x1b[0;0;%d;0;0;1_", c, c)
	}
	if _, err := k.w.Write(buf); err != nil {
		return 0, err
	}
	return len(b), nil
}
