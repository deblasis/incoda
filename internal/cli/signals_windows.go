//go:build windows

package cli

import "os"

func interruptSignals() []os.Signal { return []os.Signal{os.Interrupt} }
