// Integration tests drive the real incoda binary as separate OS processes.
//
// That is the point: incoda's guarantee is enforced by kernel file locks
// between processes, so anything tested inside one process would prove nothing
// about the property that matters.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	buildOnce sync.Once
	incodaBin string
	stampBin  string
	buildErr  error
	binDir    string
)

func binaries(t *testing.T) (string, string) {
	t.Helper()
	buildOnce.Do(func() {
		binDir, buildErr = os.MkdirTemp("", "incoda-bin-")
		if buildErr != nil {
			return
		}
		ext := ""
		if runtime.GOOS == "windows" {
			ext = ".exe"
		}
		incodaBin = filepath.Join(binDir, "incoda"+ext)
		stampBin = filepath.Join(binDir, "stamp"+ext)
		for _, b := range []struct{ out, pkg string }{
			{incodaBin, "."},
			{stampBin, "./internal/testprog/stamp"},
		} {
			cmd := exec.Command("go", "build", "-o", b.out, b.pkg)
			cmd.Env = append(os.Environ(), "GOTOOLCHAIN=auto")
			if out, err := cmd.CombinedOutput(); err != nil {
				buildErr = fmt.Errorf("build %s: %v\n%s", b.pkg, err, out)
				return
			}
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return incodaBin, stampBin
}

// laneEnv points a child at an isolated state directory and clears anything
// inherited that could steer it somewhere else.
func laneEnv(stateDir string) []string {
	env := []string{}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(k) {
		case "INCODA_DIR", "INCODA_QUEUE":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "INCODA_DIR="+stateDir)
}

type interval struct {
	label string
	enter int64
	exit  int64
}

func readInterval(t *testing.T, path string) (interval, bool) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return interval{}, false
	}
	defer f.Close()
	iv := interval{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), " ")
		if !ok {
			continue
		}
		switch k {
		case "label":
			iv.label = v
		case "enter":
			iv.enter, _ = strconv.ParseInt(v, 10, 64)
		case "exit":
			iv.exit, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	return iv, iv.enter != 0 && iv.exit != 0
}

// maxOverlap returns the largest number of intervals that were simultaneously
// open, using a sweep over the endpoints.
func maxOverlap(ivs []interval) (int, [][2]string) {
	type ev struct {
		at    int64
		delta int
		label string
	}
	var evs []ev
	for _, iv := range ivs {
		evs = append(evs, ev{iv.enter, +1, iv.label}, ev{iv.exit, -1, iv.label})
	}
	sort.Slice(evs, func(i, j int) bool {
		if evs[i].at != evs[j].at {
			return evs[i].at < evs[j].at
		}
		// Exits before enters at the same instant: adjacent, non-overlapping
		// holds must not be counted as concurrent.
		return evs[i].delta < evs[j].delta
	})
	cur, best := 0, 0
	open := map[string]bool{}
	var witness [][2]string
	for _, e := range evs {
		if e.delta > 0 {
			for other := range open {
				witness = append(witness, [2]string{other, e.label})
			}
			open[e.label] = true
		} else {
			delete(open, e.label)
		}
		cur += e.delta
		if cur > best {
			best = cur
		}
	}
	return best, witness
}

// TestMutualExclusionAcrossDifferentWorkingDirectories is the headline test.
//
// Every child is launched from a DIFFERENT working directory, because the real
// callers are several agent sessions in separate git worktrees of the same
// repository. A version of this test where all children shared one cwd would
// still pass if the state directory were accidentally made cwd-relative, so the
// differing directories are the assertion, not incidental setup.
func TestMutualExclusionAcrossDifferentWorkingDirectories(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	const n = 5
	const holdMS = 400

	var wg sync.WaitGroup
	cwds := make([]string, n)
	for i := 0; i < n; i++ {
		cwd := filepath.Join(t.TempDir(), fmt.Sprintf("worktree-%d", i))
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
		cwds[i] = cwd
	}

	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			label := fmt.Sprintf("p%d", i)
			cmd := exec.Command(incoda, "run", "--queue", "shared", "--wait", "60s",
				"--poll", "50ms", "--quiet", "--", stamp,
				filepath.Join(stamps, label+".txt"), label, strconv.Itoa(holdMS))
			cmd.Dir = cwds[i]
			cmd.Env = laneEnv(state)
			out, err := cmd.CombinedOutput()
			if err != nil {
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
		iv, ok := readInterval(t, filepath.Join(stamps, fmt.Sprintf("p%d.txt", i)))
		if !ok {
			t.Fatalf("child %d left no complete stamp; it never entered the lane", i)
		}
		ivs = append(ivs, iv)
	}
	if len(ivs) != n {
		t.Fatalf("got %d intervals, want %d", len(ivs), n)
	}
	got, witness := maxOverlap(ivs)
	if got != 1 {
		t.Fatalf("max concurrent holders = %d, want 1; overlapping pairs: %v", got, witness)
	}

	// Sanity: the run really was contended, otherwise "no overlap" is vacuous.
	first, last := ivs[0].enter, ivs[0].exit
	for _, iv := range ivs {
		if iv.enter < first {
			first = iv.enter
		}
		if iv.exit > last {
			last = iv.exit
		}
	}
	total := last - first
	if total < int64(n)*int64(holdMS)*int64(time.Millisecond)*8/10 {
		t.Fatalf("total elapsed %v is too short for %d serialised %dms holds; the children may not have contended", time.Duration(total), n, holdMS)
	}
}

// TestSlotsAllowExactlyN proves --slots generalises past mutual exclusion: two
// holders may overlap, three never may.
func TestSlotsAllowExactlyN(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	const n = 6
	const slots = 2
	const holdMS = 500

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			label := fmt.Sprintf("s%d", i)
			cmd := exec.Command(incoda, "run", "--queue", "twolane",
				"--slots", strconv.Itoa(slots), "--wait", "60s", "--poll", "50ms", "--quiet",
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
		iv, ok := readInterval(t, filepath.Join(stamps, fmt.Sprintf("s%d.txt", i)))
		if !ok {
			t.Fatalf("child %d left no complete stamp", i)
		}
		ivs = append(ivs, iv)
	}
	got, witness := maxOverlap(ivs)
	if got > slots {
		t.Fatalf("max concurrent holders = %d, want at most %d; overlapping pairs: %v", got, slots, witness)
	}
	if got < slots {
		t.Fatalf("max concurrent holders = %d; --slots %d should have let %d overlap, so the slot count is not being honoured", got, slots, slots)
	}
}

// TestHardKilledHolderFreesTheLane is the headline claim over build-lane: a
// holder that dies without running any cleanup must not wedge the queue, and no
// force-release should be needed.
func TestHardKilledHolderFreesTheLane(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	victim := exec.Command(incoda, "run", "--queue", "crash", "--wait", "60s", "--poll", "50ms", "--quiet",
		"--", stamp, filepath.Join(stamps, "victim.txt"), "victim", "60000")
	victim.Env = laneEnv(state)
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}

	// Wait until the victim genuinely holds the lane.
	deadline := time.Now().Add(20 * time.Second)
	for {
		rep := statusJSON(t, incoda, state, "crash")
		if len(rep.Queues) == 1 && len(rep.Queues[0].Holders) == 1 {
			break
		}
		if time.Now().After(deadline) {
			_ = victim.Process.Kill()
			t.Fatal("victim never acquired the lane")
		}
		time.Sleep(50 * time.Millisecond)
	}

	ticketsBefore := countTickets(t, state, "crash")
	if ticketsBefore != 1 {
		t.Fatalf("want 1 ticket on disk, got %d", ticketsBefore)
	}

	// Kill without any chance to clean up: TerminateProcess on Windows,
	// SIGKILL on Unix. Nothing incoda wrote gets to run.
	if err := victim.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = victim.Wait()

	// The ticket file is still on disk. That is expected and is the point: its
	// OS lock is gone, so the next scan reaps it.
	if n := countTickets(t, state, "crash"); n != 1 {
		t.Logf("note: %d ticket file(s) on disk right after the kill (expected 1, reaped lazily)", n)
	}

	start := time.Now()
	next := exec.Command(incoda, "run", "--queue", "crash", "--wait", "20s", "--poll", "50ms", "--quiet",
		"--", stamp, filepath.Join(stamps, "next.txt"), "next", "10")
	next.Env = laneEnv(state)
	out, err := next.CombinedOutput()
	if err != nil {
		t.Fatalf("the next acquirer should have taken the lane with no force-release: %v\n%s", err, out)
	}
	if el := time.Since(start); el > 10*time.Second {
		t.Fatalf("next acquirer took %s; the dead holder was not reaped promptly", el)
	}
	if _, ok := readInterval(t, filepath.Join(stamps, "next.txt")); !ok {
		t.Fatal("the next acquirer never ran its command")
	}
}

