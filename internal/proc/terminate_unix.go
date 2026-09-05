//go:build !windows

// Package proc is the one place incoda ends another process. It is used by
// `kill --force` when a participant does not acknowledge its kill request:
// an incoda from before kill existed, or one wedged in a syscall.
package proc

import "syscall"

// Terminate sends SIGKILL to pid. Unix cannot carry an exit code through a
// signal, so the caller's shell sees 137 rather than 124; the reason still
// reaches lane.log. The process group the child was put in is not signalled
// here on purpose: a forced kill of an old incoda can orphan its tree, and
// that is documented as a known limit rather than papered over with a guess
// at the group id.
func Terminate(pid int, code int) error {
	_ = code
	return syscall.Kill(pid, syscall.SIGKILL)
}
