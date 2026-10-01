package machine

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/lane"
)

// Blocker is one live ticket of an older incoda that the migration waits
// for. Command is already escaped for display; it is empty in a note read
// back from machine.lock, which carries only pid and key.
type Blocker struct {
	Key     string
	PID     int
	Command string
}

// Note is the line the machine.lock holder writes into the lock file, so
// waiters and status can say who holds it, for what, and which older runs
// a migration waits for (spec 3.1).
type Note struct {
	PID      int
	Op       string
	Since    time.Time
	Blockers []Blocker
}

// String renders the note: pid=N op=<op> since=<RFC 3339 UTC> and, when
// there are any, blockers=<pid>:<key>,...
func (n Note) String() string {
	s := fmt.Sprintf("pid=%d op=%s since=%s", n.PID, n.Op, n.Since.UTC().Format(time.RFC3339))
	if len(n.Blockers) > 0 {
		parts := make([]string, len(n.Blockers))
		for i, b := range n.Blockers {
			parts[i] = fmt.Sprintf("%d:%s", b.PID, b.Key)
		}
		s += " blockers=" + strings.Join(parts, ",")
	}
	return s
}

// ParseNote reads a note back. Anything that does not parse, including a
// note caught half rewritten, reports false: callers then say less, never
// something wrong. Fields a newer incoda adds are ignored.
func ParseNote(s string) (Note, bool) {
	var n Note
	for _, f := range strings.Fields(s) {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return Note{}, false
		}
		switch k {
		case "pid":
			p, err := strconv.Atoi(v)
			if err != nil || p <= 0 {
				return Note{}, false
			}
			n.PID = p
		case "op":
			if !validOp(v) {
				return Note{}, false
			}
			n.Op = v
		case "since":
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return Note{}, false
			}
			n.Since = t
		case "blockers":
			for _, b := range strings.Split(v, ",") {
				ps, key, ok := strings.Cut(b, ":")
				p, err := strconv.Atoi(ps)
				if !ok || err != nil || p <= 0 || lane.ValidateKey(key) != nil {
					return Note{}, false
				}
				n.Blockers = append(n.Blockers, Blocker{Key: key, PID: p})
			}
		}
	}
	if n.PID == 0 || n.Op == "" || n.Since.IsZero() {
		return Note{}, false
	}
	return n, true
}

// validOp keeps the op printable as is: lower-case letters and '-'.
func validOp(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && r != '-' {
			return false
		}
	}
	return true
}

// ReadNote reads the note in <state>/machine.lock without locking it (the
// lock is a byte range far past the note on Windows and an flock on Unix,
// so the content stays readable).
func ReadNote(stateDir string) (Note, bool) {
	b, err := os.ReadFile(LockPath(stateDir))
	if err != nil {
		return Note{}, false
	}
	return ParseNote(string(b))
}

// blockerList names blockers the way the waiting line and the status banner
// do: "builds pid 4711, kungfoo-ui pid 5120".
func blockerList(bs []Blocker) string {
	parts := make([]string, len(bs))
	for i, b := range bs {
		parts[i] = fmt.Sprintf("%s pid %d", b.Key, b.PID)
	}
	return strings.Join(parts, ", ")
}