// TestFIFOOrder checks the ordering claim. Waiters are launched one at a time
// and each is confirmed to be enrolled (visible in status) before the next is
// started, so the arrival order under test is enrollment order rather than
// process-start scheduling luck.
func TestFIFOOrder(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	holder := exec.Command(incoda, "run", "--queue", "fifo", "--wait", "60s", "--poll", "50ms", "--quiet",
		"--", stamp, filepath.Join(stamps, "holder.txt"), "holder", "3000")
	holder.Env = laneEnv(state)
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Wait() }()
	waitFor(t, incoda, state, "fifo", func(q queueReport) bool { return len(q.Holders) == 1 })

	const n = 4
	procs := make([]*exec.Cmd, n)
	for i := 0; i < n; i++ {
		label := fmt.Sprintf("w%d", i)
		cmd := exec.Command(incoda, "run", "--queue", "fifo", "--wait", "60s", "--poll", "50ms", "--quiet",
			"--", stamp, filepath.Join(stamps, label+".txt"), label, "50")
		cmd.Env = laneEnv(state)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		procs[i] = cmd
		want := i + 1
		waitFor(t, incoda, state, "fifo", func(q queueReport) bool { return len(q.Waiting) >= want })
	}

	// Record the queue order the tool itself reports before anything is served.
	rep := statusJSON(t, incoda, state, "fifo")
	var reported []int
	for _, e := range rep.Queues[0].Waiting {
		reported = append(reported, e.Ticket.PID)
	}
	var launched []int
	for _, p := range procs {
		launched = append(launched, p.Process.Pid)
	}
	if len(reported) != n {
		t.Fatalf("status reports %d waiters, want %d", len(reported), n)
	}
	for i := range launched {
		if reported[i] != launched[i] {
			t.Fatalf("queue order %v does not match enrollment order %v", reported, launched)
		}
	}

	for _, p := range procs {
		if err := p.Wait(); err != nil {
			t.Fatalf("waiter failed: %v", err)
		}
	}

	// Service order must equal enrollment order.
	type served struct {
		label string
		enter int64
	}
	var got []served
	for i := 0; i < n; i++ {
		label := fmt.Sprintf("w%d", i)
		iv, ok := readInterval(t, filepath.Join(stamps, label+".txt"))
		if !ok {
			t.Fatalf("waiter %s left no stamp", label)
		}
		got = append(got, served{label, iv.enter})
	}
	for i := 1; i < len(got); i++ {
		if got[i].enter <= got[i-1].enter {
			t.Fatalf("FIFO violated: %s entered at %d, not after %s at %d (order was %v)",
				got[i].label, got[i].enter, got[i-1].label, got[i-1].enter, got)
		}
	}
}

