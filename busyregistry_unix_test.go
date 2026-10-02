//go:build !windows

package main

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
)

// TestEnrollOnABusyRegistryIsInterruptible: a run whose Enroll waits for a
// registry lock another process keeps (a stopped incoda) says so once on
// stderr, and SIGINT or SIGTERM ends it promptly with 130 and no ticket
// left behind, long before its --wait.
func TestEnrollOnABusyRegistryIsInterruptible(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "seed"); code != 0 {
		t.Fatalf("migrate: %d\n%s", code, out)
	}
	dir := laneDir(state, "builds")
	holdRegistryElsewhere(t, dir)
	const busyLine = `incoda: queue "builds": registry lock held by another process; waiting (limit 30s)`
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		c := exec.Command(incoda, "run", "--queue", "builds", "--wait", "30s", "--poll", "50ms", "--",
			stamp, filepath.Join(t.TempDir(), "s.txt"), "x", "1")
		c.Env = laneEnv(state)
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		errPipe, err := c.StderrPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		lines := make(chan string, 64)
		go func() {
			sc := bufio.NewScanner(errPipe)
			for sc.Scan() {
				lines <- sc.Text()
			}
			close(lines)
			done <- c.Wait()
		}()
		t.Cleanup(func() {
			// Only this run's own group, started above with Setpgid.
			_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		})
		var got []string
		seen := false
		deadline := time.After(15 * time.Second)
		for !seen {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("%s: run ended before it said the registry was busy:\n%s", sig, strings.Join(got, "\n"))
				}
				got = append(got, l)
				seen = strings.Contains(l, busyLine)
			case <-deadline:
				t.Fatalf("%s: no busy line within 15s:\n%s", sig, strings.Join(got, "\n"))
			}
		}
		sent := time.Now()
		if err := c.Process.Signal(sig); err != nil {
			t.Fatal(err)
		}
		for l := range lines {
			got = append(got, l)
		}
		var werr error
		select {
		case werr = <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: run did not end after the signal:\n%s", sig, strings.Join(got, "\n"))
		}
		if d := time.Since(sent); d > 3*time.Second {
			t.Fatalf("%s: run took %s to end after the signal", sig, d)
		}
		var ee *exec.ExitError
		if !errors.As(werr, &ee) || ee.ExitCode() != 130 {
			t.Fatalf("%s: want exit 130, got %v:\n%s", sig, werr, strings.Join(got, "\n"))
		}
		out := strings.Join(got, "\n")
		if !strings.Contains(out, `interrupted while queueing on "builds"`) {
			t.Fatalf("%s: want the interrupted line:\n%s", sig, out)
		}
		if n := strings.Count(out, "registry lock held by another process; waiting"); n != 1 {
			t.Fatalf("%s: busy line printed %d times:\n%s", sig, n, out)
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			if lane.ValidTicketName(e.Name()) {
				t.Fatalf("%s: ticket %s left behind", sig, e.Name())
			}
		}
	}
}
