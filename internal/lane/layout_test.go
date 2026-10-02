package lane

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// treeSnapshot maps every path under root to its mode, size and
// modification time, so a test can prove that nothing was written.
func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		out[path] = fmt.Sprintf("%v %d %d", fi.Mode(), fi.Size(), fi.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTree(t *testing.T, what string, before, after map[string]string) {
	t.Helper()
	for p, v := range before {
		if after[p] != v {
			t.Fatalf("%s changed %s: %q -> %q", what, p, v, after[p])
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			t.Fatalf("%s created %s", what, p)
		}
	}
}

func TestLayoutPaths(t *testing.T) {
	s := filepath.Join("x", "state")
	if got := LanesDir(s); got != filepath.Join(s, "lanes") {
		t.Fatalf("LanesDir = %q", got)
	}
	if got := LaneDir(s, "k"); got != filepath.Join(s, "lanes", "k") {
		t.Fatalf("LaneDir = %q", got)
	}
	if got := QueuesDir(s); got != filepath.Join(s, "queues") {
		t.Fatalf("QueuesDir = %q", got)
	}
}

func TestOpenCreatesUnderLanesOnly(t *testing.T) {
	state := t.TempDir()
	q, err := Open(state, "k")
	if err != nil {
		t.Fatal(err)
	}
	q.Close()
	if !Exists(state, "k") {
		t.Fatal("Open did not create lanes/k")
	}
	if _, err := os.Lstat(QueuesDir(state)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open created queues/: %v", err)
	}
	keys, err := ListQueues(state)
	if err != nil || len(keys) != 1 || keys[0] != "k" {
		t.Fatalf("ListQueues = %v, %v", keys, err)
	}
}

func TestExistingModeNeverCreatesADirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "queues")
	if _, err := OpenIn(root, "k", Existing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Existing created the root")
	}
}

func TestReadOnlyModeChangesNothing(t *testing.T) {
	root := t.TempDir()
	q, err := OpenIn(root, "k", Create)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	en, err := q.Enroll(Ticket{Command: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	defer en.Release(0) // runs before q.Close: Release needs the registry handle
	// A dead ticket and a kill request whose ticket is gone: a normal scan
	// would remove both and log the reap.
	if err := os.WriteFile(filepath.Join(q.Dir, ticketName(1, 1)), []byte(`{"pid":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(q.Dir, ticketName(2, 2)+killExt), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	before := treeSnapshot(t, root)
	ro, err := OpenIn(root, "k", ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := ro.Observe(5)
	if err != nil {
		t.Fatal(err)
	}
	ro.Logf("queue=k event=nothing")
	ro.Close()
	if len(snap.Holders) != 1 || snap.Holders[0].File != en.Name() {
		t.Fatalf("read-only observe must still see the live ticket: %+v", snap.Holders)
	}
	sameTree(t, "a read-only observe", before, treeSnapshot(t, root))

	if _, err := OpenIn(root, "nope", ReadOnly); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only open of a missing lane: want ErrNotExist, got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "nope")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-only open created a lane")
	}
}

func TestProbeLaneFindsLiveTicketsAndCreatesNothing(t *testing.T) {
	root := t.TempDir()
	q, err := OpenIn(root, "k", Create)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	en, err := q.Enroll(Ticket{Command: []string{"zig", "build"}})
	if err != nil {
		t.Fatal(err)
	}
	defer en.Release(0)
	dead := ticketName(1, 4242)
	if err := os.WriteFile(filepath.Join(q.Dir, dead), []byte(`{"pid":4242}`), 0o644); err != nil {
		t.Fatal(err)
	}

	before := treeSnapshot(t, root)
	live, err := ProbeLane(q.Dir, soon())
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].Name != en.Name() || live[0].PID() != os.Getpid() || live[0].Ticket.CommandString() != "zig build" {
		t.Fatalf("ProbeLane = %+v", live)
	}
	if p := ProbeTicket(q.Dir, en.Name(), soon()); !p.Live {
		t.Fatal("ProbeTicket: the enrolled ticket is live")
	}
	if p := ProbeTicket(q.Dir, dead, soon()); p.Live {
		t.Fatal("ProbeTicket: an unlocked ticket is dead")
	}
	sameTree(t, "a probe", before, treeSnapshot(t, root))

	ghost := filepath.Join(root, "ghost")
	if live, err := ProbeLane(ghost, soon()); err != nil || len(live) != 0 {
		t.Fatalf("a missing lane holds nothing: %v %v", live, err)
	}
	if _, err := os.Lstat(ghost); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ProbeLane created a lane")
	}
}

func TestLockAllAndLiveLocked(t *testing.T) {
	root := t.TempDir()
	a, err := OpenIn(root, "a", Create)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := OpenIn(root, "b", Create)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	en, err := a.Enroll(Ticket{Command: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	defer en.Release(0)
	unlock, err := LockAll([]*Queue{a, b}, time.Now().Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	la, err := a.LiveLocked()
	if err != nil || len(la) != 1 {
		t.Fatalf("a: %v %v", la, err)
	}
	lb, err := b.LiveLocked()
	if err != nil || len(lb) != 0 {
		t.Fatalf("b: %v %v", lb, err)
	}
	unlock()
	// Released: an enrollment can take b's registry lock again.
	enb, err := b.Enroll(Ticket{Command: []string{"y"}})
	if err != nil {
		t.Fatal(err)
	}
	enb.Release(0)
}
