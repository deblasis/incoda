//go:build linux

package procinfo

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"
)

// List returns every process: every numeric entry of /proc. A process that
// exits between the directory read and its stat read is skipped.
func List() ([]Proc, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []Proc
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		p, err := Lookup(pid)
		if errors.Is(err, ErrNoProcess) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Lookup reads /proc/<pid>/stat. A pid with no process is ErrNoProcess;
// a read that races the process's exit answers ESRCH, which is the same.
func Lookup(pid int) (Proc, error) {
	if pid <= 0 {
		return Proc{}, ErrNoProcess
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return Proc{}, ErrNoProcess
	}
	if err != nil {
		return Proc{}, err
	}
	return parseStat(string(b))
}
