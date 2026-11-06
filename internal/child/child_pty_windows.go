//go:build windows

package child

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/deblasis/incoda/internal/pty"
	"golang.org/x/sys/windows"
)

// runPTY is Run with the pipes swapped for a pseudo-console. It keeps every
// property the pipe path has: the child goes into a Job Object with
// KILL_ON_JOB_CLOSE before its first instruction, an abort (a kill request
// through the lane) tears the tree down, a second interrupt escalates, and
// usage numbers come from the job. On top of that it owns the pty lifetime:
// the pseudo-console dies with this process (closing the HPCON at process
// exit is what ends conhost; no adoption into the job is needed, and a
// pid-reuse race while adopting could put an unrelated process in a
// kill-on-close job), and the master is drained until EOF (or cut loose
// after a grace period) so the tee never loses a tail.
func runPTY(argv []string, abort <-chan struct{}, opt Options) (Result, error) {
	p, err := pty.New(opt.PTY.Cols, opt.PTY.Rows)
	if err != nil {
		return Result{}, err
	}
	defer p.Close()

	job, err := newJobObject()
	if err != nil {
		return Result{}, err
	}
	sup := &supervisor{job: job}
	defer sup.dispose()

	// The pseudo-console attribute and CREATE_SUSPENDED together mean the
	// child starts attached to the pty but runs nothing until it is in the
	// job, same as the pipe path.
	sp, err := p.Spawn(argv, opt.Env, "", windows.CREATE_SUSPENDED)
	if err != nil {
		return Result{}, err
	}
	defer windows.CloseHandle(sp.Proc)
	if err := windows.AssignProcessToJobObject(job, sp.Proc); err != nil {
		_ = windows.TerminateProcess(sp.Proc, 1)
		_, _ = windows.WaitForSingleObject(sp.Proc, windows.INFINITE)
		return Result{}, fmt.Errorf("assign child to job object: %w", err)
	}
	if err := resumeProcess(sp.PID); err != nil {
		sup.killTree()
		return Result{}, fmt.Errorf("resume child: %w", err)
	}
	windows.CloseHandle(sp.Thread)

	// Waiter: reports the exit code and releases the pseudo-console. The
	// CloseConsole call runs in its own goroutine because a conhost wedged
	// mid-teardown must not wedge the run's exit path with it.
	exited := make(chan struct{})
	var code uint32
	go func() {
		defer close(exited)
		ev, err := windows.WaitForSingleObject(sp.Proc, windows.INFINITE)
		if err != nil || ev != windows.WAIT_OBJECT_0 {
			code = 1
			return
		}
		if err := windows.GetExitCodeProcess(sp.Proc, &code); err != nil {
			code = 1
		}
	}()
	go func() {
		<-exited
		p.CloseConsole()
	}()

	// Reader: everything the job writes, master to tee, verbatim. A tee
	// error tears the job down: on the pipe path a consumer that stopped
	// reading kills the child through the broken pipe, and this is the same
	// event.
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 32*1024)
		for {
			n, err := p.Read(buf)
			if n > 0 && opt.PTY.Tee != nil {
				if _, werr := opt.PTY.Tee.Write(buf[:n]); werr != nil {
					sup.killTree()
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// Supervisor, same shape as Run's: first interrupt is forwarded (a
	// Ctrl-C byte on the master, which conhost turns into a console
	// interrupt for the job), a second one or an abort tears the tree down.
	sigc := make(chan os.Signal, 4)
	signal.Notify(sigc, forwardedSignals()...)
	supDone := make(chan struct{})
	aborted := false
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		escalate := false
		for {
			select {
			case <-sigc:
				if escalate {
					sup.killTree()
					continue
				}
				escalate = true
				_, _ = p.Write([]byte{0x03})
			case <-abort:
				aborted = true
				sup.killTree()
				abort = nil // a closed channel would spin this loop
			case <-supDone:
				return
			}
		}
	}()

	<-exited
	close(supDone)
	signal.Stop(sigc)
	// Join the supervisor goroutine before touching the job (same reason as
	// Run: a killTree racing dispose would read a closing handle) and before
	// reading aborted.
	wg.Wait()

	// The waiter's CloseConsole ends conhost, which EOFs the master and
	// finishes the tee. If conhost never gets there, cut the reader loose
	// rather than hang the run on a teardown that will not finish.
	select {
	case <-readDone:
	case <-time.After(10 * time.Second):
		p.CloseReadEnd()
		<-readDone
	}

	// Accounting before dispose closes the job handle; the numbers go with it.
	res := Result{Code: int(code), Aborted: aborted}
	res.PeakBytes, res.HavePeak, res.CPU, res.HaveCPU = sup.usage(nil)
	return res, nil
}
