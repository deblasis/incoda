//go:build windows

package child

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type supervisor struct {
	job windows.Handle
}

// newSupervisor creates a Job Object with KILL_ON_JOB_CLOSE. When incoda exits
// for any reason its handle closes, and the kernel terminates every process
// still in the job. That is what keeps a build tree from outliving its lane
// holder.
func newSupervisor(cmd *exec.Cmd) (*supervisor, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("configure job object: %w", err)
	}

	// The child is created suspended so that it can be put in the job before it
	// executes a single instruction. Assigning after the process is already
	// running leaves a window in which it could spawn a grandchild outside the
	// job, and that grandchild is exactly the orphaned compiler we are trying to
	// prevent.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	return &supervisor{job: job}, nil
}

func (s *supervisor) afterStart(cmd *exec.Cmd) error {
	pid := uint32(cmd.Process.Pid)
	h, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_INFORMATION,
		false, pid)
	if err != nil {
		return fmt.Errorf("open child process: %w", err)
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(s.job, h); err != nil {
		return fmt.Errorf("assign child to job object: %w", err)
	}
	if err := resumeProcess(pid); err != nil {
		return fmt.Errorf("resume child: %w", err)
	}
	return nil
}

// resumeProcess resumes every thread of pid. A freshly created suspended process
// has exactly one, but resuming all of them is both correct and cheap.
func resumeProcess(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snap)

	var te windows.ThreadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	if err := windows.Thread32First(snap, &te); err != nil {
		return err
	}
	resumed := 0
	for {
		if te.OwnerProcessID == pid {
			th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
			if err == nil {
				_, _ = windows.ResumeThread(th)
				windows.CloseHandle(th)
				resumed++
			}
		}
		if err := windows.Thread32Next(snap, &te); err != nil {
			break
		}
	}
	if resumed == 0 {
		return fmt.Errorf("no threads found for pid %d", pid)
	}
	return nil
}

func (s *supervisor) killTree() {
	if s.job != 0 {
		_ = windows.TerminateJobObject(s.job, 1)
	}
}

func (s *supervisor) dispose() {
	if s.job != 0 {
		// Closing the last handle terminates whatever is left in the job.
		windows.CloseHandle(s.job)
		s.job = 0
	}
}

// forward handles a console interrupt.
//
// Windows has no per-process signal to relay. A console Ctrl+C is delivered by
// the OS to every process attached to the console, and the child shares ours
// (we deliberately do not pass CREATE_NEW_PROCESS_GROUP), so the child has
// already been told. All incoda does here is let the child wind down; a second
// interrupt tears the job down.
func (s *supervisor) forward(cmd *exec.Cmd, sig os.Signal) {}

func forwardedSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
