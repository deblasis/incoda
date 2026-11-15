package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func runIncoda(t *testing.T, incoda, state string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(incoda, args...)
	cmd.Env = laneEnv(state)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		code = exitCodeOf(err)
	}
	return string(out), code
}

// testPool is the pool linkTestKeys links test lanes to, widened so that it
// never serialises lanes a test did not mean to put behind one cap.
const testPool = "vm"

// linkTestKeys links each project key to testPool, widened to 64 slots,
// with the user's own command (config --pool). Since spec 4.1 a project
// lane refuses runs until it is linked, so every test that runs on a
// project key links it first; a test about pools picks its pools itself.
func linkTestKeys(t *testing.T, incoda, state string, keys ...string) {
	t.Helper()
	if out, code := runIncoda(t, incoda, state, "config", testPool, "--slots", "64"); code != 0 {
		t.Fatalf("widen %s: %d\n%s", testPool, code, out)
	}
	for _, k := range keys {
		if out, code := runIncoda(t, incoda, state, "config", k, "--pool", testPool); code != 0 {
			t.Fatalf("link %s: %d\n%s", k, code, out)
		}
	}
}

// TestConfigLinkFlags walks config's link flags (spec 4.3): a first link,
// the already-linked no-op, link-exists without --replace, --add-pool,
// --remove-pool (never the last pool), --unlink, --quiet-machine, the
// pool-mismatch refusals, and the echo and event=link log of each change.
func TestConfigLinkFlags(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	cfgOf := func() (pools []string, quiet bool) {
		b, err := os.ReadFile(filepath.Join(laneDir(state, "cap-gate"), "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		var c struct {
			Pools []string `json:"pools"`
			Quiet bool     `json:"quiet_machine"`
		}
		if err := json.Unmarshal(b, &c); err != nil {
			t.Fatal(err)
		}
		return c.Pools, c.Quiet
	}
	step := func(wantCode int, want string, args ...string) string {
		t.Helper()
		out, code := runIncoda(t, incoda, state, append([]string{"config", "cap-gate"}, args...)...)
		if code != wantCode || !strings.Contains(out, want) {
			t.Fatalf("config cap-gate %s: want exit %d and %q, got %d:\n%s", strings.Join(args, " "), wantCode, want, code, out)
		}
		return out
	}
	out := step(0, "link: (none) -> tests\n", "--pool", "tests")
	if !strings.Contains(out, "  kind: project\n  pools: tests\n  quiet machine: no\n") {
		t.Fatalf("config shows the link:\n%s", out)
	}
	step(0, "incoda: already linked: cap-gate -> tests\n", "--pool", "tests")
	step(120, `incoda: link-exists: "cap-gate" is linked to tests; changing a link is the user's call: ask them`, "--pool", "builds")
	step(0, "link: tests -> builds\n", "--pool", "builds", "--replace")
	step(0, "link: builds -> builds,tests\n", "--add-pool", "tests")
	step(0, "incoda: already linked: cap-gate -> builds,tests\n", "--add-pool", "tests,builds")
	step(0, "link: builds,tests -> tests\n", "--remove-pool", "builds")
	step(120, `incoda: --remove-pool would leave "cap-gate" linked to no pool; use --unlink to remove the link`, "--remove-pool", "tests")
	step(120, `incoda: pool-mismatch: "nosuch" is not a pool on this machine (pools: builds, computer-use, tests, vm)`, "--add-pool", "nosuch")
	step(0, "  quiet machine: yes\n", "--quiet-machine")
	if pools, quiet := cfgOf(); strings.Join(pools, ",") != "tests" || !quiet {
		t.Fatalf("stored: %v %v", pools, quiet)
	}
	step(0, "  quiet machine: no\n", "--quiet-machine=false")
	step(0, "link: tests -> (none)\n", "--unlink")
	step(0, "incoda: already unlinked: cap-gate\n", "--unlink")
	step(0, "  pools: (none: unlinked, runs are refused until it is linked)\n")
	step(120, "--pool sets the whole link", "--pool", "tests", "--unlink")
	step(120, "--replace goes with --pool", "--replace")
	out, code := runIncoda(t, incoda, state, "config", "builds", "--pool", "tests")
	if code != 120 || !strings.Contains(out, `incoda: pool-mismatch: "builds" is a pool; a pool never links other pools and carries no quiet_machine`) {
		t.Fatalf("a pool never links: %d\n%s", code, out)
	}
	out, code = runIncoda(t, incoda, state, "config", "builds")
	if code != 0 || !strings.Contains(out, "  kind: pool\n") || strings.Contains(out, "pools:") {
		t.Fatalf("a pool shows its kind and no link: %d\n%s", code, out)
	}
	b, _ := os.ReadFile(filepath.Join(laneDir(state, "cap-gate"), "lane.log"))
	for _, want := range []string{" by=config old= new=tests", " by=config old=tests new=builds", " by=config old=builds new=builds,tests",
		" by=config old=builds,tests new=tests", " by=config old=tests new=\n"} {
		if !strings.Contains(string(b)+"\n", want) {
			t.Fatalf("lane.log lacks %q:\n%s", want, b)
		}
	}
	if n := strings.Count(string(b), " event=link "); n != 5 {
		t.Fatalf("%d event=link lines, want one per change (5):\n%s", n, b)
	}
}

// TestConcurrentConfigFirstLinksNeverEscalate: agents running the same
// printed first link at once all succeed; exactly one writes it.
func TestConcurrentConfigFirstLinksNeverEscalate(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "seed"); code != 0 {
		t.Fatalf("migrate: %d\n%s", code, out)
	}
	var wg sync.WaitGroup
	outs := make([]string, 4)
	codes := make([]int, 4)
	for i := range outs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outs[i], codes[i] = runIncoda(t, incoda, state, "config", "kf-gate", "--pool", "tests")
		}(i)
	}
	wg.Wait()
	linked := 0
	for i := range outs {
		if codes[i] != 0 {
			t.Fatalf("writer %d: exit %d\n%s", i, codes[i], outs[i])
		}
		if strings.Contains(outs[i], "link: (none) -> tests") {
			linked++
		}
	}
	if linked != 1 {
		t.Fatalf("%d writers echoed the link, want 1:\n%s", linked, strings.Join(outs, "\n---\n"))
	}
}

