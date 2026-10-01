package cli

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/procinfo"
)

// readState resolves the state directory for a command that only reads
// lanes or addresses one (status, watch, queues, kill, force-release). It
// creates nothing, never migrates, never re-fences and never takes
// machine.lock; a migrated layout with a broken or lost registry fails
// closed with exit 122 (spec 2.1, 3.6).
func readState() (string, machine.View, error) {
	d, err := lane.StateDir()
	if err != nil {
		return "", machine.View{}, exitWith(ExitState, "cannot resolve state directory: %v", err)
	}
	v, err := machine.Inspect(d)
	if err != nil {
		return "", machine.View{}, machineExit(err)
	}
	return d, v, nil
}

// mutatingState resolves and creates the state directory for a command
// that takes a ticket or writes config, then migrates it or re-places a
// missing fence (machine.Ensure), before the caller holds any ticket.
// start and wait are the command's --wait budget, which this spends first.
func mutatingState(start time.Time, wait, poll time.Duration, chain procinfo.Chain, stderr io.Writer) (string, error) {
	d, err := stateDir()
	if err != nil {
		return "", err
	}
	v, _, _ := versionInfo()
	_, err = machine.Ensure(d, machine.Options{
		Start: start, Wait: wait, Poll: poll, Chain: chain,
		By: "incoda " + v, Stderr: stderr, Path: startGetenv("PATH"),
	})
	if err != nil {
		return "", machineExit(err)
	}
	return d, nil
}

// machineExit maps the machine package's errors to exit codes: 122 for
// machine-state, 120 for refusals, 121 for timeouts.
func machineExit(err error) error {
	var se *machine.StateError
	var rf *machine.Refusal
	var to *machine.Timeout
	var ec *exitCode
	switch {
	case errors.As(err, &se):
		return exitWith(ExitState, "%s", se.Msg)
	case errors.As(err, &rf):
		return exitWith(ExitUsage, "%s", rf.Msg)
	case errors.As(err, &to):
		return exitWith(ExitTimeout, "%s", to.Msg)
	case errors.As(err, &ec):
		return err
	}
	return exitWith(ExitState, "%v", err)
}

// printBanner prints the read-only banner of spec 3.2 on w.
func printBanner(w io.Writer, banner string) {
	if banner != "" {
		fmt.Fprintf(w, "incoda: %s\n", banner)
	}
}
