package lane

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeLog replaces the queue's handoff log with the given lines.
func writeLog(t testing.TB, q *Queue, lines []string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(q.Dir, logName), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTailLogZeroReturnsNothingAndDoesNoWork(t *testing.T) {
	// `incoda queues` observes every queue with Observe(0). Asking for no
	// events must not read or parse the log at all: the queue's log is
	// unbounded, and the observer runs on the same machine as the jobs.
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	lines := make([]string, 2000)
	for i := range lines {
		lines[i] = fmt.Sprintf("2026-10-26 16:00:%02d queue=unit event=release pid=%d rc=0 reason=%s", i%60, i, strings.Repeat("x", 80))
	}
	writeLog(t, q, lines)

	if got := q.TailLog(0); got == nil || len(got) != 0 {
		t.Fatalf("TailLog(0) = %#v, want a non-nil empty slice", got)
	}
	if got := q.TailLog(-3); len(got) != 0 {
		t.Fatalf("TailLog(-3) returned %d lines, want none", len(got))
	}
	if allocs := testing.AllocsPerRun(5, func() { q.TailLog(0) }); allocs != 0 {
		t.Fatalf("TailLog(0) allocated %.0f times; asking for no events must not touch the log", allocs)
	}
}

func TestTailLogReturnsLastLinesOldestFirst(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, fmt.Sprintf("line-%02d", i))
	}
	writeLog(t, q, lines)

	cases := []struct {
		n    int
		want []string
	}{
		{1, []string{"line-10"}},
		{3, []string{"line-08", "line-09", "line-10"}},
		{10, lines},
		{20, lines},
	}
	for _, c := range cases {
		got := q.TailLog(c.n)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("TailLog(%d) = %v, want %v", c.n, got, c.want)
		}
	}
}

func TestTailLogReadsAcrossAChunkBoundary(t *testing.T) {
	// The reader must find the tail without loading the whole file. Lines
	// here are ~1 KB, so the last five sit well inside the final chunk but
	// the file is large enough that a one-chunk implementation has to walk
	// back more than once for a larger n.
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	var lines []string
	for i := 0; i < 400; i++ {
		lines = append(lines, fmt.Sprintf("line-%03d %s", i, strings.Repeat("y", 1000)))
	}
	writeLog(t, q, lines)

	got := q.TailLog(5)
	if len(got) != 5 {
		t.Fatalf("TailLog(5) returned %d lines, want 5", len(got))
	}
	for i, want := range []string{"line-395", "line-396", "line-397", "line-398", "line-399"} {
		if !strings.HasPrefix(got[i], want+" ") {
			t.Errorf("tail line %d = %q, want prefix %q", i, got[i], want)
		}
	}

	// A request larger than one chunk still has to keep the file's order.
	got = q.TailLog(200)
	if len(got) != 200 {
		t.Fatalf("TailLog(200) returned %d lines, want 200", len(got))
	}
	if !strings.HasPrefix(got[0], "line-200 ") || !strings.HasPrefix(got[199], "line-399 ") {
		t.Fatalf("TailLog(200) kept the wrong window: first %q last %q", got[0], got[199])
	}
}

func TestTailLogHandlesCRLFBlankLinesAndNoFinalNewline(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	body := "first\r\n\r\nsecond\r\nthird"
	if err := os.WriteFile(filepath.Join(q.Dir, logName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := q.TailLog(3)
	want := []string{"first", "second", "third"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("TailLog(3) = %q, want %q", got, want)
	}
}

func TestTailLogFindsRealLinesBeforeATrailingBlankBlock(t *testing.T) {
	// A corrupt or hand-edited log can end in a block of blank lines larger
	// than one read window. Stopping as soon as the window holds n newlines
	// would return nothing, even though the n real lines are further back.
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, fmt.Sprintf("real-%02d", i))
	}
	lines = append(lines, make([]string, 70000)...)
	writeLog(t, q, lines)

	got := q.TailLog(3)
	want := []string{"real-07", "real-08", "real-09"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("TailLog(3) = %q, want %q", got, want)
	}
}

