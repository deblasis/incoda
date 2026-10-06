//go:build windows

package proc

import (
	"errors"
	"time"

	"golang.org/x/sys/windows"
)

// Handle is an open process handle. While it is open the process object
// cannot go away, so its pid cannot name another process: a check made
// after Open and an action taken through the handle address the same
// process.
type Handle struct{ h windows.Handle }

// Open opens pid for termination and for waiting on its exit.
func Open(pid int) (*Handle, error) {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return nil, err
	}
	return &Handle{h: h}, nil
}

// Terminate ends the process with code (see Terminate).
func (p *Handle) Terminate(code int) error {
	return windows.TerminateProcess(p.h, uint32(code))
}

// Wait waits up to d for the process to exit.
func (p *Handle) Wait(d time.Duration) error {
	ev, err := windows.WaitForSingleObject(p.h, uint32(d/time.Millisecond))
	if err != nil {
		return err
	}
	if ev != windows.WAIT_OBJECT_0 {
		return errors.New("the process did not exit in time")
	}
	return nil
}

// Close closes the handle.
func (p *Handle) Close() error { return windows.CloseHandle(p.h) }
