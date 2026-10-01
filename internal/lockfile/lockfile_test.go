package lockfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenExistingNeverCreates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent")
	if _, err := OpenExisting(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("OpenExisting created the file")
	}
	held, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if ok, _ := held.TryLock(); !ok {
		t.Fatal("lock")
	}
	probe, err := OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	if ok, err := probe.TryLock(); ok || err != nil {
		t.Fatalf("a held lock must refuse a second handle: ok=%v err=%v", ok, err)
	}
}

func TestIsFreeExistingNeverCreates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent")
	if free, err := IsFreeExisting(path); !free || err != nil {
		t.Fatalf("a missing file has no owner: free=%v err=%v", free, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("IsFreeExisting created the file")
	}
	held, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if ok, _ := held.TryLock(); !ok {
		t.Fatal("lock")
	}
	if free, err := IsFreeExisting(path); free || err != nil {
		t.Fatalf("a held file is not free: free=%v err=%v", free, err)
	}
}
