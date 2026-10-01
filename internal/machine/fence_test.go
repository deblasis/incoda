package machine

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/lane"
)

const specFence = "This state directory is managed by incoda >= 0.7 (system pools).\n" +
	"Lanes now live in lanes/. An older incoda stops here with \"not a directory\"\n" +
	"(exit 122) on purpose: it does not know about the machine-wide pools.\n" +
	"Upgrade it: brew upgrade incoda, or the install script at\n" +
	"https://github.com/deblasis/incoda#install (SHA256SUMS-verified).\n" +
	"Do not delete this file: that lets old binaries run jobs outside the pools.\n"

func TestFenceTextIsTheSpecConstant(t *testing.T) {
	if FenceText != specFence {
		t.Fatalf("FenceText drifted from spec 2.3:\n%s", FenceText)
	}
}

func TestFencePlaced(t *testing.T) {
	state := t.TempDir()
	if FencePlaced(state) {
		t.Fatal("missing is not placed")
	}
	if err := os.Mkdir(lane.QueuesDir(state), 0o755); err != nil {
		t.Fatal(err)
	}
	if FencePlaced(state) {
		t.Fatal("a directory is not the fence")
	}
	if err := os.Remove(lane.QueuesDir(state)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lane.QueuesDir(state), []byte("anything"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !FencePlaced(state) {
		t.Fatal("any regular file is the fence, whatever its content")
	}
}

func readFence(t *testing.T, state string) {
	t.Helper()
	b, err := os.ReadFile(lane.QueuesDir(state))
	if err != nil || string(b) != FenceText {
		t.Fatalf("fence not placed: %q %v", b, err)
	}
	if _, err := os.Lstat(fenceNewPath(state)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("queues.new left behind")
	}
}

func TestPlaceFenceOnAnEmptyStateDir(t *testing.T) {
	state := t.TempDir()
	moved, err := placeFence(state)
	if err != nil || len(moved) != 0 {
		t.Fatalf("placeFence = %v, %v", moved, err)
	}
	readFence(t, state)
}

func TestPlaceFenceMovesADirectoryToStrays(t *testing.T) {
	state := t.TempDir()
	if err := os.MkdirAll(filepath.Join(lane.QueuesDir(state), "k"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lane.QueuesDir(state), "k", "lane.log"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	moved, err := placeFence(state)
	if err != nil || len(moved) != 1 {
		t.Fatalf("placeFence = %v, %v", moved, err)
	}
	if !strings.HasPrefix(moved[0], StraysDir(state)+string(filepath.Separator)) {
		t.Fatalf("moved to %s, want under strays/", moved[0])
	}
	if b, _ := os.ReadFile(filepath.Join(moved[0], "k", "lane.log")); string(b) != "x\n" {
		t.Fatal("the stray lost its content")
	}
	readFence(t, state)
}

// TestPlaceFenceRaceRule: an old binary's MkdirAll recreates queues/
// between the move and the rename; each time it goes to strays/ and the
// placement retries.
func TestPlaceFenceRaceRule(t *testing.T) {
	state := t.TempDir()
	n := 0
	beforePlace = func() {
		if n < 3 {
			n++
			if err := os.MkdirAll(filepath.Join(lane.QueuesDir(state), "late"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	defer func() { beforePlace = func() {} }()
	moved, err := placeFence(state)
	if err != nil || len(moved) != 3 {
		t.Fatalf("placeFence = %v, %v", moved, err)
	}
	readFence(t, state)
}

func TestPlaceFenceGivesUpAfterAHundredTries(t *testing.T) {
	state := t.TempDir()
	beforePlace = func() { _ = os.MkdirAll(lane.QueuesDir(state), 0o755) }
	defer func() { beforePlace = func() {} }()
	_, err := placeFence(state)
	var se *StateError
	if !errors.As(err, &se) || se.Msg != "machine-state: cannot place the queues fence" {
		t.Fatalf("want the placement refusal, got %v", err)
	}
	// The first try finds nothing to move; every later one moves the
	// directory the previous try's hook recreated.
	entries, _ := os.ReadDir(StraysDir(state))
	if len(entries) != maxPlaceTries-1 {
		t.Fatalf("%d strays, want %d", len(entries), maxPlaceTries-1)
	}
}

// TestPlaceFenceNotIdleWhereADirectoryCannotMove: on Windows a directory
// with an open file inside cannot be renamed; that means "an older run is
// still in there", not an error.
func TestPlaceFenceNotIdleWhereADirectoryCannotMove(t *testing.T) {
	state := t.TempDir()
	if err := os.Mkdir(lane.QueuesDir(state), 0o755); err != nil {
		t.Fatal(err)
	}
	renameDir = func(string, string) error { return errors.New("sharing violation") }
	notIdleOnRenameFailure = true
	defer func() { renameDir = os.Rename; notIdleOnRenameFailure = runtime.GOOS == "windows" }()
	if _, err := placeFence(state); !errors.Is(err, errNotIdle) {
		t.Fatalf("want errNotIdle, got %v", err)
	}
}

func TestExchangeSwapsAFileAndADirectory(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no atomic exchange on " + runtime.GOOS)
	}
	d := t.TempDir()
	dir := filepath.Join(d, "queues")
	file := filepath.Join(d, "queues.new")
	if err := os.MkdirAll(filepath.Join(dir, "k"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(FenceText), 0o644); err != nil {
		t.Fatal(err)
	}
	err := exchange(file, dir)
	if errors.Is(err, errNoExchange) {
		t.Skip("this filesystem has no atomic exchange; M4 takes the rename fallback")
	}
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(dir); err != nil || string(b) != FenceText {
		t.Fatalf("queues is not the fence after the swap: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(file, "k")); err != nil || !fi.IsDir() {
		t.Fatalf("queues.new is not the old directory after the swap: %v", err)
	}
}
