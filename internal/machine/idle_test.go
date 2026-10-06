package machine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/lockfile"
	"github.com/deblasis/incoda/internal/procinfo"
)

// holdTicket makes root/key look like a lane of an older incoda with one
// live ticket, the way its Enroll leaves it: registry.lock first, then a
// ticket whose lock this test process holds, with pid and cmd in the
// payload and the name. The returned func releases the lock and leaves a
// dead ticket behind, as a killed holder would.
func holdTicket(t *testing.T, root, key string, pid int, cmd ...string) func() {
	t.Helper()
	dir := filepath.Join(root, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	reg, err := lockfile.Open(lane.RegistryLockPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	reg.Close()
	name := fmt.Sprintf("%020d-%d.ticket", time.Now().UnixNano(), pid)
	lf, err := lockfile.Open(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := lf.TryLock(); !ok || err != nil {
		t.Fatalf("lock ticket: %v %v", ok, err)
	}
	b, _ := json.Marshal(lane.Ticket{PID: pid, Queue: key, Slots: 1, Command: cmd})
	if err := lf.Truncate(b); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(func() { lf.Close() }) }
	t.Cleanup(release)
	return release
}

func takeLock(t *testing.T, state string) *Lock {
	t.Helper()
	lk, err := AcquireLock(state, LockOptions{Op: "migrate", Start: time.Now(), Wait: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lk.Release)
	return lk
}

// specWait is the wait text of spec 3.3. The stop lines are rendered for the
// user's shell, so they are built by KillLine rather than pasted as POSIX text.
var specWait = "incoda: upgrade-wait: state upgrade waits for 2 run(s) by an older incoda:\n" +
	"incoda:   builds pid 4711: zig build -Denable-llvm\n" +
	"incoda:   kungfoo-ui pid 5120: just ui\n" +
	"incoda: ask the user before stopping another session's job; they can run:\n" +
	"incoda:   " + KillLine("builds", 4711, UpgradeReason, false) + "\n" +
	"incoda:   " + KillLine("kungfoo-ui", 5120, UpgradeReason, false) + "\n" +
	"incoda: do not force-release them: the job keeps running and the upgrade would overlap it.\n"

func TestFindBlockersM2(t *testing.T) {
	state := t.TempDir()
	q := lane.QueuesDir(state)
	holdTicket(t, q, "kungfoo-ui", 5120, "just", "ui")
	holdTicket(t, q, "builds", 4711, "zig", "build", "-Denable-llvm")
	holdTicket(t, q, "idle", 6000, "x")() // released at once: a dead ticket
	bs, err := findBlockers(state, phaseM2, soon())
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 2 || bs[0] != (Blocker{Key: "builds", PID: 4711, Command: "zig build -Denable-llvm"}) || bs[1].Key != "kungfoo-ui" {
		t.Fatalf("blockers %+v", bs)
	}
	entries, _ := os.ReadDir(filepath.Join(q, "idle"))
	if len(entries) != 2 {
		t.Fatal("the probe must not reap the dead ticket")
	}
}

func TestWaitIdlePrintsTheSpecTextAndNotesBlockers(t *testing.T) {
	state := t.TempDir()
	q := lane.QueuesDir(state)
	rb := holdTicket(t, q, "builds", 4711, "zig", "build", "-Denable-llvm")
	ru := holdTicket(t, q, "kungfoo-ui", 5120, "just", "ui")
	lk := takeLock(t, state)
	noted := make(chan Note, 1)
	go func() {
		for {
			if n, ok := ReadNote(state); ok && len(n.Blockers) == 2 {
				noted <- n
				rb()
				ru()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	var errBuf bytes.Buffer
	if err := waitIdle(state, lk, Options{Start: time.Now(), Wait: time.Minute, Poll: 50 * time.Millisecond, Stderr: &errBuf}, phaseM2); err != nil {
		t.Fatal(err)
	}
	n := <-noted
	if n.Op != "migrate" || n.Blockers[0] != (Blocker{Key: "builds", PID: 4711}) {
		t.Fatalf("note while waiting: %+v", n)
	}
	if errBuf.String() != specWait {
		t.Fatalf("upgrade-wait text:\n%s\nwant:\n%s", errBuf.String(), specWait)
	}
	if n, _ := ReadNote(state); len(n.Blockers) != 0 {
		t.Fatalf("blockers must leave the note once idle: %+v", n)
	}
}

func TestWaitIdleTimesOut(t *testing.T) {
	state := t.TempDir()
	holdTicket(t, lane.QueuesDir(state), "builds", 4711, "zig", "build")
	lk := takeLock(t, state)
	err := waitIdle(state, lk, Options{Start: time.Now(), Wait: 200 * time.Millisecond, Poll: 50 * time.Millisecond}, phaseM2)
	var to *Timeout
	if !errors.As(err, &to) {
		t.Fatalf("want a Timeout, got %v", err)
	}
	lines := strings.Split(to.Msg, "\nincoda: ")
	if lines[0] != "upgrade-timeout: state upgrade still waits for 1 run(s) by an older incoda after 200ms:" ||
		lines[1] != "  builds pid 4711: zig build" ||
		lines[len(lines)-1] != "upgrade the older incoda on PATH; see incoda doctor" {
		t.Fatalf("upgrade-timeout text:\n%s", to.Msg)
	}
}

func TestWaitIdleRefusesAnAncestorHolder(t *testing.T) {
	state := t.TempDir()
	holdTicket(t, lane.QueuesDir(state), "builds", 4711, "zig", "build")
	lk := takeLock(t, state)
	var errBuf bytes.Buffer
	err := waitIdle(state, lk, Options{Start: time.Now(), Wait: time.Minute, Stderr: &errBuf,
		Chain: procinfo.Chain{PIDs: []int{4711, 1}}}, phaseM2)
	var rf *Refusal
	if !errors.As(err, &rf) || rf.Msg != `upgrade-blocked: an older incoda (pid 4711, an ancestor of this process) holds "builds"; rerun the outer command after it exits` {
		t.Fatalf("want upgrade-blocked, got %v", err)
	}
	if errBuf.Len() != 0 {
		t.Fatalf("an upgrade-blocked run prints no wait block:\n%s", errBuf.String())
	}
	// No ancestry walk on Windows: the same holder is waited for instead.
	err = waitIdle(state, lk, Options{Start: time.Now(), Wait: 0, Chain: procinfo.Chain{Skip: true, PIDs: []int{4711}}}, phaseM2)
	var to *Timeout
	if !errors.As(err, &to) {
		t.Fatalf("a Skip chain waits, got %v", err)
	}
}

func TestWaitIdleM5ProbesLanesAndStrays(t *testing.T) {
	state := t.TempDir()
	holdTicket(t, lane.LanesDir(state), "slip", 6001, "just", "gate")
	holdTicket(t, filepath.Join(StraysDir(state), "1727853243000000000"), "late", 6002, "make")
	lk := takeLock(t, state)
	var errBuf bytes.Buffer
	err := waitIdle(state, lk, Options{Start: time.Now(), Wait: 0, Stderr: &errBuf}, phaseM5)
	var to *Timeout
	if !errors.As(err, &to) {
		t.Fatalf("want a Timeout, got %v", err)
	}
	out := errBuf.String()
	for _, want := range []string{
		"incoda: upgrade-wait: state upgrade waits for 2 run(s) by an older incoda:\n",
		"incoda:   late pid 6002: make\n",
		"incoda:   slip pid 6001: just gate\n",
		"incoda:   " + KillLine("late", 6002, UpgradeReason, false) + "\n",
		"incoda:   " + KillLine("slip", 6001, UpgradeReason, false) + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}