func TestTailLogBoundsItsRead(t *testing.T) {
	// The contract is not only "the last n lines are right"; it is that a
	// small n does not pay for the whole file. Allocation count alone is not
	// enough: os.ReadFile plus the same splitter allocates the same 15 slices
	// while reading 45x the bytes. The byte bound is what a whole-file reader
	// cannot pass.
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	lines := make([]string, 100000)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%06d %s", i, strings.Repeat("x", 20))
	}
	writeLog(t, q, lines)
	fi, err := os.Stat(filepath.Join(q.Dir, logName))
	if err != nil {
		t.Fatal(err)
	}

	if allocs := testing.AllocsPerRun(3, func() { q.TailLog(5) }); allocs > 100 {
		t.Fatalf("TailLog(5) on a 100k-line log allocated %.0f times; it must not touch every line", allocs)
	}

	q.TailLog(5) // warm the file handle path
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got := q.TailLog(5)
	runtime.ReadMemStats(&after)
	if len(got) != 5 {
		t.Fatalf("TailLog(5) returned %d lines, want 5", len(got))
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > uint64(fi.Size())/4 {
		t.Fatalf("TailLog(5) allocated %d KiB for a %d KiB log; a bounded tail must not read the file",
			alloc>>10, fi.Size()>>10)
	}
}

func TestTailLogFallsBackForLargeN(t *testing.T) {
	// n above maxTailRing uses the plain split path. That path must still
	// return the last n lines in order.
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	var lines []string
	for i := 0; i < 5000; i++ {
		lines = append(lines, fmt.Sprintf("line-%05d", i))
	}
	writeLog(t, q, lines)

	got := q.TailLog(4500)
	if len(got) != 4500 {
		t.Fatalf("TailLog(4500) returned %d lines, want 4500", len(got))
	}
	if got[0] != "line-00500" || got[4499] != "line-04999" {
		t.Fatalf("TailLog(4500) window is wrong: first %q last %q", got[0], got[4499])
	}
}

func TestTailLogLeavesBareCROnAnUnterminatedLine(t *testing.T) {
	// A CR is only part of a line ending when an LF follows it. The final
	// line of a torn write has no LF, so its CR is data.
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	if err := os.WriteFile(filepath.Join(q.Dir, logName), []byte("a\nb\r"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := q.TailLog(2)
	want := []string{"a", "b\r"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("TailLog(2) = %q, want %q", got, want)
	}
}

func TestTailLogLargeNStaysLinear(t *testing.T) {
	// Asking for more lines than exist must cost a multiple of the file, not
	// a multiple of the file times the number of chunks.
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	lines := make([]string, 40000)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%05d %s", i, strings.Repeat("x", 80))
	}
	writeLog(t, q, lines)
	fi, err := os.Stat(filepath.Join(q.Dir, logName))
	if err != nil {
		t.Fatal(err)
	}

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got := q.TailLog(1_000_000)
	runtime.ReadMemStats(&after)
	if len(got) != len(lines) {
		t.Fatalf("TailLog(1e6) returned %d lines, want %d", len(got), len(lines))
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 8*uint64(fi.Size()) {
		t.Fatalf("TailLog(1e6) allocated %d MiB for a %d MiB log; the walk must stay linear",
			alloc>>20, fi.Size()>>20)
	}
}

func TestTailLogBlankTailStaysLinear(t *testing.T) {
	// The last real lines can sit behind a blank block larger than one read
	// window. Finding them is O(file), not O(file squared).
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	body := "real-1\nreal-2\n" + strings.Repeat("\n", 4<<20)
	if err := os.WriteFile(filepath.Join(q.Dir, logName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got := q.TailLog(2)
	runtime.ReadMemStats(&after)
	want := []string{"real-1", "real-2"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("TailLog(2) = %q, want %q", got, want)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 8*uint64(len(body)) {
		t.Fatalf("TailLog(2) allocated %d MiB for a %d MiB log; the walk must stay linear",
			alloc>>20, len(body)>>20)
	}
}

func BenchmarkTailLogFive(b *testing.B) {
	dir := b.TempDir()
	q, err := Open(dir, "bench")
	if err != nil {
		b.Fatal(err)
	}
	defer q.Close()

	var lines []string
	for i := 0; i < 20000; i++ {
		lines = append(lines, fmt.Sprintf("2026-10-26 16:00:%02d queue=bench event=release pid=%d rc=0 reason=%s", i%60, i, strings.Repeat("x", 120)))
	}
	writeLog(b, q, lines)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q.TailLog(5)
	}
}
