//go:build windows

package pty

import (
	"bytes"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestBytesAvailableSeesPendingBytes pins the primitive the drain's silent
// cut is gated on: a peek must see bytes that conhost has delivered to the
// master pipe before any Read takes them, and zero once they are read. A
// peek that cannot see pending bytes would let the cut drop them with no
// marker (the exact defect the gate exists to close), so this failing
// means the gate is blind, not just imprecise.
//
// The writer has to be a real process in the pseudo-console: conhost only
// produces output bytes for a child, so the test runs a one-shot cmd that
// writes one line and exits. The bytes stay readable afterwards because
// conhost holds the pipe's write end until the pseudo-console closes.
func TestBytesAvailableSeesPendingBytes(t *testing.T) {
	p, err := New(80, 25)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if n, err := p.BytesAvailable(); err != nil || n != 0 {
		t.Fatalf("fresh pty should peek empty, got %d bytes, err %v", n, err)
	}
	sp, err := p.Spawn([]string{"cmd", "/c", "echo probe"}, nil, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(sp.Proc)
	defer windows.CloseHandle(sp.Thread)
	if ev, err := windows.WaitForSingleObject(sp.Proc, 10_000); err != nil || ev != windows.WAIT_OBJECT_0 {
		t.Fatalf("probe child did not exit cleanly: event %d, err %v", ev, err)
	}
	// The line must become visible to a peek while it sits unread; poll,
	// because conhost delivers asynchronously.
	deadline := time.Now().Add(3 * time.Second)
	var pending uint32
	for time.Now().Before(deadline) {
		n, perr := p.BytesAvailable()
		if perr != nil {
			t.Fatalf("peek: %v", perr)
		}
		if n > 0 {
			pending = n
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pending == 0 {
		t.Fatal("the child's output never became visible to a peek within 3s; the silent-cut gate would be blind")
	}
	buf := make([]byte, 512)
	n, err := p.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf[:n], []byte("probe")) {
		t.Fatalf("read after peek returned %q, want the probe line", buf[:n])
	}
	// Give conhost a moment to finish delivering, then confirm the peek
	// is empty again so the gate re-arms after a delivery.
	time.Sleep(100 * time.Millisecond)
	if n, err := p.BytesAvailable(); err != nil || n != 0 {
		t.Fatalf("after reading the line, peek should be empty, got %d bytes, err %v", n, err)
	}
}
