//go:build !windows

package machine

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// ProbeVersion runs `<path> version` with stdin from /dev/null, in a
// process group of its own, and returns at most 4 KiB of its standard
// output (spec 5.5). After probeTimeout it kills the whole group;
// cmd.WaitDelay bounds the wait for an output pipe that a descendant holds
// open after the probe itself exited, and such a descendant's group is
// killed too.
func ProbeVersion(path string) (string, error) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return "", err
	}
	defer devnull.Close()
	var out cappedBuffer
	cmd := exec.Command(path, "version")
	cmd.Stdin = devnull
	cmd.Stdout = &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = probeWaitDelay
	if err := cmd.Start(); err != nil {
		return "", err
	}
	// The group id is the probe's pid. It stays reserved while the probe
	// is unreaped or any member of its group lives, which is exactly when
	// the signals below are sent.
	pgid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(probeTimeout):
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-done
		return out.String(), errProbeTimeout
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		// The probe exited but something it started still holds its
		// output open: end that group too.
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		err = nil
	}
	return out.String(), err
}
