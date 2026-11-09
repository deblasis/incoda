package joblog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ticketName(seq int64) string {
	return fmt.Sprintf("%020d-1234.ticket", seq)
}

func TestWriterCapsAtMaxBytesWithMarker(t *testing.T) {
	dir := t.TempDir()
	w, err := Create(dir, ticketName(1))
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1<<20) // 1 MiB
	total := int64(0)
	for total < MaxBytes+(1<<20) {
		n, err := w.Write(chunk)
		if err != nil {
			t.Fatalf("write at %d bytes: %v", total, err)
		}
		if n != len(chunk) {
			t.Fatalf("write returned %d, want %d", n, len(chunk))
		}
		total += int64(len(chunk))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	path, err := Path(dir, ticketName(1))
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// The cap plus one marker line; anything larger means bytes past the cap
	// kept landing.
	if st.Size() > MaxBytes+200 {
		t.Fatalf("job file grew to %d bytes, cap is %d plus a marker line", st.Size(), MaxBytes)
	}
	if st.Size() < MaxBytes {
		t.Fatalf("job file is %d bytes, should hold the first %d", st.Size(), MaxBytes)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "job log truncated at") {
		t.Fatalf("capped file must carry the in-band truncation marker")
	}
	// Writes after the cap are swallowed, not failed: stdout still gets its
	// bytes through the same tee, and the writer must not break them.
	n, err := w.Write(chunk)
	if err != nil || n != len(chunk) {
		t.Fatalf("write past the cap returned (%d, %v), want (%d, nil)", n, err, len(chunk))
	}
	if st2, _ := os.Stat(path); st2.Size() != st.Size() {
		t.Fatalf("file grew after the cap: %d then %d", st.Size(), st2.Size())
	}
}

func TestRetentionKeepsNewestPerLane(t *testing.T) {
	dir := t.TempDir()
	jobs := filepath.Join(dir, dirName)
	if err := os.MkdirAll(jobs, 0o755); err != nil {
		t.Fatal(err)
	}
	// Twelve stale job files, newest lexicographically last (ticket names are
	// zero-padded arrival nanos, so name order is arrival order).
	for i := int64(1); i <= 12; i++ {
		stale := filepath.Join(jobs, strings.TrimSuffix(ticketName(i), ".ticket")+jobExt)
		if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Creating the thirteenth trims to the newest ten, itself included.
	w, err := Create(dir, ticketName(13))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("new")); err != nil {
		t.Fatal(err)
	}
	w.Close()
	names, err := os.ReadDir(jobs)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != KeepPerLane {
		t.Fatalf("jobs dir holds %d files, want %d", len(names), KeepPerLane)
	}
	for _, i := range []int64{1, 2, 3} { // the three oldest must be gone
		gone := filepath.Join(jobs, strings.TrimSuffix(ticketName(i), ".ticket")+jobExt)
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("oldest job file %s survived the sweep", gone)
		}
	}
	for _, i := range []int64{4, 12, 13} { // and the newest kept
		kept := filepath.Join(jobs, strings.TrimSuffix(ticketName(i), ".ticket")+jobExt)
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("newest job file %s was swept: %v", kept, err)
		}
	}
}

func TestPathRejectsNamesThatAreNotTickets(t *testing.T) {
	for _, bad := range []string{"../evil", "..\\evil", "nope", "0001-2", "0001-2.txt"} {
		if _, err := Path("lane", bad); err == nil {
			t.Fatalf("Path accepted %q, which is not a ticket file name", bad)
		}
	}
	p, err := Path(filepath.Join("lanes", "k"), ticketName(7))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("lanes", "k", dirName, "00000000000000000007-1234.out")
	if p != want {
		t.Fatalf("Path = %q, want %q", p, want)
	}
}

// A job file that already exists when its ticket's output is created is a
// collision or a plant: a hard link pointed at a file someone wants
// overwritten through the run, with the ticket's name readable from status
// for the whole queue wait. The ticket names a job that has not started
// yet, so its file cannot legitimately exist; Create must refuse rather
// than truncate through the link.
func TestCreateRefusesExistingJobFile(t *testing.T) {
	dir := t.TempDir()
	path, err := Path(dir, ticketName(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(dir, ticketName(1)); err == nil {
		t.Fatal("Create reopened an existing job file instead of refusing it")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "precious" {
		t.Fatalf("the pre-existing file was modified: %q", b)
	}
}

// Files in the jobs directory that are not shaped like job files must not
// count toward retention (foreign names squatting the window would starve
// real job files of it) and must not be deleted: they are not ours.
func TestSweepIgnoresForeignNames(t *testing.T) {
	dir := t.TempDir()
	jobs := filepath.Join(dir, dirName)
	if err := os.MkdirAll(jobs, 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := []string{"notes.out", "definitely.out", "x-1.out"}
	for _, name := range foreign {
		if err := os.WriteFile(filepath.Join(jobs, name), []byte("not ours"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i := int64(1); i <= 12; i++ {
		stale := filepath.Join(jobs, strings.TrimSuffix(ticketName(i), ".ticket")+jobExt)
		if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w, err := Create(dir, ticketName(13))
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	names, err := os.ReadDir(jobs)
	if err != nil {
		t.Fatal(err)
	}
	// Ten kept job files plus the three foreign ones, untouched.
	if len(names) != KeepPerLane+len(foreign) {
		t.Fatalf("jobs dir holds %d files, want %d kept plus %d foreign left alone", len(names), KeepPerLane, len(foreign))
	}
	for _, name := range foreign {
		b, err := os.ReadFile(filepath.Join(jobs, name))
		if err != nil || string(b) != "not ours" {
			t.Fatalf("foreign file %s was touched: %q, %v", name, b, err)
		}
	}
	// The oldest three real files were swept as usual.
	for _, i := range []int64{1, 2, 3} {
		gone := filepath.Join(jobs, strings.TrimSuffix(ticketName(i), ".ticket")+jobExt)
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("oldest job file %s survived the sweep", gone)
		}
	}
}

func TestExitSidecarRoundTrip(t *testing.T) {
	dir := t.TempDir()
	w, err := Create(dir, ticketName(1))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	path, err := Path(dir, ticketName(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadExit(path); ok {
		t.Fatal("a fresh job file must read as no exit code")
	}
	if err := w.Exit(3); err != nil {
		t.Fatal(err)
	}
	code, ok := ReadExit(path)
	if !ok || code != 3 {
		t.Fatalf("exit sidecar reads %d/%v, want 3/true", code, ok)
	}
	// A malformed sidecar is no answer, never a guess.
	if err := os.WriteFile(ExitPath(path), []byte("boom\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadExit(path); ok {
		t.Fatal("a malformed sidecar must not read as a code")
	}
}

// A swept job file takes its exit sidecar with it: the directory does not
// grow a second set of stale files.
func TestSweepRemovesExitSidecars(t *testing.T) {
	dir := t.TempDir()
	jobs := filepath.Join(dir, dirName)
	if err := os.MkdirAll(jobs, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 12; i++ {
		base := filepath.Join(jobs, strings.TrimSuffix(ticketName(i), ".ticket")+jobExt)
		if err := os.WriteFile(base, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ExitPath(base), []byte("0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w, err := Create(dir, ticketName(13))
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	gone := filepath.Join(jobs, strings.TrimSuffix(ticketName(1), ".ticket")+jobExt)
	if _, err := os.Stat(ExitPath(gone)); !os.IsNotExist(err) {
		t.Fatalf("the swept log's exit sidecar survived: %v", err)
	}
}
