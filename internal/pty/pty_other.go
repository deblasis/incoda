//go:build !windows

package pty

// Pty exists only so non-Windows builds have a New to call and refuse
// against; nothing constructs one, because run turns ErrUnsupported into a
// named refusal before any child starts.
type Pty struct{}

// New refuses on non-Windows: ConPTY has no counterpart here, and a job that
// asks for pty mode should fail with a named reason, not a half-working
// terminal.
func New(cols, rows int) (*Pty, error) { return nil, ErrUnsupported }
