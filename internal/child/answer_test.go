package child

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }

// The helper's exit code is the delivery verdict, and the door turns it
// into the error an answerer reads. A swap of the cases would flip a
// truncated delivery into a success and a closed console into a generic
// failure, so each mapping is pinned.
func TestVerdictErrorMapsEveryExitCode(t *testing.T) {
	if err := verdictError(3); err == nil || err.Error() != "the job did not read what was typed at its console" {
		t.Fatalf("exit 3: %v", err)
	}
	if err := verdictError(0xC000013A); !errors.Is(err, ErrConsoleClosedAnswer) {
		t.Fatalf("console close: %v", err)
	}
	err := verdictError(1)
	if err == nil || !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("generic: %v", err)
	}
}

// One injector per console is the class-B fix: the helper's verdict
// watches the console's shared input buffer and its finish flushes what
// is unread, so two helpers at once could destroy each other's payload
// and fabricate a read. Every __coninj spawn goes through this lock -
// the replier's writes and the answer door's alike - and a second writer
// is parked until the first has finished, proving the block.
func TestLockedInjectorParksASecondWriter(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	gate := make(chan struct{})
	secondParked := make(chan struct{})
	w := writerFunc(func(b []byte) (int, error) {
		mu.Lock()
		calls = append(calls, string(b))
		mu.Unlock()
		if string(b) == "first" {
			<-gate
		}
		return len(b), nil
	})
	inj := &lockedInjector{w: w}
	go func() { _, _ = inj.Write([]byte("first")) }()
	time.Sleep(100 * time.Millisecond)
	go func() {
		close(secondParked)
		_, _ = inj.Write([]byte("second"))
	}()
	<-secondParked
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	sofar := append([]string(nil), calls...)
	mu.Unlock()
	if len(sofar) != 1 || sofar[0] != "first" {
		t.Fatalf("the second writer ran before the first finished: %v", sofar)
	}
	close(gate)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := len(calls) == 2 && calls[0] == "first" && calls[1] == "second"
		mu.Unlock()
		if done {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("writers never completed in order: %v", calls)
}
