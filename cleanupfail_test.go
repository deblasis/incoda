//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
)

// TestStrayCleanupFailureNeverFailsARun: a fully dead stray lane in a
// batch the user cannot write cannot be deleted. That is housekeeping: a
// pool run and a re-fencing config still succeed, machine.log records
// event=cleanup-failed, and the stray stays.
func TestStrayCleanupFailureNeverFailsARun(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	incoda, stamp := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "seed"); code != 0 {
		t.Fatalf("migrate: %d\n%s", code, out)
	}
	batch := filepath.Join(machine.StraysDir(state), "1")
	dead := filepath.Join(batch, "deadk")
	if err := os.MkdirAll(dead, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{lane.RegistryLockPath(dead), filepath.Join(dead, "00000000000000000001-4711.ticket")} {
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(batch, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(batch, 0o755) })

	out, code := runIncoda(t, incoda, state, "run", "--queue", "builds", "--wait", "10s", "--poll", "50ms", "--",
		stamp, filepath.Join(t.TempDir(), "s.txt"), "x", "1")
	if code != 0 {
		t.Fatalf("a failed cleanup must not fail the pool run: %d\n%s", code, out)
	}
	if err := os.Remove(filepath.Join(state, "queues")); err != nil {
		t.Fatal(err)
	}
	if out, code := runIncoda(t, incoda, state, "config", "seed", "--slots", "1"); code != 0 {
		t.Fatalf("a failed cleanup must not fail the re-fencing config: %d\n%s", code, out)
	}
	if !machine.FencePlaced(state) {
		t.Fatal("config re-places the fence")
	}
	if _, err := os.Stat(dead); err != nil {
		t.Fatal("the stray it could not delete stays")
	}
	b, _ := os.ReadFile(machine.MachineLogPath(state))
	if n := strings.Count(string(b), "event=cleanup-failed"); n < 2 || !strings.Contains(string(b), "path="+dead+" err=") {
		t.Fatalf("machine.log must record each failed cleanup:\n%s", b)
	}
}
