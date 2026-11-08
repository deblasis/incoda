package lane

import (
	"fmt"
	"strings"

	"github.com/deblasis/incoda/internal/textsafe"
)

// MayInteract is the owner gate every pty input path shares (answer and
// the run-side door): fail closed on a missing identity, a ticket with no
// recorded owner, and a mismatch, each named. Viewing - attach and the
// watch capture - is open to everyone; typing at the job is not. Both
// sides are compared trimmed: the run trims what it records, and a
// shell-exported $INCODA_OWNER with stray whitespace must not make a job
// unanswerable by two visually identical names.
func MayInteract(t Ticket, me string) error {
	me = strings.TrimSpace(me)
	if me == "" {
		return fmt.Errorf("answer needs --owner or $INCODA_OWNER: answering is limited to the job's own owner")
	}
	if strings.TrimSpace(t.Owner) == "" {
		return fmt.Errorf("job pid %d on queue %q recorded no owner; answer is refused. Runs that want to be answerable must pass --owner", t.PID, t.Queue)
	}
	if strings.TrimSpace(t.Owner) != me {
		return fmt.Errorf("answer refused: job pid %d on queue %q belongs to owner %q; you are %q. Only the job's owner may type at it",
			t.PID, t.Queue, textsafe.Escape(t.Owner), textsafe.Escape(me))
	}
	return nil
}
