//go:build !windows

package machine

import (
	"os"
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

// startSleeper starts `sleep 30`, in its own process group when ownGroup
// is set, and always kills it (and its group) in cleanup.
func startSleeper(t *testing.T, ownGroup bool) (*exec.Cmd, procinfo.Proc) {
	t.Helper()
	c := exec.Command("sleep", "30")
	if ownGroup {
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = c.Process.Kill()
			_ = c.Wait()
		}
	})
	p, err := procinfo.Lookup(c.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	return c, p
}

func killAndReap(t *testing.T, c *exec.Cmd) {
	t.Helper()
	_ = c.Process.Kill()
	_ = c.Wait()
}

func TestOrphanRecordIsLiveUntilItsTreeIsEmpty(t *testing.T) {
	state := t.TempDir()
	c, p := startSleeper(t, false)
	rec := &Orphan{Key: "builds", PID: 999990, Command: []string{"zig", "build"},
		Descendants: []OrphanProc{{PID: p.PID, Start: p.Start}}}
	path, err := writeOrphan(state, rec)
	if err != nil || !strings.HasPrefix(filepath.Base(path), "999990-") || !strings.HasSuffix(path, ".orphan") {
		t.Fatalf("writeOrphan = %s %v", path, err)
	}
	live, err := LiveOrphans(state, true)
	if err != nil || len(live) != 1 || live[0].Key != "builds" || live[0].File != filepath.Base(path) {
		t.Fatalf("LiveOrphans = %+v %v", live, err)
	}
	bs, err := findBlockers(state, phaseM2, soon())
	if err != nil || len(bs) != 1 || !bs[0].Orphan || bs[0].PID != 999990 ||
		bs[0].Command != "zig build (its job is still exiting after a kill: pids "+strconv.Itoa(p.PID)+")" {
		t.Fatalf("findBlockers = %+v %v", bs, err)
	}
	if lines := blockerLines(bs, phaseM2); strings.Contains(strings.Join(lines, "\n"), "incoda kill") {
		t.Fatalf("an orphan has no participant left to kill:\n%s", strings.Join(lines, "\n"))
	}
	killAndReap(t, c)
	live, err = LiveOrphans(state, false)
	if err != nil || len(live) != 0 {
		t.Fatalf("after the tree ended: %+v %v", live, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("a read without sweep must not delete the record")
	}
	if _, err := LiveOrphans(state, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a sweep deletes a stale record")
	}
}

func TestOrphanRecordIgnoresAReusedPid(t *testing.T) {
	state := t.TempDir()
	_, p := startSleeper(t, false)
	if _, err := writeOrphan(state, &Orphan{Key: "k", PID: 999991,
		Descendants: []OrphanProc{{PID: p.PID, Start: p.Start + 1}}}); err != nil {
		t.Fatal(err)
	}
	if live, _ := LiveOrphans(state, false); len(live) != 0 {
		t.Fatal("a pid with another start time is a different process")
	}
}

func TestOrphanRecordCountsAGroupWithMembers(t *testing.T) {
	state := t.TempDir()
	c, p := startSleeper(t, true)
	if p.PGID != p.PID {
		t.Fatalf("the sleeper must lead its own group: %+v", p)
	}
	if _, err := writeOrphan(state, &Orphan{Key: "k", PID: 999992, Groups: []int{p.PGID}}); err != nil {
		t.Fatal(err)
	}
	if live, _ := LiveOrphans(state, false); len(live) != 1 {
		t.Fatal("a recorded group with a member is live")
	}
	killAndReap(t, c)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if live, _ := LiveOrphans(state, false); len(live) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("an empty group is not live")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSweepOrphansIsPerKeyAndKeepsLiveOnes(t *testing.T) {
	state := t.TempDir()
	_, p := startSleeper(t, false)
	for _, o := range []*Orphan{
		{Key: "a", PID: 999993},
		{Key: "b", PID: 999994},
		{Key: "a", PID: 999995, Descendants: []OrphanProc{{PID: p.PID, Start: p.Start}}},
	} {
		if _, err := writeOrphan(state, o); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond) // distinct file names
	}
	if err := os.WriteFile(filepath.Join(OrphansDir(state), "junk.orphan"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := SweepOrphans(state, "a")
	if err != nil || n != 1 {
		t.Fatalf("SweepOrphans(a) = %d %v", n, err)
	}
	all, bad, _ := ReadOrphans(state)
	if len(all) != 2 || len(bad) != 1 || bad[0].File != "junk.orphan" {
		t.Fatalf("left: %+v bad %+v", all, bad)
	}
}

func TestOrphanAndTicketOfOnePidAreOneBlocker(t *testing.T) {
	state := t.TempDir()
	_, p := startSleeper(t, false)
	holdTicket(t, lane.QueuesDir(state), "builds", 4711, "zig", "build")
	if _, err := writeOrphan(state, &Orphan{Key: "builds", PID: 4711,
		Descendants: []OrphanProc{{PID: p.PID, Start: p.Start}}}); err != nil {
		t.Fatal(err)
	}
	bs, err := findBlockers(state, phaseM2, soon())
	if err != nil || len(bs) != 1 || bs[0].Orphan {
		t.Fatalf("findBlockers = %+v %v", bs, err)
	}
}
