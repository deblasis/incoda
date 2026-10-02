//go:build !darwin && !linux

package procinfo

// List is not available here (Windows, the BSDs).
func List() ([]Proc, error) { return nil, ErrUnsupported }

// Lookup is not available here.
func Lookup(int) (Proc, error) { return Proc{}, ErrUnsupported }
