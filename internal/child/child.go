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
)

// ExitError carries a non-zero child exit status.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return "child exited non-zero" }

// Run starts argv, forwards interrupt signals to it, and returns its exit code.
// Signals arriving while the child runs are forwarded; a second signal escalates
// to tearing the whole process tree down.
func Run(argv []string, stdin *os.File, stdout, stderr *os.File) (int, error) {
	if len(argv) == 0 {
		return 0, errors.New("no command given")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = os.Environ()

	sup, err := newSupervisor(cmd)
	if err != nil {
		return 0, err
	}
	defer sup.dispose()

	if err := cmd.Start(); err != nil {
		return 0, err
	}
	if err := sup.afterStart(cmd); err != nil {
		sup.killTree()
		_ = cmd.Wait()
		return 0, err
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
			case <-done:
				return
			}
		}
	}()

	waitErr := cmd.Wait()
	once.Do(func() { close(done) })
	signal.Stop(sigc)

	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			code := ee.ExitCode()
			if code < 0 {
				// Killed by a signal (Unix) or an unmappable status. Use the
				// shell convention so the caller still sees a failure.
				code = 128
			}
			return code, nil
		}
		return 0, waitErr
	}
	return 0, nil
}
