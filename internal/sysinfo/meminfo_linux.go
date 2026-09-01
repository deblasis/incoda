//go:build linux

package sysinfo

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

func readMemory() Memory {
	m := Memory{Source: "/proc/meminfo"}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		m.Err = err.Error()
		return m
	}
	defer f.Close()

	var swapTotal, swapFree uint64
	var haveSwapTotal, haveSwapFree bool
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(val)
		if len(fields) == 0 {
			continue
		}
		kb, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		b := kb * 1024
		switch key {
		case "MemTotal":
			m.TotalBytes, m.HaveTotal = b, true
		case "MemAvailable":
			m.AvailableBytes, m.HaveAvailable = b, true
		case "SwapTotal":
			swapTotal, haveSwapTotal = b, true
		case "SwapFree":
			swapFree, haveSwapFree = b, true
		}
	}
	if haveSwapTotal && haveSwapFree && swapTotal >= swapFree {
		m.SwapUsedBytes, m.HaveSwap = swapTotal-swapFree, true
	}
	return m
}
