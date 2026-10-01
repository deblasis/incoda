// Package atomicfile writes a file so that a reader sees either the old
// content or the new one, never half of either: the data goes to a temp
// file beside the target, which is then renamed over it. config.json,
// machine.json and migration.json are all written this way (spec 4.4).
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
func Write(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
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
