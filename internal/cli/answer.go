package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/textsafe"
)

// answerTextMax bounds what one answer may carry: enough for any typed
// reply, short of using the door as a file transfer into a console that
// reads one line at a time.
const answerTextMax = 4096

// cmdAnswer types an owner's reply at a pty job that reported itself
// blocked over OSC 7501. The gates run in the run process, against the
// live ticket and the live record, because that is where the truth is: the
// command's own checks are the fast refusals, and the run's are the real
// ones. The ack carries the request's own id: a verdict is read from the
// door file while it still exists, and from the lane's log - which
// survives the file - once it does not, so no answer is ever reported
// delivered on the strength of a file that something else removed.
func cmdAnswer(args []string, stdout, stderr io.Writer) error {
	start := time.Now()
	fs := newFlagSet("answer", stderr)
	queue := fs.String("queue", "", "queue key (defaults to $INCODA_QUEUE)")
	pid := fs.Int("pid", 0, "pid of the blocked pty holder, as status shows it")
	text := fs.String("text", "", "the reply to type at the job's console input; a carriage return completes it unless --raw")
	owner := fs.String("owner", os.Getenv("INCODA_OWNER"), "identity that may answer (the job's own owner; --owner / $INCODA_OWNER, the same as attach)")
	raw := fs.Bool("raw", false, "type the text exactly as given, with no completing carriage return")
	wait := fs.Duration("wait", 10*time.Second, "how long to wait for the run to deliver the reply or refuse it")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: incoda answer --queue KEY --pid N --text TEXT [--owner WHO] [--raw] [--wait 10s]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for answer"}
	}
	if *pid <= 0 {
		return usagef("answer needs --pid, the participant's pid as `incoda status` shows it")
	}
	if !*raw {
		*text += "\r"
	}
	if len(*text) > answerTextMax {
		return usagef("answer text is %d bytes; the most one answer may carry is %d", len(*text), answerTextMax)
	}
	key, err := resolveKey(*queue)
	if err != nil {
		return err
	}
	dir, _, err := readState()
	if err != nil {
		return err
	}
	q, err := lane.OpenIn(lane.LanesDir(dir), key, lane.Existing)
	if err != nil {
		return usagef("queue %q has no live participant with pid %d: %v", key, *pid, lane.ErrNoParticipant)
	}
	defer q.Close()
	q.SetBudget(start, *wait)

	me := strings.TrimSpace(*owner)
	if me == "" {
		// The fast refusal for a missing identity: the run would say the
		// same, one poll later.
		return usagef("attach needs --owner or $INCODA_OWNER: answer is limited to the job's own owner")
	}
	req := lane.AnswerRequest{ID: fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano()), By: me, ByPID: os.Getpid(), Text: *text}
	entry, err := q.RequestAnswer(*pid, req)
	if err != nil {
		return usagef("%v", err)
	}
	// The command's own gates mirror the run's: refuse here and take the
	// request back, so the run never handles a request it would only
	// refuse. What gets past both gates is decided where the ticket lives.
	takeBack := func() { _ = q.WithdrawAnswer(*pid, req.ID) }
	if !entry.Ticket.PTY {
		takeBack()
		return usagef("job pid %d on queue %q is not a pty job: it reports no status, and the answer door needs a console to type into", *pid, key)
	}
	if err := validateEntryForAttach(key, &entry); err != nil {
		takeBack()
		return err
	}
	if err := lane.MayAttach(entry.Ticket, me); err != nil {
		takeBack()
		return usagef("%v", err)
	}
	fmt.Fprintf(stdout, "answer: pid %d on queue %q: %s\n", *pid, key,
		textsafe.Escape(entry.Ticket.CommandString()))

	answerPath := filepath.Join(q.Dir, entry.File+lane.AnswerExt)
	logPath := lane.LogPath(q.Dir)
	deadline := time.Now().Add(*wait)
	// A verdict carries an id so only THIS answer's outcome can end this
	// command, and the reason is escaped: the door file is writable by any
	// local process, and its strings are data, never terminal chrome.
	verdict, reason := pollAnswerVerdict(answerPath, logPath, req.ID, deadline)
	switch verdict {
	case "delivered":
		fmt.Fprintf(stdout, "answer: delivered\n")
		return nil
	case "refused":
		return usagef("answer refused: %s", textsafe.Escape(reason))
	default:
		return exitWith(ExitAnswerPending,
			"answer pending: pid %d has not taken the reply after %s: it may not be reporting blocked, or its run is wedged. "+
				"The request stays addressed to the ticket until the run acts on it", *pid, *wait)
	}
}

