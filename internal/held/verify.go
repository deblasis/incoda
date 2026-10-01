package held

import (
	"errors"

	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/procinfo"
)

// Why says why an entry was not passed through.
type Why string

const (
	Dead         Why = "dead"
	Malformed    Why = "malformed"
	NotAncestor  Why = "not-ancestor"
	Unverifiable Why = "unverifiable"
)

// Dropped is an entry that is not passed through. Live entries (not-ancestor,
// unverifiable) still count for ordering and the process group.
type Dropped struct {
	Raw    string
	Key    string
	Ticket string
	Why    Why
	Live   bool
}

// Result splits the inherited entries: L is every live entry, P the live
// entries whose holder is a verified ancestor. L decides ordering and the
// process group; P decides pass-through.
type Result struct {
	L       []Entry
	P       []Entry
	Dropped []Dropped
}

// PassKeys is the set of keys in P.
func (r Result) PassKeys() map[string]bool { return keys(r.P) }

// LiveKeys is the set of keys in L.
func (r Result) LiveKeys() map[string]bool { return keys(r.L) }

func keys(es []Entry) map[string]bool {
	m := map[string]bool{}
	for _, e := range es {
		m[e.Key] = true
	}
	return m
}

// Verify parses raw and checks each entry: liveness by probing its ticket
// lock, then ancestry against chain. The probe takes the lane's registry
// lock (the one Enroll and Release hold) and opens files without ever
// creating them, so it can neither race a ticket into existence nor make a
// released ticket look alive.
//
// The caller must not already hold any lane's registry lock: the probe
// takes that lock with a blocking Lock call on a freshly opened handle, and
// a second handle in the same process competing for the same flock (or, on
// Windows, the same byte-range lock) would self-deadlock rather than see
// its own earlier lock. Verify is meant to run before any lane is opened.
func Verify(stateDir, raw string, chain procinfo.Chain) Result {
	var r Result
	entries, bad := Parse(raw)
	for _, b := range bad {
		r.Dropped = append(r.Dropped, Dropped{Raw: b.Raw, Why: Malformed})
	}
	for _, e := range entries {
		live, payloadPID, perr := probe(stateDir, e)
		d := Dropped{Raw: e.String(), Key: e.Key, Ticket: e.Ticket}
		if !live {
			d.Why = Dead
			r.Dropped = append(r.Dropped, d)
			continue
		}
		r.L = append(r.L, e)
		if chain.Skip {
			r.P = append(r.P, e)
			continue
		}
		namePID, _ := lane.TicketNamePID(e.Ticket)
		switch {
		case perr != nil:
			d.Why, d.Live = Unverifiable, true
		case payloadPID != namePID:
			d.Why, d.Live = NotAncestor, true
		case chain.Contains(namePID):
			r.P = append(r.P, e)
			continue
		case chain.Err != nil:
			d.Why, d.Live = Unverifiable, true
		default:
			d.Why, d.Live = NotAncestor, true
		}
		r.Dropped = append(r.Dropped, d)
	}
	return r
}

// probe reports whether e's ticket is held by a live process, and the pid
// recorded in its payload. perr reports a payload that could not be read.
// The probe itself is lane.ProbeTicket, shared with the migration's idle
// checks; a probe that failed counts as dead here, as it did in plan 1.
func probe(stateDir string, e Entry) (live bool, payloadPID int, perr error) {
	p := lane.ProbeTicket(lane.LaneDir(stateDir, e.Key), e.Ticket)
	if !p.Live {
		return false, 0, nil
	}
	if p.PayloadErr != nil {
		return true, 0, p.PayloadErr
	}
	if p.Ticket.PID == 0 {
		return true, 0, errors.New("ticket payload has no pid")
	}
	return true, p.Ticket.PID, nil
}
