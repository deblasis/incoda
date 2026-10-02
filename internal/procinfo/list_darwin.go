//go:build darwin

package procinfo

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// p_stat values from <sys/proc.h>; golang.org/x/sys/unix has no names for
// them.
const (
	sstop = 4 // SSTOP
	szomb = 5 // SZOMB
)

func fromKinfo(k *unix.KinfoProc) Proc {
	st := byte('R')
	switch k.Proc.P_stat {
	case sstop:
		st = 'T'
	case szomb:
		st = 'Z'
	}
	return Proc{
		PID:   int(k.Proc.P_pid),
		PPID:  int(k.Eproc.Ppid),
		PGID:  int(k.Eproc.Pgid),
		Start: uint64(k.Proc.P_starttime.Sec)*1_000_000 + uint64(k.Proc.P_starttime.Usec),
		State: st,
	}
}

// List returns every process: sysctl(CTL_KERN, KERN_PROC, KERN_PROC_ALL).
func List() ([]Proc, error) {
	ks, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, fmt.Errorf("sysctl kern.proc.all: %w", err)
	}
	out := make([]Proc, 0, len(ks))
	for i := range ks {
		out = append(out, fromKinfo(&ks[i]))
	}
	return out, nil
}

// Lookup reads one process: sysctl(CTL_KERN, KERN_PROC, KERN_PROC_PID).
// A pid with no process is ErrNoProcess.
func Lookup(pid int) (Proc, error) {
	if pid <= 0 {
		return Proc{}, ErrNoProcess
	}
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err == nil && int(k.Proc.P_pid) == pid {
		return fromKinfo(k), nil
	}
	// sysctl answers EIO (an empty reply) for a pid with no process.
	if kerr := unix.Kill(pid, 0); errors.Is(kerr, unix.ESRCH) {
		return Proc{}, ErrNoProcess
	}
	if err == nil {
		err = fmt.Errorf("sysctl kern.proc.pid.%d answered for pid %d", pid, k.Proc.P_pid)
	}
	return Proc{}, err
}
