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
