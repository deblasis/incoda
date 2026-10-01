package atomicfile

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteReplacesAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.json")
	for _, body := range []string{"one\n", "two\n"} {
		if err := Write(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(path); string(b) != body {
			t.Fatalf("content %q, want %q", b, body)
		}
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("the temp file was left behind")
	}
}

func TestRenameFailsFastOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows retries")
	}
	err := Rename(filepath.Join(t.TempDir(), "absent"), filepath.Join(t.TempDir(), "x"))
	if err == nil {
		t.Fatal("renaming a missing file must fail")
	}
}

// TestWriteFailedRenameRemovesTemp: when the rename is refused (the target
// is a non-empty directory) Write returns the error and leaves no temp file.
// Durability (the fsyncs of the file and its directory) cannot be checked
// by a unit test: it only shows after a kernel panic or a power loss.
func TestWriteFailedRenameRemovesTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.json")
	if err := os.MkdirAll(filepath.Join(path, "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("body\n"), 0o644); err == nil {
		t.Fatal("renaming over a non-empty directory must fail")
	}
	if _, err := os.Lstat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("the temp file was left behind after a failed rename")
	}
	if fi, err := os.Lstat(path); err != nil || !fi.IsDir() {
		t.Fatalf("the target must be untouched: %v", err)
	}
}

// TestWriteContentComplete: a larger body arrives whole, with the mode.
func TestWriteContentComplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.json")
	body := []byte(strings.Repeat("0123456789abcdef", 64*1024))
	if err := Write(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("content: %d bytes, want %d (%v)", len(got), len(body), err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", fi.Mode().Perm())
		}
	}
	if _, err := os.Lstat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("the temp file was left behind")
	}
}
