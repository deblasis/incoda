package lane

import (
	"fmt"
	"strings"

	"github.com/deblasis/incoda/internal/textsafe"
)

// MayAttach is the owner gate every pty viewer shares: fail closed on a
// missing identity, a ticket with no recorded owner, and a mismatch, each
// named. Both sides are compared trimmed: the run trims what it records, and
// a shell-exported $INCODA_OWNER with stray whitespace must not make a job
// unwatchable by two visually identical names.
func MayAttach(t Ticket, me string) error {
	me = strings.TrimSpace(me)
	if me == "" {
		return fmt.Errorf("attach needs --owner or $INCODA_OWNER: attach is limited to the job's own owner")
	}
	if strings.TrimSpace(t.Owner) == "" {
		return fmt.Errorf("job pid %d on queue %q recorded no owner; attach is refused. Runs that want to be attachable must pass --owner", t.PID, t.Queue)
	}
	if strings.TrimSpace(t.Owner) != me {
		return fmt.Errorf("attach refused: job pid %d on queue %q belongs to owner %q; you are %q. Only the job's owner may attach",
			t.PID, t.Queue, textsafe.Escape(t.Owner), textsafe.Escape(me))
	}
	return nil
}
