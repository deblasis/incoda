package machine

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// fakePinned records what killPinned did, in order, into calls.
type fakePinned struct {
	calls *[]string
	held  *bool
}

func (f fakePinned) Terminate(code int) error {
	*f.calls = append(*f.calls, "terminate")
	*f.held = false // the kernel frees the ticket when the incoda dies
	return nil
}

func (f fakePinned) Wait(time.Duration) error {
	*f.calls = append(*f.calls, "wait")
	return nil
}

func (f fakePinned) Close() error {
	*f.calls = append(*f.calls, "close")
	return nil
}

// TestKillPinnedOpensThenRechecksThenTerminates: the Windows old-holder
// kill opens the process (the handle pins it) before it re-checks the
// ticket, terminates only through that handle, and terminates nothing
// when the ticket is no longer held (exit 120, no longer holds).
func TestKillPinnedOpensThenRechecksThenTerminates(t *testing.T) {
	savedOpen, savedHeld, savedWait := openPinnedFn, ticketHeldFn, treeGoneWait
	t.Cleanup(func() { openPinnedFn, ticketHeldFn, treeGoneWait = savedOpen, savedHeld, savedWait })
	treeGoneWait = time.Second
	tg := KillTarget{Kind: TargetOld, Key: "builds", PID: 4711, Dir: t.TempDir(), Ticket: "x.ticket"}

	setup := func(held bool, openErr error) (*[]string, *bool) {
		calls := &[]string{}
		h := held
		openPinnedFn = func(pid int) (pinnedProcess, error) {
			*calls = append(*calls, "open")
			if pid != tg.PID {
				t.Fatalf("opened pid %d", pid)
			}
			if openErr != nil {
				return nil, openErr
			}
			return fakePinned{calls: calls, held: &h}, nil
		}
		ticketHeldFn = func(KillTarget) (bool, error) {
			*calls = append(*calls, "check")
			return h, nil
		}
		return calls, &h
	}

	t.Run("still held", func(t *testing.T) {
		calls, _ := setup(true, nil)
		if _, err := killPinned(tg); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(*calls, ","); !strings.HasPrefix(got, "open,check,terminate,wait,") || !strings.HasSuffix(got, ",close") {
			t.Fatalf("order: %s", got)
		}
	})
	t.Run("no longer held", func(t *testing.T) {
		calls, _ := setup(false, nil)
		_, err := killPinned(tg)
		var r *Refusal
		if !errors.As(err, &r) || r.Msg != `kill: pid 4711 no longer holds "builds"` {
			t.Fatalf("want the no-longer-holds refusal, got %v", err)
		}
		if got := strings.Join(*calls, ","); got != "open,check,close" {
			t.Fatalf("nothing may be terminated: %s", got)
		}
	})
	t.Run("open fails and the ticket is free", func(t *testing.T) {
		calls, _ := setup(false, errors.New("gone"))
		_, err := killPinned(tg)
		var r *Refusal
		if !errors.As(err, &r) {
			t.Fatalf("want the no-longer-holds refusal, got %v", err)
		}
		if got := strings.Join(*calls, ","); got != "open,check" {
			t.Fatalf("calls: %s", got)
		}
	})
	t.Run("open fails while the ticket is held", func(t *testing.T) {
		setup(true, errors.New("access denied"))
		_, err := killPinned(tg)
		var se *StateError
		if !errors.As(err, &se) || !strings.Contains(se.Msg, "cannot open older incoda pid 4711: access denied") {
			t.Fatalf("want a state error, got %v", err)
		}
	})
}
