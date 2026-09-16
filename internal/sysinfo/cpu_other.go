//go:build !linux && !darwin && !windows

package sysinfo

import "runtime"

func readCPU() CPU {
	return CPU{Source: runtime.GOOS, Err: "no cpu readout implemented for " + runtime.GOOS}
}
