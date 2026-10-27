package cli

import (
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/textsafe"
)

// cmdPool is incoda pool. It has one subcommand, add, which registers a new
// machine-wide pool without touching any pool or lane that exists. A
// subcommand rather than a config flag: config KEY works on a key, and a pool
// has no registry entry until this runs, so there is no key to configure yet.
func cmdPool(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "add" {
		return usagef("usage: incoda pool add NAME [--slots N] [--description TEXT] [--wait DUR]")
	}
	return cmdPoolAdd(args[1:], stdout, stderr)
}

func cmdPoolAdd(args []string, stdout, stderr io.Writer) error {
	start := time.Now()
	name := ""
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		name, args = args[0], args[1:]
	}
	fs := newFlagSet("pool add", stderr)
	slots := fs.Int("slots", 0, "the pool's slot count (0 means the default of 1)")
	desc := fs.String("description", "", "one line saying what the pool guards, shown by status and watch")
	wait := &waitValue{d: time.Minute}
	fs.Var(wait, "wait", "how long to wait for machine.lock and a state upgrade: a Go duration (1m) or bare seconds; negative waits forever")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: incoda pool add NAME [--slots N] [--description TEXT] [--wait DUR]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: "bad flags for pool add"}
	}
	if name == "" || fs.NArg() != 0 {
		return usagef("usage: incoda pool add NAME [--slots N] [--description TEXT] [--wait DUR]")
	}
	if *slots < 0 {
		return usagef("--slots must be at least 1, got %d", *slots)
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if err := checkTexts(given, map[string]string{"description": *desc}); err != nil {
		return err
	}

	chain := procinfo.ParentChain()
	dir, _, err := mutatingState(start, wait.d, 200*time.Millisecond, chain, stderr)
	if err != nil {
		return err
	}
	v, _, _ := versionInfo()
	res, err := machine.AddPool(dir, name, machine.PoolSpec{Slots: *slots, Description: *desc}, "pool-add", machine.Options{
		Start: start, Wait: wait.d, Poll: 200 * time.Millisecond, Chain: chain, By: "incoda " + v, Stderr: stderr,
	})
	if err != nil {
		return machineExit(err)
	}
	n := res.Config.Slots
	if n < 1 {
		n = 1
	}
	fmt.Fprintf(stdout, "pool add: %s: registered (slots %d)\n", name, n)
	fmt.Fprintf(stdout, "pool add: machine.json generation %d -> %d, pools now: %s\n",
		res.OldGeneration, res.Registry.Generation, textsafe.Escape(machine.SetText(res.Registry.Pools)))
	fmt.Fprintf(stdout, "pool add: no other pool or lane was changed\n")
	return nil
}
