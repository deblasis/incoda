package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/textsafe"
)

// cmdConfig shows or changes a queue's standing configuration. With no
// setting flag it prints what the queue holds; with one or more it writes
// them and prints the result. The key is positional so that the common
// shape reads as a sentence: `incoda config wintty-build --slots 2`.
func cmdConfig(args []string, stdout, stderr io.Writer) error {
	start := time.Now()
	explicit := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		explicit, args = args[0], args[1:]
	}
	fs := newFlagSet("config", stderr)
	queue := fs.String("queue", explicit, "queue key (or the first positional argument; defaults to $INCODA_QUEUE)")
	slots := fs.Int("slots", 0, "the queue's slot count; runs that do not pass --slots take it, runs that disagree with it are refused (0 means 1)")
	desc := fs.String("description", "", "one line saying what the queue guards, shown by status and watch")
	requireReason := fs.Bool("require-reason", false, "refuse a run that has no --reason")
	closeMsg := fs.String("close", "", "refuse every run with this message, for a retired key that should name its replacements")
	open := fs.Bool("open", false, "clear a --close")
	pool := &poolsValue{}
	fs.Var(pool, "pool", "link the queue to these pools (comma-separated); on a linked queue only with --replace, which is the user's call")
	fs.Var(pool, "pools", "alias of --pool")
	replace := fs.Bool("replace", false, "with --pool: replace an existing link (the user's call)")
	addPool := &poolsValue{}
	fs.Var(addPool, "add-pool", "add these pools to the link (on an unlinked queue: the same as --pool)")
	removePool := &poolsValue{}
	fs.Var(removePool, "remove-pool", "remove these pools from the link (never the last one: use --unlink)")
	unlink := fs.Bool("unlink", false, "remove the link; runs on the queue are then refused until it is linked again")
	quietMachine := fs.Bool("quiet-machine", false, "every run on this queue takes quiet-machine: an exclusive ticket on every pool (--quiet-machine=false clears it)")
	noColor := fs.Bool("no-color", false, "never emit ANSI color, even on a terminal (the NO_COLOR environment variable does the same)")
	wait := &waitValue{d: time.Minute}
	fs.Var(wait, "wait", "how long to wait for machine.lock and a state upgrade: a Go duration (1m) or bare seconds; negative waits forever")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: incoda config KEY [--slots N] [--description TEXT] [--require-reason[=false]] [--close MSG | --open]\n"+
			"                   [--pool P,P [--replace] | --add-pool P,P | --remove-pool P,P | --unlink] [--quiet-machine[=false]] [--wait DUR]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for config"}
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if *slots < 0 {
		return usagef("--slots must be at least 1, got %d", *slots)
	}
	if *closeMsg != "" && *open {
		return usagef("--close and --open contradict each other")
	}
	edit := linkEdit{replace: *replace, unlink: *unlink}
	if pool.set {
		edit.set = pool.keys
	}
	if addPool.set {
		edit.add = addPool.keys
	}
	if removePool.set {
		edit.remove = removePool.keys
	}
	if given["quiet-machine"] {
		edit.quiet = quietMachine
	}
	if err := edit.check(); err != nil {
		return err
	}
	if err := checkTexts(given, map[string]string{"description": *desc, "close": *closeMsg}); err != nil {
		return err
	}
	key, err := resolveKey(*queue)
	if err != nil {
		return err
	}
	chain := procinfo.ParentChain()
	dir, reg, err := mutatingState(start, wait.d, 200*time.Millisecond, chain, stderr)
	if err != nil {
		return err
	}

	apply := func(cfg *lane.Config) bool {
		changed := false
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "slots":
				cfg.Slots = *slots
			case "description":
				cfg.Description = *desc
			case "require-reason":
				cfg.RequireReason = *requireReason
			case "close":
				cfg.Closed = *closeMsg
			case "open":
				cfg.Closed = ""
			default:
				return
			}
			changed = true
		})
		return changed
	}
	logConfig := func(cfg lane.Config) {
		lane.AppendLog(lane.LaneDir(dir, key), "queue=%s event=config pid=%d slots=%d require_reason=%v closed=%s", key, os.Getpid(), cfg.Slots, cfg.RequireReason, textsafe.LogValue(cfg.Closed))
	}
	var cfg lane.Config
	switch {
	case edit.any():
		// A link write (spec 4.3, 4.4): machine.lock, then the lane's
		// registry lock, one load-modify-store, the pools checked against
		// machine.json under the lock. Other fields given in the same
		// command are written in the same step.
		res, err := machine.WriteLink(dir, key, "config", machine.Options{
			Start: start, Wait: wait.d, Poll: 200 * time.Millisecond, Chain: chain, Stderr: stderr,
		}, func(reg *machine.Registry, c *lane.Config) error {
			if bad := reg.NotPools(edit.named()); len(bad) > 0 {
				return machine.NotAPool(reg, bad)
			}
			pools, quiet, err := edit.apply(key, *c)
			if err != nil {
				return err
			}
			other := apply(c)
			if machine.SameSet(pools, c.Pools) && quiet == c.QuietMachine && !other {
				return lane.ErrNoChange
			}
			c.Pools, c.QuietMachine = pools, quiet
			return nil
		})
		if err != nil {
			return machineExit(err)
		}
		reg, cfg = res.Registry, res.New
		if res.Changed && apply(&lane.Config{}) {
			logConfig(cfg)
		}
		switch {
		case !machine.SameSet(res.Old.Pools, res.New.Pools):
			fmt.Fprintf(stdout, "link: %s -> %s\n", machine.SetText(res.Old.Pools), machine.SetText(res.New.Pools))
		case edit.touchesPools() && len(res.New.Pools) == 0:
			fmt.Fprintf(stderr, "incoda: already unlinked: %s\n", key)
		case edit.touchesPools():
			fmt.Fprintf(stderr, "incoda: already linked: %s -> %s\n", key, machine.SetText(res.New.Pools))
		}
	case apply(&lane.Config{}):
		q, err := lane.Open(dir, key)
		if err != nil {
			return exitWith(ExitState, "%v", err)
		}
		q.SetBudget(start, wait.d)
		cfg, err = q.UpdateConfig(func(c *lane.Config) error { apply(c); return nil })
		q.Close()
		if err == nil {
			logConfig(cfg)
		}
		if err := configError(key, err); err != nil {
			return err
		}
	default:
		cfg, err = lane.ReadConfig(lane.LaneDir(dir, key))
		if err := configError(key, err); err != nil {
			return err
		}
	}

	p := paletteFor(stdout, *noColor)
	fmt.Fprintf(stdout, "queue %q\n", key)
	if cfg.Slots < 1 {
		fmt.Fprintf(stdout, "  %s 1 %s\n", p.Dim("slots:"), p.Dim("(default)"))
	} else {
		fmt.Fprintf(stdout, "  %s %d\n", p.Dim("slots:"), cfg.Slots)
	}
	if cfg.Description == "" {
		fmt.Fprintf(stdout, "  %s %s\n", p.Dim("description:"), p.Dim("(none)"))
	} else {
		fmt.Fprintf(stdout, "  %s %s\n", p.Dim("description:"), textsafe.Escape(cfg.Description))
	}
	fmt.Fprintf(stdout, "  %s %s\n", p.Dim("require reason:"), yesNo(cfg.RequireReason))
	if cfg.Closed == "" {
		fmt.Fprintf(stdout, "  %s no\n", p.Dim("closed:"))
	} else {
		fmt.Fprintf(stdout, "  %s %s\n", p.Dim("closed:"), p.BoldRed(textsafe.Escape(cfg.Closed)))
	}
	if reg.IsPool(key) {
		fmt.Fprintf(stdout, "  %s pool\n", p.Dim("kind:"))
		return nil
	}
	fmt.Fprintf(stdout, "  %s project\n", p.Dim("kind:"))
	if len(cfg.Pools) == 0 {
		fmt.Fprintf(stdout, "  %s %s\n", p.Dim("pools:"), p.Yellow("(none: unlinked, runs are refused until it is linked)"))
	} else {
		fmt.Fprintf(stdout, "  %s %s\n", p.Dim("pools:"), textsafe.Escape(machine.SetText(cfg.Pools)))
	}
	fmt.Fprintf(stdout, "  %s %s\n", p.Dim("quiet machine:"), yesNo(cfg.QuietMachine))
	return nil
}

// configError maps a config read or write error to its exit: 122, with the
// machine-state prefix for a config written by a newer incoda.
func configError(key string, err error) error {
	var ns *lane.NewerSchemaError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ns):
		return exitWith(ExitState, "machine-state: %v", err)
	}
	return exitWith(ExitState, "queue %q: %v", key, err)
}

// textFields are the free-text flags a setup command writes into a config,
// with the field name a bad-text refusal names (spec 4.6).
var textFields = []struct{ flag, field string }{
	{"description", "description"},
	{"close", "closed"},
}

// checkTexts refuses, before anything is written, a text flag the caller
// gave whose value carries control, bidi or invalid UTF-8 characters or is
// longer than 200 characters.
func checkTexts(given map[string]bool, values map[string]string) error {
	for _, f := range textFields {
		if !given[f.flag] {
			continue
		}
		if err := textsafe.CheckWrite(f.field, values[f.flag]); err != nil {
			return usagef("%v", err)
		}
	}
	return nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
