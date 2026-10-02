// Command argv is a test fixture standing in for incoda when a test pastes
// a printed fix line into a shell: it writes the arguments it received and
// its working directory, as JSON, to the file named by ARGV_OUT, so the
// test can check the shell reproduced them exactly.
package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	out := os.Getenv("ARGV_OUT")
	if out == "" {
		fmt.Fprintln(os.Stderr, "argv: ARGV_OUT is not set")
		os.Exit(2)
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "argv:", err)
		os.Exit(2)
	}
	b, _ := json.Marshal(map[string]any{"args": os.Args[1:], "cwd": cwd})
	if err := os.WriteFile(out, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "argv:", err)
		os.Exit(2)
	}
}
