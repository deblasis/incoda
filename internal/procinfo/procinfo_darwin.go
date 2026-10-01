//go:build darwin

package procinfo

import (
	"fmt"

	"golang.org/x/sys/unix"
)

const skipWalk = false

// ParentPID reads e_ppid from sysctl kern.proc.pid.
func ParentPID(pid int) (int, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	if int(kp.Proc.P_pid) != pid {
		return 0, fmt.Errorf("no process %d", pid)
	}
	return int(kp.Eproc.Ppid), nil
}
