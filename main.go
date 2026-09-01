// Command incoda serialises heavy processes -- builds and GUI/UI test runs --
// through named, machine-local queues.
package main

import (
	"os"

	"github.com/deblasis/incoda/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
