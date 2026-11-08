package tui

import (
	"charm.land/lipgloss/v2"

	"github.com/deblasis/incoda/internal/joblog"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/osc7501"
	"github.com/deblasis/incoda/internal/report"
)

// identityFloor is the least room the who-and-how-long part of a participant
// line keeps when a status claims the end of it; below the width where both
// fit, the status gives way (see programStatus), never the other way round.
const identityFloor = 12

// statusKey names one participant's log in the status cache.
func statusKey(queue, file string) string { return queue + "\x00" + file }

// refreshStatus rebuilds the per-participant program status from the job
// logs. It runs once per loaded report, never from View: a log is followed
// incrementally (only appended bytes are read, none when it did not grow)
// and a participant that left the report drops its cache entry, so the
// screen paints from memory however often it redraws.
//
// Only a pty job the viewer owns gets a status. The status carries text the
// job wrote, and the capture view hands a job's output to its owner alone
// (lane.MayAttach); the row must not be a way around that.
func (m *Model) refreshStatus(rep *report.Report) {
	seen := map[string]bool{}
	next := map[string]osc7501.Record{}
	if rep != nil {
		for _, q := range rep.Queues {
			laneDir := lane.LaneDir(m.opt.Dir, q.Key)
			for _, group := range [][]lane.Entry{q.Holders, q.Waiting} {
				for _, e := range group {
					if !e.Ticket.PTY || e.PayloadError != "" || lane.MayAttach(e.Ticket, m.opt.Owner) != nil {
						continue
					}
					path, err := joblog.Path(laneDir, e.File)
					if err != nil {
						continue
					}
					k := statusKey(q.Key, e.File)
					seen[k] = true
					ls := m.logs[k]
					if ls == nil {
						ls = &osc7501.LogStatus{}
						m.logs[k] = ls
					}
					if r, ok := ls.Read(path); ok {
						next[k] = r
					}
				}
			}
		}
	}
	for k := range m.logs {
		if !seen[k] {
			delete(m.logs, k)
		}
	}
	m.status = next
}

// programStatus is the participant's status for the end of its line: the
// root record's "[state p%] msg" and its state, "" when there is none.
// The state and progress head is always kept whole; the msg gives up room
// first, and when even the head would crowd the identity part below its
// floor the status is left off the line.
func (m Model) programStatus(p participant, w int) (string, string) {
	r, ok := m.status[statusKey(m.key, p.entry.File)]
	if !ok {
		return "", ""
	}
	room := w - identityFloor - 2
	head := r.Head()
	if lipgloss.Width(head) > room {
		return "", ""
	}
	return r.Line(min(room-lipgloss.Width(head)-1, max(w/3, 10), 60)), r.State
}
