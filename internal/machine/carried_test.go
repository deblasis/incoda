package machine

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
)

// TestSetBlockersAfterRelease: a released lock refuses to write a note
// instead of dereferencing a closed handle.
func TestSetBlockersAfterRelease(t *testing.T) {
	lk := takeLock(t, t.TempDir())
	lk.Release()
	if err := lk.SetBlockers([]Blocker{{Key: "k", PID: 1}}); err == nil {
		t.Fatal("SetBlockers after Release must fail")
	}
	var nilLock *Lock
	if err := nilLock.SetBlockers(nil); err == nil {
		t.Fatal("SetBlockers on a nil lock must fail")
	}
}

// TestNotIdleWaitNamesTheOldRunsInQueues: while Windows refuses to move
// queues/, the wait names the older runs that hold tickets there (in the
// note and once on stderr), and falls back to the open-file line when it
// finds none.
func TestNotIdleWaitNamesTheOldRunsInQueues(t *testing.T) {
	state := t.TempDir()
	holdTicket(t, lane.QueuesDir(state), "builds", 4711, "zig", "build")
	lk := takeLock(t, state)
	var errBuf bytes.Buffer
	o := Options{Start: time.Now(), Wait: time.Minute, Poll: time.Millisecond, Stderr: &errBuf}
	var w notIdleWait
	for i := 0; i < 2; i++ {
		if err := w.wait(state, lk, o); err != nil {
			t.Fatal(err)
		}
	}
	want := "incoda: upgrade-wait: state upgrade waits for 1 run(s) by an older incoda:\nincoda:   builds pid 4711: zig build\n"
	if !strings.HasPrefix(errBuf.String(), want) || strings.Count(errBuf.String(), "upgrade-wait:") != 1 {
		t.Fatalf("stderr:\n%s", errBuf.String())
	}
	if n, ok := ReadNote(state); !ok || len(n.Blockers) != 1 || n.Blockers[0].PID != 4711 {
		t.Fatalf("note %+v %v", n, ok)
	}

	empty := t.TempDir()
	errBuf.Reset()
	w = notIdleWait{}
	if err := w.wait(empty, takeLock(t, empty), o); err != nil {
		t.Fatal(err)
	}
	if errBuf.String() != "incoda: "+notIdleLine+"\n" {
		t.Fatalf("stderr without blockers: %q", errBuf.String())
	}

	o.Wait = 0
	var to *Timeout
	if err := w.wait(state, lk, o); !errors.As(err, &to) || !strings.HasPrefix(to.Msg, "upgrade-timeout: state upgrade still waits for 1 run(s)") {
		t.Fatalf("budget spent: %v", err)
	}
}

