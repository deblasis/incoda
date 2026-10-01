package held

import (
	"os"
	"testing"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/procinfo"
)

// enroll creates a live ticket owned by this test process.
func enroll(t *testing.T, state, key string) (*lane.Queue, *lane.Enrollment) {
	t.Helper()
	q, err := lane.Open(state, key)
	if err != nil {
		t.Fatal(err)
	}
	en, err := q.Enroll(lane.Ticket{Command: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	return q, en
}

func TestVerifyLiveAncestorPassesThrough(t *testing.T) {
	state := t.TempDir()
	q, en := enroll(t, state, "anc")
	defer q.Close()
	defer en.Release(0)
	// The ticket's pid is this process; pretend it is our parent.
	chain := procinfo.Chain{PIDs: []int{os.Getpid()}}
	r := Verify(state, "anc="+en.Name(), chain)
	if len(r.L) != 1 || len(r.P) != 1 || len(r.Dropped) != 0 {
		t.Fatalf("got %+v", r)
	}
	if !r.PassKeys()["anc"] || !r.LiveKeys()["anc"] {
		t.Fatalf("key sets wrong: %+v", r)
	}
}

func TestVerifyLiveNonAncestorCountsButDoesNotPass(t *testing.T) {
	state := t.TempDir()
	q, en := enroll(t, state, "na")
	defer q.Close()
	defer en.Release(0)
	r := Verify(state, "na="+en.Name(), procinfo.Chain{PIDs: []int{1}})
	if len(r.L) != 1 || len(r.P) != 0 || len(r.Dropped) != 1 || r.Dropped[0].Why != NotAncestor || !r.Dropped[0].Live {
		t.Fatalf("got %+v", r)
	}
}

func TestVerifyUnverifiableWhenChainBroken(t *testing.T) {
	state := t.TempDir()
	q, en := enroll(t, state, "uv")
	defer q.Close()
	defer en.Release(0)
	r := Verify(state, "uv="+en.Name(), procinfo.Chain{Err: procinfo.ErrUnsupported})
	if len(r.L) != 1 || len(r.P) != 0 || r.Dropped[0].Why != Unverifiable {
		t.Fatalf("got %+v", r)
	}
}

func TestVerifySkipTrustsLiveEntries(t *testing.T) {
	state := t.TempDir()
	q, en := enroll(t, state, "win")
	defer q.Close()
	defer en.Release(0)
	r := Verify(state, "win="+en.Name(), procinfo.Chain{Skip: true})
	if len(r.P) != 1 {
		t.Fatalf("got %+v", r)
	}
}

func TestVerifyDeadAndMalformedAreDropped(t *testing.T) {
	state := t.TempDir()
	q, en := enroll(t, state, "dead")
	name := en.Name()
	en.Release(0)
	q.Close()
	raw := "dead=" + name + ",ghost=00000000000000000001-1.ticket,bare"
	r := Verify(state, raw, procinfo.Chain{PIDs: []int{os.Getpid()}})
	if len(r.L) != 0 || len(r.P) != 0 || len(r.Dropped) != 3 {
		t.Fatalf("got %+v", r)
	}
	whys := map[string]Why{}
	for _, d := range r.Dropped {
		whys[d.Raw] = d.Why
	}
	if whys["dead="+name] != Dead || whys["ghost=00000000000000000001-1.ticket"] != Dead || whys["bare"] != Malformed {
		t.Fatalf("whys %v", whys)
	}
	// A probe of a missing lane must not create it.
	if lane.Exists(state, "ghost") {
		t.Fatal("Verify created the ghost lane")
	}
	// A probe of a released ticket must not recreate it.
	if _, err := os.Stat(lane.TicketFilePath(lane.QueueDir(state, "dead"), name)); !os.IsNotExist(err) {
		t.Fatal("Verify recreated a released ticket")
	}
}
