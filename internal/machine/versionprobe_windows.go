//go:build windows

package machine

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// ProbeVersion runs `<path> version` with stdin from NUL, in a process
// group of its own, and returns at most 4 KiB of its standard output (spec
// 5.5). After probeTimeout it terminates the probe; cmd.WaitDelay bounds
// the wait for an output pipe a descendant holds open.
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
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	cmd.WaitDelay = probeWaitDelay
	if err := cmd.Start(); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(probeTimeout):
		_ = cmd.Process.Kill()
		<-done
		return out.String(), errProbeTimeout
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	return out.String(), err
}
