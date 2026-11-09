package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/joblog"
	"github.com/deblasis/incoda/internal/lane"
)

// TestConfigPtyFlagIsRetired: the lane-config pty default is gone from
// the surface (consoles are the run default, --pty=false the per-job
// override), so config rejects --pty outright and its display never
// claims a pty default.
func TestConfigPtyFlagIsRetired(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INCODA_DIR", dir)
	if code := Main([]string{"config", "seed"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("migrate: %d", code)
	}
	var out bytes.Buffer
	var errb bytes.Buffer
	if code := Main([]string{"config", "seed", "--pty"}, &out, &errb); code != ExitUsage {
		t.Fatalf("config --pty should be a usage error now, got %d:\n%s", code, errb.String())
	}
	out.Reset()
	if code := Main([]string{"config", "seed"}, &out, io.Discard); code != 0 {
		t.Fatalf("config seed: %d", code)
	}
	if strings.Contains(out.String(), "pty default") {
		t.Fatalf("config display must not claim a pty default anymore:\n%s", out.String())
	}
}

// TestRunPtyDefaultMatchesPlatform: without a flag, a run takes a console
// on Windows and pipes elsewhere; --pty=false forces pipes per job.
func TestRunPtyDefaultMatchesPlatform(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INCODA_DIR", dir)
	if code := Main([]string{"config", "seed"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("migrate: %d", code)
	}
	relink(t, dir, "seed", "tests")
	run := func(extra ...string) string {
		args := append([]string{"run", "--queue", "seed", "--reason", "pty default"}, extra...)
		if runtime.GOOS == "windows" {
			args = append(args, "--", "cmd", "/c", "exit", "0")
		} else {
			args = append(args, "--", "true")
		}
		if code := Main(args, io.Discard, io.Discard); code != 0 {
			t.Fatalf("run %v: %d", extra, code)
		}
		b, err := os.ReadFile(lane.LogPath(lane.LaneDir(dir, "seed")))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	logDefault := run()
	want := map[bool]int{true: 1, false: 0}[runtime.GOOS == "windows"]
	if got := strings.Count(logDefault, "pty=true"); got != want {
		t.Fatalf("default run should enqueue pty=true only on Windows; log:\n%s", logDefault)
	}
	logPipes := run("--pty=false")
	if strings.Contains(logPipes[len(logDefault):], "pty=true") {
		t.Fatalf("--pty=false must keep the job on pipes:\n%s", logPipes)
	}
}

// TestRunPtyRefusesOffWindows: an explicit --pty on a system without
// ConPTY is refused with a named reason before any ticket is taken (the
// platform default there is pipes, so only the flag can ask).
func TestRunPtyRefusesOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the refusal is the non-Windows behavior")
	}
	dir := t.TempDir()
	t.Setenv("INCODA_DIR", dir)
	if code := Main([]string{"config", "seed"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("migrate: %d", code)
	}
	var stderr bytes.Buffer
	code := Main([]string{"run", "--queue", "seed", "--pty", "--", "true"}, io.Discard, &stderr)
	if code != ExitUsage || !strings.Contains(stderr.String(), "not implemented") {
		t.Fatalf("want 120 naming the missing implementation, got %d:\n%s", code, stderr.String())
	}
}

// TestRunPtyRecordsJobExit: a pty run records how the job ended beside its
// capture. The ticket is released with the run, so this sidecar is what a
// viewer that was watching the pid reads once the process is gone.
func TestRunPtyRecordsJobExit(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("pty mode is Windows-only")
	}
	dir := t.TempDir()
	t.Setenv("INCODA_DIR", dir)
	if code := Main([]string{"config", "seed"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("migrate: %d", code)
	}
	relink(t, dir, "seed", "tests")
	if code := Main([]string{"run", "--queue", "seed", "--reason", "exit code", "--", "cmd", "/c", "exit", "7"}, io.Discard, io.Discard); code != 7 {
		t.Fatalf("run must pass the child's code through, got %d", code)
	}
	files, err := filepath.Glob(filepath.Join(joblog.Dir(lane.LaneDir(dir, "seed")), "*.out"))
	if err != nil || len(files) != 1 {
		t.Fatalf("want one job file, got %v (%v)", files, err)
	}
	code, ok := joblog.ReadExit(files[0])
	if !ok || code != 7 {
		t.Fatalf("exit sidecar reads %d/%v, want 7/true", code, ok)
	}
}