func TestWaitTimeoutExitCode(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	holder := exec.Command(incoda, "run", "--queue", "busy", "--wait", "60s", "--poll", "50ms", "--quiet",
		"--", stamp, filepath.Join(stamps, "h.txt"), "h", "20000")
	holder.Env = laneEnv(state)
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Process.Kill(); _ = holder.Wait() }()
	waitFor(t, incoda, state, "busy", func(q queueReport) bool { return len(q.Holders) == 1 })

	// A bare integer is read as seconds, the build-lane habit.
	cmd := exec.Command(incoda, "run", "--queue", "busy", "--wait", "1", "--poll", "50ms",
		"--", stamp, filepath.Join(stamps, "late.txt"), "late", "10")
	cmd.Env = laneEnv(state)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected a timeout failure, got success:\n%s", out)
	}
	if code := exitCodeOf(err); code != 121 {
		t.Fatalf("timeout exit code = %d, want 121\n%s", code, out)
	}
	if !strings.Contains(string(out), "still busy") {
		t.Fatalf("timeout message should say the queue is still busy, got:\n%s", out)
	}

	// --wait 0 must fail immediately rather than polling.
	start := time.Now()
	cmd = exec.Command(incoda, "run", "--queue", "busy", "--wait", "0",
		"--", stamp, filepath.Join(stamps, "now.txt"), "now", "10")
	cmd.Env = laneEnv(state)
	out, err = cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("--wait 0 on a busy queue should fail:\n%s", out)
	}
	if code := exitCodeOf(err); code != 121 {
		t.Fatalf("--wait 0 exit code = %d, want 121\n%s", code, out)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("--wait 0 took %s; it should not have polled", el)
	}
}

