// Package atomicfile writes a file so that a reader sees either the old
// content or the new one, never half of either: the data goes to a temp
// file beside the target, is flushed to disk, and is then renamed over it.
// config.json, machine.json and migration.json are all written this way
// (spec 4.4).
package atomicfile

import (
	"os"
	"runtime"
	"time"
)

const (
	retries  = 10
	retryGap = 50 * time.Millisecond
)

// Write replaces path with data. The temp file is path+".tmp"; callers
// serialise writers of one path with a lock (the lane's registry lock, or
// machine.lock), so the fixed name never races.
//
// The temp file is fsynced before the rename and, off Windows, the parent
// directory after it. Without that a kernel panic or power loss can leave
// the renamed file present but empty or zeroed, and an empty machine.json
// fails every command closed until a human rebuilds it.
func Write(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := writeSynced(tmp, data, perm); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(path)
}

// writeSynced writes data to name and fsyncs it before closing.
func writeSynced(name string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Rename renames, retrying on Windows, where a reader holding the target
// open makes the rename fail for a moment: 10 tries, 50ms apart.
func Rename(from, to string) error {
	var err error
	for i := 0; i < retries; i++ {
		if err = os.Rename(from, to); err == nil || runtime.GOOS != "windows" {
			return err
		}
		if i < retries-1 {
			time.Sleep(retryGap)
		}
	}
	return err
}
