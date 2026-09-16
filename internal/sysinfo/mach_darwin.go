//go:build darwin

package sysinfo

import (
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Mach host_statistics constants from mach/host_info.h.
const (
	hostVMInfo       = 2
	hostVMInfoCount  = 15 // HOST_VM_INFO_COUNT
	hostCPULoadInfo  = 3
	hostCPULoadCount = 4 // CPU_STATE_MAX

	kernSuccess = 0
)

// vmStatistics mirrors struct vm_statistics (15 natural_t fields = 60 bytes).
type vmStatistics struct {
	freeCount        uint32
	activeCount      uint32
	inactiveCount    uint32
	wireCount        uint32
	zeroFillCount    uint32
	reactivations    uint32
	pageins          uint32
	pageouts         uint32
	faults           uint32
	cowFaults        uint32
	lookups          uint32
	hits             uint32
	purgeableCount   uint32
	purges           uint32
	speculativeCount uint32
}

type hostCPULoadData struct {
	cpuTicks [hostCPULoadCount]uint32
}

var (
	machOnce sync.Once
	machOK   bool

	machHostSelf     func() uint32
	hostStatistics   func(host, flavor uint32, info unsafe.Pointer, count *uint32) int32
	vmKernelPageSize *uint64
)

func initMach() {
	handle, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
	if err != nil {
		return
	}
	purego.RegisterLibFunc(&machHostSelf, handle, "mach_host_self")
	purego.RegisterLibFunc(&hostStatistics, handle, "host_statistics")
	addr, err := purego.Dlsym(handle, "vm_kernel_page_size")
	if err != nil {
		return
	}
	vmKernelPageSize = *(**uint64)(unsafe.Pointer(&addr))
	machOK = machHostSelf != nil && hostStatistics != nil && vmKernelPageSize != nil
}

func darwinAvailableBytes() (uint64, bool) {
	machOnce.Do(initMach)
	if !machOK {
		return 0, false
	}
	var stat vmStatistics
	count := uint32(hostVMInfoCount)
	if hostStatistics(machHostSelf(), hostVMInfo, unsafe.Pointer(&stat), &count) != kernSuccess {
		return 0, false
	}
	pageSize := *vmKernelPageSize
	availPages := uint64(stat.freeCount) + uint64(stat.inactiveCount) + uint64(stat.purgeableCount)
	return availPages * pageSize, true
}

func darwinCPUTotals() (cpuTotals, bool) {
	machOnce.Do(initMach)
	if !machOK {
		return cpuTotals{}, false
	}
	var load hostCPULoadData
	count := uint32(hostCPULoadCount)
	if hostStatistics(machHostSelf(), hostCPULoadInfo, unsafe.Pointer(&load), &count) != kernSuccess {
		return cpuTotals{}, false
	}
	// HOST_CPU_LOAD_INFO: idle ticks sit at index 2 on current macOS (index 3
	// is nice and often zero). Same layout gopsutil uses.
	const cpuStateIdle = 2
	var total uint64
	for _, v := range load.cpuTicks {
		total += uint64(v)
	}
	return cpuTotals{total: total, idle: uint64(load.cpuTicks[cpuStateIdle])}, true
}
