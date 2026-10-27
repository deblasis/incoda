package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func incodaIn(t *testing.T, bin, state string, extra []string, args ...string) (string, int) {
	t.Helper()
	return runWithEnv(t, bin, state, extra, args...)
}

// TestPoolAddThenLinkThenRun is the sequence an unlinked lane needs: the new
// pool exists, a lane can be linked to it, and a run on that lane starts.
func TestPoolAddThenLinkThenRun(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()

	if out, rc := incodaIn(t, incoda, state, nil, "config", "wintty-merge", "--pool", "merges"); rc != 120 || !strings.Contains(out, "pool-mismatch") {
		t.Fatalf("before pool add: rc=%d\n%s", rc, out)
	}
	out, rc := incodaIn(t, incoda, state, nil, "pool", "add", "merges", "--slots", "1", "--description", "serialises merges")
	if rc != 0 || !strings.Contains(out, "pool add: merges: registered (slots 1)") || !strings.Contains(out, "no other pool or lane was changed") {
		t.Fatalf("pool add: rc=%d\n%s", rc, out)
	}
	if out, rc := incodaIn(t, incoda, state, nil, "config", "wintty-merge", "--pool", "merges"); rc != 0 || !strings.Contains(out, "link:") {
		t.Fatalf("link: rc=%d\n%s", rc, out)
	}
	if out, rc := incodaIn(t, incoda, state, nil, "run", "--queue", "wintty-merge", "--", incoda, "version"); rc != 0 {
		t.Fatalf("run: rc=%d\n%s", rc, out)
	}
	if out, rc := incodaIn(t, incoda, state, nil, "pool", "add", "merges"); rc != 120 || !strings.Contains(out, "pool-exists") {
		t.Fatalf("duplicate: rc=%d\n%s", rc, out)
	}
	if out, rc := incodaIn(t, incoda, state, nil, "pool", "add", "../x"); rc != 120 {
		t.Fatalf("bad name: rc=%d\n%s", rc, out)
	}
	for _, args := range [][]string{{"pool"}, {"pool", "add"}, {"pool", "remove", "x"}, {"pool", "add", "a", "b"}} {
		if out, rc := incodaIn(t, incoda, state, nil, args...); rc != 120 {
			t.Fatalf("%v: rc=%d\n%s", args, rc, out)
		}
	}
	log, _ := os.ReadFile(filepath.Join(state, "lanes", "merges", "lane.log"))
	if !strings.Contains(string(log), "event=pool-add") || !strings.Contains(string(log), "by=pool-add") {
		t.Fatalf("lane.log:\n%s", log)
	}
}

// TestPoolAddConcurrentProcesses: separate processes racing for the same
// name and for distinct names lose nothing and elect one winner.
func TestPoolAddConcurrentProcesses(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	if _, rc := incodaIn(t, incoda, state, nil, "queues"); rc != 0 {
		t.Fatal("queues")
	}
	if _, rc := incodaIn(t, incoda, state, nil, "config", "seed", "--slots", "1"); rc != 0 {
		t.Fatal("seed")
	}
	names := []string{"a1", "a2", "a3", "a4", "same", "same", "same"}
	rcs := make([]int, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var buf bytes.Buffer
			cmd := exec.Command(incoda, "pool", "add", n)
			cmd.Env = laneEnv(state)
			cmd.Stdout, cmd.Stderr = &buf, &buf
			if err := cmd.Run(); err != nil {
				rcs[i] = exitCodeOf(err)
			}
		}()
	}
	wg.Wait()
	won := 0
	for i, rc := range rcs {
		switch {
		case names[i] != "same" && rc != 0:
			t.Fatalf("%s rc=%d", names[i], rc)
		case names[i] == "same" && rc == 0:
			won++
		case names[i] == "same" && rc != 120:
			t.Fatalf("same rc=%d", rc)
		}
	}
	if won != 1 {
		t.Fatalf("%d winners", won)
	}
	b, _ := os.ReadFile(filepath.Join(state, "machine.json"))
	for _, n := range []string{"a1", "a2", "a3", "a4", "same", "builds", "computer-use", "tests", "vm"} {
		if !strings.Contains(string(b), `"`+n+`"`) {
			t.Fatalf("%s lost:\n%s", n, b)
		}
	}
}

// TestPoolAddKilledBeforeTheRegistryWrite: the process dies after the pool's
// config.json is written and before machine.json is replaced. The registry
// is the old one, every command still works, and the same command finishes.
func TestPoolAddKilledBeforeTheRegistryWrite(t *testing.T) {
	crash := crashBinary(t)
	incoda, _ := binaries(t)
	state := t.TempDir()
	if _, rc := incodaIn(t, incoda, state, nil, "config", "seed", "--slots", "1"); rc != 0 {
		t.Fatal("seed")
	}
	before, _ := os.ReadFile(filepath.Join(state, "machine.json"))
	_, rc := runWithEnv(t, crash, state, []string{"INCODA_TEST_CRASH_AT=pool-add-config"}, "pool", "add", "merges", "--slots", "1")
	if rc != 97 {
		t.Fatalf("crash rc=%d", rc)
	}
	after, _ := os.ReadFile(filepath.Join(state, "machine.json"))
	if !bytes.Equal(before, after) {
		t.Fatalf("registry changed by an interrupted add:\n%s\n%s", before, after)
	}
	if out, rc := incodaIn(t, incoda, state, nil, "config", "merges"); rc != 0 || !strings.Contains(out, "project") {
		t.Fatalf("half state unusable: rc=%d\n%s", rc, out)
	}
	if out, rc := incodaIn(t, incoda, state, nil, "pool", "add", "merges", "--slots", "1"); rc != 0 {
		t.Fatalf("rerun: rc=%d\n%s", rc, out)
	}
	if out, _ := incodaIn(t, incoda, state, nil, "config", "merges"); !strings.Contains(out, "kind: pool") {
		t.Fatalf("not a pool:\n%s", out)
	}
}
