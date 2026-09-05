// Package child runs the supervised command.
//
// The important property is that killing incoda must not leave a build tree
// running. A `zig build` or `dotnet build` that outlives its lane holder keeps
// the machine occupied while the lane says it is free, which is precisely the
// collision the lane exists to prevent. Each platform gets the strongest
// containment it offers: a Job Object on Windows, a process group on Unix.
package child

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"time"
)

// ExitError carries a non-zero child exit status.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return "child exited non-zero" }

// Result is what Run reports about a finished command: its exit code and
// what it cost, as far as the platform can tell.
type Result struct {
	Code int
	// PeakBytes is the peak committed memory of the whole job tree on
	// Windows, where the Job Object accounts for every descendant, and the
	// direct child's maximum resident set on Unix, where rusage only sees
	// what was waited for. HavePeak is false when neither is available.
	PeakBytes uint64
	HavePeak  bool
	// CPU is user plus kernel time, with the same tree-versus-child caveat.
	CPU     time.Duration
	HaveCPU bool
}

// Run starts argv, forwards interrupt signals to it, and returns its exit code
// and resource usage. Signals arriving while the child runs are forwarded; a
// second signal escalates to tearing the whole process tree down. Closing
// abort tears the tree down too: it is how a kill request addressed to the
// lane holder reaches the job it is running. A nil abort is never fired.
func Run(argv []string, stdin *os.File, stdout, stderr *os.File, abort <-chan struct{}) (Result, error) {
	if len(argv) == 0 {
		return Result{}, errors.New("no command given")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = os.Environ()

	sup, err := newSupervisor(cmd)
	if err != nil {
		return Result{}, err
	}
	defer sup.dispose()

	if err := cmd.Start(); err != nil {
		return Result{}, err
	}
	if err := sup.afterStart(cmd); err != nil {
		sup.killTree()
		_ = cmd.Wait()
		return Result{}, err
	}

	sigc := make(chan os.Signal, 4)
	signal.Notify(sigc, forwardedSignals()...)
	done := make(chan struct{})
	var once sync.Once
	go func() {
		escalate := false
		for {
			select {
			case s := <-sigc:
				if escalate {
					sup.killTree()
					continue
				}
				escalate = true
				sup.forward(cmd, s)
			case <-abort:
				sup.killTree()
				abort = nil // a closed channel would spin this loop
			case <-done:
				return
			}
		}
	}()

	waitErr := cmd.Wait()
	once.Do(func() { close(done) })
	signal.Stop(sigc)

	// Read the accounting before dispose closes the job handle; after that
	// the numbers are gone with the job.
	res := Result{}
	res.PeakBytes, res.HavePeak, res.CPU, res.HaveCPU = sup.usage(cmd)

	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			code := ee.ExitCode()
			if code < 0 {
				// Killed by a signal (Unix) or an unmappable status. Use the
				// shell convention so the caller still sees a failure.
				code = 128
			}
			res.Code = code
			return res, nil
		}
		return Result{}, waitErr
	}
	return res, nil
}
