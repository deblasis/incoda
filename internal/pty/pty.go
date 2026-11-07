// Package pty wraps a Windows pseudo-console (ConPTY) for one job process:
// the pair, the spawn plumbing that connects a child to it, and the teardown
// order that keeps handles from leaking.
//
// The rest of incoda only needs three things from this package: New (make a
// pair), Spawn (start a child inside it) and the Close/CloseConsole split
// (teardown). Everything else exists because os/exec cannot carry the
// PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE attribute: syscall.SysProcAttr has no
// field for it, so the CreateProcess call is done here by hand.
package pty

import "errors"

// Defaults for a job's pseudo-console. 120x30 matches the terminal most
// sessions here run in; a caller that needs another size passes it to New,
// and the run command always takes these.
const (
	DefaultCols = 120
	DefaultRows = 30
)

// ErrUnsupported is what New returns on a system without a pseudo-console
// implementation. The run command turns it into a refusal before any child
// starts.
var ErrUnsupported = errors.New("pty mode needs Windows (ConPTY); it is not implemented on this system")
