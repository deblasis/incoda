//go:build !windows

package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/machine"
)

// TestStoppedRunOfThisBinaryGetsTheResumeLineOnly: Ctrl-Z on a run of this
// binary stops its incoda while its job, in a group of its own, keeps
// running. status and doctor must not offer the kill rerun (it would end
// only that incoda and leave the job running with the lane free): they
// print the resume line alone.
func TestStoppedRunOfThisBinaryGetsTheResumeLineOnly(t *testing.T) {
	runnerSentinel(t)
	incoda, _ := binaries(t)
	state := t.TempDir()
	started := filepath.Join(t.TempDir(), "started")
	c := exec.Command(incoda, "run", "--queue", "jc", "--poll", "50ms", "--quiet", "--", "sh", "-c", `touch "$0"; sleep 3`, started)
	c.Env = laneEnv(state)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	pid := c.Process.Pid
	t.Cleanup(func() {
		// Resumed, the run reaps its job (sleep 3) and exits on its own.
		_ = syscall.Kill(pid, syscall.SIGCONT)
		done := make(chan struct{})
		go func() { _ = c.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = c.Process.Kill()
			<-done
		}
	})
	// Stop it only once its job runs: stopped inside Enroll or
	// MarkAcquired it would keep the registry lock, and status could then
	// only say it cannot tell.
	waitForFile(t, started)
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); !stopped(pid); {
		if time.Now().After(deadline) {
			t.Fatal("the run did not stop")
		}
		time.Sleep(10 * time.Millisecond)
	}
	want := machine.JobControlLine("jc", pid)
	out, code := runIncoda(t, incoda, state, "status", "--queue", "jc", "--no-color")
	if code != 0 || !strings.HasSuffix(out, "\n"+want+"\n") || strings.Contains(out, "a kill was interrupted") || strings.Contains(out, "incoda kill") {
		t.Fatalf("want only %q at the end of status, got %d:\n%s", want, code, out)
	}
	out, code = doctor(t, incoda, state)
	if code != 0 || !strings.Contains(out, "attention: "+want+"\n") || strings.Contains(out, "a kill was interrupted") {
		t.Fatalf("doctor must print the resume line alone, got %d:\n%s", code, out)
	}
}
