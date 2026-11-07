//go:build windows

package child

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/deblasis/incoda/internal/joblog"
	"github.com/deblasis/incoda/internal/pty"
	"golang.org/x/sys/windows"
)

// drainGrace caps the whole post-exit drain: however slowly conhost
// delivers, once this much time has passed since the drain began (or since
// the last delivered byte) the read is cut and the cut says so in-band,
// because bytes still in flight die with it. quiesceWindow is the faster
// path: with the job gone and the input end closed, a pipe that stays
// silent this long AND peeks empty (or peeks its write end gone, which is
// empty and final) may be cut without a marker; the peek is what proves
// the pipe empty now, and only the write-end-gone answer proves it final.
// Measured on this build:
// ClosePseudoConsole returns immediately and conhost's exit epilogue lands
// at once, but conhost never exits while the master read end is open, so
// EOF is not what ends the drain here; the quiesce cut is.
const (
	drainGrace    = 10 * time.Second
	quiesceWindow = 3 * time.Second
)

// quietCutOK reports whether the master pipe may be cut with no in-band
// marker. The peek has to prove the pipe empty, and it can give exactly
// two proofs. A clean peek of zero bytes proves the pipe empty NOW, with
// the writer silent for the whole window after teardown; it does not prove
// the pipe final - a conhost holding its write end open through a starved
// tail could still land one after the cut, which is the residue the
// in-loop comment at the cut discloses. A peek that reports the write end
// gone (broken pipe / no data) proves empty AND final, because nothing can
// ever land in a pipe whose writer is dead; in the real drain state (job
// gone, input end and pseudo-console closed) that is the ONLY answer the
// peek gives, so a gate that refuses on any error refuses every clean exit
// and pays the grace cut with a false "tail was lost" marker each time.
// Any other error keeps refusing: the gate fails toward the marker, never
// toward silence.
func quietCutOK(p *pty.Pty) bool {
	n, err := p.BytesAvailable()
	return peekVerdict(n, err)
}

// peekVerdict is quietCutOK's decision as a pure function, so the policy
// itself is table-tested arm by arm. nil with bytes pending refuses (those
// bytes would be dropped unmarked); nil with zero authorizes; the two
// write-end-gone errors authorize (empty AND final); every other error
// refuses, because a peek that failed for an unknown reason has proven
// nothing, and the honest outcome for unknown state is the grace cut WITH
// its marker. The pipe-backed guards cannot cover this last arm: their
// peeks only ever return nil or the write-end-gone errors in reachable
// states, so an over-accepting gate would ship green under them.
func peekVerdict(avail uint32, peekErr error) bool {
	if peekErr == nil {
		return avail == 0
	}
	return errors.Is(peekErr, windows.ERROR_BROKEN_PIPE) || errors.Is(peekErr, windows.ERROR_NO_DATA)
}

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
	defer windows.CloseHandle(sp.Thread)
	if err := windows.AssignProcessToJobObject(job, sp.Proc); err != nil {
		_ = windows.TerminateProcess(sp.Proc, 1)
		_, _ = windows.WaitForSingleObject(sp.Proc, windows.INFINITE)
		return Result{}, fmt.Errorf("assign child to job object: %w", err)
	}
	if err := resumeProcess(sp.PID); err != nil {
		sup.killTree()
		return Result{}, fmt.Errorf("resume child: %w", err)
	}

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
	// event. Every delivered read also publishes progress, which restarts
	// the post-exit drain grace below.
	//
	// The tee is wrapped in a status tap: it hands the bytes on unchanged
	// and only watches them for OSC 7501 support queries. The reply is
	// injected by the replier's own goroutine, off this pipe entirely, so a
	// job that never reads its input cannot stall this loop. input remains
	// the one serialized door to the master input pipe: the supervisor's
	// Ctrl-C path.
	input := &serialWriter{w: p}
	// Replies go in through the injector (see coninj_windows.go): the pipe
	// this serialWriter guards is the Ctrl-C path, and a reply written to
	// it would either be swallowed as VT input or echoed by a cooked-mode
	// reader into the job's own output.
	replies := newReplier(consoleInjector{pid: sp.PID})
	var tee io.Writer = io.Discard
	if opt.PTY.Tee != nil {
		tee = opt.PTY.Tee
	}
	tap := newStatusTap(tee, replies)
	readDone := make(chan struct{})
	progress := make(chan struct{}, 1)
	go func() {
		defer close(readDone)
		buf := make([]byte, 32*1024)
		for {
			n, err := p.Read(buf)
			if n > 0 {
				if _, werr := tap.Write(buf[:n]); werr != nil {
					fmt.Fprintf(os.Stderr, "incoda: writing job output failed (%v); killing the job\n", werr)
					sup.killTree()
					return
				}
				select {
				case progress <- struct{}{}:
				default:
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
				_, _ = input.Write([]byte{0x03})
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

	// The job is gone. Close the input end and the pseudo-console so
	// conhost flushes whatever exit epilogue it has and stops serving the
	// session: ClosePseudoConsole returns at once, and conhost on this
	// build exits only once the master ends go, so the read below is ended
	// by the quiesce cut rather than an EOF that never comes. Quiet for a
	// whole quiesce window with the job dead is only the OCCASION to cut;
	// the quiet cut also needs the peek's proof that the pipe is empty
	// (or that its write end is gone, which is stronger). A stream still
	// moving gets the whole grace window, and the cut says so in-band
	// because those bytes die with it, in every sink at once.
	replies.stop() // no reply may race the input handle closing below
	go p.CloseConsole()
	input.Close()  // the pump stops taking writes; its in-flight one is cancelled below
	p.CloseInput() // the cancel unblocks a reply write parked on an unread input
	grace := time.NewTimer(drainGrace)
	defer grace.Stop()
	quiesce := time.NewTimer(quiesceWindow)
	defer quiesce.Stop()
drain:
	for {
		select {
		case <-readDone:
			break drain
		case <-progress:
			if !grace.Stop() {
				select {
				case <-grace.C:
				default:
				}
			}
			grace.Reset(drainGrace)
			if !quiesce.Stop() {
				select {
				case <-quiesce.C:
				default:
				}
			}
			quiesce.Reset(quiesceWindow)
		case <-grace.C:
			p.CloseReadEnd()
			<-readDone
			if opt.PTY.Tee != nil {
				_, _ = opt.PTY.Tee.Write([]byte(joblog.CutLine))
			}
			break drain
		case <-quiesce.C:
			// Silence with the job gone, and the pipe provably empty:
			// bytes can be sitting in the pipe while the reader is
			// between Read calls, or have arrived the instant this
			// timer fired; cutting there would drop them with no
			// marker, so they keep the drain alive (their delivery
			// restarts both timers, and a stream that never settles is
			// cut by the grace timer WITH the marker, which is the
			// honest outcome for bytes that die mid-flight). What no
			// gate can see is conhost's own unwritten buffers; waiting
			// for conhost to exit first is a deadlock, because it never
			// exits while the master read end is open.
			if !quietCutOK(p) {
				quiesce.Reset(quiesceWindow)
				continue
			}
			p.CloseReadEnd()
			<-readDone
			break drain
		}
	}

	// Accounting before dispose closes the job handle; the numbers go with it.
	res := Result{Code: int(code), Aborted: aborted}
	res.PeakBytes, res.HavePeak, res.CPU, res.HaveCPU = sup.usage(nil)
	return res, nil
}
