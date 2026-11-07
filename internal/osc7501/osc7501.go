// Package osc7501 reads the OSC 7501 program-status protocol out of a byte
// stream a job wrote to a terminal: ESC ] 7501 ; k=v:k=v BEL (or ESC \).
// It exists for two readers that must never alter the stream they look at:
// the pty runner, which answers a program's support query, and watch, which
// shows the newest status from a job's log.
//
// Everything arrives from another process, so every field is length-capped
// and a report that breaks a rule is discarded whole, the way the terminal
// that defines the protocol discards it: a report watch shows is a report
// the terminal would have accepted.
package osc7501

import (
	"encoding/base64"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"

	"github.com/deblasis/incoda/internal/textsafe"
)

// Limits, in bytes, mirroring the terminal-side parser. Every limit counts
// bytes, not characters.
const (
	// MaxSequenceBytes is the longest whole sequence, ESC ] through the
	// terminator; MaxBodyBytes is what is left for the body after "7501;"
	// when the terminator is the longer ESC \.
	MaxSequenceBytes = 4096
	MaxBodyBytes     = MaxSequenceBytes - len("\x1b]7501;") - len("\x1b\\")

	MaxKeyBytes          = 16
	MaxIDBytes           = 128
	MaxIDSegmentBytes    = 32
	MaxIDDepth           = 8
	MaxAppBytes          = 32
	MaxTitleEncodedBytes = 256
	MaxTitleBytes        = 192
	MaxMsgEncodedBytes   = 2732
	MaxMsgBytes          = 2048

	// maxRecords bounds the record set a log is replayed into: a log of
	// endless distinct ids must not grow the viewer without limit.
	maxRecords = 64
)

const prefix = "7501;"

var (
	states = map[string]bool{"idle": true, "working": true, "done": true, "blocked": true, "error": true, "clear": true}
	kinds  = map[string]bool{"permission": true, "question": true, "auth": true}
)

// Record is one status report. Progress is nil when the program sent none
// (or sent one the state does not carry).
type Record struct {
	ID       string
	State    string
	Kind     string
	Progress *int
	App      string
	Title    string
	Msg      string
}

// SupportReply is what a terminal answers to a query, ending in ST.
func SupportReply() []byte { return reply(false) }

// IsQuery reports whether data is exactly one support query: ESC ] 7501 ; ?
// and a BEL or ST terminator.
func IsQuery(data []byte) bool {
	return string(data) == "\x1b]7501;?\x07" || string(data) == "\x1b]7501;?\x1b\\"
}

// reply is the support reply ending in the terminator the query used, so a
// program that scans its input for a BEL-terminated answer recognizes it:
// BEL for a BEL query, ESC \ for anything else.
func reply(bel bool) []byte {
	const body = "\x1b]7501;?:states=idle,working,done,blocked,error,clear:kinds=permission,question,auth"
	if bel {
		return []byte(body + "\x07")
	}
	return []byte(body + "\x1b\\")
}

const (
	stNormal = iota
	stEsc    // saw ESC
	stOSC    // inside ESC ] ... collecting the body
	stString // inside a DCS/APC/SOS/PM string
)

// Scanner finds OSC 7501 sequences in output delivered in arbitrary chunks;
// a sequence cut by a chunk boundary is carried to the next Scan. The zero
// value is ready. It never modifies or retains the data it is given.
//
// It follows the terminal's own state machine at the points that matter: ESC,
// CAN and SUB end an OSC string (and dispatch what it holds), C0 controls
// inside the body are ignored, BEL and ESC \ both terminate.
type Scanner struct {
	state    int
	buf      []byte
	overflow bool
	skip     bool // an OSC that is not 7501: nothing is kept
	w        io.Writer
}

// Respond makes the scanner write SupportReply to w for every complete
// query it sees. w receives one Write per reply; the caller owns
// serialization with any other writer to the same destination. A write error
// is dropped: a program that cannot be answered just gets no reply, the
// same as on a terminal that lacks the protocol.
func (s *Scanner) Respond(w io.Writer) { s.w = w }

