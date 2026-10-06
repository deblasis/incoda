//go:build !darwin && !linux

package procinfo

import "runtime"

const skipWalk = runtime.GOOS == "windows"

// ParentPID is not available here; held entries are then unverifiable.
func ParentPID(int) (int, error) { return 0, ErrUnsupported }
