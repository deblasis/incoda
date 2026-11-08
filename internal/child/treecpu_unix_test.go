//go:build !windows

package child

import (
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/procinfo"
)

func TestTreeCPUCountsDescendantsAndGroupOnly(t *testing.T) {
	s := time.Second
	ps := []procinfo.Proc{
		{PID: 10, PPID: 1, PGID: 10, CPU: 1 * s},  // the job's root
		{PID: 11, PPID: 10, PGID: 10, CPU: 2 * s}, // child
		{PID: 12, PPID: 11, PGID: 12, CPU: 4 * s}, // grandchild that left the group
		{PID: 13, PPID: 1, PGID: 10, CPU: 8 * s},  // reparented, still in the group
		{PID: 20, PPID: 1, PGID: 20, CPU: 99 * s}, // unrelated
		{PID: 21, PPID: 20, PGID: 20, CPU: 99 * s},
	}
	if got := treeCPU(ps, 10, 10); got != 15*s {
		t.Fatalf("root, descendants and group members: got %v, want 15s", got)
	}
	// Without a group of its own only the descendants count.
	if got := treeCPU(ps, 10, 0); got != 7*s {
		t.Fatalf("descendants only: got %v, want 7s", got)
	}
}
