package machine

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeIncoda writes an executable script named incoda in dir that leaves a
// marker if anything ever runs it.
func fakeIncoda(t *testing.T, dir, marker string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "incoda")
	if err := os.WriteFile(p, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOtherIncodasFindsOthersAndSkipsSelf(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script and a symlink")
	}
	root := t.TempDir()
	marker := filepath.Join(root, "ran")
	self := fakeIncoda(t, filepath.Join(root, "self"), marker)
	other := fakeIncoda(t, filepath.Join(root, "old"), marker)
	linkDir := filepath.Join(root, "link")
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self, filepath.Join(linkDir, "incoda")); err != nil {
		t.Fatal(err)
	}
	notExec := filepath.Join(root, "noexec")
	if err := os.MkdirAll(notExec, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notExec, "incoda"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := strings.Join([]string{filepath.Join(root, "self"), linkDir, filepath.Join(root, "old"), notExec, filepath.Join(root, "missing"), filepath.Join(root, "old")}, string(os.PathListSeparator))
	got := OtherIncodas(path, self)
	if len(got) != 1 || got[0] != other {
		t.Fatalf("OtherIncodas = %v, want [%s]", got, other)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("M0 executed an incoda from PATH")
	}
}

func TestMigrationWarnsAboutAnOtherIncodaOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script")
	}
	root := t.TempDir()
	marker := filepath.Join(root, "ran")
	self := fakeIncoda(t, filepath.Join(root, "self"), marker)
	other := fakeIncoda(t, filepath.Join(root, "old"), marker)
	pathChecked.Store(false)
	defer pathChecked.Store(false)
	opts := func(errBuf *bytes.Buffer) Options {
		return Options{Start: time.Now(), Wait: 10 * time.Second, Stderr: errBuf, Path: filepath.Dir(other), Exe: self}
	}
	var errBuf bytes.Buffer
	if _, err := Ensure(t.TempDir(), opts(&errBuf)); err != nil {
		t.Fatal(err)
	}
	want := "incoda: upgrade-warning: another incoda at " + other + `; if it is older than 0.7 it will stop with "not a directory" after this upgrade (incoda doctor shows its version)` + "\n"
	if !strings.HasPrefix(errBuf.String(), want) {
		t.Fatalf("output:\n%s\nwant prefix:\n%s", errBuf.String(), want)
	}
	errBuf.Reset()
	if _, err := Ensure(t.TempDir(), opts(&errBuf)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errBuf.String(), "upgrade-warning") {
		t.Fatal("M0 runs once per process")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("M0 executed an incoda from PATH")
	}
}
