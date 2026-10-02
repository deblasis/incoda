package procinfo

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Proc is one process as the process listing sees it.
type Proc struct {
	PID, PPID, PGID int
	// Start identifies this incarnation of PID, so a recorded pid is never
	// confused with a later process that reused the number: microseconds
	// since the epoch on macOS (p_starttime), clock ticks since boot on
	// Linux (field 22 of /proc/<pid>/stat). Only equality is meaningful.
	Start uint64
	// State is 'T' for a stopped process, 'Z' for a zombie and 'R' for
	// anything else.
	State byte
}

// ErrNoProcess means no process has the pid.
var ErrNoProcess = errors.New("no such process")

// Stopped reports whether pid is in the stopped state (T). A missing
// process is not stopped.
func Stopped(pid int) (bool, error) {
	p, err := Lookup(pid)
	if errors.Is(err, ErrNoProcess) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return p.State == 'T', nil
}

// parseStat parses the content of /proc/<pid>/stat: field 3 (state), 4
// (ppid), 5 (pgrp) and 22 (starttime). The command name in field 2 may hold
// spaces and parentheses, so fields are counted after the last ')'. It is
// plain parsing, kept apart from the Linux reader so every platform tests
// it.
func parseStat(s string) (Proc, error) {
	open := strings.IndexByte(s, '(')
	i := strings.LastIndexByte(s, ')')
	if open <= 0 || i < open {
		return Proc{}, fmt.Errorf("unparseable stat line")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(s[:open]))
	if err != nil {
		return Proc{}, fmt.Errorf("unparseable pid in stat line: %w", err)
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 || len(f[0]) != 1 {
		return Proc{}, fmt.Errorf("short stat line for pid %d", pid)
	}
	ppid, err1 := strconv.Atoi(f[1])
	pgid, err2 := strconv.Atoi(f[2])
	start, err3 := strconv.ParseUint(f[19], 10, 64)
	if err := errors.Join(err1, err2, err3); err != nil {
		return Proc{}, fmt.Errorf("stat line for pid %d: %w", pid, err)
	}
	st := byte('R')
	switch f[0][0] {
	case 'T', 't':
		st = 'T'
	case 'Z', 'X':
		st = 'Z'
	}
	return Proc{PID: pid, PPID: ppid, PGID: pgid, Start: start, State: st}, nil
}
