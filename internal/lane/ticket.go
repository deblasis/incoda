package lane

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/textsafe"
)

const ticketExt = ".ticket"

// Ticket is the JSON payload written inside a ticket file. It exists purely so
// that `incoda status` can describe who is in the queue; correctness never
// depends on it, only on the OS lock the file carries.
type Ticket struct {
	PID   int    `json:"pid"`
	Queue string `json:"queue"`
	Slots int    `json:"slots"`
	// Exclusive asks for the queue alone: while this ticket is live the
	// effective slot count is 1, whatever the queue or anyone else asked
	// for. It is for jobs whose result is a duration, which a neighbour
	// would falsify without failing anything.
	Exclusive   bool     `json:"exclusive,omitempty"`
	ArrivalNano int64    `json:"arrival_nano"`
	Arrival     string   `json:"arrival"`
	AcquireNano int64    `json:"acquire_nano,omitempty"`
	Acquired    string   `json:"acquired,omitempty"`
	Command     []string `json:"command"`
	Reason      string   `json:"reason,omitempty"`
	// Owner names who queued the job: an agent session id, a worktree name,
	// whatever the caller's world calls itself. The cwd already says where a
	// job runs from; with a dozen sessions on one machine "whose is that" is
	// the next question, and a kill request wants a name to address.
	Owner    string `json:"owner,omitempty"`
	Hostname string `json:"hostname"`
	Dir      string `json:"cwd"`
	// Via are, on a pool ticket, the run's named project keys that link to
	// the pool (spec 2.7); empty when the run names the pool directly.
	// Status and watch group pool holders by it.
	Via []string `json:"via,omitempty"`
	// Wait is the run's --wait as given; empty when it was not given.
	Wait string `json:"wait,omitempty"`
}

// attribution is the k=v block every lifecycle line (enqueue/acquire/release)
// carries: WHERE the job ran (dir, the launch cwd), and when set WHY (reason)
// and WHOSE (owner). Values go through textsafe.LogValue so a space quotes
// them and a control character can never split the event across lines.
// Readers that do not know the fields ignore them, and old lines lack them.
func (t Ticket) attribution() string {
	s := ""
	if t.Dir != "" {
		s += " dir=" + textsafe.LogValue(t.Dir)
	}
	if t.Reason != "" {
		s += " reason=" + strconv.Quote(textsafe.Escape(t.Reason))
	}
	if t.Owner != "" {
		s += " owner=" + strconv.Quote(textsafe.Escape(t.Owner))
	}
	return s
}

// ticketName encodes the arrival order into the filename so that ordering can
// be recovered from a directory listing alone, without opening anything.
//
// Ordering rule, in full: tickets sort by arrival nanosecond, then by pid, then
// by filename. The nanosecond field is zero-padded to 20 digits so the
// lexicographic and numeric orders agree. Every participant derives the order
// from the same set of filenames, so they all reach the same answer.
func ticketName(arrivalNano int64, pid int) string {
	return fmt.Sprintf("%020d-%d%s", arrivalNano, pid, ticketExt)
}

// ticketOrder is the sort key recovered from a ticket filename.
type ticketOrder struct {
	arrivalNano int64
	pid         int
	name        string
}

func parseTicketName(name string) (ticketOrder, bool) {
	if !strings.HasSuffix(name, ticketExt) {
		return ticketOrder{}, false
	}
	base := strings.TrimSuffix(name, ticketExt)
	dash := strings.LastIndex(base, "-")
	if dash <= 0 || dash == len(base)-1 {
		return ticketOrder{}, false
	}
	nano, err := strconv.ParseInt(base[:dash], 10, 64)
	if err != nil {
		return ticketOrder{}, false
	}
	pid, err := strconv.Atoi(base[dash+1:])
	if err != nil {
		return ticketOrder{}, false
	}
	return ticketOrder{arrivalNano: nano, pid: pid, name: name}, true
}

func (a ticketOrder) less(b ticketOrder) bool {
	if a.arrivalNano != b.arrivalNano {
		return a.arrivalNano < b.arrivalNano
	}
	if a.pid != b.pid {
		return a.pid < b.pid
	}
	return a.name < b.name
}

func sortTickets(t []Entry) {
	sort.Slice(t, func(i, j int) bool { return t[i].order.less(t[j].order) })
}

// Entry is one live participant as seen by a scan.
type Entry struct {
	File    string `json:"file"`
	Ticket  Ticket `json:"ticket"`
	Holding bool   `json:"holding"`
	// WaitingSeconds counts from arrival; HeldSeconds counts from acquisition
	// and is zero for a participant that has not acquired a slot yet.
	WaitingSeconds float64 `json:"waiting_seconds"`
	HeldSeconds    float64 `json:"held_seconds"`
	// PayloadError records why a ticket's JSON could not be read. An
	// unreadable payload is never fatal -- the OS lock is what enforces the
	// queue -- but it must be visible, because a silently unreadable payload
	// once made every queue look like a one-slot queue.
	PayloadError string `json:"payload_error,omitempty"`

	order ticketOrder
}

func (e *Entry) fill(now time.Time) {
	if e.Ticket.ArrivalNano > 0 {
		e.WaitingSeconds = now.Sub(time.Unix(0, e.Ticket.ArrivalNano)).Seconds()
	}
	if e.Ticket.AcquireNano > 0 {
		e.HeldSeconds = now.Sub(time.Unix(0, e.Ticket.AcquireNano)).Seconds()
	}
}

// CommandString renders the recorded command for human output.
func (t Ticket) CommandString() string {
	if len(t.Command) == 0 {
		return "(none)"
	}
	parts := make([]string, len(t.Command))
	for i, a := range t.Command {
		if strings.ContainsAny(a, " \t\"") {
			parts[i] = strconv.Quote(a)
		} else {
			parts[i] = a
		}
	}
	return strings.Join(parts, " ")
}

func ticketPath(dir, name string) string { return filepath.Join(dir, name) }

// ValidTicketName reports whether name is exactly a ticket file name as
// ticketName writes it: digits, a dash, digits, ".ticket", nothing else. It
// is checked before a name taken from the environment is joined into a path.
func ValidTicketName(name string) bool {
	if _, ok := parseTicketName(name); !ok {
		return false
	}
	base := strings.TrimSuffix(name, ticketExt)
	for _, r := range base {
		if (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return strings.Count(base, "-") == 1
}

// TicketNamePID returns the pid embedded in a valid ticket name.
func TicketNamePID(name string) (int, bool) {
	if !ValidTicketName(name) {
		return 0, false
	}
	ord, _ := parseTicketName(name)
	return ord.pid, true
}

// TicketFilePath is the path of a ticket file inside a queue directory.
func TicketFilePath(queueDir, name string) string { return ticketPath(queueDir, name) }
