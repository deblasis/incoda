package lockfile

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
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

// One File is shared by every goroutine that touches a Queue's registry:
// holder pollers, CLI calls, and the Close at teardown. The CI race
// detector caught a real Unlock interleaving with Close on the answer
// path, so the sharing itself is pinned here - hammer Lock/Unlock/Held
// from several goroutines while a fourth closes the handle, and nothing
// races or deadlocks.
func TestFileSurvivesConcurrentUse(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "registry.lock"))
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if err := l.Lock(); err != nil {
					return
				}
				l.Held()
				_ = l.Unlock()
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	if err := l.Close(); err != nil {
		t.Fatalf("close under concurrent use: %v", err)
	}
	close(stop)
	wg.Wait()
}
