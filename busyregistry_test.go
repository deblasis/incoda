package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/lockfile"
	"github.com/deblasis/incoda/internal/machine"
)

// TestHelperHoldLock is not a test of its own. Run as a helper process
// with INCODA_TEST_HOLD_LOCK set, it takes that file's lock, prints
// "held" and keeps the lock until its stdin closes.
func TestHelperHoldLock(t *testing.T) {
	path := os.Getenv("INCODA_TEST_HOLD_LOCK")
	if path == "" {
		t.Skip("helper process only")
	}
	f, err := lockfile.Open(path)
	if err != nil {
		os.Exit(2)
	}
	if err := f.Lock(); err != nil {
		os.Exit(3)
	}
	fmt.Println("held")
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

// holdRegistryElsewhere makes dir a lane and starts a helper process that
// holds its registry lock until the test ends, the way a stopped incoda
// does.
func holdRegistryElsewhere(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldLock$")
	cmd.Env = append(os.Environ(), "INCODA_TEST_HOLD_LOCK="+lane.RegistryLockPath(dir))
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		in.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "held\n" {
		t.Fatalf("helper did not take the lock: %q %v", line, err)
	}
}

// TestRegistryLockHeldElsewhereNeverHangs: a registry lock another process
// keeps for ever (a stray's, and a lane's of this layout) costs status,
// doctor, kill and a pool run at most their bounds. status and doctor name
// the lane they cannot tell, kill fails closed with exit 122, and a run on
// a pool counts the stray as held and times out within its --wait.
func TestRegistryLockHeldElsewhereNeverHangs(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "seed"); code != 0 {
		t.Fatalf("migrate: %d\n%s", code, out)
	}
	holdRegistryElsewhere(t, filepath.Join(machine.StraysDir(state), "1", "oldk"))
	holdRegistryElsewhere(t, laneDir(state, "seed"))
	const cannot = "cannot tell: registry lock held by another process"
	timed := func(limit time.Duration, args ...string) (string, int) {
		t.Helper()
		start := time.Now()
		out, code := runIncoda(t, incoda, state, args...)
		if d := time.Since(start); d > limit {
			t.Fatalf("incoda %s took %s:\n%s", strings.Join(args, " "), d, out)
		}
		return out, code
	}

	out, code := timed(5*time.Second, "status", "--queue", "seed", "--no-color")
	if code != 0 || !strings.Contains(out, `queue "seed": `+cannot+"\n") ||
		!strings.HasSuffix(out, "\nunpooled lane of an older incoda: strays/1/oldk: "+cannot+"\n") {
		t.Fatalf("status must name both lanes it cannot tell: %d\n%s", code, out)
	}

	start := time.Now()
	out, code = doctor(t, incoda, state)
	if d := time.Since(start); d > 10*time.Second || !strings.Contains(out, "1/oldk: cannot probe ("+cannot+")") {
		t.Fatalf("doctor (%s) must name the stray it cannot tell: %d\n%s", d, code, out)
	}

	out, code = timed(5*time.Second, "kill", "--queue", "oldk", "--pid", "4711", "--reason", "test", "--wait", "300ms")
	if code != 122 || !strings.Contains(out, cannot) {
		t.Fatalf("kill must fail closed: %d\n%s", code, out)
	}

	out, code = timed(8*time.Second, "run", "--queue", "builds", "--wait", "1s", "--poll", "50ms", "--",
		stamp, filepath.Join(t.TempDir(), "s.txt"), "x", "1")
	if code != 121 || !strings.Contains(out, "unpooled lane of an older incoda: strays/1/oldk: "+cannot) {
		t.Fatalf("a pool run counts the busy stray and times out: %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(machine.StraysDir(state), "1", "oldk")); err != nil {
		t.Fatal("a stray that cannot be told is never deleted")
	}
}
