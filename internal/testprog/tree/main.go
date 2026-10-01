//go:build !windows

// tree records its pid, parent pid and process group, starts a grandchild
// copy of itself that only sleeps, records the grandchild's pid, and waits.
// Tests use it to check that a kill through incoda reaches the whole tree.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "sleep" {
		time.Sleep(60 * time.Second)
		return
	}
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: tree OUTFILE")
		os.Exit(2)
	}
	gc := exec.Command(os.Args[0], "sleep")
	if err := gc.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	pgid, _ := syscall.Getpgid(os.Getpid())
	body := fmt.Sprintf("pid %d\nppid %d\npgid %d\ngrandchild %d\n", os.Getpid(), os.Getppid(), pgid, gc.Process.Pid)
	tmp := os.Args[1] + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Rename(tmp, os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = gc.Wait()
}
