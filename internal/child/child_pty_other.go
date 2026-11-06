//go:build !windows

package child

import (
	"fmt"
	"runtime"

	"github.com/deblasis/incoda/internal/pty"
)

// runPTY refuses on non-Windows: ConPTY is the only pseudo-console wired up,
// and a job that asked for pty mode should fail with a named reason before
// any child starts, not after.
func runPTY(argv []string, abort <-chan struct{}, opt Options) (Result, error) {
	return Result{}, fmt.Errorf("%w (%s)", pty.ErrUnsupported, runtime.GOOS)
}
