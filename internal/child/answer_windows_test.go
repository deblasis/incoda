//go:build windows

package child

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// keyEvents carries text as key events the console reassembles: non-BMP
// runes must arrive as their UTF-16 surrogate halves (a lone truncated half
// is mojibake on the job's side), and CR is a key event like any other, so
// a cooked line reader completes on it.
func TestKeyEventsCarrySurrogatesAndCR(t *testing.T) {
	recs := keyEvents([]byte("a😀\r"))
	var runes []uint16
	for _, r := range recs {
		if r.Event.KeyDown == 1 {
			runes = append(runes, r.Event.Rune)
		}
	}
	want := []uint16{'a', 0xD83D, 0xDE00, '\r'}
	if len(runes) != len(want) {
		t.Fatalf("got %d down events (%v), want %d", len(runes), runes, len(want))
	}
	for i := range want {
		if runes[i] != want[i] {
			t.Fatalf("rune %d = %x, want %x (all: %v)", i, runes[i], want[i], runes)
		}
	}
}

// A door with no injector behind it must refuse by name, not panic: a
// non-pty run (or a run whose spawn failed before the injector existed)
// can be asked a question all the same.
func TestAnswerDoorRefusesWithoutAPty(t *testing.T) {
	d := &AnswerDoor{}
	if err := d.Deliver("yes"); err == nil || !strings.Contains(err.Error(), "answer needs a pty") {
		t.Fatalf("want a named refusal, got %v", err)
	}
}

// The answer is typed input the job reads as if the owner sat down at the
// keyboard: a cooked-mode line reader gets the text plus the CR that
// completes its read, and nothing is echoed into the passthrough stream,
// so the job's log and the run's stdout stay exactly what the job wrote.
func TestAnswerDeliversEchoFreeToACookedModeReader(t *testing.T) {
	py, err := exec.LookPath("python")
	if err != nil {
		t.Skip("no python on PATH")
	}
	script := filepath.Join(t.TempDir(), "a.py")
	src := `
import sys, threading, os, time
def wd():
    time.sleep(8); os._exit(0)
threading.Thread(target=wd, daemon=True).start()
sys.stdout.write("PROMPT> "); sys.stdout.flush()
line = sys.stdin.readline()
sys.stdout.write("\r\nGOT<" + line.rstrip("\r\n") + ">\r\n"); sys.stdout.flush()
time.sleep(1)
`
	if err := os.WriteFile(script, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	var tee lockedBuffer
	door := &AnswerDoor{}
	done := make(chan struct{})
	var runErr error
	go func() {
		defer close(done)
		_, runErr = Run([]string{py, script}, nil, nil, nil, nil,
			Options{PTY: &PTYOptions{Tee: &tee, Answer: door}})
	}()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(string(tee.Bytes()), "PROMPT>") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(string(tee.Bytes()), "PROMPT>") {
		t.Fatalf("the job never showed its prompt; tee: %q", tee.Bytes())
	}
	if err := door.Deliver("yes\r"); err != nil {
		t.Fatalf("deliver: %v\ntee so far: %q", err, tee.Bytes())
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the job never finished after the answer")
	}
	if runErr != nil {
		t.Fatal(runErr)
	}
	out := string(tee.Bytes())
	if !strings.Contains(out, "GOT<yes>") {
		t.Fatalf("the job did not read the answer:\n%q", out)
	}
	if strings.Count(out, "yes") != 1 {
		t.Fatalf("the answer echoed into the job output (console echo):\n%q", out)
	}
}

// One answer at a time: a second Deliver while the first is in flight
// waits its turn rather than interleaving two injections. The first
// writer parks on a gate the test opens only once the second caller is
// parked on the door - proving the block, not just the order.
func TestAnswerDoorSerializesDeliveries(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	gate := make(chan struct{})
	secondParked := make(chan struct{})
	w := writerFunc(func(b []byte) (int, error) {
		mu.Lock()
		calls = append(calls, string(b))
		mu.Unlock()
		if string(b) == "first\r" {
			<-gate
		}
		return len(b), nil
	})
	d := &AnswerDoor{}
	d.setInjector(w)
	go func() { _ = d.Deliver("first\r") }()
	time.Sleep(100 * time.Millisecond)
	go func() {
		close(secondParked)
		_ = d.Deliver("second\r")
	}()
	<-secondParked
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	sofar := append([]string(nil), calls...)
	mu.Unlock()
	if len(sofar) != 1 || sofar[0] != "first\r" {
		t.Fatalf("the second delivery ran before the first finished: %v", sofar)
	}
	close(gate)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := len(calls) == 2 && calls[0] == "first\r" && calls[1] == "second\r"
		mu.Unlock()
		if done {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("deliveries never completed in order: %v", calls)
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }

// A per-key reader - a raw-mode line editor, any question prompt that
// echoes and thinks between keys - consumes the injected events across
// reads with gaps far past any poll cadence. The delivery verdict must
// wait for the WHOLE payload to drain: breaking on partial movement
// flushes the unread remainder under the reader and calls it delivered.
func TestAnswerDeliversWholePayloadToAPerKeyReader(t *testing.T) {
	py, err := exec.LookPath("python")
	if err != nil {
		t.Skip("no python on PATH")
	}
	script := filepath.Join(t.TempDir(), "perkey.py")
	src := `
import msvcrt, sys, threading, os, time
def wd():
    time.sleep(20); os._exit(9)
threading.Thread(target=wd, daemon=True).start()
sys.stdout.write("PROMPT> "); sys.stdout.flush()
got = ""
while len(got) < 4:
    if msvcrt.kbhit():
        got += msvcrt.getwch()
    time.sleep(0.01)
sys.stdout.write("\r\nREAD<" + repr(got) + ">END\r\n"); sys.stdout.flush()
time.sleep(0.5)
`
	if err := os.WriteFile(script, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	var tee lockedBuffer
	door := &AnswerDoor{}
	done := make(chan struct{})
	var runErr error
	go func() {
		defer close(done)
		_, runErr = Run([]string{py, script}, nil, nil, nil, nil,
			Options{PTY: &PTYOptions{Tee: &tee, Answer: door}})
	}()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(string(tee.Bytes()), "PROMPT>") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(string(tee.Bytes()), "PROMPT>") {
		t.Fatalf("the job never showed its prompt; tee: %q", tee.Bytes())
	}
	if err := door.Deliver("yes\r"); err != nil {
		t.Fatalf("deliver: %v\ntee so far: %q", err, tee.Bytes())
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("the per-key reader never finished; tee: %q", tee.Bytes())
	}
	if runErr != nil {
		t.Fatal(runErr)
	}
	out := string(tee.Bytes())
	if !strings.Contains(out, "READ<'yes") {
		t.Fatalf("the reader got a truncated payload:\n%q", out)
	}
	if !strings.Contains(out, ">END") {
		t.Fatalf("the read never completed:\n%q", out)
	}
}
