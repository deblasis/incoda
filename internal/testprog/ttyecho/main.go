// Command ttyecho is a test fixture for pty mode. It reports what kind of
// stdio it got, writes exact bytes, or emits a periodic marker loop, so the
// integration tests can pin what a job's output looks like after a
// pseudo-console has been in the path.
//
// It is a separate process for the same reason stamp is: the tests are about
// what a real child of CreateProcess sees, not what a goroutine can fake.
package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: ttyecho <tty|write|spin> ...")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "tty":
		say := "pipe"
		if term.IsTerminal(int(os.Stdout.Fd())) {
			say = "tty"
		}
		fmt.Printf("stdout=%s\n", say)
	case "write":
		// write hex:<hex>: the decoded bytes, verbatim, no newline added.
		if len(os.Args) != 3 || !strings.HasPrefix(os.Args[2], "hex:") {
			fmt.Fprintln(os.Stderr, "usage: ttyecho write hex:<hex>")
			os.Exit(2)
		}
		b, err := hex.DecodeString(strings.TrimPrefix(os.Args[2], "hex:"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "bad hex:", err)
			os.Exit(2)
		}
		if _, err := os.Stdout.Write(b); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(3)
		}
	case "spin":
		// spin <marker> <every-millis> <max-seconds>: one marker line per
		// tick until the time is up, then exit 0. The pattern is chosen so
		// a live attach can see progress without a fixed line count.
		if len(os.Args) != 5 {
			fmt.Fprintln(os.Stderr, "usage: ttyecho spin <marker> <every-millis> <max-seconds>")
			os.Exit(2)
		}
		marker := os.Args[2]
		everyMS, err := strconv.Atoi(os.Args[3])
		if err != nil || everyMS <= 0 {
			fmt.Fprintln(os.Stderr, "bad interval")
			os.Exit(2)
		}
		maxSec, err := strconv.Atoi(os.Args[4])
		if err != nil || maxSec <= 0 {
			fmt.Fprintln(os.Stderr, "bad duration")
			os.Exit(2)
		}
		deadline := time.Now().Add(time.Duration(maxSec) * time.Second)
		for i := 0; time.Now().Before(deadline); i++ {
			fmt.Printf("%s %d\r\n", marker, i)
			time.Sleep(time.Duration(everyMS) * time.Millisecond)
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown mode:", os.Args[1])
		os.Exit(2)
	}
}
