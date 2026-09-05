package lane

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestKillRequestReachesTheTicket(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	en, err := q.Enroll(Ticket{Command: []string{"zig", "build"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := en.KillRequested(); ok {
		t.Fatal("a fresh ticket has no kill request")
	}

	req := KillRequest{By: "alex@box", ByPID: 77, Reason: "stale build, branch moved"}
	entry, err := q.RequestKill(os.Getpid(), req)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Ticket.PID != os.Getpid() {
		t.Fatalf("RequestKill returned the wrong participant: %+v", entry.Ticket)
	}
	got, ok := en.KillRequested()
	if !ok {
		t.Fatal("the holder must see its kill request")
	}
	if got.By != req.By || got.Reason != req.Reason || got.ByPID != req.ByPID || got.At == "" {
		t.Fatalf("request did not round-trip: %+v", got)
	}
	if line := lastLog(t, q); !strings.Contains(line, "event=kill-request") || !strings.Contains(line, "reason=") {
		t.Fatalf("the request must be logged, got %q", line)
	}

	// Release removes the request file along with the ticket.
	en.Release(124)
	if leftovers := killFiles(t, q.Dir); len(leftovers) != 0 {
		t.Fatalf("kill file survived release: %v", leftovers)
	}
}

func TestKillRequestUnknownPid(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if _, err := q.RequestKill(999999, KillRequest{By: "x", Reason: "y"}); err == nil {
		t.Fatal("a pid that is not in the queue must be refused")
	}
}

func TestAcquireStopsOnKillRequest(t *testing.T) {
	// A waiter that is killed must leave the queue with the request in
	// hand, so run can say who cancelled it and why.
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	holder, err := q.Enroll(Ticket{Command: []string{"h"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.Acquire(context.Background(), AcquireOptions{Wait: 0}); err != nil {
		t.Fatal(err)
	}
	defer holder.Release(0)

	// Same pid as the holder, so the request has to be addressed by ticket
	// rather than by pid: RequestKill takes a pid because that is what a
	// human sees, and in one process two tickets share it. Use the file.
	waiter, err := q.Enroll(Ticket{Command: []string{"w"}})
	if err != nil {
		t.Fatal(err)
	}
	defer waiter.Release(0)
	// The killer is another process in real life, so it gets its own
	// Queue handle here: a Queue's registry lock is not safe for two
	// goroutines, and sharing q with the waiter's poll would be a data
	// race that exists only in the test.
	killer, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer killer.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(150 * time.Millisecond)
		_ = killer.requestKillFile(waiter.name, KillRequest{By: "alex@box", Reason: "not needed any more"})
	}()
	err = waiter.Acquire(context.Background(), AcquireOptions{Wait: 5 * time.Second, Poll: 50 * time.Millisecond})
	// Acquire returns the moment the request file is visible, which can be
	// before the killer has released its registry lock; join it so the
	// deferred Close does not race that release.
	<-done
	var killed *KilledError
	if !asKilled(err, &killed) {
		t.Fatalf("Acquire should report the kill, got %v", err)
	}
	if killed.Request.Reason != "not needed any more" {
		t.Fatalf("wrong request surfaced: %+v", killed.Request)
	}
}

func TestReaperRemovesStrayKillFiles(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	name := ticketName(time.Now().UnixNano()-1_000_000, 424242)
	if err := os.WriteFile(filepath.Join(q.Dir, name), []byte(`{"pid":424242}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(q.Dir, name+killExt), []byte(`{"by":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Observe(0); err != nil {
		t.Fatal(err)
	}
	if leftovers := killFiles(t, q.Dir); len(leftovers) != 0 {
		t.Fatalf("a kill file whose ticket was reaped must go with it: %v", leftovers)
	}
}

func killFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), killExt) {
			out = append(out, e.Name())
		}
	}
	return out
}

func asKilled(err error, target **KilledError) bool {
	if k, ok := err.(*KilledError); ok {
		*target = k
		return true
	}
	return false
}
