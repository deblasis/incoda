// Package osc7501 reads the OSC 7501 program-status protocol out of a byte
// stream a job wrote to a terminal: ESC ] 7501 ; k=v:k=v BEL (or ESC \).
// It exists for two readers that must never alter the stream they look at:
// the pty runner, which answers a program's support query, and watch, which
// shows the newest status from the tail of a job's log.
//
// Everything arrives from another process, so every field is length-capped
// and a record that breaks a cap is refused whole rather than clipped.
package osc7501

import (
	"encoding/base64"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/deblasis/incoda/internal/textsafe"
)

// Limits, in bytes, mirroring the terminal-side parser. Title and msg are
// capped both encoded and decoded.
const (
	MaxSequenceBytes     = 4096
	MaxIDBytes           = 128
	MaxIDSegmentBytes    = 32
	MaxIDDepth           = 8
	MaxAppBytes          = 32
	MaxTitleEncodedBytes = 256
	MaxTitleBytes        = 192
	MaxMsgEncodedBytes   = 2732
	MaxMsgBytes          = 2048

	// TailBytes is how much of a job log ReadLogStatus looks at.
	TailBytes = 8 << 10
)

var (
	states = map[string]bool{"idle": true, "working": true, "done": true, "blocked": true, "error": true, "clear": true}
	kinds  = map[string]bool{"permission": true, "question": true, "auth": true}
)

// Record is one status report. Progress is nil when the program sent none.
type Record struct {
	ID       string
	State    string
	Kind     string
	Progress *int
	App      string
	Title    string
	Msg      string
}

// SupportReply is what a terminal answers to a query.
func SupportReply() []byte {
	return []byte("\x1b]7501;?:states=idle,working,done,blocked,error,clear:kinds=permission,question,auth\x1b\\")
}

// IsQuery reports whether data is exactly one support query.
func IsQuery(data []byte) bool {
	s := string(data)
	return s == "\x1b]7501;?\x07" || s == "\x1b]7501;?\x1b\\"
}

const (
	stNormal = iota
	stEsc    // saw ESC
	stOSC    // inside ESC ] ... collecting the body
	stOSCEsc // inside the body, saw ESC (ST pending)
)

// Scanner finds OSC 7501 sequences in output delivered in arbitrary chunks;
// a sequence cut by a chunk boundary is carried to the next Scan. The zero
// value is ready. It never modifies or retains the data it is given.
type Scanner struct {
	state    int
	buf      []byte
	overflow bool
	w        io.Writer
}

// Respond makes the scanner write SupportReply to w for every complete
// query it sees. w receives one Write per reply; the caller owns
// serialization with any other writer to the same destination. A write error
// is dropped: a program that cannot be answered just gets no reply, the
// same as on a terminal that lacks the protocol.
func (s *Scanner) Respond(w io.Writer) { s.w = w }

// Scan feeds the next chunk of output, calling cb for each complete record.
func (s *Scanner) Scan(data []byte, cb func(Record)) {
	for _, c := range data {
		switch s.state {
		case stNormal:
			if c == 0x1b {
				s.state = stEsc
			}
		case stEsc:
			switch c {
			case ']':
				s.begin()
			case 0x1b:
			default:
				s.state = stNormal
			}
		case stOSC:
			switch c {
			case 0x07:
				s.finish(cb)
			case 0x1b:
				s.state = stOSCEsc
			case 0x18, 0x1a: // CAN, SUB abort a control string
				s.state = stNormal
			default:
				s.add(c)
			}
		case stOSCEsc:
			switch c {
			case '\\':
				s.finish(cb)
			case ']':
				s.begin()
			case 0x1b:
			default:
				s.state = stNormal
			}
		}
	}
}

func (s *Scanner) begin() {
	s.state = stOSC
	s.buf = s.buf[:0]
	s.overflow = false
}

func (s *Scanner) add(c byte) {
	if s.overflow {
		return
	}
	if len(s.buf) >= MaxSequenceBytes {
		s.overflow = true
		return
	}
	s.buf = append(s.buf, c)
}

