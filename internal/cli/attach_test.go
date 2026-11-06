package cli

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/lane"
)

// TestMayAttachGate: attach is limited to the job's own owner, and every way
// that gate can fail names who won and who asked. The refusal is a usage
// error (120), same as every other named refusal.
func TestMayAttachGate(t *testing.T) {
	job := lane.Ticket{PID: 4242, Queue: "builds", Owner: "alice"}
	cases := []struct {
		name, me string
		ticket   lane.Ticket
		wantOK   bool
		wantMsg  []string
	}{
		{
			name:   "owner matches",
			me:     "alice",
			wantOK: true,
		},
		{
			name:   "owner matches after whitespace",
			me:     "  alice  ",
			wantOK: true,
		},
		{
			name:   "ticket owner has stray whitespace",
			me:     "alice",
			ticket: lane.Ticket{PID: 4242, Queue: "builds", Owner: " alice "},
			wantOK: true,
		},
		{
			name:    "no identity",
			me:      "",
			wantMsg: []string{"--owner or $INCODA_OWNER"},
		},
		{
			name:    "identity is only spaces",
			me:      "   ",
			wantMsg: []string{"--owner or $INCODA_OWNER"},
		},
		{
			name:    "another owner",
			me:      "bob",
			wantMsg: []string{"belongs to owner \"alice\"", "you are \"bob\"", "Only the job's owner may attach"},
		},
		{
			name:    "ticket without owner",
			me:      "bob",
			ticket:  lane.Ticket{PID: 4242, Queue: "builds"},
			wantMsg: []string{"recorded no owner", "refused"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tk := tc.ticket
			if tk.Owner == "" && tc.ticket.PID == 0 {
				tk = job
			}
			err := mayAttach(tk, tc.me)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("owner %q should attach, got %v", tc.me, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("identity %q should be refused", tc.me)
			}
			for _, want := range tc.wantMsg {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("refusal %q missing %q", err.Error(), want)
				}
			}
		})
	}
}

// The pid must name exactly one live participant. A first-match pick would
// stream whoever's ticket sorts first, and ticket state is plantable by
// anything sharing the state directory, so two candidates is a refusal, not
// a guess.
func TestFindEntryRefusesAmbiguousPid(t *testing.T) {
	realEntry := lane.Entry{File: "00000000000000000001-1111.ticket", Ticket: lane.Ticket{PID: 77, Owner: "alice"}}
	planted := lane.Entry{File: "00000000000000000002-2222.ticket", Ticket: lane.Ticket{PID: 77, Owner: "alice"}}
	snap := &lane.Snapshot{}
	snap.Holders = []lane.Entry{realEntry}
	if e, holding, err := findEntry(snap, 77); err != nil || e == nil || !holding {
		t.Fatalf("single holder: got (%v, %v, %v)", e, holding, err)
	}
	snap.Holders = append(snap.Holders, planted)
	if e, _, err := findEntry(snap, 77); err == nil || e != nil {
		t.Fatalf("two holders with one pid must refuse, got (%v, %v)", e, err)
	} else if !strings.Contains(err.Error(), "appears on 2 live tickets") {
		t.Fatalf("refusal must name the ambiguity, got %q", err.Error())
	}
	snap.Holders = snap.Holders[:1]
	snap.Waiting = []lane.Entry{planted}
	if e, _, err := findEntry(snap, 77); err == nil || e != nil {
		t.Fatalf("holder plus waiter with one pid must refuse, got (%v, %v)", e, err)
	}
	snap.Holders = nil
	if e, holding, err := findEntry(snap, 77); err != nil || e == nil || holding {
		t.Fatalf("single waiter: got (%v, %v, %v)", e, holding, err)
	}
	snap.Waiting = nil
	if e, _, err := findEntry(snap, 999); err != nil || e != nil {
		t.Fatalf("no participant is not an error, got (%v, %v)", e, err)
	}
}

// The drain after the run dies must deliver the whole tail, not one buffer's
// worth: the exit burst is the part an operator attaching to a finishing
// job most wants, and it regularly dwarfs 32 KiB.
func TestDrainToEOFDeliversWholeTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "job.out")
	payload := make([]byte, 1<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out bytes.Buffer
	buf := make([]byte, 32*1024)
	off, err := drainToEOF(f, 0, buf, &out)
	if err != nil {
		t.Fatal(err)
	}
	if off != int64(len(payload)) {
		t.Fatalf("drained %d bytes of %d", off, len(payload))
	}
	if !bytes.Equal(out.Bytes(), payload) {
		t.Fatalf("drained %d bytes but they are not the file's bytes", out.Len())
	}
}
