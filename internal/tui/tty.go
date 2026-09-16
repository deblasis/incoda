package tui

import (
	"os"
	"os/signal"
	"syscall"
)

// watchTTYRestoreSeq is bubbletea v2 MouseModeCellMotion teardown plus ?1000l
// for belt-and-suspenders: alt screen off, cursor on, every mouse mode off.
const watchTTYRestoreSeq = "\x1b[?1049l\x1b[?25h" +
	"\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l"

// restoreWatchTTY turns off mouse tracking and leaves the alt screen. Bubble
// Tea normally does this on exit, but a kill/SIGHUP (or closing the terminal
// panel) can skip cleanup and leave the shell reading SGR mouse events as
// input — the "0;58;29M" spam. Writing to /dev/tty reaches the real terminal
// even when stdout is redirected.
func restoreWatchTTY() {
	f, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(watchTTYRestoreSeq)
}

// installTTYRestore runs restoreWatchTTY on common termination signals and
// returns a stop func for Run to call on the way out.
func installTTYRestore() func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-ch
		restoreWatchTTY()
	}()
	return func() { signal.Stop(ch) }
}
