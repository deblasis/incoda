// Package procinfo reads the parent of a process, so a nested incoda can
// check that a held ticket belongs to one of its ancestors.
package procinfo

import (
	"errors"
	"os"
)

// maxHops bounds the walk up the parent chain.
const maxHops = 64

// ErrUnsupported is returned where the platform has no parent lookup.
var ErrUnsupported = errors.New("parent lookup not supported on this platform")

// Chain is this process's ancestors, nearest first, stopping before pid 1.
// Err is set when a lookup failed partway; PIDs then holds what was found.
// Skip is set where no walk is needed (Windows: the Job Object ends every
// descendant with the holder, so a live held ticket is always an ancestor's).
type Chain struct {
	PIDs []int
	Err  error
	Skip bool
}

// Contains reports whether pid is in the chain.
func (c Chain) Contains(pid int) bool {
	for _, p := range c.PIDs {
		if p == pid {
			return true
		}
	}
	return false
}

// ParentChain walks from getppid up to (not including) pid 1, at most
// maxHops steps.
func ParentChain() Chain {
	if skipWalk {
		return Chain{Skip: true}
	}
	var c Chain
	p := os.Getppid()
	for i := 0; i < maxHops && p > 1; i++ {
		c.PIDs = append(c.PIDs, p)
		next, err := ParentPID(p)
		if err != nil {
			c.Err = err
			return c
		}
		p = next
	}
	return c
}
