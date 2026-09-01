// Command stamp is a test fixture: it records the wall-clock interval during
// which it was running, so that the integration tests can prove whether two
// lane holders ever overlapped.
//
// It is deliberately a separate process rather than an in-test goroutine.
// Mutual exclusion between goroutines would prove nothing about the OS locks
// that incoda actually relies on.
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

func main() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: stamp <outfile> <label> <hold-millis> [exit-code]")
		os.Exit(2)
	}
	out, label := os.Args[1], os.Args[2]
	holdMS, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, "bad hold:", err)
		os.Exit(2)
	}
	code := 0
	if len(os.Args) > 4 {
		code, _ = strconv.Atoi(os.Args[4])
	}

	enter := time.Now().UnixNano()
	// The enter stamp is flushed before the hold so that a hard kill during the
	// hold still leaves evidence that this process was inside the lane.
	write(out, fmt.Sprintf("label %s\nenter %d\n", label, enter))
	time.Sleep(time.Duration(holdMS) * time.Millisecond)
	exit := time.Now().UnixNano()
	write(out, fmt.Sprintf("label %s\nenter %d\nexit %d\n", label, enter, exit))
	os.Exit(code)
}

func write(path, s string) {
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "stamp write:", err)
		os.Exit(3)
	}
}
