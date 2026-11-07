package cli

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/lockfile"
)

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

// TestValidateEntryForAttach: the two refusals ahead of the owner gate
// (unreadable payload, name/pid disagreement) each name what they saw, and
// a clean entry passes. A guard that cannot fail here ships as prose.
func TestValidateEntryForAttach(t *testing.T) {
	clean := &lane.Entry{File: "01790000000000000000-777.ticket", Ticket: lane.Ticket{PID: 777, Queue: "builds"}}
	if err := validateEntryForAttach("builds", clean); err != nil {
		t.Fatalf("clean entry must pass, got %v", err)
	}
	broken := &lane.Entry{File: clean.File, Ticket: clean.Ticket, PayloadError: "json: bad token"}
	err := validateEntryForAttach("builds", broken)
	if err == nil || !strings.Contains(err.Error(), "unreadable") || !strings.Contains(err.Error(), "json: bad token") {
		t.Fatalf("unreadable payload must refuse naming the parse error, got %v", err)
	}
	mism := &lane.Entry{File: "01790000000000000000-777.ticket", Ticket: lane.Ticket{PID: 778, Queue: "builds"}}
	err = validateEntryForAttach("builds", mism)
	if err == nil || !strings.Contains(err.Error(), "names pid 777") || !strings.Contains(err.Error(), "claims pid 778") {
		t.Fatalf("name/pid disagreement must refuse naming both pids, got %v", err)
	}
}

// errReaderAt fails its ReadAt after the first chunk with a real error,
// while Stat keeps succeeding: the shape a failing disk or a closed handle
// under a live path presents, and the one the old swallow-on-any-error
// read as silence.
type errReaderAt struct {
	f         *os.File
	failAfter int64
	calls     int
}

func (e *errReaderAt) ReadAt(p []byte, off int64) (int, error) {
	e.calls++
	if int64(e.calls)*int64(len(p)) > e.failAfter {
		return 0, errors.New("device gone")
	}
	return e.f.ReadAt(p, off)
}

func (e *errReaderAt) Stat() (os.FileInfo, error) { return e.f.Stat() }

// TestDrainToEOFPropagatesReadError: a read error mid-drain must surface,
// not dissolve into the poll loop; a drain that reports success after a
// failed read has silently dropped the tail it was draining.
func TestDrainToEOFPropagatesReadError(t *testing.T) {
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
	fake := &errReaderAt{f: f, failAfter: 100 * 1024}
	var out bytes.Buffer
	buf := make([]byte, 32*1024)
	_, err = drainToEOF(fake, 0, buf, &out)
	if err == nil {
		t.Fatal("a real read error was swallowed; the tail after it is dropped in silence")
	}
	if !strings.Contains(err.Error(), "job file read") || !strings.Contains(err.Error(), "device gone") {
		t.Fatalf("error must name the read and the cause, got %v", err)
	}
	if out.Len() >= len(payload) {
		t.Fatalf("delivered %d bytes of %d; the truncated delivery is the point", out.Len(), len(payload))
	}
}

// TestAttachRefusesMangledTicketWiring drives the real attach command, not
// the helper: a planted ticket whose file name and payload disagree about
// the pid must be refused end to end, before any job file is opened. The
// test holds the ticket's liveness lock itself so the lane scan lists the
// ticket as a live participant (a lock-free ticket is skipped as not
// enrolled). A wiring that dropped the authorization call would sail past
// the owner gate (the owner here matches) and stall on a job file that does
// not exist, which this test reads as a failure.
func TestAttachRefusesMangledTicketWiring(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INCODA_DIR", dir)
	if code := Main([]string{"config", "seed"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("migrate: %d", code)
	}
	name := fmt.Sprintf("%020d-%d.ticket", time.Now().UnixNano(), 4242)
	laneDir := lane.LaneDir(dir, "seed")
	if err := os.MkdirAll(laneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(laneDir, name)
	payload := `{"pid":4243,"queue":"seed","owner":"alice","pty":true,"command":["x"]}`
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	lf, err := lockfile.OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()
	if err := lf.Lock(); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	code := Main([]string{"attach", "--queue", "seed", "--pid", "4243", "--owner", "alice"}, io.Discard, &stderr)
	if code != ExitUsage {
		t.Fatalf("a name/pid mismatch must refuse with 120, got %d:\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "names pid 4242") || !strings.Contains(stderr.String(), "claims pid 4243") {
		t.Fatalf("refusal must name both pids, got:\n%s", stderr.String())
	}
}