// TestQueueConfigSuppliesSlots: callers should not have to agree on --slots
// by hand. The queue's own config is the default, and three runs that never
// mention slots overlap exactly as the config allows.
func TestQueueConfigSuppliesSlots(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "cfgslots")
	stamps := t.TempDir()

	if out, code := runIncoda(t, incoda, state, "config", "cfgslots", "--slots", "2", "--description", "CPU and RAM"); code != 0 {
		t.Fatalf("config: exit %d\n%s", code, out)
	}
	out, code := runIncoda(t, incoda, state, "config", "cfgslots")
	if code != 0 || !strings.Contains(out, "slots: 2") || !strings.Contains(out, "CPU and RAM") {
		t.Fatalf("config should print what it holds, exit %d:\n%s", code, out)
	}

	const n = 6
	// The hold is long enough that the first two holders must overlap
	// whatever spawn jitter does: the concurrency assertions must depend on
	// the slot count, not on process-startup timing.
	const holdMS = 5000
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			label := fmt.Sprintf("c%d", i)
			cmd := exec.Command(incoda, "run", "--queue", "cfgslots", "--wait", "60s", "--poll", "50ms", "--quiet",
				"--", stamp, filepath.Join(stamps, label+".txt"), label, strconv.Itoa(holdMS))
			cmd.Env = laneEnv(state)
			if out, err := cmd.CombinedOutput(); err != nil {
				errs[i] = fmt.Errorf("child %d: %v\n%s", i, err, out)
			}
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var ivs []interval
	for i := 0; i < n; i++ {
		iv, ok := readInterval(t, filepath.Join(stamps, fmt.Sprintf("c%d.txt", i)))
		if !ok {
			t.Fatalf("child %d left no stamp", i)
		}
		ivs = append(ivs, iv)
	}
	if got, w := maxOverlap(ivs); got != 2 {
		t.Fatalf("max concurrent holders = %d, want exactly 2 from the queue config; %v", got, w)
	}

	// The report carries the config so a watcher can show it.
	rep := statusJSON(t, incoda, state, "cfgslots")
	var raw map[string]any
	cmd := exec.Command(incoda, "status", "--json", "--queue", "cfgslots")
	cmd.Env = laneEnv(state)
	b, _ := cmd.Output()
	_ = json.Unmarshal(b, &raw)
	if rep.EffectiveSlots() != 2 || !strings.Contains(string(b), `"description": "CPU and RAM"`) {
		t.Fatalf("status --json should report the configured slots and description:\n%s", b)
	}
}