// TestCommitRefenceCapCountsOnlyRealRefences: the fence vanishes once
// before the commit and Windows refuses to move the recreated queues/ more
// often than the cap. Only fences actually re-placed count, so the
// migration still commits.
func TestCommitRefenceCapCountsOnlyRealRefences(t *testing.T) {
	state := t.TempDir()
	seedOld(t, state)
	checks := 0
	beforeCommitCheck = func() {
		checks++
		if checks == 1 {
			if err := os.Remove(lane.QueuesDir(state)); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(lane.QueuesDir(state), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	var refused atomic.Int32
	renameDir = func(from, to string) error {
		if strings.HasPrefix(to, StraysDir(state)) && refused.Load() < maxCommitRefences+50 {
			refused.Add(1)
			return errors.New("sharing violation")
		}
		return os.Rename(from, to)
	}
	isNotIdle = func(error) bool { return true }
	defer func() { beforeCommitCheck, renameDir, isNotIdle = func() {}, os.Rename, notIdleError }()
	var errBuf bytes.Buffer
	_, err := Ensure(state, Options{Start: time.Now(), Wait: time.Minute, Poll: time.Millisecond, By: "incoda test", Stderr: &errBuf})
	if err != nil {
		t.Fatalf("%v\n%s", err, errBuf.String())
	}
	if refused.Load() != maxCommitRefences+50 || checks != 2 {
		t.Fatalf("refused %d, checks %d", refused.Load(), checks)
	}
	assertMigrated(t, state, true)
}

// TestBootstrapSkipsAConfigThatTurnsMalformed: a pool config that becomes
// unreadable between M7's check and its write is left in place, like any
// malformed config, and the migration still commits.
func TestBootstrapSkipsAConfigThatTurnsMalformed(t *testing.T) {
	state := t.TempDir()
	seedOld(t, state)
	beforeBootstrapWrite = func(key string) {
		if key == "builds" {
			if err := os.WriteFile(filepath.Join(lane.LaneDir(state, key), "config.json"), []byte("nope"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	defer func() { beforeBootstrapWrite = func(string) {} }()
	reg, _, err := ensure(t, state)
	if err != nil || !reg.IsPool("builds") {
		t.Fatalf("Ensure = %+v %v", reg, err)
	}
	if b, _ := os.ReadFile(filepath.Join(lane.LaneDir(state, "builds"), "config.json")); string(b) != "nope" {
		t.Fatalf("the malformed config was rewritten: %q", b)
	}
}

// TestRow9SkipsM0AndTheMigrateNote: a lost registry is no migration. With
// a stale queues/ directory in the fence's place, Ensure re-places the
// fence under a "refence" note (never "migrate"), prints no PATH warning,
// moves the directory to strays/ and fails closed.
func TestRow9SkipsM0AndTheMigrateNote(t *testing.T) {
	state := t.TempDir()
	if _, _, err := ensure(t, state); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(RegistryPath(state)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lane.QueuesDir(state)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(lane.QueuesDir(state), "stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	pathDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pathDir, "incoda"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	pathChecked.Store(false)
	defer pathChecked.Store(true)
	var ops []string
	renameDir = func(from, to string) error {
		if n, ok := ReadNote(state); ok {
			ops = append(ops, n.Op)
		}
		return os.Rename(from, to)
	}
	defer func() { renameDir = os.Rename }()
	var errBuf bytes.Buffer
	_, err := Ensure(state, Options{Start: time.Now(), Wait: 10 * time.Second, Poll: 20 * time.Millisecond,
		Stderr: &errBuf, Path: pathDir, Exe: os.Args[0]})
	var se *StateError
	if !errors.As(err, &se) || se.Msg != "machine-state: machine.json: missing while lanes/ exists; run incoda doctor" {
		t.Fatalf("want the lost-registry refusal, got %v", err)
	}
	if strings.Contains(errBuf.String(), "upgrade-warning") {
		t.Fatalf("a lost registry must not run M0:\n%s", errBuf.String())
	}
	if len(ops) != 1 || ops[0] != "refence" {
		t.Fatalf("machine.lock note ops during the re-fence: %v", ops)
	}
	if !FencePlaced(state) {
		t.Fatal("the fence must be back")
	}
	batches, _ := os.ReadDir(StraysDir(state))
	if len(batches) != 1 {
		t.Fatalf("strays: %v", batches)
	}
	if _, err := os.Stat(filepath.Join(StraysDir(state), batches[0].Name(), "stale")); err != nil {
		t.Fatal(err)
	}
}

// TestEmptyDirRaceSendsTheFirstOldRunToStrays: on an empty state directory
// an older incoda's first run creates queues/ between lanes/ and the
// fence. The race rule moves it to strays/, M5 waits for its live ticket,
// and M6 merges the lane into lanes/.
func TestEmptyDirRaceSendsTheFirstOldRunToStrays(t *testing.T) {
	state := t.TempDir()
	var released atomic.Bool
	first := true
	beforePlace = func() {
		if !first {
			return
		}
		first = false
		release := holdTicket(t, lane.QueuesDir(state), "early", 999998, "make")
		go func() {
			time.Sleep(300 * time.Millisecond)
			released.Store(true)
			release()
		}()
	}
	defer func() { beforePlace = func() {} }()
	_, out, err := ensure(t, state)
	if err != nil {
		t.Fatal(err)
	}
	if !released.Load() {
		t.Fatal("the migration committed while the early run was live")
	}
	if !strings.Contains(out, "early pid 999998: make") || !strings.Contains(out, "incoda kill --queue early --pid 999998 --reason 'incoda upgrade'\n") {
		t.Fatalf("M5 must name the early run with its stop line:\n%s", out)
	}
	assertMigrated(t, state, false)
	if !lane.Exists(state, "early") {
		t.Fatal("the early lane was not merged into lanes/")
	}
}

// TestAcquireLockEscapesItsErrors: a state directory path with a control
// character reaches the terminal escaped, never raw.
func TestAcquireLockEscapesItsErrors(t *testing.T) {
	state := filepath.Join(t.TempDir(), "a\x1bb", "missing")
	_, err := AcquireLock(state, LockOptions{Op: "migrate", Start: time.Now(), Wait: time.Second})
	var se *StateError
	if !errors.As(err, &se) || strings.ContainsRune(se.Msg, '\x1b') || !strings.Contains(se.Msg, `a\x1bb`) {
		t.Fatalf("want an escaped open error, got %v", err)
	}
}