func TestExitCodePassthrough(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	for _, want := range []int{0, 1, 7, 42} {
		cmd := exec.Command(incoda, "run", "--queue", "codes", "--quiet",
			"--", stamp, filepath.Join(stamps, "c.txt"), "c", "1", strconv.Itoa(want))
		cmd.Env = laneEnv(state)
		out, err := cmd.CombinedOutput()
		got := 0
		if err != nil {
			got = exitCodeOf(err)
		}
		if got != want {
			t.Fatalf("child exit %d came back as %d\n%s", want, got, out)
		}
	}
}

func TestKeyValidationAndMissingKey(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()

	// No --queue and no INCODA_QUEUE must be a usage error, never a silent
	// fallback onto a shared default lane.
	cmd := exec.Command(incoda, "run", "--", "cmd", "/c", "echo", "hi")
	cmd.Env = laneEnv(state)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("run without a queue key should fail:\n%s", out)
	}
	if code := exitCodeOf(err); code != 120 {
		t.Fatalf("missing key exit code = %d, want 120\n%s", code, out)
	}
	if !strings.Contains(string(out), "no default queue") {
		t.Fatalf("the error should explain there is no default queue, got:\n%s", out)
	}

	for _, bad := range []string{"..", "a/b", "with space", "nul"} {
		cmd := exec.Command(incoda, "status", "--queue", bad)
		cmd.Env = laneEnv(state)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("key %q should be rejected:\n%s", bad, out)
		}
		if code := exitCodeOf(err); code != 120 {
			t.Fatalf("key %q gave exit %d, want 120\n%s", bad, code, out)
		}
	}

	// INCODA_QUEUE supplies the default; --queue wins over it.
	cmd = exec.Command(incoda, "status", "--json")
	cmd.Env = append(laneEnv(state), "INCODA_QUEUE=fromenv")
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status with INCODA_QUEUE failed: %v\n%s", err, out)
	}
	var rep report
	if err := json.Unmarshal(out, &rep); err != nil {
		t.Fatalf("status --json is not valid JSON: %v\n%s", err, out)
	}
	if len(rep.Queues) != 1 || rep.Queues[0].Key != "fromenv" {
		t.Fatalf("INCODA_QUEUE was not used: %+v", rep.Queues)
	}

	cmd = exec.Command(incoda, "status", "--json", "--queue", "explicit")
	cmd.Env = append(laneEnv(state), "INCODA_QUEUE=fromenv")
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status failed: %v\n%s", err, out)
	}
	rep = report{}
	if err := json.Unmarshal(out, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Queues[0].Key != "explicit" {
		t.Fatalf("--queue should beat INCODA_QUEUE, got %q", rep.Queues[0].Key)
	}
}

func TestStatusOnNeverUsedQueue(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()

	cmd := exec.Command(incoda, "status", "--queue", "brandnew")
	cmd.Env = laneEnv(state)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status on an unused queue must not error: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "never used") {
		t.Fatalf("status should say the queue was never used, got:\n%s", out)
	}
	if !strings.Contains(string(out), state) {
		t.Fatalf("status should print the resolved state dir %q, got:\n%s", state, out)
	}
}

func TestForceReleaseRefusesLiveHolderFromCLI(t *testing.T) {
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()

	holder := exec.Command(incoda, "run", "--queue", "fr", "--wait", "60s", "--poll", "50ms", "--quiet",
		"--", stamp, filepath.Join(stamps, "h.txt"), "h", "20000")
	holder.Env = laneEnv(state)
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Process.Kill(); _ = holder.Wait() }()
	waitFor(t, incoda, state, "fr", func(q queueReport) bool { return len(q.Holders) == 1 })

	cmd := exec.Command(incoda, "force-release", "--queue", "fr")
	cmd.Env = laneEnv(state)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("force-release must refuse a live holder:\n%s", out)
	}
	s := string(out)
	if !strings.Contains(s, "LIVE") || !strings.Contains(s, "--live") {
		t.Fatalf("the refusal must name the live holders and the --live escape, got:\n%s", s)
	}
	if !strings.Contains(s, "collision") {
		t.Fatalf("the refusal must explain WHY (a blind force-release caused a real collision), got:\n%s", s)
	}

	cmd = exec.Command(incoda, "force-release", "--queue", "fr", "--live")
	cmd.Env = laneEnv(state)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("force-release --live should succeed: %v\n%s", err, out)
	}
}

