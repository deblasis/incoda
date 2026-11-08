// Package jobstatus follows a job's log and keeps the OSC 7501 record set
// the job wrote to it. It lives apart from the protocol package so the
// lane layer can name a Record without inheriting the log machinery: the
// protocol stays a pure parser, and this package is the one that reads.
package jobstatus

import (
	"bytes"
	"io"
	"os"
	"time"

	"github.com/deblasis/incoda/internal/joblog"
	"github.com/deblasis/incoda/internal/osc7501"
)

// firstLook is how much of a log a first Read looks at: a log is capped at
// 16 MiB and a viewer must not read that to paint one line. The same bound
// applies when a log grew by more than this between two Reads.
const firstLook = 512 << 10

// markerPrefix starts the one-line notes incoda itself appends to a job log
// when it stops recording (size cap, write error, pseudo-console cut). They
// are always the last line: nothing is recorded after one.
const markerPrefix = "\r\nincoda: "

// LogStatus follows one job log and keeps its record set, reading only the
// bytes that were appended since the last Read. It is the answer to "what is
// this job's status" for a viewer that asks again every few seconds: nothing
// is read from an unchanged file and nothing is parsed twice. A LogStatus
// follows one path; Read with another path starts over. The zero value is
// ready. It is not safe for concurrent use.
type LogStatus struct {
	path   string
	off    int64
	mod    time.Time
	sc     osc7501.Scanner
	set    osc7501.Set
	tail   []byte
	nread  int64
	capped bool
	whole  bool // a one-shot read: from the start, not the last firstLook
}

// BytesRead is the total number of log bytes read so far.
func (l *LogStatus) BytesRead() int64 { return l.nread }

// Read returns the root record of the log at path: the record set is built
// from every report in the part of the log read so far, so the job's own
// record still speaks for it after a long stretch of ordinary output. No
// log, no reports, or a log that stopped recording (its last line is an
// incoda marker, so the status before it is frozen and may be stale) gives
// false.
func (l *LogStatus) Read(path string) (osc7501.Record, bool) {
	if path != l.path {
		*l = LogStatus{path: path, nread: l.nread, whole: l.whole}
	}
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return osc7501.Record{}, false
	}
	size := fi.Size()
	if size < l.off || size == l.off && !fi.ModTime().Equal(l.mod) && l.off > 0 {
		// Truncated or replaced: what was read says nothing about this file.
		*l = LogStatus{path: path, nread: l.nread, whole: l.whole}
	}
	if size > l.off {
		// The first look at a log reads it whole: whatever record opened it
		// still speaks for the job, however deep later output buried it.
		// Every later look reads only what was appended.
		if l.off == 0 {
			l.whole = true
		}
		l.pull(path, size)
		l.whole = false
	}
	l.mod = fi.ModTime()
	if l.capped {
		return osc7501.Record{}, false
	}
	return l.set.Root()
}

// ReadWhole is Read for a look that will not come back: the whole log is
// read from its start (bounded by the per-job log cap), so a record
// however deep under later output still speaks for the job. A viewer that
// polls uses Read instead, where only appended bytes are read.
func (l *LogStatus) ReadWhole(path string) (osc7501.Record, bool) {
	l.whole = true
	return l.Read(path)
}

func (l *LogStatus) pull(path string, size int64) {
	from := l.off
	look := int64(firstLook)
	if l.whole {
		look = joblog.MaxBytes // a job log stops growing at the cap: read it all
	}
	if size-from > look {
		from = size - look
		l.sc = osc7501.Scanner{} // skipped bytes: whatever was half-read is gone
		l.tail = l.tail[:0]
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return
	}
	buf := make([]byte, size-from)
	n, _ := io.ReadFull(f, buf)
	buf = buf[:n]
	l.nread += int64(n)
	l.off = from + int64(n)
	l.sc.Scan(buf, l.set.Apply)
	l.tail = append(l.tail, buf...)
	if len(l.tail) > 512 {
		l.tail = append(l.tail[:0], l.tail[len(l.tail)-512:]...)
	}
	// The marker line alone is forgeable by the job itself; the runner's
	// sidecar is what proves the note is incoda's own.
	l.capped = endsInMarker(l.tail) && joblog.Capped(path)
}

// endsInMarker reports whether the last line of b is one of incoda's own
// stop-recording notes — exactly those texts, so a job cannot write its own
// "incoda:" line and freeze the status it shows.
func endsInMarker(b []byte) bool {
	b, ok := bytes.CutSuffix(b, []byte("\r\n"))
	if !ok {
		return false
	}
	i := bytes.LastIndex(b, []byte("\r\n"))
	if i < 0 || !bytes.HasPrefix(b[i:], []byte(markerPrefix)) {
		return false
	}
	return joblog.IsStopMarker(string(b[i+len(markerPrefix):]))
}
