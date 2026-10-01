package procinfo

import (
	"os"
	"runtime"
	"testing"
)

func TestParentChainStartsAtParent(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no ancestry walk on " + runtime.GOOS)
	}
	c := ParentChain()
	if c.Err != nil {
		t.Fatal(c.Err)
	}
	if len(c.PIDs) == 0 || c.PIDs[0] != os.Getppid() {
		t.Fatalf("chain %v should start at getppid %d", c.PIDs, os.Getppid())
	}
	if !c.Contains(os.Getppid()) || c.Contains(os.Getpid()) {
		t.Fatalf("Contains is wrong for %v", c.PIDs)
	}
	if pp, err := ParentPID(os.Getpid()); err != nil || pp != os.Getppid() {
		t.Fatalf("ParentPID(self) = %d, %v", pp, err)
	}
	// Every process on darwin/linux descends from pid 1, so a clean walk
	// must reach it and include it as the last entry.
	if got := c.PIDs[len(c.PIDs)-1]; got != 1 {
		t.Fatalf("chain %v should end at pid 1, ends at %d", c.PIDs, got)
	}
}

func TestChainContainsPidOne(t *testing.T) {
	c := Chain{PIDs: []int{42, 7, 1}}
	if !c.Contains(1) {
		t.Fatalf("Contains(1) should be true for %v", c.PIDs)
	}
	if c.Contains(2) {
		t.Fatalf("Contains(2) should be false for %v", c.PIDs)
	}
}

func TestParentPIDOfMissingProcess(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip()
	}
	if _, err := ParentPID(1 << 30); err == nil {
		t.Fatal("a missing pid must be an error")
	}
}
