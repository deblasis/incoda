//go:build linux

package procinfo

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const skipWalk = false

// ParentPID reads field 4 of /proc/<pid>/stat. The command name in field 2
// may contain spaces and parentheses, so fields are counted after the last ')'.
func ParentPID(pid int) (int, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, fmt.Errorf("unparseable /proc/%d/stat", pid)
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0, fmt.Errorf("unparseable /proc/%d/stat", pid)
	}
	return strconv.Atoi(f[1])
}