// pollAnswerVerdict waits for THIS request's verdict: in the door file
// while the file still exists, and in the lane's log once it is gone - the
// log survives the file, so a verdict that landed an instant before the
// ticket was swept is still read, and a file that vanished without any
// verdict of ours is never mistaken for one.
func pollAnswerVerdict(answerPath, logPath, id string, deadline time.Time) (verdict, reason string) {
	for {
		// The log is the durable form of a verdict: consult it before any
		// conclusion drawn from the file, because the file can be renamed
		// over by a newer answer the instant our own verdict was written
		// into it - the verdict survives in the log, and reading the file
		// first would misreport a delivered answer as superseded.
		if tail, err := readLogTail(logPath, 64<<10); err == nil {
			for _, line := range strings.Split(tail, "\n") {
				if !lineCarriesID(line, id) {
					continue
				}
				switch {
				case strings.Contains(line, "event=answer-delivered"):
					return "delivered", ""
				case strings.Contains(line, "event=answer-refused"):
					return "refused", reasonFromLogLine(line)
				}
			}
		}
		// The door file carries the same verdict one poll earlier, and it
		// is the only place a verdict exists before the holder logs it:
		// stat-bounded like the holder's own read, because the file is
		// writable by any local process.
		if fi, err := os.Stat(answerPath); err == nil && fi.Mode().IsRegular() && fi.Size() <= lane.AnswerFileMax {
			if b, err := os.ReadFile(answerPath); err == nil {
				var req lane.AnswerRequest
				if json.Unmarshal(b, &req) == nil {
					if req.ID == id && req.Verdict == "delivered" {
						return "delivered", ""
					}
					if req.ID == id && req.Verdict == "refused" {
						return "refused", req.Refused
					}
					// A different request owns the file now and the log
					// holds no verdict of ours: superseded.
					if req.ID != id {
						return "refused", "a newer answer superseded this one"
					}
				}
			}
		}
		if time.Now().After(deadline) {
			return "", ""
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// reasonFromLogLine pulls the logged refusal reason out of a lane-log
// line, best effort; the log line is the durable form of the verdict.
// The logged value is textsafe.LogValue output - quoted when the reason
// carries anything unsafe - so a quoted form is unquoted for display.
func reasonFromLogLine(line string) string {
	const mark = "reason="
	i := strings.Index(line, mark)
	if i < 0 {
		return "the run refused the answer"
	}
	v := strings.TrimSpace(line[i+len(mark):])
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		if u, err := strconv.Unquote(v); err == nil {
			return u
		}
	}
	return v
}

// lineCarriesID matches " id=<id>" as a whole field: an id that is a
// prefix of another request's id must not claim that request's verdict.
func lineCarriesID(line, id string) bool {
	i := strings.Index(line, " id="+id)
	if i < 0 {
		return false
	}
	rest := i + len(" id=") + len(id)
	return rest == len(line) || line[rest] == ' ' || line[rest] == '\n'
}

// readLogTail reads the last n bytes of a log file.
func readLogTail(path string, n int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	var off int64
	if fi.Size() > n {
		off = fi.Size() - n
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return "", err
	}
	b := make([]byte, fi.Size()-off)
	if _, err := io.ReadFull(f, b); err != nil {
		return "", err
	}
	return string(b), nil
}
