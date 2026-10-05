//go:build windows

package lockfile

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// openLockable opens path with FILE_SHARE_DELETE so that a reaper in another
// process can unlink the ticket once its owner is gone. Without share-delete an
// open handle on Windows makes the path undeletable, which would reintroduce
// exactly the stale-state problem we are removing.
//
// A handle opened for writing asks Windows to update the parent directory's
// mtime on open, which makes a read-only command that merely opens a lane's
// registry lock look like it wrote to the state directory. openExistingLockable
// therefore asks for read access only.
func openLockable(path string) (*os.File, error) {
	return openWindows(path, windows.OPEN_ALWAYS, windows.GENERIC_READ|windows.GENERIC_WRITE)
}

// openExistingLockable opens without write access: nothing writes through this
// handle (only Truncate does, and no read-only caller does), and asking for
// GENERIC_WRITE makes Windows update the parent directory's mtime on open.
func openExistingLockable(path string) (*os.File, error) {
	return openWindows(path, windows.OPEN_EXISTING, windows.GENERIC_READ)
}

func openWindows(path string, disposition, access uint32) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(
		p,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		disposition,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

// The locked region is a single byte at offset 2^62, far past the end of any
// ticket payload, and NOT byte 0.
//
// This is not cosmetic. A Windows byte-range lock denies reads of the locked
// range to every other handle, so locking byte 0 would make a ticket file's
// JSON unreadable by `incoda status` and by the scanner that computes the slot
// count -- which silently degraded every queue to one slot the first time this
// was written. Locking a byte beyond EOF gives the same mutual exclusion while
// leaving the payload readable. (SQLite uses the same trick for the same
// reason.)
const (
	lockOffsetHigh = 0x4000_0000 // offset 2^62, expressed as the high dword
	lockOffsetLow  = 0
	lockLen        = 1
)

func newOverlapped() *windows.Overlapped {
	ol := new(windows.Overlapped)
	ol.Offset = lockOffsetLow
	ol.OffsetHigh = lockOffsetHigh
	return ol
}

func lockFile(f *os.File, block bool) (bool, error) {
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if !block {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, lockLen, 0, newOverlapped())
	if err == nil {
		return true, nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return false, nil
	}
	return false, err
}

func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockLen, 0, newOverlapped())
}
