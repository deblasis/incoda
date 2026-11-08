package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
// ones. The ack is the request file's fate: removed once the reply was
// typed and read, rewritten with a reason when the run refused.
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
	entry, err := q.RequestAnswer(*pid, lane.AnswerRequest{By: me, ByPID: os.Getpid(), Text: *text})
	if err != nil {
		return usagef("%v", err)
	}
	// The command's own gate mirrors the run's: refuse here and take the
	// request back, so the run never handles a request it would only
	// refuse. What gets past both gates is decided where the ticket lives.
	if err := validateEntryForAttach(key, &entry); err != nil {
		_ = q.WithdrawAnswer(*pid)
		return err
	}
	if err := lane.MayAttach(entry.Ticket, me); err != nil {
		_ = q.WithdrawAnswer(*pid)
		return usagef("%v", err)
	}
	fmt.Fprintf(stdout, "answer: pid %d on queue %q: %s\n", *pid, key,
		textsafe.Escape(entry.Ticket.CommandString()))

	answerPath := filepath.Join(q.Dir, entry.File+lane.AnswerExt)
	deadline := time.Now().Add(*wait)
	for {
		b, err := os.ReadFile(answerPath)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintf(stdout, "answer: delivered\n")
				return nil
			}
			return exitWith(ExitState, "cannot read the answer verdict: %v", err)
		}
		var req lane.AnswerRequest
		if jsonErr := json.Unmarshal(b, &req); jsonErr == nil && req.Refused != "" {
			return usagef("answer refused: %s", req.Refused)
		}
		if time.Now().After(deadline) {
			return exitWith(ExitAnswerPending,
				"answer pending: pid %d has not taken the reply after %s: it may not be reporting blocked, or its run is wedged. "+
					"The request stays addressed to the ticket until the run acts on it", *pid, *wait)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
