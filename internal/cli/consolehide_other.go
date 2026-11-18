//go:build !windows

package cli

import "io"

// maybeHideConsole is a no-op outside Windows: jobs run on pipes and no
// console window is ever allocated for incoda or its children.
func maybeHideConsole(args []string, stderr io.Writer) {}
