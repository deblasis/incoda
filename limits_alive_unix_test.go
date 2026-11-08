//go:build !windows

package main

import (
	"errors"
	"syscall"
)

// pidAlive reports whether a process with this pid is still running.
func pidAlive(pid int) bool {
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
