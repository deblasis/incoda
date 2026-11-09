//go:build !windows

package child

import (
	"sort"
	"testing"

	"github.com/deblasis/incoda/internal/procinfo"
)

// killSet is who a limit kill must end: the child, everything below it, and
// the members of its group, never an unrelated process.
func TestKillSetCoversDescendantsAndGroup(t *testing.T) {
	ps := []procinfo.Proc{
		{PID: 10, PPID: 1, PGID: 10},
		{PID: 11, PPID: 10, PGID: 10},
		{PID: 12, PPID: 11, PGID: 12}, // left the group, still a descendant
		{PID: 13, PPID: 1, PGID: 10},  // reparented, still in the group
		{PID: 20, PPID: 1, PGID: 20},
		{PID: 21, PPID: 20, PGID: 20},
	}
	ids := func(pgid int) []int {
		var out []int
		for pid := range killSet(ps, 10, pgid) {
			out = append(out, pid)
		}
		sort.Ints(out)
		return out
	}
	if got := ids(10); len(got) != 4 || got[0] != 10 || got[3] != 13 {
		t.Fatalf("with a group: %v", got)
	}
	// A nested run has no group of its own: descendants only.
	if got := ids(0); len(got) != 3 || got[2] != 12 {
		t.Fatalf("without a group: %v", got)
	}
}
