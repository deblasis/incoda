//go:build !windows

package proc

import (
	"errors"
	"syscall"
	"time"
)

// Handle names a process for Terminate. Unix has no portable process
// handle, so it is the pid alone and pins nothing; the callers that need a
// pin (the old-holder kill) stop the process first instead.
type Handle struct{ pid int }

// Open checks that pid exists.
func Open(pid int) (*Handle, error) {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return nil, err
	}
	return &Handle{pid: pid}, nil
}

// Terminate sends SIGKILL (see Terminate).
func (p *Handle) Terminate(code int) error { return Terminate(p.pid, code) }

// Wait waits up to d for the pid to disappear.
func (p *Handle) Wait(d time.Duration) error {
	deadline := time.Now().Add(d)
	for syscall.Kill(p.pid, 0) == nil {
		if time.Now().After(deadline) {
			return errors.New("the process did not exit in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

// Close does nothing.
func (p *Handle) Close() error { return nil }