func (r report) EffectiveSlots() int {
	if len(r.Queues) == 0 {
		return 0
	}
	return r.Queues[0].EffectiveSlots
}

func TestClosedQueueRefusesRuns(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "old")
	stamps := t.TempDir()

	msg := "retired: use wintty-build for builds and wintty-desktop for harnesses"
	if out, code := runIncoda(t, incoda, state, "config", "old", "--close", msg); code != 0 {
		t.Fatalf("config --close: exit %d\n%s", code, out)
	}
	out, code := runIncoda(t, incoda, state, "run", "--queue", "old", "--", stamp, filepath.Join(stamps, "x.txt"), "x", "10")
	if code != 120 {
		t.Fatalf("run on a closed queue should exit 120, got %d\n%s", code, out)
	}
	if !strings.Contains(out, msg) {
		t.Fatalf("the refusal must carry the closing message, got:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(stamps, "x.txt")); !os.IsNotExist(err) {
		t.Fatal("the command must not have run")
	}
	if out, code := runIncoda(t, incoda, state, "config", "old", "--open"); code != 0 {
		t.Fatalf("config --open: exit %d\n%s", code, out)
	}
	if out, code := runIncoda(t, incoda, state, "run", "--queue", "old", "--quiet", "--", stamp, filepath.Join(stamps, "y.txt"), "y", "10"); code != 0 {
		t.Fatalf("reopened queue should run, exit %d\n%s", code, out)
	}
}

