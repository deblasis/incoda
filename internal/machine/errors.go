// Package machine owns the machine-level state of layout 2: machine.lock,
// machine.json, the queues fence, and the migration from the queues/
// layout of older releases (spec 2.1, 2.3, 3.1 to 3.3, 3.6).
package machine

import (
	"fmt"
	"strings"
)

// StateError fails closed with exit 122. Msg is the whole message after
// "incoda: "; a message of several lines carries "\nincoda: " between them.
type StateError struct {
	Msg string
	// newer marks the "written by a newer incoda" refusal, which a registry
	// rebuild must not overwrite.
	newer bool
}

func (e *StateError) Error() string { return e.Msg }

// Refusal is an exit-120 refusal (upgrade-blocked:, kind-busy:).
type Refusal struct{ Msg string }

func (e *Refusal) Error() string { return e.Msg }

// Timeout is an exit-121 refusal: the --wait budget ran out before any lane
// was taken (machine-lock-timeout:, upgrade-timeout:).
type Timeout struct{ Msg string }

func (e *Timeout) Error() string { return e.Msg }

func stateErrorf(format string, args ...any) *StateError {
	return &StateError{Msg: "machine-state: " + fmt.Sprintf(format, args...)}
}

// joinLines joins the lines of one message so that every line after the
// first also starts with "incoda: " once the CLI prints the message.
func joinLines(lines []string) string { return strings.Join(lines, "\nincoda: ") }

// upgradeBlocked is the refusal of spec 3.3 M2 and 3.1: an older incoda that
// the migration waits for is an ancestor of this process, so waiting would
// never end.
func upgradeBlocked(pid int, key string) *Refusal {
	return &Refusal{Msg: fmt.Sprintf("upgrade-blocked: an older incoda (pid %d, an ancestor of this process) holds %q; rerun the outer command after it exits", pid, key)}
}
