package jobstatus

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/joblog"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func rec(pairs string, term string) string { return "\x1b]7501;" + pairs + term }

// The terminal's record set is replayed from the whole log, not from the
// last 8 KiB window: the root that scrolled out still speaks for the job.
func TestReadLogStatusKeepsRootBeyondTheWindow(t *testing.T) {
	log := rec("state=working:id=build:progress=10:msg="+b64("whole build"), "\x07") +
		strings.Repeat("compiling line\r\n", 700) +
		rec("state=working:id=build/step:msg="+b64("compiling foo.c"), "\x07")
	p := filepath.Join(t.TempDir(), "j.out")
	if err := os.WriteFile(p, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	var st LogStatus
	r, ok := st.Read(p)
	if !ok || r.ID != "build" || r.Msg != "whole build" {
		t.Fatalf("the build's own record must speak for it: %+v ok=%v", r, ok)
	}
}

// The status survives the output that follows it.
func TestReadLogStatusSurvivesTrailingOutput(t *testing.T) {
	log := rec("state=done:id=job", "\x07") + strings.Repeat("summary line\r\n", 1000)
	p := filepath.Join(t.TempDir(), "j.out")
	if err := os.WriteFile(p, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	var st LogStatus
	if r, ok := st.Read(p); !ok || r.State != "done" {
		t.Fatalf("done must survive trailing output: %+v ok=%v", r, ok)
	}
}

// Reading is incremental: a second Read with the file unchanged does not
// touch it, and a grown file is read from where the last read stopped, so a
// record split across two polls is still found.
func TestLogStatusIsIncremental(t *testing.T) {
	p := filepath.Join(t.TempDir(), "j.out")
	full := rec("state=working:id=job:progress=40:msg="+b64("linking"), "\x07")
	cut := len(full) / 2
	if err := os.WriteFile(p, []byte("noise"+full[:cut]), 0o600); err != nil {
		t.Fatal(err)
	}
	var st LogStatus
	if _, ok := st.Read(p); ok {
		t.Fatal("half a record is no record")
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(full[cut:] + "tail"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	r, ok := st.Read(p)
	if !ok || r.Msg != "linking" {
		t.Fatalf("record split across polls: %+v ok=%v", r, ok)
	}
	// Unchanged file: not read again (remove it; the cached answer stands
	// only while size and mtime match, so recreate the same stat is not
	// possible; instead assert the byte counter).
	before := st.BytesRead()
	if _, ok := st.Read(p); !ok {
		t.Fatal("unchanged file lost its status")
	}
	if st.BytesRead() != before {
		t.Fatalf("an unchanged log was read again: %d -> %d", before, st.BytesRead())
	}
	// The first look reads a log whole, bounded by the per-job log cap, so
	// a record survives however much output follows it; the file past the
	// cap is hostile input, not a job log, and only its tail is read.
	big := filepath.Join(t.TempDir(), "big.out")
	if err := os.WriteFile(big, append(bytes.Repeat([]byte("x"), 20<<20), full...), 0o600); err != nil {
		t.Fatal(err)
	}
	var st2 LogStatus
	if r, ok := st2.Read(big); !ok || r.Msg != "linking" {
		t.Fatalf("big log: %+v ok=%v", r, ok)
	}
	if st2.BytesRead() > joblog.MaxBytes {
		t.Fatalf("a 20 MiB log was read with %d bytes on the first look (cap %d)", st2.BytesRead(), joblog.MaxBytes)
	}
	// A record buried deep INSIDE the cap still speaks for the job.
	deep := filepath.Join(t.TempDir(), "deep.out")
	if err := os.WriteFile(deep, append(append([]byte(full), bytes.Repeat([]byte("x"), 600<<10)...), []byte("tail\r\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	var st3 LogStatus
	if r, ok := st3.Read(deep); !ok || r.Msg != "linking" {
		t.Fatalf("deep-buried record: %+v ok=%v", r, ok)
	}
}

// A log that hit its cap ends with a marker line and carries the runner's
// out-of-band sidecar; the status before it is frozen and unknowable, so
// none is shown. The marker text alone — byte-exact, even — is not enough:
// a job can print it, and only the sidecar says the note is incoda's own.
func TestStatusNotShownPastTheLogCap(t *testing.T) {
	for _, marker := range []string{
		"\r\nincoda: job log truncated at 16777216 bytes; later output only reached the run's stdout\r\n",
		"\r\nincoda: job output cut: the pseudo-console stopped draining; the tail after this line was lost\r\n",
		"\r\nincoda: write error: disk full\r\n",
	} {
		log := rec("state=working:id=build:progress=40:msg="+b64("linking"), "\x07") + strings.Repeat("x", 100) + marker
		p := filepath.Join(t.TempDir(), "j.out")
		if err := os.WriteFile(p, []byte(log), 0o600); err != nil {
			t.Fatal(err)
		}
		joblog.MarkCapped(p)
		var st LogStatus
		if r, ok := st.Read(p); ok {
			t.Errorf("%q: log is capped, got live-looking %q", marker, r.Line(40))
		}
		// Same text, no sidecar: that is a job's forgery, not a cap, and
		// the status stays.
		p2 := filepath.Join(t.TempDir(), "j.out")
		if err := os.WriteFile(p2, []byte(log), 0o600); err != nil {
			t.Fatal(err)
		}
		var st2 LogStatus
		if r, ok := st2.Read(p2); !ok || r.Msg != "linking" {
			t.Errorf("%q: marker text without the sidecar must not cap the log: %+v ok=%v", marker, r, ok)
		}
	}
}

func TestLogStatusReadsRecordsAndNothingElse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "job.out")
	old := rec("state=error:id=old:msg="+b64("stale"), "\x07")
	body := old + strings.Repeat("x", 20000) + rec("state=working:id=job:progress=55:msg="+b64("linking"), "\x07") + "\r\nmore output\r\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var st LogStatus
	r, ok := st.Read(path)
	if !ok || r.ID != "job" || r.State != "working" || r.Msg != "linking" {
		t.Fatalf("got %+v ok=%v", r, ok)
	}
	var none LogStatus
	if _, ok := none.Read(filepath.Join(dir, "missing.out")); ok {
		t.Fatal("a missing log has no status")
	}
	plain := filepath.Join(dir, "plain.out")
	if err := os.WriteFile(plain, []byte("just text\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var pl LogStatus
	if _, ok := pl.Read(plain); ok {
		t.Fatal("a log with no records has no status")
	}
}