// TestClosedWhileWaiting: a lane closed while a run waits on it ends that
// run on its next poll with closed-while-waiting (exit 120) and nothing
// left behind; the holder already admitted keeps running. A config written
// by a newer incoda while a run waits fails it closed (122). The tests pool
// is used directly, so no link is involved.
func TestClosedWhileWaiting(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	holder, _ := startHolder(t, incoda, stamp, state, "tests", "holder", 4000, "50ms")
	defer func() { _ = holder.Process.Kill(); _ = holder.Wait() }()
	waitFor(t, incoda, state, "tests", func(q queueReport) bool { return len(q.Holders) == 1 })

	wait := func(t *testing.T) (*exec.Cmd, *syncBuffer) {
		t.Helper()
		var out syncBuffer
		w := exec.Command(incoda, "run", "--queue", "tests", "--wait", "60s", "--poll", "50ms",
			"--", stamp, filepath.Join(t.TempDir(), "w.txt"), "w", "10")
		w.Env = laneEnv(state)
		w.Stdout, w.Stderr = &out, &out
		if err := w.Start(); err != nil {
			t.Fatal(err)
		}
		waitFor(t, incoda, state, "tests", func(q queueReport) bool { return len(q.Waiting) == 1 })
		return w, &out
	}
	w, out := wait(t)
	if o, code := runIncoda(t, incoda, state, "config", "tests", "--close", "maintenance"); code != 0 {
		t.Fatalf("close: %d\n%s", code, o)
	}
	if code := exitCodeOf(w.Wait()); code != 120 || !strings.Contains(out.String(), `incoda: closed-while-waiting: "tests": maintenance`) {
		t.Fatalf("want exit 120 closed-while-waiting, got %d:\n%s", code, out.String())
	}
	if n := countTickets(t, state, "tests"); n != 1 {
		t.Fatalf("only the holder's ticket stays, found %d", n)
	}
	if o, code := runIncoda(t, incoda, state, "config", "tests", "--open"); code != 0 {
		t.Fatalf("open: %d\n%s", code, o)
	}

	w, out = wait(t)
	if err := os.WriteFile(filepath.Join(laneDir(state, "tests"), "config.json"), []byte(`{"schema":9,"slots":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := exitCodeOf(w.Wait()); code != 122 || !strings.Contains(out.String(), "incoda: machine-state: ") || !strings.Contains(out.String(), "newer incoda") {
		t.Fatalf("want exit 122 machine-state, got %d:\n%s", code, out.String())
	}
	if err := holder.Wait(); err != nil {
		t.Fatalf("the admitted holder must finish normally: %v", err)
	}
}

func TestRequireReason(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "strict")
	stamps := t.TempDir()

	if out, code := runIncoda(t, incoda, state, "config", "strict", "--require-reason"); code != 0 {
		t.Fatalf("config: exit %d\n%s", code, out)
	}
	out, code := runIncoda(t, incoda, state, "run", "--queue", "strict", "--", stamp, filepath.Join(stamps, "a.txt"), "a", "10")
	if code != 120 || !strings.Contains(out, "--reason") {
		t.Fatalf("a run without --reason on a strict queue should exit 120 naming the flag, got %d:\n%s", code, out)
	}
	if out, code := runIncoda(t, incoda, state, "run", "--queue", "strict", "--reason", "because", "--quiet",
		"--", stamp, filepath.Join(stamps, "b.txt"), "b", "10"); code != 0 {
		t.Fatalf("with a reason it runs, exit %d\n%s", code, out)
	}
}

// TestMultiKeyAcquiresEveryQueue: `--queue b,a` holds both keys for the
// duration, so a job that needs the desktop AND the build capacity can say so
// in one run, and a plain run on either key waits behind it.
func TestMultiKeyAcquiresEveryQueue(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "mk-a", "mk-b")
	stamps := t.TempDir()

	holder := exec.Command(incoda, "run", "--queue", "mk-b,mk-a", "--wait", "60s", "--poll", "50ms", "--quiet",
		"--", stamp, filepath.Join(stamps, "both.txt"), "both", "4000")
	holder.Env = laneEnv(state)
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Process.Kill(); _ = holder.Wait() }()
	waitFor(t, incoda, state, "mk-a", func(q queueReport) bool { return len(q.Holders) == 1 })
	waitFor(t, incoda, state, "mk-b", func(q queueReport) bool { return len(q.Holders) == 1 })

	for _, key := range []string{"mk-a", "mk-b"} {
		out, code := runIncoda(t, incoda, state, "run", "--queue", key, "--wait", "0",
			"--", stamp, filepath.Join(stamps, key+".txt"), key, "10")
		if code != 121 {
			t.Fatalf("a run on %s should wait behind the multi-key holder (121), got %d\n%s", key, code, out)
		}
	}
	if err := holder.Wait(); err != nil {
		t.Fatalf("multi-key holder failed: %v", err)
	}
	if countTickets(t, state, "mk-a")+countTickets(t, state, "mk-b") != 0 {
		t.Fatal("multi-key tickets were not all released")
	}
	// The same key twice, and a key with a bad name, are usage errors.
	if _, code := runIncoda(t, incoda, state, "run", "--queue", "mk-a,mk-a", "--", stamp, filepath.Join(stamps, "d.txt"), "d", "10"); code != 120 {
		t.Fatalf("duplicate keys should be a usage error, got %d", code)
	}
	if _, code := runIncoda(t, incoda, state, "run", "--queue", "mk-a,bad key", "--", stamp, filepath.Join(stamps, "e.txt"), "e", "10"); code != 120 {
		t.Fatalf("an invalid key in the list should be a usage error, got %d", code)
	}
}

// TestExclusiveRunWaitsForAnEmptyQueue proves --exclusive from the outside:
// a two-slot queue with one holder would admit a plain run, and refuses the
// exclusive one until the holder leaves.
func TestExclusiveRunWaitsForAnEmptyQueue(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "excl")
	stamps := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "excl", "--slots", "2"); code != 0 {
		t.Fatalf("config: %s", out)
	}
	holder := exec.Command(incoda, "run", "--queue", "excl", "--quiet", "--poll", "50ms",
		"--", stamp, filepath.Join(stamps, "h.txt"), "h", "2500")
	holder.Env = laneEnv(state)
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Wait() }()
	waitFor(t, incoda, state, "excl", func(q queueReport) bool { return len(q.Holders) == 1 })

	out, code := runIncoda(t, incoda, state, "run", "--queue", "excl", "--exclusive", "--wait", "0",
		"--", stamp, filepath.Join(stamps, "x.txt"), "x", "10")
	if code != 121 {
		t.Fatalf("exclusive run should not join a holder, got %d\n%s", code, out)
	}
	out, code = runIncoda(t, incoda, state, "run", "--queue", "excl", "--exclusive", "--wait", "30s", "--poll", "50ms", "--quiet",
		"--", stamp, filepath.Join(stamps, "x.txt"), "x", "10")
	if code != 0 {
		t.Fatalf("exclusive run should acquire once the queue drains, got %d\n%s", code, out)
	}
	hv, _ := readInterval(t, filepath.Join(stamps, "h.txt"))
	xv, _ := readInterval(t, filepath.Join(stamps, "x.txt"))
	if xv.enter < hv.exit {
		t.Fatalf("exclusive job entered at %d before the holder left at %d", xv.enter, hv.exit)
	}
	_ = strconv.Itoa
}

// TestExclusiveComesAfterTheJobsAheadOfIt: --exclusive is a barrier at its
// turn, not a queue-wide pause. Four holders fill a four-slot queue; a plain
// job and then an exclusive one queue behind them, and a second plain job
// queues behind the exclusive one. The plain job ahead of the exclusive one
// starts as soon as one of the short holders leaves, while the exclusive one
// waits for the queue to drain to zero; nothing joins it, and the job behind
// it starts only after it leaves.
func TestExclusiveComesAfterTheJobsAheadOfIt(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "excl-order")
	if out, code := runIncoda(t, incoda, state, "config", "excl-order", "--slots", "4"); code != 0 {
		t.Fatalf("config: %s", out)
	}
	stamps := t.TempDir()

	start := func(label string, holdMS int, exclusive, quiet bool, stderr *bytes.Buffer) *exec.Cmd {
		t.Helper()
		args := []string{"run", "--queue", "excl-order", "--wait", "60s", "--poll", "50ms"}
		if quiet {
			args = append(args, "--quiet")
		}
		if exclusive {
			args = append(args, "--exclusive")
		}
		args = append(args, "--", stamp, filepath.Join(stamps, label+".txt"), label, strconv.Itoa(holdMS))
		cmd := exec.Command(incoda, args...)
		cmd.Env = laneEnv(state)
		if stderr != nil {
			cmd.Stderr = stderr
		}
		if err := cmd.Start(); err != nil {
			t.Fatalf("start %s: %v", label, err)
		}
		return cmd
	}
	waitLabel := func(label string) {
		t.Helper()
		waitFor(t, incoda, state, "excl-order", func(q queueReport) bool {
			for _, list := range [][]entry{q.Holders, q.Waiting} {
				for _, e := range list {
					for _, a := range e.Ticket.Command {
						if a == label {
							return true
						}
					}
				}
			}
			return false
		})
	}

	var cmds []*exec.Cmd
	defer func() {
		for _, c := range cmds {
			_ = c.Process.Kill()
			_ = c.Wait()
		}
	}()

	// h0 is the slowest holder; the other three leave early enough that a
	// free slot appears while h0 still holds, which is exactly the slot the
	// plain job ahead of the exclusive one takes. The holders start together:
	// only E, X and F need a known order.
	for i, holdMS := range []int{20000, 4000, 4000, 4000} {
		cmds = append(cmds, start(fmt.Sprintf("h%d", i), holdMS, false, true, nil))
	}
	waitFor(t, incoda, state, "excl-order", func(q queueReport) bool { return len(q.Holders) == 4 })
	cmds = append(cmds, start("e", 6000, false, true, nil))
	waitLabel("e")
	xcmd := start("x", 5000, true, true, nil)
	cmds = append(cmds, xcmd)
	waitLabel("x")
	// f arrives behind the exclusive request, so its busy line must name the
	// exclusive one as the barrier and never call it a holder.
	var fbuf bytes.Buffer
	cmds = append(cmds, start("f", 10, false, false, &fbuf))
	waitLabel("f")

	// While x holds, the compact report says the lane admits nobody, and both
	// text surfaces carry the marker so a free-looking fraction is explained.
	waitForWithin(t, incoda, state, "excl-order", 60*time.Second, func(q queueReport) bool {
		return q.ExclusiveHolder && len(q.Holders) == 1 && q.Holders[0].Ticket.PID == xcmd.Process.Pid
	})
	st := exec.Command(incoda, "status", "--no-color", "--queue", "excl-order")
	st.Env = laneEnv(state)
	sout, err := st.Output()
	if err != nil {
		t.Fatalf("status: %v\n%s", err, sout)
	}
	if !strings.Contains(string(sout), "slot(s) held (exclusive)") {
		t.Fatalf("status did not mark the exclusive holder:\n%s", sout)
	}
	qs := exec.Command(incoda, "queues", "--no-color")
	qs.Env = laneEnv(state)
	qout, err := qs.Output()
	if err != nil {
		t.Fatalf("queues: %v\n%s", err, qout)
	}
	if !strings.Contains(string(qout), "held (exclusive)") {
		t.Fatalf("queues did not mark the exclusive holder:\n%s", qout)
	}

	for _, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	iv := func(label string) interval {
		t.Helper()
		v, ok := readInterval(t, filepath.Join(stamps, label+".txt"))
		if !ok {
			t.Fatalf("%s left no finished stamp", label)
		}
		return v
	}
	h0, e, x, f := iv("h0"), iv("e"), iv("x"), iv("f")

	if e.enter >= h0.exit {
		t.Fatalf("plain job entered at %d, not before the slowest holder left at %d: the exclusive request still paused the queue", e.enter, h0.exit)
	}
	if x.enter < h0.exit {
		t.Fatalf("exclusive job entered at %d before the slowest holder left at %d", x.enter, h0.exit)
	}
	if x.enter < e.exit {
		t.Fatalf("exclusive job entered at %d before the job ahead of it left at %d", x.enter, e.exit)
	}
	if f.enter < x.exit {
		t.Fatalf("job behind the exclusive one entered at %d before it left at %d", f.enter, x.exit)
	}

	// The waiting job behind x saw the barrier named and never saw x called
	// a holder; with the pre-change loop (first N entries printed as
	// holders) the second assertion fails.
	busy := fbuf.String()
	if !strings.Contains(busy, "holder pid") {
		t.Fatalf("a waiting job should name the holders ahead of it:\n%s", busy)
	}
	if !strings.Contains(busy, fmt.Sprintf("exclusive pid %d ahead", xcmd.Process.Pid)) {
		t.Fatalf("the wait line should name the exclusive request ahead (pid %d):\n%s", xcmd.Process.Pid, busy)
	}
	if strings.Contains(busy, fmt.Sprintf("holder pid %d", xcmd.Process.Pid)) {
		t.Fatalf("a waiting exclusive request was printed as a holder:\n%s", busy)
	}
}

// TestConfigRefusesControlCharacters: descriptions and closed texts are
// shown in status and in refusals, so a write path must refuse anything
// that repaints a terminal or reorders text, and anything over 200
// characters, before it writes (spec 4.6). One row per write path.
func TestConfigRefusesControlCharacters(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"config", "badtext", "--description", "red\x1b[31m"}, "incoda: bad-text: description contains control characters"},
		{[]string{"config", "badtext", "--description", "a\u202eb"}, "incoda: bad-text: description contains control characters"},
		{[]string{"config", "badtext", "--close", "two\nlines"}, "incoda: bad-text: closed contains control characters"},
		{[]string{"config", "badtext", "--close", "tab\there"}, "incoda: bad-text: closed contains control characters"},
		{[]string{"config", "badtext", "--description", strings.Repeat("x", 201)}, "incoda: bad-text: description is longer than 200 characters"},
	} {
		out, code := runIncoda(t, incoda, state, c.args...)
		if code != 120 || !strings.Contains(out, c.want) {
			t.Errorf("%q: want exit 120 and %q, got %d:\n%s", c.args, c.want, code, out)
		}
	}
	if _, err := os.Stat(laneDir(state, "badtext")); !os.IsNotExist(err) {
		t.Fatal("a refused write must leave nothing behind")
	}
}

// TestNewerConfigSchemaRefusesRuns: a config written by a newer incoda may
// carry rules this binary does not know, so it fails closed.
func TestNewerConfigSchemaRefusesRuns(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "newer", "--slots", "1"); code != 0 {
		t.Fatalf("config: %d\n%s", code, out)
	}
	path := filepath.Join(laneDir(state, "newer"), "config.json")
	if err := os.WriteFile(path, []byte(`{"schema":9,"slots":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runIncoda(t, incoda, state, "run", "--queue", "newer", "--", stamp, filepath.Join(t.TempDir(), "x"), "x", "1")
	if code != 122 || !strings.Contains(out, "incoda: machine-state:") || !strings.Contains(out, "newer incoda") {
		t.Fatalf("want exit 122 machine-state, got %d:\n%s", code, out)
	}
}

// TestLogLineStaysOneLine: a command word with a newline must not split a
// lane.log event into two lines.
func TestLogLineStaysOneLine(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	linkTestKeys(t, incoda, state, "oneline")
	marker := filepath.Join(t.TempDir(), "m.txt")
	if out, code := runIncoda(t, incoda, state, "run", "--queue", "oneline", "--quiet", "--", stamp, marker, "a\nb", "1"); code != 0 {
		t.Fatalf("run: exit %d\n%s", code, out)
	}
	b, err := os.ReadFile(filepath.Join(laneDir(state, "oneline"), "lane.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if !strings.Contains(line, "queue=oneline event=") {
			t.Fatalf("a log event was split across lines:\n%s", b)
		}
	}
}

// TestConfigEchoEscapesAHandEditedLink: a link read back from a
// hand-edited config.json is printed escaped by config's link: echo and
// its link-exists refusal.
func TestConfigEchoEscapesAHandEditedLink(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "seed"); code != 0 {
		t.Fatalf("migrate: %d\n%s", code, out)
	}
	dir := laneDir(state, "cap-gate")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"schema":2,"pools":["t\u001bx"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runIncoda(t, incoda, state, "config", "cap-gate", "--pool", "tests")
	if code != 120 || !strings.Contains(out, `link-exists: "cap-gate" is linked to t\x1bx;`) || strings.Contains(out, "\x1b") {
		t.Fatalf("link-exists: %d %q", code, out)
	}
	out, code = runIncoda(t, incoda, state, "config", "cap-gate", "--pool", "tests", "--replace")
	if code != 0 || !strings.Contains(out, `link: t\x1bx -> tests`) || strings.Contains(out, "\x1b") {
		t.Fatalf("link echo: %d %q", code, out)
	}
}
