package lane

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The answer door mirrors the kill door's shape: a companion file beside
// the ticket, written under the registry lock through a rename, read by
// the participant's own poll. The verdict is a rewrite carrying the
// request's own id - never a deletion - so the file's fate can only ever
// be read as the answer to the exact request it carries.
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

	req := AnswerRequest{ID: "req-1", By: "alice", ByPID: 77, Text: "yes, ship it"}
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
	if got.By != req.By || got.Text != req.Text || got.ByPID != req.ByPID || got.At == "" || got.ID != "req-1" {
		t.Fatalf("request did not round-trip: %+v", got)
	}
	if line := lastLog(t, q); !strings.Contains(line, "event=answer-request") {
		t.Fatalf("the request must be logged, got %q", line)
	}

	// Delivered: the verdict rewrites the file, scoped to the request's
	// id, and never deletes it - absence must not be readable as an ack.
	if err := en.AnswerDelivered("req-1"); err != nil {
		t.Fatal(err)
	}
	got, ok = en.AnswerRequested()
	if !ok || got.Verdict != "delivered" || got.ID != "req-1" {
		t.Fatalf("a delivered verdict must carry its id, got %+v ok=%v", got, ok)
	}
	if got.Text != "" {
		t.Fatalf("a verdict must not echo the payload back to disk, got %q", got.Text)
	}

	// Refused: same shape, with the reason.
	if _, err := q.RequestAnswer(os.Getpid(), AnswerRequest{ID: "req-2", By: "alice", Text: "again"}); err != nil {
		t.Fatal(err)
	}
	if err := en.AnswerRefused("req-2", "job pid %d is not reporting blocked"); err != nil {
		t.Fatal(err)
	}
	got, ok = en.AnswerRequested()
	if !ok || got.Verdict != "refused" || got.Refused == "" || got.Text != "" {
		t.Fatalf("a refusal must carry its reason and drop the payload, got %+v ok=%v", got, ok)
	}

	// Release sweeps answer files with the ticket, like kill files.
	en.Release(0)
	for _, ext := range []string{AnswerExt, killExt} {
		if leftovers := ticketCompanions(t, q.Dir, ext); len(leftovers) != 0 {
			t.Fatalf("%s file survived release: %v", ext, leftovers)
		}
	}
}

// An ack answers only the request it delivered: a second answer that
// landed while the first was in flight keeps its own file, and the first
// one's verdict must not touch it - nor may a withdrawn request take back
// someone else's.
func TestAnswerAcksAreScopedToTheirRequest(t *testing.T) {
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

	if _, err := q.RequestAnswer(os.Getpid(), AnswerRequest{ID: "a", By: "alice", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	// B lands while A's delivery is in flight.
	if _, err := q.RequestAnswer(os.Getpid(), AnswerRequest{ID: "b", By: "alice", Text: "second"}); err != nil {
		t.Fatal(err)
	}
	if err := en.AnswerDelivered("a"); err != nil {
		t.Fatal(err)
	}
	got, ok := en.AnswerRequested()
	if !ok || got.ID != "b" || got.Verdict != "" || got.Text != "second" {
		t.Fatalf("A's ack destroyed B's pending request: %+v ok=%v", got, ok)
	}
	// A withdrawal scoped to A leaves B alone too.
	if err := q.WithdrawAnswer(os.Getpid(), "a"); err != nil {
		t.Fatal(err)
	}
	if got, ok := en.AnswerRequested(); !ok || got.ID != "b" {
		t.Fatalf("a scoped withdrawal must not take a foreign request: %+v ok=%v", got, ok)
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

// A withdrawn request never existed as far as the holder is concerned.
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
	if _, err := q.RequestAnswer(os.Getpid(), AnswerRequest{ID: "w1", By: "alice", Text: "take me back"}); err != nil {
		t.Fatal(err)
	}
	if err := q.WithdrawAnswer(os.Getpid(), "w1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := en.AnswerRequested(); ok {
		t.Fatal("a withdrawn answer must not be requested")
	}
}

// The door reads bounded: a planted file past the bound is no request at
// all - the trusted reader's cost for any local writer's file is one stat.
func TestAnswerReadIsBounded(t *testing.T) {
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
	big := `{"id":"big","text":"` + strings.Repeat("x", AnswerFileMax) + `"}`
	path := filepath.Join(q.Dir, en.name+AnswerExt)
	if err := os.WriteFile(path, []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := en.AnswerRequested(); ok {
		t.Fatal("an oversized door file must not be read as a request")
	}
	// A malformed payload is not a request either.
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := en.AnswerRequested(); ok {
		t.Fatal("a malformed door file must not be read as a request")
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
	if _, err := q.RequestAnswer(os.Getpid(), AnswerRequest{ID: "r1", By: "alice", Text: "y"}); err != nil {
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
