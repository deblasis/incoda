//go:build !windows

package machine

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/procinfo"
)

// stoppedProcess starts sleep in a group of its own and stops it. Cleanup
// resumes it before killing it.
func stoppedProcess(t *testing.T) int {
	t.Helper()
	c := exec.Command("sleep", "60")
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	pid := c.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGCONT)
		_ = c.Process.Kill()
		_ = c.Wait()
	})
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		if s, _ := procinfo.Stopped(pid); s {
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatal("sleep did not stop")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestStoppedHolderLinesTellAnOlderIncodaFromThisBinary: the kill rerun
// line is offered only for an older incoda (an old-layout holder); a
// stopped holder or waiter of this binary gets the resume line alone,
// because a kill of it would end only its incoda and leave its job
// running with the lane free.
func TestStoppedHolderLinesTellAnOlderIncodaFromThisBinary(t *testing.T) {
	state, _ := migrated(t)
	mine, old := stoppedProcess(t), stoppedProcess(t)
	holdTicket(t, lane.LanesDir(state), "mine", mine, "zig", "build")
	holdTicket(t, filepath.Join(StraysDir(state), "1"), "old", old, "zig", "build")
	v, err := Inspect(state)
	if err != nil {
		t.Fatal(err)
	}
	mineLine := JobControlLine("mine", mine)
	if !strings.HasPrefix(mineLine, "stopped holder: pid ") || !strings.HasSuffix(mineLine, ` on "mine" is stopped (job control?); resume it: kill -CONT `+strconv.Itoa(mine)) {
		t.Fatalf("job control line: %q", mineLine)
	}
	oldLines := StoppedLines("old", old)

	got := strings.Join(StatusWarnings(state, v, []Holder{{Key: "mine", PID: mine}}, soon()), "\n")
	if !strings.Contains(got, mineLine) || strings.Contains(got, "--pid "+strconv.Itoa(mine)) {
		t.Fatalf("status: this binary's stopped holder gets the resume line and no kill:\n%s", got)
	}
	if !strings.Contains(got, strings.Join(oldLines, "\n")) {
		t.Fatalf("status: an older incoda keeps the kill rerun line:\n%s", got)
	}

	h := Diagnose(state)
	att := strings.Join(h.Attention, "\n")
	if !strings.Contains(att, mineLine) || strings.Contains(att, "--pid "+strconv.Itoa(mine)) || !strings.Contains(att, strings.Join(oldLines, "\n")) {
		t.Fatalf("doctor:\n%s", att)
	}
}

// TestStoppedHolderBeforeTheUpgradeIsOld: before the upgrade every holder
// is an older incoda and keeps the rerun line.
func TestStoppedHolderBeforeTheUpgradeIsOld(t *testing.T) {
	state := t.TempDir()
	pid := stoppedProcess(t)
	holdTicket(t, lane.QueuesDir(state), "oldq", pid, "x")
	att := strings.Join(Diagnose(state).Attention, "\n")
	if !strings.Contains(att, strings.Join(StoppedLines("oldq", pid), "\n")) {
		t.Fatalf("doctor:\n%s", att)
	}
}
