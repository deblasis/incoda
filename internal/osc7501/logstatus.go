package osc7501

import (
	"bytes"
	"io"
	"os"
	"time"
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
	sc     Scanner
	set    Set
	tail   []byte
	nread  int64
	capped bool
}

// BytesRead is the total number of log bytes read so far.
func (l *LogStatus) BytesRead() int64 { return l.nread }

// Read returns the root record of the log at path: the record set is built
// from every report in the part of the log read so far, so the job's own
// record still speaks for it after a long stretch of ordinary output. No
// log, no reports, or a log that stopped recording (its last line is an
// incoda marker, so the status before it is frozen and may be stale) gives
// false.
func (l *LogStatus) Read(path string) (Record, bool) {
	if path != l.path {
		*l = LogStatus{path: path, nread: l.nread}
	}
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return Record{}, false
	}
	size := fi.Size()
	if size < l.off || size == l.off && !fi.ModTime().Equal(l.mod) && l.off > 0 {
		// Truncated or replaced: what was read says nothing about this file.
		*l = LogStatus{path: path, nread: l.nread}
	}
	if size > l.off {
		l.pull(path, size)
	}
	l.mod = fi.ModTime()
	if l.capped {
		return Record{}, false
	}
	return l.set.Root()
}

func (l *LogStatus) pull(path string, size int64) {
	from := l.off
	if size-from > firstLook {
		from = size - firstLook
		l.sc = Scanner{} // skipped bytes: whatever was half-read is gone
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
	l.capped = endsInMarker(l.tail)
}

// endsInMarker reports whether the last line of b is an incoda note.
func endsInMarker(b []byte) bool {
	b, ok := bytes.CutSuffix(b, []byte("\r\n"))
	if !ok {
		return false
	}
	i := bytes.LastIndex(b, []byte("\r\n"))
	return i >= 0 && bytes.HasPrefix(b[i:], []byte(markerPrefix))
}
