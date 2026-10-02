package machine

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/textsafe"
)

// FenceText is the whole content of <state>/queues on layout 2 (spec 2.3).
// It is a constant of this binary, never read from config or the
// environment. Every released incoda before 0.7 calls
// MkdirAll(<state>/queues) first and stops with "not a directory" (exit
// 122) when it finds this file.
const FenceText = `This state directory is managed by incoda >= 0.7 (system pools).
Lanes now live in lanes/. An older incoda stops here with "not a directory"
(exit 122) on purpose: it does not know about the machine-wide pools.
Upgrade it: brew upgrade incoda, or the install script at
https://github.com/deblasis/incoda#install (SHA256SUMS-verified).
Do not delete this file: that lets old binaries run jobs outside the pools.
`

// maxPlaceTries bounds the race-rule loop of spec 3.3 M4.
const maxPlaceTries = 100

var (
	// errNoExchange means the filesystem or OS has no atomic exchange; M4
	// takes the rename fallback.
	errNoExchange = errors.New("no atomic exchange on this filesystem")
	// errNotIdle means a directory could not be renamed because something
	// still has a file open inside it (Windows): an older run is still
	// there, so the caller waits and tries again within its budget.
	errNotIdle = errors.New("a directory to move still has an open file inside")
)

// Seams for tests; production never changes them.
var (
	exchangeFn  = exchange
	beforePlace = func() {}
	renameDir   = os.Rename
	// isNotIdle classifies a refused directory rename (notIdleError).
	isNotIdle = notIdleError
)

// FencePlaced reports whether the fence is in place: <state>/queues is a
// regular file, whatever its content (one lstat, spec 2.3).
func FencePlaced(stateDir string) bool {
	fi, err := os.Lstat(lane.QueuesDir(stateDir))
	return err == nil && fi.Mode().IsRegular()
}

// StraysDir is <state>/strays: where a queues/ directory goes when it is
// found in the fence's place (an old binary's run, or the old layout during
// the rename fallback). Plan 2b counts the live tickets left there.
func StraysDir(stateDir string) string { return filepath.Join(stateDir, "strays") }

func fenceNewPath(stateDir string) string { return filepath.Join(stateDir, "queues.new") }

// writeFenceNew writes the constant to <state>/queues.new.
func writeFenceNew(stateDir string) error {
	if err := os.WriteFile(fenceNewPath(stateDir), []byte(FenceText), 0o644); err != nil {
		return stateErrorf("cannot write %s: %s", textsafe.Escape(fenceNewPath(stateDir)), textsafe.Escape(err.Error()))
	}
	return nil
}

// placeFence writes the constant to queues.new and renames it to queues
// with the race rule of spec 3.3 M4: whatever sits at queues and is not a
// regular file (an old binary's MkdirAll can recreate queues/ at any moment)
// is moved to strays/<unix-nanos> first. The decision is made by lstat,
// never by the rename's errno (APFS answers EEXIST where Linux answers
// EISDIR). It gives up after maxPlaceTries, naming the last rename error.
// It returns the strays it made.
func placeFence(stateDir string) ([]string, error) {
	if err := writeFenceNew(stateDir); err != nil {
		return nil, err
	}
	q := lane.QueuesDir(stateDir)
	var moved []string
	var last error
	for i := 0; i < maxPlaceTries; i++ {
		if fi, err := os.Lstat(q); err == nil && !fi.Mode().IsRegular() {
			dst, err := moveToStrays(stateDir, q)
			if err != nil {
				if isNotIdle(err) {
					return moved, errNotIdle
				}
				return moved, stateErrorf("cannot move %s to strays/: %s", textsafe.Escape(q), textsafe.Escape(err.Error()))
			}
			moved = append(moved, dst)
		}
		beforePlace()
		if last = os.Rename(fenceNewPath(stateDir), q); last == nil {
			return moved, nil
		}
	}
	return moved, stateErrorf("cannot place the queues fence: %s", textsafe.Escape(last.Error()))
}

// moveToStrays renames path to strays/<unix-nanos>, picking the next free
// name. Strays are only written under machine.lock, so the check before
// the rename does not race.
func moveToStrays(stateDir, path string) (string, error) {
	if err := os.MkdirAll(StraysDir(stateDir), 0o755); err != nil {
		return "", err
	}
	for {
		dst := filepath.Join(StraysDir(stateDir), strconv.FormatInt(time.Now().UnixNano(), 10))
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if err := renameDir(path, dst); err != nil {
			return "", err
		}
		return dst, nil
	}
}
