package tui

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"time"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/proc"
	"github.com/deblasis/incoda/internal/procinfo"
	"github.com/deblasis/incoda/internal/textsafe"
)

// killedExit is the exit code a forced kill hands the participant, the same
// 124 that `incoda run` uses when it stops itself on a request. It is
// repeated here rather than imported because the cli package imports this
// one.
const killedExit = 124

// killProbeWait bounds the probes that look for a kill target, the default
// --wait of incoda kill.
const killProbeWait = 5 * time.Second

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

// target finds pid on key in the layout the state directory has now
// (lanes/ once migrated, queues/ before, strays/ for an older incoda that
// runs outside the pools) without creating anything: a kill addresses the
// layout it finds (spec 3.2).
func (k LaneKiller) target(key string, pid int) (machine.KillTarget, error) {
	v, err := machine.Inspect(k.Dir)
	if err != nil {
		return machine.KillTarget{}, err
	}
	t, err := machine.FindKillTarget(k.Dir, v, key, pid, lane.ProbeDeadline(time.Time{}, killProbeWait))
	if err != nil {
		return machine.KillTarget{}, err
	}
	if t.Kind == machine.TargetNone {
		return t, fmt.Errorf("queue %q has no live participant with pid %d: %w", key, pid, lane.ErrNoParticipant)
	}
	return t, nil
}

func (k LaneKiller) Request(key string, pid int, reason string) error {
	t, err := k.target(key, pid)
	if err != nil {
		return err
	}
	if t.Old() {
		// An older incoda gets no request: it is ended with its whole job
		// at once (spec 3.2, old-holder kill).
		return k.killOld(t, reason)
	}
	q, err := lane.OpenIn(t.Root, key, lane.Existing)
	if err != nil {
		return err
	}
	defer q.Close()
	_, err = q.RequestKill(pid, lane.KillRequest{By: k.By, ByPID: k.ByPID, Reason: reason})
	return err
}

func (k LaneKiller) Gone(key string, pid int, wait time.Duration) (bool, error) {
	t, err := k.target(key, pid)
	if errors.Is(err, lane.ErrNoParticipant) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	q, err := lane.OpenIn(t.Root, key, lane.Existing)
	if err != nil {
		return false, err
	}
	defer q.Close()
	return q.WaitGone(pid, wait, 100*time.Millisecond)
}

// Force ends pid. An older incoda is ended with its whole job (the
// old-holder kill of spec 3.2); a holder of this binary is terminated as
// before.
func (k LaneKiller) Force(key string, pid int, reason string) error {
	t, err := k.target(key, pid)
	if err != nil {
		return err
	}
	if t.Old() {
		return k.killOld(t, reason)
	}
	q, err := lane.OpenIn(t.Root, key, lane.Existing)
	if err != nil {
		return err
	}
	defer q.Close()
	// Pin the process before the last check (a handle on Windows), then
	// terminate it through the handle only if it still holds a ticket.
	h, err := proc.Open(pid)
	if err == nil {
		defer h.Close()
	}
	if gone, gerr := q.WaitGone(pid, 0, 100*time.Millisecond); gerr == nil && gone {
		return nil
	}
	if err != nil {
		return err
	}
	if err := h.Terminate(killedExit); err != nil {
		return err
	}
	q.Logf("queue=%s event=kill pid=%d by=%s reason=%s forced=true", key, pid, textsafe.LogValue(k.By), textsafe.LogValue(reason))
	return nil
}

// killOld ends an older incoda with its whole job and logs it where its
// ticket is.
func (k LaneKiller) killOld(t machine.KillTarget, reason string) error {
	res, err := machine.KillOldHolder(k.Dir, t, procinfo.ParentChain())
	if err != nil {
		return err
	}
	lane.AppendLog(t.Dir, "queue=%s event=kill pid=%d by=%s reason=%s forced=true old=true processes=%d",
		t.Key, t.PID, textsafe.LogValue(k.By), textsafe.LogValue(reason), res.Processes)
	return nil
}
