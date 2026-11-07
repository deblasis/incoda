//go:build windows

package child

import (
	"errors"
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

// TestQuietCutOKAfterTeardown enters the state every real drain lives in:
// job gone, pseudo-console closed, input end closed, epilogue delivered.
// There conhost's write end is gone and a peek reports the pipe broken and
// empty - nothing can ever land in it again, which is "provably empty", so
// the gate must allow the quiet cut. The gate as first shipped refused this
// state forever (its peek errored, and the gate treated any error as
// unknown), so every clean exit paid the full grace window and a false
// "tail was lost" marker; the other unit guards never closed the input end
// and the pseudo-console, which is why they stayed green through it.
//
// The drain below is peek-driven and never parks: a ReadFile queued before
// the write end closes is never woken by it (measured), so reads happen only
// when a peek has proven bytes deliverable, and the loop ends on the peek
// that reports the write end gone.
func TestQuietCutOKAfterTeardown(t *testing.T) {
	p, err := pty.New(80, 25)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	sp, err := p.Spawn([]string{"cmd", "/c", "echo probe"}, nil, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(sp.Proc)
	defer windows.CloseHandle(sp.Thread)
	if ev, err := windows.WaitForSingleObject(sp.Proc, 10_000); err != nil || ev != windows.WAIT_OBJECT_0 {
		t.Fatalf("probe child did not exit cleanly: event %d, err %v", ev, err)
	}
	// The production teardown order (runPTY: console in the background,
	// input end closed inline).
	go p.CloseConsole()
	p.CloseInput()

	deadline := time.Now().Add(5 * time.Second)
	for {
		n, perr := p.BytesAvailable()
		if perr != nil {
			if !errors.Is(perr, windows.ERROR_BROKEN_PIPE) && !errors.Is(perr, windows.ERROR_NO_DATA) {
				t.Fatalf("peek failed in a way teardown does not explain: %v", perr)
			}
			break
		}
		if n > 0 {
			buf := make([]byte, n)
			if _, rerr := p.Read(buf); rerr != nil {
				t.Fatalf("read of %d peeked bytes failed: %v", n, rerr)
			}
			continue
		}
		if time.Now().After(deadline) {
			t.Fatal("the pipe never went broken within 5s of teardown; this test pins the torn-down state and cannot run vacuously against a live conhost")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !quietCutOK(p) {
		t.Fatal("the gate refused the torn-down pipe: broken means empty AND final, and refusing it makes every clean exit pay the grace cut and a false data-loss marker")
	}
}

// TestPeekVerdict pins the silent-cut policy itself, arm by arm, as a pure
// table: which peek answers authorize cutting the master with no in-band
// marker, and which refuse. The gate's failure direction - toward the
// marker, never toward silence - lives here where a mutation can hit it;
// the pipe-backed guards above cannot see it, because their peeks only
// ever return nil or the write-end-gone errors in reachable states, so an
// over-accepting gate ships green under them.
func TestPeekVerdict(t *testing.T) {
	cases := []struct {
		name  string
		avail uint32
		err   error
		want  bool
	}{
		{"clean empty pipe cuts", 0, nil, true},
		{"clean pipe with pending bytes refuses", 18, nil, false},
		{"broken pipe cuts", 0, windows.ERROR_BROKEN_PIPE, true},
		{"no-data pipe cuts", 0, windows.ERROR_NO_DATA, true},
		{"busy pipe refuses", 0, windows.ERROR_PIPE_BUSY, false},
		{"an unknown error refuses", 0, errors.New("device gone"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := peekVerdict(tc.avail, tc.err); got != tc.want {
				t.Fatalf("peekVerdict(%d, %v) = %v, want %v", tc.avail, tc.err, got, tc.want)
			}
		})
	}
}
