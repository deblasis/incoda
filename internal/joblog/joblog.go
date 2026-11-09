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
	"strconv"
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
	// CutLine is written through the run's output tee when a pseudo-console
	// drain is cut: conhost delivered nothing for a whole grace window while
	// the job's bytes were still in flight. Unlike the size cap, the tail is
	// lost everywhere at once, stdout included, so the marker says so. CRLF
	// and the prefix match markTruncated, whose stream this is.
	CutLine = "\r\nincoda: job output cut: the pseudo-console stopped draining; the tail after this line was lost\r\n"

	// cappedExt is the sidecar that says a job log stopped recording, out
	// of band, where the job itself cannot write one (see MarkCapped).
	cappedExt = ".capped"
	// exitExt is the sidecar that records how the run's job ended, next to
	// the capture it belongs to. The capture itself has no room for a
	// footer (a job can end mid-line), and the ticket that carried the pid
	// is released with the run, so this is what a viewer that was watching
	// the job reads once the process is gone.
	exitExt = ".exit"

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
	path    string
	written int64
	full    bool
}

// Create opens the job file for ticket, creating the lane's jobs/ directory
// and trimming it to the newest KeepPerLane-1 files first (the new file is
// the KeepPerLane-th). The file is created exclusively: a fresh ticket's
// job file must not exist yet, and one that does is either a collision or
// a plant (a hard link to a file its creator wants overwritten), so it is
// refused rather than truncated through.
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
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("joblog: %s already exists; a fresh ticket's job file must be created, not reopened", path)
		}
		return nil, fmt.Errorf("joblog: %w", err)
	}
	return &Writer{f: f, path: path}, nil
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

// stopMarkerPrefixes are the "why" texts of the one-line notes this package
// writes when it stops recording (plus the pseudo-console cut note, which
// goes through the tee with the same shape). IsStopMarker matches exactly
// these, so a job's own "incoda: ..." line cannot freeze its status row.
var stopMarkerPrefixes = []string{
	"job output cut:",
	"write error: ",
	"job log truncated at ",
}

// IsStopMarker reports whether line (without its CRLF and the "incoda: "
// prefix) is one of incoda's own stop-recording notes.
func IsStopMarker(line string) bool {
	for _, p := range stopMarkerPrefixes {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

func (w *Writer) markTruncated(why string) {
	if w.full || w.f == nil {
		return
	}
	w.full = true
	line := "\r\nincoda: " + why + "\r\n"
	_, _ = w.f.WriteString(line)
	w.written += int64(len(line))
	w.MarkCapped()
}

// MarkCapped notes out-of-band that this log stopped recording. The in-band
// marker line alone is forgeable: a job can print the exact text and freeze
// its own status row, so the reader also demands this sidecar, which only
// the runner writes. Best-effort, like every other note.
func (w *Writer) MarkCapped() {
	MarkCapped(w.path)
}

// MarkCapped touches the capped sidecar of a job log path.
func MarkCapped(logPath string) {
	if logPath == "" {
		return
	}
	f, err := os.OpenFile(logPath+cappedExt, os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		_ = f.Close()
	}
}

// Capped notes whether a job log's runner said it stopped recording.
func Capped(logPath string) bool {
	_, err := os.Stat(logPath + cappedExt)
	return err == nil
}

// ExitPath is the sidecar beside a job log that holds its exit code.
func ExitPath(logPath string) string {
	return strings.TrimSuffix(logPath, jobExt) + exitExt
}

// Exit records code beside the job file: the capture a viewer streams has
// no room for a footer (a job can end mid-line), and the ticket that
// carried the pid is released with the run, so this is what a viewer that
// was watching reads afterwards. Best-effort by design: a code that could
// not be written reads as no code, never as a wrong one.
func (w *Writer) Exit(code int) error {
	if w.path == "" {
		return nil
	}
	return WriteExit(w.path, code)
}

// WriteExit records code beside the job log at logPath.
func WriteExit(logPath string, code int) error {
	if logPath == "" {
		return nil
	}
	if err := os.WriteFile(ExitPath(logPath), []byte(strconv.Itoa(code)+"\n"), 0o644); err != nil {
		return fmt.Errorf("joblog: recording the job's exit code: %w", err)
	}
	return nil
}

// ReadExit reads the exit code recorded beside the job log at logPath. A
// missing or unreadable sidecar is simply no answer: an old incoda never
// wrote one, and a viewer showing nothing beats a viewer showing a guess.
func ReadExit(logPath string) (int, bool) {
	b, err := os.ReadFile(ExitPath(logPath))
	if err != nil {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, false
	}
	return code, true
}

// sweep deletes the oldest job files so that at most keep-1 remain, by name:
// ticket names are zero-padded arrival nanoseconds, so lexicographic order
// is arrival order. Only names with a ticket's shape count and get deleted:
// anything else in the directory is not ours to manage, and foreign names
// squatting the retention window would starve real job files of it. Failures
// are ignored the way log rotation ignores them; the cap on any one file
// still holds.
func sweep(dir string) error {
	all, err := filepath.Glob(filepath.Join(dir, "*"+jobExt))
	if err != nil {
		return fmt.Errorf("joblog: %w", err)
	}
	names := all[:0]
	for _, name := range all {
		if !lane.ValidTicketName(strings.TrimSuffix(filepath.Base(name), jobExt) + ".ticket") {
			continue
		}
		names = append(names, name)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if len(names) < KeepPerLane-1 {
		return nil
	}
	for _, name := range names[KeepPerLane-1:] {
		_ = os.Remove(name)
		_ = os.Remove(name + cappedExt) // a swept log's sidecar goes with it
		_ = os.Remove(ExitPath(name))   // and so does its exit code
	}
	return nil
}
