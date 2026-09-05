package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/deblasis/incoda/internal/lane"
)

// cmdConfig shows or changes a queue's standing configuration. With no
// setting flag it prints what the queue holds; with one or more it writes
// them and prints the result. The key is positional so that the common
// shape reads as a sentence: `incoda config wintty-build --slots 2`.
func cmdConfig(args []string, stdout, stderr io.Writer) error {
	explicit := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		explicit, args = args[0], args[1:]
	}
	fs := newFlagSet("config", stderr)
	queue := fs.String("queue", explicit, "queue key (or the first positional argument; defaults to $INCODA_QUEUE)")
	slots := fs.Int("slots", 0, "default --slots for runs that do not pass one (0 means 1)")
	desc := fs.String("description", "", "one line saying what the queue guards, shown by status and watch")
	requireReason := fs.Bool("require-reason", false, "refuse a run that has no --reason")
	closeMsg := fs.String("close", "", "refuse every run with this message, for a retired key that should name its replacements")
	open := fs.Bool("open", false, "clear a --close")
	noColor := fs.Bool("no-color", false, "never emit ANSI color, even on a terminal (the NO_COLOR environment variable does the same)")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: incoda config KEY [--slots N] [--description TEXT] [--require-reason[=false]] [--close MSG | --open]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for config"}
	}
	if *slots < 0 {
		return usagef("--slots must be at least 1, got %d", *slots)
	}
	if *closeMsg != "" && *open {
		return usagef("--close and --open contradict each other")
	}
	key, err := resolveKey(*queue)
	if err != nil {
		return err
	}
	dir, err := stateDir()
	if err != nil {
		return err
	}
	q, err := lane.Open(dir, key)
	if err != nil {
		return exitWith(ExitState, "%v", err)
	}
	defer q.Close()

	cfg, err := q.LoadConfig()
	if err != nil {
		return exitWith(ExitState, "queue %q: %v", key, err)
	}
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
	if changed {
		if err := q.SaveConfig(cfg); err != nil {
			return exitWith(ExitState, "cannot write config for %q: %v", key, err)
		}
		q.Logf("queue=%s event=config pid=%d slots=%d require_reason=%v closed=%q", key, os.Getpid(), cfg.Slots, cfg.RequireReason, cfg.Closed)
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
		fmt.Fprintf(stdout, "  %s %s\n", p.Dim("description:"), cfg.Description)
	}
	fmt.Fprintf(stdout, "  %s %s\n", p.Dim("require reason:"), yesNo(cfg.RequireReason))
	if cfg.Closed == "" {
		fmt.Fprintf(stdout, "  %s no\n", p.Dim("closed:"))
	} else {
		fmt.Fprintf(stdout, "  %s %s\n", p.Dim("closed:"), p.BoldRed(cfg.Closed))
	}
	return nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
