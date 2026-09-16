//go:build linux

package sysinfo

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

func readCPU() CPU {
	return sampleCPU("/proc/stat", cpuTotalsFromProc)
}

func cpuTotalsFromProc() (cpuTotals, bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuTotals{}, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return cpuTotals{}, false
	}
	return parseProcStatCPU(sc.Text())
}

// parseProcStatCPU parses the aggregate "cpu" line from /proc/stat.
func parseProcStatCPU(line string) (cpuTotals, bool) {
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuTotals{}, false
	}
	vals := make([]uint64, 0, len(fields)-1)
	for _, f := range fields[1:] {
		n, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return cpuTotals{}, false
		}
		vals = append(vals, n)
	}
	var total, idle uint64
	for _, v := range vals {
		total += v
	}
	idle = vals[3]
	if len(vals) > 4 {
		idle += vals[4] // iowait counts as idle for utilization
	}
	return cpuTotals{total: total, idle: idle}, true
}
