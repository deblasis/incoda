//go:build windows

package sysinfo

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// golang.org/x/sys/windows does not wrap GlobalMemoryStatusEx, so it is bound
// here directly. This is the only hand-rolled syscall in the tree; everything
// else uses the x/sys wrappers.
var (
	modkernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx = modkernel32.NewProc("GlobalMemoryStatusEx")
)

// memoryStatusEx mirrors MEMORYSTATUSEX from sysinfoapi.h.
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func globalMemoryStatusEx(st *memoryStatusEx) error {
	r, _, e := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(st)))
	if r == 0 {
		if e != nil {
			return e
		}
		return windows.ERROR_INVALID_PARAMETER
	}
	return nil
}

func readMemory() Memory {
	m := Memory{Source: "GlobalMemoryStatusEx"}
	var st memoryStatusEx
	st.Length = uint32(unsafe.Sizeof(st))
	if err := globalMemoryStatusEx(&st); err != nil {
		m.Err = err.Error()
		return m
	}
	m.TotalBytes, m.HaveTotal = st.TotalPhys, true
	m.AvailableBytes, m.HaveAvailable = st.AvailPhys, true
	// Commit charge beyond resident physical memory is the closest Windows
	// analogue to the Mac swap gauge: it is roughly what the page file backs.
	if st.TotalPageFile > st.TotalPhys {
		committed := st.TotalPageFile - st.AvailPageFile
		resident := st.TotalPhys - st.AvailPhys
		if committed > resident {
			m.SwapUsedBytes = committed - resident
		}
		m.HaveSwap = true
	}
	return m
}
