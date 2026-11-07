package child

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/osc7501"
)

type recWriter struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	writes  [][]byte
	block   chan struct{} // when non-nil, Write waits for it to close
	started chan struct{}
}

func (w *recWriter) Write(p []byte) (int, error) {
	if w.started != nil {
		select {
		case w.started <- struct{}{}:
		default:
		}
	}
	if w.block != nil {
		<-w.block
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes = append(w.writes, append([]byte(nil), p...))
	return w.buf.Write(p)
}

func (w *recWriter) writesSeen() [][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writes
}

func (w *recWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// The reader goroutine only scans and enqueues; replies reach the writer in
// order, whole, and the passthrough bytes are never touched.
func TestScanTeeAnswersQueriesAndPassesBytesThrough(t *testing.T) {
	w := &recWriter{}
	r := newReplier(w)
	var tee bytes.Buffer
	st := newStatusTap(&tee, r)
	stream := []byte("a\x1b]7501;?\x07b\x1b]7501;state=working\x07c\x1b]7501;?\x1b\\d")
	for _, c := range stream { // worst case split: one byte per read
		if _, err := st.Write([]byte{c}); err != nil {
			t.Fatal(err)
		}
	}
	r.stop()
	if !bytes.Equal(tee.Bytes(), stream) {
		t.Fatalf("passthrough mutated: %q", tee.Bytes())
	}
	want := string(osc7501.SupportReply()) + string(osc7501.SupportReply())
	if w.String() != want {
		t.Fatalf("replies = %q, want two support replies", w.String())
	}
}

// A job that never reads its input must not stall the output path: replies
// that do not fit are dropped, Write keeps returning, and stop gives up
// waiting on a wedged writer.
func TestReplierNeverBlocksTheReader(t *testing.T) {
	w := &recWriter{block: make(chan struct{}), started: make(chan struct{}, 1)}
	r := newReplier(w)
	r.stopWait = 100 * time.Millisecond
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			r.reply()
		}
		r.stop()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reader blocked behind an unread input pipe")
	}
	close(w.block)
}

// After stop no further write reaches the destination (the handle behind it
// is about to be closed).
func TestReplierStoppedWritesNothing(t *testing.T) {
	w := &recWriter{}
	r := newReplier(w)
	r.stop()
	r.reply()
	time.Sleep(20 * time.Millisecond)
	if w.String() != "" {
		t.Fatalf("write after stop: %q", w.String())
	}
}

// ConPTY announces win32-input-mode (?9001h) and then parses plain input
// as VT, which swallows an ESC-led reply. Each reply character therefore
// goes in as a key event, down then up, and the whole reply is ONE write to
// the serialized input so nothing can land between its events.
func TestKeyWriterEncodesWin32InputEventsInOneWrite(t *testing.T) {
	w := &recWriter{}
	kw := keyWriter{w: w}
	n, err := kw.Write([]byte("\x1b]"))
	if err != nil || n != 2 {
		t.Fatalf("Write = %d, %v; want 2, nil", n, err)
	}
	want := "\x1b[0;0;27;1;0;1_\x1b[0;0;27;0;0;1_" + "\x1b[0;0;93;1;0;1_\x1b[0;0;93;0;0;1_"
	if w.String() != want {
		t.Fatalf("encoded = %q, want %q", w.String(), want)
	}
	if got := len(w.writesSeen()); got != 1 {
		t.Fatalf("want one underlying write, got %d", got)
	}
}
