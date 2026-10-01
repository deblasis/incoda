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
}

func TestParentPIDOfMissingProcess(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip()
	}
	if _, err := ParentPID(1 << 30); err == nil {
		t.Fatal("a missing pid must be an error")
	}
}