// Scan feeds the next chunk of output, calling cb (when not nil) for each
// complete record. With a nil cb nothing is decoded: only queries matter.
func (s *Scanner) Scan(data []byte, cb func(Record)) {
	for _, c := range data {
		switch s.state {
		case stNormal:
			if c == 0x1b {
				s.state = stEsc
			}
		case stEsc:
			switch {
			case c == ']':
				s.begin()
			case c == 'P', c == '_', c == 'X', c == '^':
				// DCS, APC, SOS, PM: a string to the terminal. It ends at the
				// next ESC the way the terminal's parser leaves it (dispatch and
				// re-enter escape), so a query that follows is a query.
				s.state = stString
			case c == 0x1b, c == 0x7f, c < 0x20 && c != 0x18 && c != 0x1a:
				// the terminal stays in escape for these
			default:
				s.state = stNormal
			}
		case stString:
			switch {
			case c == 0x1b:
				s.state = stEsc
			case c == 0x18, c == 0x1a: // CAN, SUB end the string
				s.state = stNormal
			}
		case stOSC:
			switch {
			case c == 0x07:
				s.finish(cb, true)
				s.state = stNormal
			case c == 0x1b:
				s.finish(cb, false)
				s.state = stEsc
			case c == 0x18, c == 0x1a: // CAN, SUB
				s.finish(cb, false)
				s.state = stNormal
			case c < 0x20:
				// ignored inside the string
			default:
				s.add(c)
			}
		}
	}
}

func (s *Scanner) begin() {
	s.state = stOSC
	s.buf = s.buf[:0]
	s.overflow = false
	s.skip = false
}

func (s *Scanner) add(c byte) {
	if s.skip || s.overflow {
		return
	}
	if len(s.buf) >= len(prefix)+MaxBodyBytes {
		s.overflow = true
		return
	}
	s.buf = append(s.buf, c)
	if len(s.buf) == len(prefix) && string(s.buf) != prefix {
		s.skip = true
		s.buf = s.buf[:0]
	}
}

func (s *Scanner) finish(cb func(Record), bel bool) {
	if s.overflow || s.skip || len(s.buf) < len(prefix) {
		return
	}
	body := s.buf[len(prefix):]
	if len(body) == 1 && body[0] == '?' {
		if s.w != nil {
			_, _ = s.w.Write(reply(bel))
		}
		return
	}
	if cb == nil {
		return
	}
	if r, ok := parse(string(body)); ok {
		cb(r)
	}
}

// name bytes and value bytes, as the terminal's alphabet defines them.
func nameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '_' || c == '.' || c == '+' || c == '-'
}

func valueOK(v string) bool {
	for i := range len(v) {
		if c := v[i]; !nameByte(c) && c != ',' && c != '/' && c != '=' {
			return false
		}
	}
	return true
}

func isName(v string) bool {
	if v == "" {
		return false
	}
	for i := range len(v) {
		if !nameByte(v[i]) {
			return false
		}
	}
	return true
}

func idOK(id string) bool {
	if len(id) > MaxIDBytes {
		return false
	}
	depth := 0
	for seg := range strings.SplitSeq(id, "/") {
		depth++
		if depth > MaxIDDepth || len(seg) > MaxIDSegmentBytes || !isName(seg) {
			return false
		}
	}
	return true
}

// text decodes a base64 title or msg: padding is optional, the text must be
// UTF-8 with no control character, and the decoded size is capped.
func text(v string, maxDec int) (string, bool) {
	dec := base64.RawStdEncoding
	if strings.HasSuffix(v, "=") {
		dec = base64.StdEncoding
	}
	b, err := dec.DecodeString(v)
	if err != nil || len(b) > maxDec || !utf8.Valid(b) {
		return "", false
	}
	for _, r := range string(b) {
		if r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f {
			return "", false
		}
	}
	return string(b), true
}

func progress(v string) *int {
	if v == "" {
		return nil
	}
	n := 0
	for i := range len(v) {
		if v[i] < '0' || v[i] > '9' {
			return nil
		}
		n = n*10 + int(v[i]-'0')
		if n > 100 {
			return nil
		}
	}
	return &n
}

const ascii = " \t\n\v\f\r"

