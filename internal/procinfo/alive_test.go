package procinfo

import (
	"os"
	"os/exec"
	"testing"
)

func TestAlive(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Fatal("this process is alive")
	}
	if Alive(0) || Alive(-1) {
		t.Fatal("pid 0 and negative pids are never alive")
	}
	// A child that has exited and been waited for is gone.
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if Alive(cmd.Process.Pid) {
		t.Fatalf("pid %d exited and was reaped", cmd.Process.Pid)
	}
}
