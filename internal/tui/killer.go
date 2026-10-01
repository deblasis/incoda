package tui

import (
	"os"
	"os/user"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/proc"
	"github.com/deblasis/incoda/internal/textsafe"
)

// killedExit is the exit code a forced kill hands the participant, the same
// 124 that `incoda run` uses when it stops itself on a request. It is
// repeated here rather than imported because the cli package imports this
// one.
const killedExit = 124

// Killer is what the kill prompt talks to. It is an interface so the model
// can be driven in tests without a state directory or a process to end.
type Killer interface {
	// Request leaves the kill request beside pid's ticket on key.
	Request(key string, pid int, reason string) error
	// Gone reports whether pid has left key, waiting up to wait for it.
	Gone(key string, pid int, wait time.Duration) (bool, error)
	// Force terminates pid outright and records the forced kill on key.
	Force(key string, pid int, reason string) error
}

// LaneKiller is the real Killer: the same request file and termination path
// as `incoda kill`, so the TUI and the command cannot drift apart.
type LaneKiller struct {
	Dir   string
	By    string
	ByPID int
}

// NewLaneKiller names the killer as user@host the way `incoda kill` does.
func NewLaneKiller(dir string) LaneKiller {
	name := "unknown"
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	if host, _ := os.Hostname(); host != "" {
		name += "@" + host
	}
	return LaneKiller{Dir: dir, By: name, ByPID: os.Getpid()}
}

// open finds key's lane in the layout the state directory has now (lanes/
// once migrated, queues/ before) without creating anything: a kill
// addresses the layout it finds (spec 3.2).
func (k LaneKiller) open(key string) (*lane.Queue, error) {
	v, err := machine.Inspect(k.Dir)
	if err != nil {
		return nil, err
	}
	return lane.OpenIn(v.Root, key, lane.Existing)
}

func (k LaneKiller) Request(key string, pid int, reason string) error {
	q, err := k.open(key)
	if err != nil {
		return err
	}
	defer q.Close()
	_, err = q.RequestKill(pid, lane.KillRequest{By: k.By, ByPID: k.ByPID, Reason: reason})
	return err
}

func (k LaneKiller) Gone(key string, pid int, wait time.Duration) (bool, error) {
	q, err := k.open(key)
	if err != nil {
		return false, err
	}
	defer q.Close()
	return q.WaitGone(pid, wait, 100*time.Millisecond)
}

func (k LaneKiller) Force(key string, pid int, reason string) error {
	if err := proc.Terminate(pid, killedExit); err != nil {
		return err
	}
	q, err := k.open(key)
	if err != nil {
		return err
	}
	defer q.Close()
	q.Logf("queue=%s event=kill pid=%d by=%s reason=%s forced=true", key, pid, textsafe.LogValue(k.By), textsafe.LogValue(reason))
	return nil
}