// parse reads one report body (everything after "7501;"). It is forgiving
// about a single pair and strict about the report: a pair without "=", or
// an unknown key, is skipped; any key or value over its limit, a bad id,
// or text that is not base64 of safe UTF-8 discards the whole report, even
// when a later pair of the same key would have replaced it.
func parse(body string) (Record, bool) {
	var state, id, kind, prog, app, title, msg string
	for body != "" {
		var field string
		field, body, _ = strings.Cut(body, ":")
		eq := strings.IndexByte(field, '=')
		if eq < 0 {
			continue
		}
		key := strings.Trim(field[:eq], ascii)
		if len(key) > MaxKeyBytes {
			return Record{}, false
		}
		val := strings.Trim(field[eq+1:], ascii)
		valid := valueOK(val)
		switch key {
		case "state":
			if valid {
				state = val
			}
		case "kind":
			if valid {
				kind = val
			}
		case "progress":
			if valid {
				prog = val
			}
		case "app":
			if valid {
				if len(val) > MaxAppBytes {
					return Record{}, false
				}
				app = val
			}
		case "id":
			if !valid || !idOK(val) {
				return Record{}, false
			}
			id = val
		case "title":
			if !valid || len(val) > MaxTitleEncodedBytes {
				return Record{}, false
			}
			t, ok := text(val, MaxTitleBytes)
			if !ok {
				return Record{}, false
			}
			title = t
		case "msg":
			if !valid || len(val) > MaxMsgEncodedBytes {
				return Record{}, false
			}
			m, ok := text(val, MaxMsgBytes)
			if !ok {
				return Record{}, false
			}
			msg = m
		}
	}
	if !states[state] {
		return Record{}, false
	}
	r := Record{ID: id, State: state, Title: title, Msg: msg}
	if isName(app) {
		r.App = app
	}
	if state == "blocked" && kinds[kind] {
		r.Kind = kind
	}
	if state == "working" || state == "blocked" {
		r.Progress = progress(prog)
	}
	return r, true
}

func depth(id string) int {
	if id == "" {
		return 0
	}
	return strings.Count(id, "/") + 1
}

// Set is the terminal's record set replayed from a stream of reports: one
// record per id, a new report replacing the old one completely, and clear
// removing the addressed record and every record beneath it (an id-less
// clear removes them all). The zero value is empty.
type Set struct {
	recs []entry
	seq  uint64
}

type entry struct {
	r   Record
	seq uint64
}

// Apply folds one report into the set.
func (s *Set) Apply(r Record) {
	if r.State == "clear" {
		if r.ID == "" {
			s.recs = s.recs[:0]
			return
		}
		kept := s.recs[:0]
		for _, e := range s.recs {
			if e.r.ID != r.ID && !strings.HasPrefix(e.r.ID, r.ID+"/") {
				kept = append(kept, e)
			}
		}
		s.recs = kept
		return
	}
	s.seq++
	for i := range s.recs {
		if s.recs[i].r.ID == r.ID {
			s.recs[i] = entry{r, s.seq}
			return
		}
	}
	if len(s.recs) >= maxRecords {
		oldest := 0
		for i, e := range s.recs {
			if e.seq < s.recs[oldest].seq {
				oldest = i
			}
		}
		s.recs[oldest] = entry{r, s.seq}
		return
	}
	s.recs = append(s.recs, entry{r, s.seq})
}

// Root picks the record that speaks for the whole job: the fewest id path
// segments (an id-less record is shallowest), the latest report on a tie.
func (s *Set) Root() (Record, bool) {
	best := -1
	for i, e := range s.recs {
		if best < 0 {
			best = i
			continue
		}
		d, bd := depth(e.r.ID), depth(s.recs[best].r.ID)
		if d < bd || d == bd && e.seq > s.recs[best].seq {
			best = i
		}
	}
	if best < 0 {
		return Record{}, false
	}
	return s.recs[best].r, true
}

// Root replays rs into a Set and returns its root record.
func Root(rs []Record) (Record, bool) {
	var s Set
	for _, r := range rs {
		s.Apply(r)
	}
	return s.Root()
}

func cellWidth(s string) int { return runewidth.StringWidth(s) }

// Head is "[state p%]": the part of the display line that is never cut.
func (r Record) Head() string {
	head := r.State
	if r.Progress != nil {
		head += " " + strconv.Itoa(*r.Progress) + "%"
	}
	return "[" + head + "]"
}

// Line is "[state p%] msg" for display, msg cut to maxMsg terminal cells
// with an ellipsis. The msg came from the job, so it is escaped first: a
// control sequence in it must not repaint the viewer's terminal.
func (r Record) Line(maxMsg int) string {
	return JoinMsg(r.Head(), textsafe.Escape(r.Msg), maxMsg)
}

// JoinMsg is head, a space and msg cut to maxMsg cells; no msg, or no room
// for one, gives head alone.
func JoinMsg(head, msg string, maxMsg int) string {
	if msg == "" || maxMsg < 1 {
		return head
	}
	if cellWidth(msg) > maxMsg {
		msg = runewidth.Truncate(msg, maxMsg, "…")
	}
	return head + " " + msg
}
