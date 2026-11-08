package lane

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The answer door mirrors the kill door exactly: a companion file beside
// the ticket, written under the registry lock through a rename, read by the
// participant's own poll. The ack is the file's fate: the holder deletes it
// once the answer is delivered, and rewrites it with a reason when it
// refuses, so the answerer can tell the two apart from outside the run.
func TestAnswerRequestReachesTheTicket(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	en, err := q.Enroll(Ticket{Command: []string{"zig", "build"}, PTY: true, Owner: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := en.AnswerRequested(); ok {
		t.Fatal("a fresh ticket has no answer request")
	}

	req := AnswerRequest{By: "alice", ByPID: 77, Text: "yes, ship it"}
	entry, err := q.RequestAnswer(os.Getpid(), req)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Ticket.PID != os.Getpid() {
		t.Fatalf("RequestAnswer returned the wrong participant: %+v", entry.Ticket)
	}
	got, ok := en.AnswerRequested()
	if !ok {
		t.Fatal("the holder must see its answer request")
	}
	if got.By != req.By || got.Text != req.Text || got.ByPID != req.ByPID || got.At == "" {
		t.Fatalf("request did not round-trip: %+v", got)
	}
	if line := lastLog(t, q); !strings.Contains(line, "event=answer-request") {
		t.Fatalf("the request must be logged, got %q", line)
	}

	// Delivered: the holder removes the file, which is the ack the
	// answerer polls for.
	if err := en.AnswerDelivered(); err != nil {
		t.Fatal(err)
	}
	if _, ok := en.AnswerRequested(); ok {
		t.Fatal("a delivered answer must not still be requested")
	}

	// Refused: the holder rewrites the file with the reason, and the
	// answerer reads it back through the same door.
	if _, err := q.RequestAnswer(os.Getpid(), req); err != nil {
		t.Fatal(err)
	}
	if err := en.AnswerRefused("job pid %d is not reporting blocked"); err != nil {
		t.Fatal(err)
	}
	got, ok = en.AnswerRequested()
	if !ok || got.Refused == "" {
		t.Fatalf("a refusal must carry its reason, got %+v ok=%v", got, ok)
	}

	// Release sweeps answer files with the ticket, like kill files.
	en.Release(0)
	for _, ext := range []string{AnswerExt, killExt} {
		if leftovers := ticketCompanions(t, q.Dir, ext); len(leftovers) != 0 {
			t.Fatalf("%s file survived release: %v", ext, leftovers)
		}
	}
}

func TestAnswerRequestUnknownPid(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if _, err := q.RequestAnswer(999999, AnswerRequest{By: "x", Text: "y"}); err == nil {
		t.Fatal("a pid that is not in the queue must be refused")
	}
}

// A withdrawn request never existed as far as the holder is concerned: the
// CLI uses this to take back a request it gated itself out of, so the run
// never sees a request it would only refuse.
func TestWithdrawAnswerRemovesTheRequest(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	en, err := q.Enroll(Ticket{Command: []string{"zig", "build"}, PTY: true, Owner: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.RequestAnswer(os.Getpid(), AnswerRequest{By: "mallory", Text: "rm -rf"}); err != nil {
		t.Fatal(err)
	}
	if err := q.WithdrawAnswer(os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if _, ok := en.AnswerRequested(); ok {
		t.Fatal("a withdrawn answer must not be requested")
	}
}

// An answer request whose ticket is gone is reaped with the ticket, the
// same rule kill files follow.
func TestAnswerFileReapedWithGoneTicket(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	en, err := q.Enroll(Ticket{Command: []string{"zig", "build"}, PTY: true, Owner: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.RequestAnswer(os.Getpid(), AnswerRequest{By: "alice", Text: "y"}); err != nil {
		t.Fatal(err)
	}
	en.Release(0)
	// A scan (any later participant's) reaps the orphan.
	if _, err := q.Enroll(Ticket{Command: []string{"go", "test"}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(ticketCompanions(t, q.Dir, AnswerExt)) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("answer file outlived its ticket: %v", ticketCompanions(t, q.Dir, AnswerExt))
}

func ticketCompanions(t *testing.T, dir, ext string) []string {
	t.Helper()
	var found []string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ext) {
			found = append(found, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}
