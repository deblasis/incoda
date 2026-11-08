package child

import (
	"bytes"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/osc7501"
)

type wedgeWriter struct{ release, in chan struct{} }

func (w *wedgeWriter) Write(p []byte) (int, error) {
	select {
	case w.in <- struct{}{}:
	default:
	}
	<-w.release
	return len(p), nil
}

// The supervisor's Ctrl-C shares the door with reply writes but not their
// fate: a reply write parked on an unread input pipe parks the pump alone,
// never the Ctrl-C behind it.
func TestCtrlCIsNotBlockedByAWedgedReply(t *testing.T) {
	w := &wedgeWriter{release: make(chan struct{}), in: make(chan struct{}, 1)}
	defer close(w.release)
	input := &serialWriter{w: w}
	r := newReplier(input)
	r.stopWait = 50 * time.Millisecond
	r.Write(osc7501.SupportReply())
	<-w.in
	done := make(chan struct{})
	go func() { input.Write([]byte{0x03}); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Ctrl-C write blocked behind a wedged reply write")
	}
}

type fastWriter struct{ n atomic.Int64 }

func (f *fastWriter) Write(p []byte) (int, error) { f.n.Add(1); return len(p), nil }

// A query flood is answered at a bounded rate: at most one reply per gap,
// so what lands in the job's input queue cannot grow without limit.
func TestReplyRateIsBounded(t *testing.T) {
	w := &fastWriter{}
	r := newReplier(w)
	tap := newStatusTap(&bytes.Buffer{}, r)
	q := []byte("\x1b]7501;?\x07")
	for range 1000 {
		tap.Write(q)
		time.Sleep(time.Millisecond)
	}
	r.stop()
	if n := w.n.Load(); n > 100 {
		t.Fatalf("%d replies for 1000 queries", n)
	}
}

// A burst of queries in one read is owed one reply each: none is dropped by
// a queue cap while the writer is merely slow.
func TestBurstKeepsOneReplyPerQuery(t *testing.T) {
	w := &wedgeWriter{release: make(chan struct{}), in: make(chan struct{}, 1)}
	r := newReplier(w)
	tap := newStatusTap(&bytes.Buffer{}, r)
	tap.Write([]byte(bytes.Repeat([]byte("\x1b]7501;?\x07"), 30)))
	<-w.in
	r.mu.Lock()
	owed := len(r.owed)
	r.mu.Unlock()
	close(w.release)
	r.stop()
	if owed+1 < 30 {
		t.Fatalf("30 queries in one read -> %d replies kept (cap %d)", owed+1, maxOwedReplies)
	}
}
