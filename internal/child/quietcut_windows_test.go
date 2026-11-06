//go:build windows

package child

import (
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/pty"
	"golang.org/x/sys/windows"
)

// TestQuietCutGateSeesPendingBytes pins the drain's silent-cut gate: a pty
// with unread bytes in its master pipe must NOT be cuttable without a
// marker, and one that is drained must be. The gate is what keeps the
// quiesce timer from dropping a tail that raced it; a gate that always
// says empty re-arms exactly that silent drop.
//
// The writer must be a real process in the pseudo-console (conhost
// produces output only for a child), so the test runs a one-shot cmd that
// writes one line and exits.
func TestQuietCutGateSeesPendingBytes(t *testing.T) {
	p, err := pty.New(80, 25)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if !quietCutOK(p) {
		t.Fatal("fresh pty should be cuttable (pipe empty)")
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
	// The line sits unread in the pipe; the gate must refuse a quiet cut
	// until it is drained.
	deadline := time.Now().Add(3 * time.Second)
	for quietCutOK(p) {
		if time.Now().After(deadline) {
			t.Fatal("the child's output never made the gate refuse a quiet cut within 3s; every tail racing the quiesce timer would be dropped silently")
		}
		time.Sleep(10 * time.Millisecond)
	}
	buf := make([]byte, 512)
	if _, err := p.Read(buf); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if !quietCutOK(p) {
		t.Fatal("drained pty should be cuttable again")
	}
}
