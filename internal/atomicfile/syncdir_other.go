//go:build !unix

package atomicfile

// syncDir is a no-op where a directory cannot be fsynced: Windows has no
// directory fsync, so the file's own fsync is all Write can do there.
func syncDir(string) error { return nil }