func (s *Scanner) finish(cb func(Record)) {
	s.state = stNormal
	if s.overflow {
		return
	}
	body := string(s.buf)
	if body == "7501;?" {
		if s.w != nil {
			_, _ = s.w.Write(SupportReply())
		}
		return
	}
	body, ok := strings.CutPrefix(body, "7501;")
	if !ok {
		return
	}
	if r, ok := parse(body); ok && cb != nil {
		cb(r)
	}
}

func nameOK(v string, limit int) bool {
	if v == "" || len(v) > limit {
		return false
	}
	for i := range len(v) {
		c := v[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '_', c == '.', c == '+', c == '-':
		default:
			return false
		}
	}
	return true
}

func idOK(id string) bool {
	if len(id) > MaxIDBytes {
		return false
	}
	segs := strings.Split(id, "/")
	if len(segs) > MaxIDDepth {
		return false
	}
	for _, seg := range segs {
		if !nameOK(seg, MaxIDSegmentBytes) {
			return false
		}
	}
	return true
}

// text decodes a base64 field. ok=false refuses the record (over a cap);
// a value that is merely malformed comes back empty with ok=true: the field
// is dropped, the record stands.
func text(v string, maxEnc, maxDec int) (string, bool) {
	if len(v) > maxEnc {
		return "", false
	}
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return "", true
	}
	if len(b) > maxDec {
		return "", false
	}
	if !utf8.Valid(b) {
		return "", true
	}
	return string(b), true
}

func parse(body string) (Record, bool) {
	vals := map[string]string{}
	for _, pair := range strings.Split(body, ":") {
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		vals[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	var r Record
	if !states[vals["state"]] {
		return r, false
	}
	r.State = vals["state"]
	if id, ok := vals["id"]; ok {
		if !idOK(id) {
			return r, false
		}
		r.ID = id
	}
	if k := vals["kind"]; kinds[k] {
		r.Kind = k
	}
	if p, ok := vals["progress"]; ok {
		if n, err := strconv.Atoi(p); err == nil && n >= 0 && n <= 100 {
			r.Progress = &n
		}
	}
	if a := vals["app"]; nameOK(a, MaxAppBytes) {
		r.App = a
	}
	var ok bool
	if r.Title, ok = text(vals["title"], MaxTitleEncodedBytes, MaxTitleBytes); !ok {
		return r, false
	}
	if r.Msg, ok = text(vals["msg"], MaxMsgEncodedBytes, MaxMsgBytes); !ok {
		return r, false
	}
	return r, true
}

func depth(id string) int {
	if id == "" {
		return 0
	}
	return strings.Count(id, "/") + 1
}

// Root picks the record that speaks for the whole job: the fewest id path
// segments (an id-less record is shallowest), the latest one on a tie.
func Root(rs []Record) (Record, bool) {
	var best Record
	found := false
	for _, r := range rs {
		if !found || depth(r.ID) <= depth(best.ID) {
			best, found = r, true
		}
	}
	return best, found
}

// Line is "[state p%] msg" for display, msg cut to maxMsg characters with an
// ellipsis. The msg came from the job, so it is escaped first: a control
// sequence in it must not repaint the viewer's terminal.
func (r Record) Line(maxMsg int) string {
	head := r.State
	if r.Progress != nil {
		head += " " + strconv.Itoa(*r.Progress) + "%"
	}
	line := "[" + head + "]"
	if r.Msg == "" {
		return line
	}
	msg := []rune(textsafe.Escape(r.Msg))
	if len(msg) > maxMsg {
		if maxMsg < 1 {
			return line
		}
		msg = append(msg[:maxMsg-1], '…')
	}
	return line + " " + string(msg)
}

// ReadLogStatus reads the last TailBytes of the job log at path and returns
// the root record found there. No file, no records: no status.
func ReadLogStatus(path string) (Record, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Record{}, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Record{}, false
	}
	off := max(fi.Size()-TailBytes, 0)
	buf := make([]byte, min(fi.Size(), TailBytes))
	n, err := f.ReadAt(buf, off)
	if n == 0 && err != nil {
		return Record{}, false
	}
	var s Scanner
	var rs []Record
	s.Scan(buf[:n], func(r Record) { rs = append(rs, r) })
	return Root(rs)
}
