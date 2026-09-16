//go:build windows

package sysinfo

import "golang.org/x/sys/windows"

func readCPU() CPU {
	return sampleCPU("GetSystemTimes", systemTimes)
}

func systemTimes() (cpuTotals, bool) {
	var idle, kernel, user windows.Filetime
	if err := windows.GetSystemTimes(&idle, &kernel, &user); err != nil {
		return cpuTotals{}, false
	}
	idleTicks := filetimeToUint64(&idle)
	kernelTicks := filetimeToUint64(&kernel)
	userTicks := filetimeToUint64(&user)
	return cpuTotals{
		total: kernelTicks + userTicks,
		idle:  idleTicks,
	}, true
}

func filetimeToUint64(ft *windows.Filetime) uint64 {
	return uint64(ft.HighDateTime)<<32 + uint64(ft.LowDateTime)
}
