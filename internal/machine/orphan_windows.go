//go:build windows

package machine

// groupHasMembers is never asked on Windows: an old-holder kill there only
// terminates the old incoda (its job object ends the tree) and writes no
// record.
func groupHasMembers(int) bool { return false }
