package machine

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"

	"github.com/deblasis/incoda/internal/textsafe"
)

// pathChecked makes M0 run once per process.
var pathChecked atomic.Bool

// checkPath is M0 (spec 3.3): before the migration takes machine.lock, warn
// about every other incoda on PATH, which stops with "not a directory" once
// the fence is placed if it is older than 0.7. It never executes anything
// and never refuses.
func checkPath(o Options) {
	if !pathChecked.CompareAndSwap(false, true) {
		return
	}
	for _, p := range OtherIncodas(o.Path, o.Exe) {
		fmt.Fprintf(o.stderr(), "incoda: upgrade-warning: another incoda at %s; if it is older than 0.7 it will stop with \"not a directory\" after this upgrade (incoda doctor shows its version)\n", textsafe.Escape(p))
	}
}

// OtherIncodas lists, in PATH order, every executable regular file named
// incoda (incoda.exe on Windows) in a PATH directory that is not the same
// file as self after resolving symlinks. self "" means os.Executable. An
// empty PATH entry is the current directory, as exec.LookPath reads it.
// Nothing is executed.
func OtherIncodas(path, self string) []string {
	var out []string
	for _, e := range IncodasOnPath(path, self) {
		if !e.Self {
			out = append(out, e.Path)
		}
	}
	return out
}

// PathIncoda is one incoda found on PATH.
type PathIncoda struct {
	Path string
	// Self is set when it is the same file as this binary.
	Self bool
}

// IncodasOnPath lists, in PATH order, every executable regular file named
// incoda (incoda.exe on Windows) in a PATH directory, marking the one that
// is this binary. Nothing is executed.
func IncodasOnPath(path, self string) []PathIncoda {
	if self == "" {
		self, _ = os.Executable()
	}
	var selfInfo os.FileInfo
	if real, err := filepath.EvalSymlinks(self); err == nil {
		selfInfo, _ = os.Stat(real)
	}
	name := "incoda"
	if runtime.GOOS == "windows" {
		name = "incoda.exe"
	}
	seen := map[string]bool{}
	var out []PathIncoda
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
		}
		p := filepath.Join(dir, name)
		if seen[p] {
			continue
		}
		seen[p] = true
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			continue
		}
		fi, err := os.Stat(real)
		if err != nil || !fi.Mode().IsRegular() || (runtime.GOOS != "windows" && fi.Mode().Perm()&0o111 == 0) {
			continue
		}
		out = append(out, PathIncoda{Path: p, Self: selfInfo != nil && os.SameFile(fi, selfInfo)})
	}
	return out
}
