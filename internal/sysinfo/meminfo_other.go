//go:build !windows && !linux && !darwin

package sysinfo

import "runtime"

func readMemory() Memory {
	return Memory{Source: runtime.GOOS, Err: "no memory readout implemented for " + runtime.GOOS}
}
