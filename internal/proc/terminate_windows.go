//go:build windows

// Package proc is the one place incoda ends another process. It is used by
// `kill --force` when a participant does not acknowledge its kill request:
// an incoda from before kill existed, or one wedged in a syscall.
package proc

import (
	"golang.org/x/sys/windows"
)

// Terminate ends pid with the given exit code. Terminating the incoda
// process closes its Job Object handle, and kill-on-close takes the job tree
// down with it; the kernel then drops the ticket lock. The exit code is
// carried through TerminateProcess, so even a forced kill tells the
// caller's shell that it was the lane and not the build that ended it.
func Terminate(pid int, code int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, uint32(code))
}
