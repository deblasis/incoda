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

// Chain is this process's ancestors, nearest first, ending at pid 1 when the
// walk reaches it (every process descends from pid 1, so a clean walk always
// ends there). Err is set when a lookup failed partway; PIDs then holds what
// was found. Skip is set where no walk is needed (Windows: the Job Object
// ends every descendant with the holder, so a live held ticket is always an
// ancestor's).
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

// ParentChain walks from getppid up to and including pid 1, at most maxHops
// steps. When the outer incoda is itself pid 1 (a container entrypoint), its
// nested runs still see it as an ancestor.
func ParentChain() Chain {
	if skipWalk {
		return Chain{Skip: true}
	}
	var c Chain
	p := os.Getppid()
	for i := 0; i < maxHops; i++ {
		c.PIDs = append(c.PIDs, p)
		if p == 1 {
			return c
		}
		next, err := ParentPID(p)
		if err != nil {
			c.Err = err
			return c
		}
		p = next
	}
	return c
}
