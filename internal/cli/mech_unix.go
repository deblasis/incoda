//go:build !windows

package cli

const lockMechanism = "flock LOCK_EX"
