// Package joblog stores the per-job output file a pty-mode run tees to:
// one file per ticket under the lane's jobs/ directory, capped in size and
// counted per lane, because a queue whose jobs each leave a file behind is a
// queue that grows forever.
package joblog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/deblasis/incoda/internal/lane"
)

const (
	// MaxBytes is the per-job cap. Past it the file keeps its first MaxBytes
	// and records one truncation line; the run's stdout still receives every
	// byte, so nothing is lost that the caller could not see anyway.
	MaxBytes int64 = 16 << 20
	// KeepPerLane is how many job files a lane keeps, newest first.
	KeepPerLane = 10

	dirName = "jobs"
	jobExt  = ".out"
)

// Dir is the lane subdirectory holding job files.
func Dir(laneDir string) string { return filepath.Join(laneDir, dirName) }

// Path is the job file a ticket's output goes to. ticket must be a real
// ticket file name (validated, not trusted: names arrive through scans and
// arguments).
func Path(laneDir, ticket string) (string, error) {
	if !lane.ValidTicketName(ticket) {
		return "", fmt.Errorf("joblog: %q is not a ticket file name", ticket)
	}
	return filepath.Join(Dir(laneDir), strings.TrimSuffix(ticket, ".ticket")+jobExt), nil
}

// Writer is a job file that stops growing at MaxBytes. Its Write never
// fails: the run's tee order puts the job file beside stdout, and a full or
// broken job file must not cost the run its stdout bytes.
type Writer struct {
	f       *os.File
	written int64
	full    bool
}

// Create opens the job file for ticket, creating the lane's jobs/ directory
// and trimming it to the newest KeepPerLane-1 files first (the new file is
// the KeepPerLane-th).
func Create(laneDir, ticket string) (*Writer, error) {
	path, err := Path(laneDir, ticket)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("joblog: %w", err)
	}
	if err := sweep(filepath.Dir(path)); err != nil {
		return nil, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("joblog: %w", err)
	}
	return &Writer{f: f}, nil
}

// Write stores what fits. The first time the cap is hit (or a write error
// stops the file), one in-band marker line says so; after that writes are
// swallowed. A closed writer swallows too: the tee it sits in must keep
// feeding stdout whatever the job file's state is.
func (w *Writer) Write(p []byte) (int, error) {
	if w.f == nil || w.full || len(p) == 0 {
		return len(p), nil
	}
	room := MaxBytes - w.written
	if int64(len(p)) <= room {
		n, err := w.f.Write(p)
		w.written += int64(n)
		if err != nil {
			w.markTruncated(fmt.Sprintf("write error: %v", err))
		}
		return len(p), nil
	}
	if room > 0 {
		n, _ := w.f.Write(p[:room])
		w.written += int64(n)
	}
	w.markTruncated(fmt.Sprintf("job log truncated at %d bytes; later output only reached the run's stdout", MaxBytes))
	return len(p), nil
}

// Close closes the file. It is safe to call twice.
func (w *Writer) Close() error {
	if w.f == nil {
		return nil
	}
	f := w.f
	w.f = nil
	return f.Close()
}

func (w *Writer) markTruncated(why string) {
	if w.full || w.f == nil {
		return
	}
	w.full = true
	line := "\r\nincoda: " + why + "\r\n"
	_, _ = w.f.WriteString(line)
	w.written += int64(len(line))
}

// sweep deletes the oldest job files so that at most keep-1 remain, by name:
// ticket names are zero-padded arrival nanoseconds, so lexicographic order
// is arrival order. Failures are ignored the way log rotation ignores them;
// the cap on any one file still holds.
func sweep(dir string) error {
	names, err := filepath.Glob(filepath.Join(dir, "*"+jobExt))
	if err != nil {
		return fmt.Errorf("joblog: %w", err)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if len(names) < KeepPerLane-1 {
		return nil
	}
	for _, name := range names[KeepPerLane-1:] {
		_ = os.Remove(name)
	}
	return nil
}
