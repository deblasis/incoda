//go:build !windows

package child

import (
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

type supervisor struct {
	pgid int
}

// newSupervisor puts the child in its own process group so that incoda can
// signal the entire tree with one kill(-pgid). The trade is that a terminal
// Ctrl+C no longer reaches the child on its own, which is why forward relays it
// explicitly.
func newSupervisor(cmd *exec.Cmd) (*supervisor, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &supervisor{}, nil
}

func (s *supervisor) afterStart(cmd *exec.Cmd) error {
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		// Fall back to the pid: the child is its own group leader by
		// construction, so this is the same number in practice.
		pgid = cmd.Process.Pid
	}
	s.pgid = pgid
	return nil
}

func (s *supervisor) forward(cmd *exec.Cmd, sig os.Signal) {
	sg, ok := sig.(syscall.Signal)
	if !ok {
		sg = syscall.SIGTERM
	}
	if s.pgid > 0 {
		_ = syscall.Kill(-s.pgid, sg)
		return
	}
	if cmd.Process != nil {
		_ = cmd.Process.Signal(sig)
	}
}

// usage reads the direct child's rusage. It is an honest approximation and
// no more: the kernel rolls a grandchild into these numbers only if the
// child waited for it, so a build that leaves compilers running detached is
// under-counted. The Windows Job Object does not have that gap, which is why
// the two platforms document different scopes for the same field.
func (s *supervisor) usage(cmd *exec.Cmd) (peak uint64, havePeak bool, cpu time.Duration, haveCPU bool) {
	ps := cmd.ProcessState
	if ps == nil {
		return 0, false, 0, false
	}
	cpu, haveCPU = ps.UserTime()+ps.SystemTime(), true
	if ru, ok := ps.SysUsage().(*syscall.Rusage); ok && ru != nil && ru.Maxrss > 0 {
		// ru_maxrss is kilobytes on Linux and bytes on macOS; the BSDs
		// followed macOS. Linux is the odd one out, so it gets the scale.
		unit := uint64(1)
		if runtime.GOOS == "linux" {
			unit = 1024
		}
		peak, havePeak = uint64(ru.Maxrss)*unit, true
	}
	return peak, havePeak, cpu, haveCPU
}

func (s *supervisor) killTree() {
	if s.pgid > 0 {
		_ = syscall.Kill(-s.pgid, syscall.SIGKILL)
	}
}

// dispose is a no-op on Unix: there is no job handle whose closure would clean
// up. A tree that survives a hard kill of incoda is a real limitation here and
// is documented as such.
func (s *supervisor) dispose() {}

func forwardedSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
}
