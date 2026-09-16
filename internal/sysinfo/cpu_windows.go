//go:build windows

package sysinfo

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetSystemTimes = modkernel32.NewProc("GetSystemTimes")

func readCPU() CPU {
	return sampleCPU("GetSystemTimes", systemTimes)
}

func systemTimes() (cpuTotals, bool) {
	var idle, kernel, user windows.Filetime
	r, _, e := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if r == 0 {
		if e != nil && e != windows.ERROR_SUCCESS {
			return cpuTotals{}, false
		}
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
