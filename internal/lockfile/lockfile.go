// Package lockfile wraps the OS-level advisory file locks that incoda relies on
// for liveness. The point of using the kernel's own lock rather than a pid file
// is that the kernel drops the lock when the owning process dies for any reason
// at all: normal exit, panic, SIGKILL, TerminateProcess, or the machine losing
// power. There is no staleness to detect and no takeover path to get wrong.
package lockfile

import "os"

// File is an open file handle that can carry an exclusive whole-file lock.
//
// A File is not safe for concurrent use by multiple goroutines.
type File struct {
	f      *os.File
	locked bool
}

// Open opens (creating if absent) path in a mode that permits locking, and that
// permits other processes to open, read, and delete the same path while we hold
// it. The share-delete part matters on Windows, where an open handle without it
// blocks unlink entirely.
func Open(path string) (*File, error) {
	f, err := openLockable(path)
	if err != nil {
		return nil, err
	}
	return &File{f: f}, nil
}

// TryLock attempts a non-blocking exclusive lock. It reports whether the lock
// was taken. A false return with a nil error means somebody else holds it.
func (l *File) TryLock() (bool, error) {
	if l.locked {
		return true, nil
	}
	ok, err := lockFile(l.f, false)
	if ok {
		l.locked = true
	}
	return ok, err
}

// Lock blocks until the exclusive lock is acquired.
func (l *File) Lock() error {
	if l.locked {
		return nil
	}
	_, err := lockFile(l.f, true)
	if err != nil {
		return err
	}
	l.locked = true
	return nil
}

// Unlock releases the lock but keeps the handle open.
func (l *File) Unlock() error {
	if !l.locked {
		return nil
	}
	err := unlockFile(l.f)
	l.locked = false
	return err
}

// Close releases the lock (if held) and closes the handle.
func (l *File) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	if l.locked {
		_ = unlockFile(l.f)
		l.locked = false
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// Held reports whether this File currently holds the lock.
func (l *File) Held() bool { return l != nil && l.locked }

// Truncate replaces the file contents with b. The caller must hold the lock.
func (l *File) Truncate(b []byte) error {
	if err := l.f.Truncate(0); err != nil {
		return err
	}
	if _, err := l.f.Seek(0, 0); err != nil {
		return err
	}
	if _, err := l.f.Write(b); err != nil {
		return err
	}
	return l.f.Sync()
}

// IsFree reports whether path can be exclusively locked right now, which is the
// liveness test: a ticket file whose lock is free has no living owner.
func IsFree(path string) (bool, error) {
	l, err := Open(path)
	if err != nil {
		return false, err
	}
	defer l.Close()
	return l.TryLock()
}
