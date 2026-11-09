// Command limits is a test fixture for run's wall-clock and no-CPU limits.
//
//	limits sleep MS         idle for MS milliseconds, using no CPU
//	limits spin MS          burn CPU for MS milliseconds
//	limits tree MODE MS [PIDFILE]
//	                        start a grandchild in MODE (sleep or spin) for MS
//	                        milliseconds, write its pid to PIDFILE when given,
//	                        and idle in the parent for as long
//
// The tree mode is what shows that CPU burned by a descendant counts for the
// job: the process the run started stays idle while the grandchild works.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: limits sleep|spin MS | limits tree sleep|spin MS")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "sleep":
		time.Sleep(millis(os.Args[2]))
	case "spin":
		spin(millis(os.Args[2]))
	case "tree":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: limits tree sleep|spin MS")
			os.Exit(2)
		}
		gc := exec.Command(os.Args[0], os.Args[2], os.Args[3])
		gc.Stdout, gc.Stderr = os.Stdout, os.Stderr
		if err := gc.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if len(os.Args) > 4 {
			tmp := os.Args[4] + ".tmp"
			if err := os.WriteFile(tmp, []byte(strconv.Itoa(gc.Process.Pid)), 0o644); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			if err := os.Rename(tmp, os.Args[4]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
		// The parent only waits: whatever CPU the job uses, it is not this
		// process's.
		_ = gc.Wait()
	default:
		fmt.Fprintln(os.Stderr, "unknown mode", os.Args[1])
		os.Exit(2)
	}
}

func millis(s string) time.Duration {
	n, err := strconv.Atoi(s)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bad duration:", err)
		os.Exit(2)
	}
	return time.Duration(n) * time.Millisecond
}

var sink uint64

func spin(d time.Duration) {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		for i := 0; i < 1<<16; i++ {
			sink += uint64(i) * 2654435761
		}
	}
}
