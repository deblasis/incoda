package child

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// SpinCommand is the hidden subcommand incoda runs on itself to calibrate the
// CPU counter: it burns CPU for the duration given as its argument.
const SpinCommand = "__cpuspin"

// calibrateWindow bounds how long the calibration waits for the counter to
// move while a process spins; it is generous because the machine may be
// loaded and the spinner has to start first.
const calibrateWindow = 6 * time.Second

var spinSink uint64

// Spin is the body of SpinCommand: args[0] is how long to burn CPU.
func Spin(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: incoda "+SpinCommand+" DURATION")
		return 2
	}
	d, err := time.ParseDuration(args[0])
	if err != nil || d < 0 {
		fmt.Fprintln(os.Stderr, "bad duration")
		return 2
	}
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		for i := 0; i < 1<<16; i++ {
			spinSink += uint64(i) * 2654435761
		}
	}
	return 0
}

// CalibrateCPU proves that the CPU counter the stall limit reads is alive on
// this machine: it starts a process that spins, supervised exactly as a job
// is, and requires the probe's reading to rise. A reading that never moves
// would make every job look idle, so a nil result is the only licence to
// arm --stall; any error means the limit must not be armed.
func CalibrateCPU() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find incoda's own executable: %w", err)
	}
	return calibrate([]string{exe, SpinCommand, "30s"}, calibrateWindow)
}

// calibrate runs argv (a process that burns CPU) under the real supervisor
// and watches the probe for window. It returns nil once the reading rises.
func calibrate(argv []string, window time.Duration) error {
	in, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer out.Close()

	probe := &Probe{}
	abort := make(chan struct{})
	ended := make(chan error, 1)
	go func() {
		_, err := Run(argv, in, out, out, abort, Options{Probe: probe, OwnGroup: true})
		ended <- err
	}()
	stopSpinner := func() {
		close(abort)
		<-ended
	}

	deadline := time.NewTimer(window)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	var first time.Duration
	have := false
	for {
		select {
		case err := <-ended:
			if err != nil {
				return fmt.Errorf("the calibration process did not start: %w", err)
			}
			return errors.New("the calibration process ended before the counter moved")
		case <-deadline.C:
			stopSpinner()
			if !have {
				return errors.New("the CPU counter could not be read")
			}
			return fmt.Errorf("the CPU counter did not rise while a process spun for %s", window)
		case <-tick.C:
			cpu, ok := probe.CPU()
			if !ok {
				continue
			}
			if !have {
				first, have = cpu, true
				continue
			}
			if cpu > first {
				stopSpinner()
				return nil
			}
		}
	}
}
