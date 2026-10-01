//go:build windows

package procinfo

import (
	"errors"

	"golang.org/x/sys/windows"
)

// stillActive is STILL_ACTIVE, the exit code GetExitCodeProcess reports for
// a running process; golang.org/x/sys/windows has no name for it.
const stillActive = 259

// Alive reports whether a process with pid is running. Access denied means
// it exists but belongs to someone else, which still counts.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return true
	}
	return code == stillActive
}