func TestDoctorAndVersionAndQueues(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()

	cmd := exec.Command(incoda, "doctor")
	cmd.Env = laneEnv(state)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, out)
	}
	s := string(out)
	for _, want := range []string{state, "locking: enforced", "cwd-independent: yes", "WARNING: INCODA_DIR is set"} {
		if !strings.Contains(s, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, s)
		}
	}

	cmd = exec.Command(incoda, "version")
	cmd.Env = laneEnv(state)
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "incoda ") {
		t.Fatalf("version: %v\n%s", err, out)
	}

	cmd = exec.Command(incoda, "queues")
	cmd.Env = laneEnv(state)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("queues failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "no queues") {
		t.Fatalf("queues on a fresh state dir should say so:\n%s", out)
	}
}

func TestChildProcessTreeDiesWithIncoda(t *testing.T) {
	// A build tree that outlives its lane holder keeps the machine busy while
	// the lane reports free, which is the exact collision incoda exists to
	// prevent. On Windows the Job Object guarantees this; on Unix the process
	// group only helps for signalled exits, so the assertion is Windows-only
	// and the limitation is documented in the README.
	if runtime.GOOS != "windows" {
		t.Skip("job-object containment is a Windows guarantee; see README known limits")
	}
	incoda, stamp := binaries(t)
	state := t.TempDir()
	stamps := t.TempDir()
	marker := filepath.Join(stamps, "tree.txt")

	victim := exec.Command(incoda, "run", "--queue", "tree", "--quiet",
		"--", stamp, marker, "tree", "8000")
	victim.Env = laneEnv(state)
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, incoda, state, "tree", func(q queueReport) bool { return len(q.Holders) == 1 })
	// Give the stamp child time to write its enter marker.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = victim.Process.Kill()
			t.Fatal("the child never started")
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := victim.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = victim.Wait()

	// The stamp process would rewrite the file with an exit line at +8s if it
	// survived. Wait past that and assert it never did.
	time.Sleep(3 * time.Second)
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "exit ") {
		t.Fatal("the child outlived incoda; the job object did not contain it")
	}
}

// ---- helpers ----

type ticketPayload struct {
	PID     int      `json:"pid"`
	Slots   int      `json:"slots"`
	Command []string `json:"command"`
	Dir     string   `json:"cwd"`
}

type entry struct {
	Ticket ticketPayload `json:"ticket"`
}

type queueReport struct {
	Key            string  `json:"key"`
	Exists         bool    `json:"exists"`
	EffectiveSlots int     `json:"effective_slots"`
	Free           bool    `json:"free"`
	Holders        []entry `json:"holders"`
	Waiting        []entry `json:"waiting"`
}

type report struct {
	Schema         int           `json:"schema"`
	StateDir       string        `json:"state_dir"`
	StateDirSource string        `json:"state_dir_source"`
	Queues         []queueReport `json:"queues"`
}

func statusJSON(t *testing.T, incoda, state, key string) report {
	t.Helper()
	cmd := exec.Command(incoda, "status", "--json", "--queue", key)
	cmd.Env = laneEnv(state)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("status --json: %v\n%s", err, out)
	}
	var rep report
	if err := json.Unmarshal(out, &rep); err != nil {
		t.Fatalf("status --json is not valid JSON: %v\n%s", err, out)
	}
	if rep.Schema != 1 {
		t.Fatalf("unexpected report schema %d", rep.Schema)
	}
	return rep
}

func waitFor(t *testing.T, incoda, state, key string, ok func(queueReport) bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		rep := statusJSON(t, incoda, state, key)
		if len(rep.Queues) == 1 && ok(rep.Queues[0]) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for queue %q to reach the expected state; last: %+v", key, rep.Queues)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func countTickets(t *testing.T, state, key string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(state, "queues", key))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".ticket") {
			n++
		}
	}
	return n
}

func exitCodeOf(err error) int {
	var ee *exec.ExitError
	if ok := asExitError(err, &ee); ok {
		return ee.ExitCode()
	}
	return -1
}

func asExitError(err error, target **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		*target = ee
		return true
	}
	return false
}
