//go:build !windows

package cli

import (
	"io"
	"time"
)

// maybeHideConsole is a no-op outside Windows: jobs run on pipes and no
// console window is ever allocated for incoda or its children.
func maybeHideConsole(args []string, stderr io.Writer) {}

// consoleFactsSummary reports that there is no console window to hide.
func consoleFactsSummary() string {
	return "no console window (jobs run on pipes)"
}

// settleState mirrors the Windows post-hide decision for tests on other
// platforms: a remap always re-hides, the verdict is due after the quiet
// window or the overall cap.
func settleState(visible bool, sinceLastHide, sinceFirstHide time.Duration) (needHide bool, done bool) {
	if visible {
		return true, sinceFirstHide >= 2*hideRetryWindow
	}
	return false, sinceLastHide >= hideRetryWindow || sinceFirstHide >= 2*hideRetryWindow
}
