package tui

import (
	"github.com/deblasis/incoda/internal/joblog"
	"github.com/deblasis/incoda/internal/jobstatus"
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
// Any pty job gets a status. The status carries text the job wrote, and
// the capture view is open to everyone, so the row hides nothing.
func (m *Model) refreshStatus(rep *report.Report) {
	seen := map[string]bool{}
	next := map[string]osc7501.Record{}
	if rep != nil {
		for _, q := range rep.Queues {
			laneDir := lane.LaneDir(m.opt.Dir, q.Key)
			for _, group := range [][]lane.Entry{q.Holders, q.Waiting} {
				for _, e := range group {
					if !e.Ticket.PTY || e.PayloadError != "" {
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
						ls = &jobstatus.LogStatus{}
						m.logs[k] = ls
					}
					if r, ok := ls.Read(path); ok {
						// A done report from a live run is a sub-task's, not
						// the job's ending: it claims nothing while the pid
						// lives (see osc7501.Record.WhileAlive).
						if r, ok = r.WhileAlive(m.opt.PtyAlive(e.Ticket.PID)); ok {
							next[k] = r
						}
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

// programStatus is the participant's record from the status cache, for the
// badge beside the pty mark and the msg at the end of its line; ok=false
// when the log carries none. The msg is cut to what the line has room
// for; the badge (a glyph, plus a percent) always fits.
func (m Model) programStatus(p participant, w int) (osc7501.Record, bool) {
	r, ok := m.status[statusKey(m.key, p.entry.File)]
	if !ok {
		return osc7501.Record{}, false
	}
	if room := w - identityFloor - 2; room < 10 {
		return osc7501.Record{}, false
	}
	return r, true
}

// statusMsg is the record's msg for the end of a participant line, cut to
// what the line can spare once the identity part keeps its floor.
func statusMsg(r osc7501.Record, w int) string {
	return r.Text(min(max(w/3, 10), 60))
}
