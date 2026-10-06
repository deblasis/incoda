//go:build darwin || linux

package procinfo

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func skipNoList(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no process listing on " + runtime.GOOS)
	}
}

func TestListAndLookupSeeThisProcess(t *testing.T) {
	skipNoList(t)
	ps, err := List()
	if err != nil {
		t.Fatal(err)
	}
	var self Proc
	for _, p := range ps {
		if p.PID == os.Getpid() {
			self = p
		}
	}
	if self.PID == 0 || self.PPID != os.Getppid() || self.PGID != syscall.Getpgrp() || self.Start == 0 || self.State == 'T' {
		t.Fatalf("this process in the listing: %+v", self)
	}
	got, err := Lookup(os.Getpid())
	if err != nil || got.Start != self.Start || got.PGID != self.PGID {
		t.Fatalf("Lookup(self) = %+v %v, listing has %+v", got, err, self)
	}
	if _, err := Lookup(1 << 30); !errors.Is(err, ErrNoProcess) {
		t.Fatalf("a missing pid: %v", err)
	}
}

// TestStoppedState: a child stopped with SIGSTOP reads as 'T' until
// SIGCONT. The child is always resumed and killed in cleanup.
func TestStoppedState(t *testing.T) {
	skipNoList(t)
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	pid := c.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGCONT)
		_ = c.Process.Kill()
		_ = c.Wait()
	})
	waitState := func(want bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			got, err := Stopped(pid)
			if err != nil {
				t.Fatal(err)
			}
			if got == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("pid %d stopped=%v, want %v", pid, got, want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitState(false)
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	waitState(true)
	if err := syscall.Kill(pid, syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	waitState(false)
}
