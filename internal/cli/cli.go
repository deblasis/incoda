// Package cli implements incoda's command line.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/lane"
)

// Exit codes. The child's own status is passed through untouched, so lane-level
// failures use a band that a build tool is very unlikely to produce. They are
// documented here, in `incoda help`, and in the README, and they are part of the
// interface: scripts may rely on them.
const (
	ExitOK          = 0
	ExitUsage       = 120 // bad arguments, bad key, or a refused force-release
	ExitTimeout     = 121 // --wait elapsed without a free slot
	ExitState       = 122 // state directory or OS lock unusable
	ExitSpawn       = 123 // the lane was acquired but the command could not start
	ExitKilled      = 124 // the run was killed through the lane (`incoda kill`)
	ExitKillPending = 125 // kill: the participant did not acknowledge in time
	ExitInterrupt   = 130 // incoda itself was interrupted while queueing
)

type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

const rootUsage = `incoda - keyed queueing for heavy processes (builds, GUI/UI test runs)

usage:
  incoda run --queue KEY[,KEY...] [--pool P,P] [--slots N] [--exclusive] [--max-cpu PCT] [--idle-for DUR] [--wait DUR] [--reason TEXT] [--owner WHO] [--] <cmd...>
  incoda status [--queue KEY] [--all] [--json]
  incoda watch [--queue KEY] [--interval 2s] [--once | --plain]
  incoda queues
  incoda config KEY [--slots N] [--description TEXT] [--require-reason] [--close MSG | --open] [--pool P,P [--replace] | --add-pool P,P | --remove-pool P,P | --unlink] [--quiet-machine[=false]] [--wait DUR]
  incoda kill --queue KEY --pid N --reason TEXT [--wait 5s] [--force]
  incoda force-release --queue KEY [--live]
  incoda doctor [--rebuild-registry POOL,POOL... [--wait DUR]]
  incoda version

The queue key comes from --queue or the INCODA_QUEUE environment variable.
There is no default key: an unkeyed run is refused rather than silently
sharing a lane with unrelated work. A comma-separated list holds every key
named, taken in sorted order. A run gives its child INCODA_HELD, the
KEY=TICKET entries of the lanes it holds; a nested run on a key listed there
passes through instead of queueing behind its own parent, once it has checked
that the ticket is alive and held by its ancestor. INCODA_OWNER is the
default for --owner.

State is machine-local and per-user, and is NEVER derived from the working
directory. Every caller of a key contends for the same lane no matter which
folder, worktree or checkout it runs from. Resolution order for the state
directory: $INCODA_DIR, then %LOCALAPPDATA%\incoda (Windows),
~/Library/Application Support/incoda (macOS),
$XDG_STATE_HOME/incoda or ~/.local/state/incoda (Linux).

Lanes live in <state>/lanes/. The first run or config on a state directory
used by an older incoda upgrades it once: it waits for that incoda's runs
to finish, then makes <state>/queues a file that stops older binaries
(exit 122) and registers the machine-wide pools builds, computer-use, tests
and vm in <state>/machine.json. status, watch and queues never upgrade.

exit codes:
  <child>  run passes the command's own exit status through unchanged
  120      usage error (bad flags, missing/invalid queue key, refused force-release),
           or a refusal such as upgrade-blocked
  121      --wait elapsed while still queued, waiting for machine.lock,
           waiting for older incoda runs before the state upgrade, or
           waiting for idle
  122      state directory, machine.json or OS file locking unusable
  123      lane acquired but the command could not be started
  124      the run was killed through the lane (incoda kill); stderr says by whom and why
  125      kill: the participant did not acknowledge in time (rerun with --force)
  130      incoda was interrupted while queueing

run 'incoda <command> -h' for per-command flags.
`

