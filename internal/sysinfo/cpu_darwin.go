//go:build darwin

package sysinfo

func readCPU() CPU {
	return sampleCPU("mach HOST_CPU_LOAD_INFO", darwinCPUTotals)
}
