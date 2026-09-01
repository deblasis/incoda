//go:build !windows

package cli

import (
	"os"
	"syscall"
)

func interruptSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
