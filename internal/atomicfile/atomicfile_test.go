package atomicfile

import (
	"os"
	"path/filepath"
	"runtime"
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
