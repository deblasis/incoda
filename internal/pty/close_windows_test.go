//go:build windows

package pty

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/windows"
	"testing"
	"time"
)

// A reader parked on the master must also be woken by Close alone: Close
// is the catch-all teardown (deferred at every runPTY exit), and a future
// caller that never touches CloseReadEnd must inherit the same cancel,
// not the wedge the bare CloseHandle leaves behind.
func TestCloseWakesParkedReader(t *testing.T) {
	p, err := New(DefaultCols, DefaultRows)
	if err != nil {
		t.Fatal(err)
	}

	readDone := make(chan error, 1)
	go func() {
		buf := make([]byte, 64)
		_, rerr := p.Read(buf)
		readDone <- rerr
	}()

	time.Sleep(300 * time.Millisecond)

	p.Close()
	select {
	case rerr := <-readDone:
		if rerr == nil {
			t.Fatal("Close left the reader returning no error; it should report the abort")
		}
		if !errors.Is(rerr, syscall.ERROR_OPERATION_ABORTED) {
			t.Fatalf("the reader woke with %v; only ERROR_OPERATION_ABORTED proves the cancel ran", rerr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close left the reader parked: the catch-all teardown cancels nothing and a wedged conhost blocks the run forever")
	}

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
		if !errors.Is(rerr, syscall.ERROR_OPERATION_ABORTED) {
			t.Fatalf("the cut read woke with %v; only ERROR_OPERATION_ABORTED proves it was the cancel that woke it, not a broken path", rerr)
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

// A write parked on the master input pipe (a reply to a job that stopped
// reading its input) must not wedge the teardown behind it: CloseHandle
// alone does not wake a synchronous WriteFile, so CloseInput cancels first,
// exactly like the read-end cut it mirrors.
func TestCloseInputUnparksAWedgedWrite(t *testing.T) {
	var r, w windows.Handle
	if err := windows.CreatePipe(&r, &w, nil, 0); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(r)
	p := &Pty{mIn: w}
	writeDone := make(chan error, 1)
	go func() {
		chunk := make([]byte, 4096)
		for {
			if _, err := p.Write(chunk); err != nil {
				writeDone <- err
				return
			}
		}
	}()
	time.Sleep(300 * time.Millisecond)
	closed := make(chan struct{})
	go func() { p.CloseInput(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("CloseInput blocked behind a parked Write")
	}
	select {
	case err := <-writeDone:
		if err == nil {
			t.Fatal("the parked write returned nil; it should report the abort")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the parked write never returned after the cancel")
	}
}