// Main is the process entry point. It returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, rootUsage)
		return ExitUsage
	}
	cmd, rest := args[0], args[1:]

	var err error
	switch cmd {
	case "run":
		err = cmdRun(rest, stdout, stderr)
	case "status":
		err = cmdStatus(rest, stdout, stderr)
	case "watch":
		err = cmdWatch(rest, stdout, stderr)
	case "queues":
		err = cmdQueues(rest, stdout, stderr)
	case "config":
		err = cmdConfig(rest, stdout, stderr)
	case "kill":
		err = cmdKill(rest, stdout, stderr)
	case "force-release":
		err = cmdForceRelease(rest, stdout, stderr)
	case "doctor":
		err = cmdDoctor(rest, stdout, stderr)
	case "version", "--version", "-V":
		v, c, d := versionInfo()
		fmt.Fprintf(stdout, "incoda %s\ncommit: %s\nbuilt:  %s\n", v, c, d)
		return ExitOK
	case "help", "-h", "--help":
		fmt.Fprint(stdout, rootUsage)
		return ExitOK
	default:
		fmt.Fprintf(stderr, "incoda: unknown command %q\n\n", cmd)
		fmt.Fprint(stderr, rootUsage)
		return ExitUsage
	}

	if err == nil {
		return ExitOK
	}
	var ec *exitCode
	if errors.As(err, &ec) {
		if ec.msg != "" {
			fmt.Fprintf(stderr, "incoda: %s\n", ec.msg)
		}
		return ec.code
	}
	var ue *usageError
	if errors.As(err, &ue) {
		fmt.Fprintf(stderr, "incoda: %s\n", ue.msg)
		return ExitUsage
	}
	fmt.Fprintf(stderr, "incoda: %v\n", err)
	return ExitState
}

type exitCode struct {
	code int
	msg  string
}

func (e *exitCode) Error() string {
	if e.msg == "" {
		return fmt.Sprintf("exit %d", e.code)
	}
	return e.msg
}

func exitWith(code int, format string, args ...any) error {
	return &exitCode{code: code, msg: fmt.Sprintf(format, args...)}
}

// resolveKey applies --queue, then INCODA_QUEUE. It never falls back to a
// shared default: two unrelated projects silently sharing one lane would be a
// worse failure than an error message.
func resolveKey(explicit string) (string, error) {
	key := strings.TrimSpace(explicit)
	src := "--queue"
	if key == "" {
		key = strings.TrimSpace(os.Getenv("INCODA_QUEUE"))
		src = "INCODA_QUEUE"
	}
	if key == "" {
		return "", usagef("no queue key: pass --queue KEY or set INCODA_QUEUE. There is no default queue, because sharing one by accident is exactly the collision this tool prevents")
	}
	if err := lane.ValidateKey(key); err != nil {
		return "", usagef("invalid queue key from %s: %v", src, err)
	}
	return key, nil
}

// stateDir resolves the state directory and creates it. It never creates
// <state>/queues: that path belongs to older releases, and on layout 2 it is
// the fence file (internal/machine).
func stateDir() (string, error) {
	d, err := lane.StateDir()
	if err != nil {
		return "", exitWith(ExitState, "cannot resolve state directory: %v", err)
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", exitWith(ExitState, "cannot create state directory %s: %v", d, err)
	}
	return d, nil
}

// waitValue parses --wait. It accepts a Go duration ("30m", "90s") and also a
// bare integer read as seconds, because build-lane took `--wait 1800` and that
// habit should keep working.
type waitValue struct {
	d   time.Duration
	set bool
	// raw is the value as given, recorded on tickets (spec 2.7) and
	// repeated in printed fix lines.
	raw string
}

func (w *waitValue) String() string {
	if w == nil {
		return ""
	}
	return w.d.String()
}

func (w *waitValue) Set(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return errors.New("empty duration")
	}
	w.raw = s
	if n, err := strconv.Atoi(s); err == nil {
		if n < 0 {
			w.d, w.set = -1, true
			return nil
		}
		w.d, w.set = time.Duration(n)*time.Second, true
		return nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%q is neither a Go duration (30m, 90s) nor a whole number of seconds", s)
	}
	w.d, w.set = d, true
	return nil
}

// ParseWait exposes the --wait grammar for tests.
func ParseWait(s string) (time.Duration, error) {
	var w waitValue
	if err := w.Set(s); err != nil {
		return 0, err
	}
	return w.d, nil
}

func newFlagSet(name string, out io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("incoda "+name, flag.ContinueOnError)
	fs.SetOutput(out)
	return fs
}
