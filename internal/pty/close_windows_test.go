//go:build windows

package pty

import (
	"os"
	"testing"
	"time"
)

// A read parked on the master must be woken by CloseReadEnd, not left
// blocked for the life of the process: closing a handle does not cancel a
// synchronous read parked on it, the cancel does. A CloseReadEnd that only
// closes would hang any run whose conhost wedges past its grace window.
func TestCloseReadEndWakesParkedReader(t *testing.T) {
	p, err := New(DefaultCols, DefaultRows)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	readDone := make(chan error, 1)
	go func() {
		buf := make([]byte, 64)
		_, rerr := p.Read(buf)
		readDone <- rerr
	}()

	// Give the reader a moment to park on the empty master.
	time.Sleep(300 * time.Millisecond)

	p.CloseReadEnd()
	select {
	case rerr := <-readDone:
		if rerr == nil {
			t.Fatal("the cut read returned no error; it should report the abort")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CloseReadEnd left the reader parked: a cut that does not cancel first blocks the run forever")
	}

	// Close after CloseReadEnd shares the close: the master read end must
	// die exactly once however the run tears down.
	p.Close()

	// CloseConsole (inside Close, in its own goroutine) ends conhost; no
	// conhost parented to this process may outlive the pair.
	deadline := time.Now().Add(5 * time.Second)
	for {
		pids, cerr := ConhostPIDs(os.Getpid())
		if cerr == nil && len(pids) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("conhost still alive after Close: %v (err %v)", pids, cerr)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
