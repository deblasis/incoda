package runplan

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/deblasis/incoda/internal/fixline"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/machine"
	"github.com/deblasis/incoda/internal/textsafe"
)

// quietWait is the --wait a printed line adds for a suggestion that
// carries quiet_machine when the caller gave none (spec 4.1, 2.8).
const quietWait = "5m"

// refusal joins the lines of one refusal block; the CLI prints "incoda: "
// before the first, and every later line carries it too.
func refusal(lines []string) *machine.Refusal {
	return &machine.Refusal{Msg: strings.Join(lines, "\nincoda: ")}
}

// Suggestion is the link spec 3.5 suggests for a key, usable only when
// every pool it names is registered on this machine.
type Suggestion struct {
	machine.Suggestion
	// Usable is false when the key matches no pattern or names a pool
	// this machine does not have; Why then says which.
	Usable bool
	Why    string
}

// Suggest looks up key's suggestion against reg.
func Suggest(reg *machine.Registry, key string) Suggestion {
	s, ok := machine.Suggest(key)
	if !ok {
		return Suggestion{Why: "no name pattern matches"}
	}
	if missing := reg.NotPools(s.Pools); len(missing) > 0 {
		return Suggestion{Suggestion: s, Why: fmt.Sprintf("the suggested pools %s are not all pools on this machine", strings.Join(s.Pools, ","))}
	}
	return Suggestion{Suggestion: s, Usable: true}
}

// Text is the suggestion as printed: "tests", or "tests, quiet_machine".
func (s Suggestion) Text() string {
	t := strings.Join(s.Pools, ",")
	if s.QuietMachine {
		t += ", quiet_machine"
	}
	return t
}

// PoolRows are the "pools on this machine" lines: key, slots and the
// description, escaped and cut to 60 columns.
func PoolRows(stateDir string, reg *machine.Registry) []string {
	width := 0
	for _, p := range reg.Pools {
		width = max(width, len(p))
	}
	var rows []string
	for _, p := range reg.Pools {
		cfg, err := lane.ReadConfig(lane.LaneDir(stateDir, p))
		slots, desc := "? slots", "(config.json unreadable)"
		if err == nil {
			n := max(cfg.Slots, 1)
			slots = fmt.Sprintf("%d slots", n)
			if n == 1 {
				slots = "1 slot"
			}
			desc = cut(textsafe.Escape(cfg.Description), 60)
		}
		row := fmt.Sprintf("  %-*s  %-8s %s", width, p, slots, desc)
		rows = append(rows, strings.TrimRight(row, " "))
	}
	return rows
}

// cut shortens s to n characters, the last three of them "...".
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-3]) + "..."
}

// quoted renders keys as "a", "b".
func quoted(keys []string) string {
	q := make([]string, len(keys))
	for i, k := range keys {
		q[i] = fmt.Sprintf("%q", k)
	}
	return strings.Join(q, ", ")
}

// fixFor is the caller's run line with the queue and pool the fix sets,
// plus --wait 5m when quiet is set and the caller gave no --wait. links
// are the links the printed config lines set before the run line (a
// project key absent from it keeps its stored link). It returns the line
// and the reason no runnable line may be printed, if a lane the line would
// take is closed or requires a reason and the run has none (spec 2.6).
func fixFor(stateDir string, reg *machine.Registry, req Request, queue, pool []string, links map[string][]string, quiet bool) (fixline.Run, string) {
	r := req.Fix
	r.Queue, r.Pool = queue, pool
	r.Flags = append([]fixline.Flag(nil), req.Fix.Flags...)
	if quiet && !req.WaitGiven {
		r.Flags = append(r.Flags, fixline.Flag{Name: "wait", Value: quietWait})
	}
	return r, lineRefused(stateDir, reg, req, queue, pool, links)
}

// lineRefused computes the lane set a printed line would take, as Make
// does: the keys in queue, plus each project key's link (from links when
// the fix sets it, else as stored), narrowed by the line's --pool. It
// says why the first lane in total order that would refuse the line does,
// or "" when none would.
func lineRefused(stateDir string, reg *machine.Registry, req Request, queue, pool []string, links map[string][]string) string {
	lanes := map[string]*Lane{}
	add := func(k string, isPool bool) *Lane {
		if l := lanes[k]; l != nil {
			return l
		}
		cfg, _ := lane.ReadConfig(lane.LaneDir(stateDir, k))
		l := &Lane{Key: k, Pool: isPool, Cfg: cfg}
		lanes[k] = l
		return l
	}
	for _, k := range queue {
		add(k, reg.IsPool(k)).Named = true
	}
	for _, k := range queue {
		if reg.IsPool(k) {
			continue
		}
		link, set := links[k]
		if !set {
			link = lanes[k].Cfg.Pools
		}
		use := machine.SortedSet(link)
		if pool != nil {
			use = pool
		}
		for _, p := range use {
			l := add(p, true)
			l.Via = append(l.Via, k)
		}
	}
	ls := make([]Lane, 0, len(lanes))
	for _, l := range lanes {
		ls = append(ls, *l)
	}
	sortLanes(ls)
	noReason := strings.TrimSpace(req.Reason) == ""
	for _, l := range ls {
		if l.Pool && !l.Named && req.Held[l.Key] {
			continue
		}
		if l.Cfg.Closed != "" {
			return fmt.Sprintf("queue %q is closed%s", l.Key, withRole(l))
		}
		if l.Cfg.RequireReason && noReason {
			return fmt.Sprintf("queue %q requires --reason%s and this run has none", l.Key, withRole(l))
		}
	}
	return ""
}

// namedOrder is the run's named keys in the total order: project keys,
// then pools, each sorted.
func namedOrder(reg *machine.Registry, named []string) []string {
	ls := make([]Lane, len(named))
	for i, k := range named {
		ls[i] = Lane{Key: k, Pool: reg.IsPool(k)}
	}
	sortLanes(ls)
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.Key
	}
	return out
}

// configLine is "incoda config KEY --pool S [--quiet-machine]".
func configLine(sh fixline.Shell, key string, s Suggestion) string {
	w := []fixline.Word{fixline.Lit("incoda"), fixline.Lit("config"), fixline.Key(key), fixline.Lit("--pool"), fixline.Key(strings.Join(s.Pools, ","))}
	if s.QuietMachine {
		w = append(w, fixline.Lit("--quiet-machine"))
	}
	return fixline.Line{Words: w}.Render(sh).Text
}

// unlinkedRefusal is the unlinked refusal of spec 4.1 for the named
// project keys in keys, which have no link. projects counts the run's
// named project keys: with one, the fix is that run with --pool set to the
// suggestion (its first link); with several, one config line per key and
// then the run line without --pool.
func unlinkedRefusal(stateDir string, reg *machine.Registry, req Request, keys []string, projects int) *machine.Refusal {
	sh := fixline.Native()
	lines := []string{"unlinked: " + strings.Join(keys, ", ")}
	if len(keys) == 1 {
		lines = append(lines, fmt.Sprintf("queue %q is not linked to any pool; every project queue names the machine-wide pools its jobs use.", keys[0]))
	} else {
		lines = append(lines, fmt.Sprintf("queues %s are not linked to any pool; every project queue names the machine-wide pools its jobs use.", quoted(keys)))
	}
	lines = append(lines, "pools on this machine:")
	lines = append(lines, PoolRows(stateDir, reg)...)

	sugg := make([]Suggestion, len(keys))
	all := true
	for i, k := range keys {
		sugg[i] = Suggest(reg, k)
		all = all && sugg[i].Usable
	}
	links := make([]string, len(keys))
	for i, k := range keys {
		links[i] = "incoda link " + k
	}
	askUser := []string{
		"(incoda init links every queue in one pass)",
		"do not pick a pool yourself, and never because it is free.",
	}

	if len(keys) == 1 {
		s, k := sugg[0], keys[0]
		if !s.Usable {
			lines = append(lines, fmt.Sprintf("suggested: none (%s)", s.Why),
				fmt.Sprintf("ask the user which pools this queue's jobs use; they run: incoda link %s", k))
			return refusal(append(lines, askUser...))
		}
		lines = append(lines, fmt.Sprintf("suggested: %s (name matches %s)", s.Text(), s.Pattern))
		takes := "these pools"
		if s.QuietMachine {
			takes = "these pools and quiet-machine"
		}
		if projects == 1 {
			lines = append(lines, fmt.Sprintf("to link it to the suggestion (stored; every later run on this queue takes %s):", takes))
			run, why := fixFor(stateDir, reg, req, namedOrder(reg, req.Named), s.Pools, map[string][]string{k: s.Pools}, s.QuietMachine)
			lines = append(lines, fixline.RunLines(sh, run, why, "run it")...)
		} else {
			lines = append(lines, fmt.Sprintf("to link it to the suggestion (stored; every later run on this queue takes %s), then run:", takes),
				"  "+configLine(sh, k, s))
			run, why := fixFor(stateDir, reg, req, namedOrder(reg, req.Named), nil, map[string][]string{k: s.Pools}, s.QuietMachine)
			lines = append(lines, fixline.RunLines(sh, run, why, "run it")...)
		}
		if s.QuietMachine && !req.WaitGiven {
			lines = append(lines, "(--wait 5m added: quiet-machine holds every pool it has drained while it waits for the rest)")
		}
		return refusal(append(lines, fmt.Sprintf("if the suggestion does not fit, ask the user; they run: incoda link %s", k)))
	}

	parts := make([]string, len(keys))
	quiet := false
	for i, k := range keys {
		if sugg[i].Usable {
			parts[i] = fmt.Sprintf("%s -> %s (name matches %s)", k, sugg[i].Text(), sugg[i].Pattern)
			quiet = quiet || sugg[i].QuietMachine
		} else {
			parts[i] = fmt.Sprintf("%s: none (%s)", k, sugg[i].Why)
		}
	}
	lines = append(lines, "suggested: "+strings.Join(parts, "; "))
	if !all {
		lines = append(lines, "ask the user which pools these queues' jobs use; they run: "+strings.Join(links, ", "))
		return refusal(append(lines, askUser...))
	}
	lines = append(lines, "to link them to the suggestions (stored; every later run on these queues takes these pools), then run:")
	set := map[string][]string{}
	for i, k := range keys {
		lines = append(lines, "  "+configLine(sh, k, sugg[i]))
		set[k] = sugg[i].Pools
	}
	run, why := fixFor(stateDir, reg, req, namedOrder(reg, req.Named), nil, set, quiet)
	lines = append(lines, fixline.RunLines(sh, run, why, "run it")...)
	if quiet && !req.WaitGiven {
		lines = append(lines, "(--wait 5m added: quiet-machine holds every pool it has drained while it waits for the rest)")
	}
	return refusal(append(lines, "if a suggestion does not fit, ask the user; they run: "+strings.Join(links, ", ")))
}

// linkNeedsUser refuses a --pool first link on an unlinked key that is not
// its suggestion (spec 4.2): run makes a first link only when the set
// equals the suggestion; anything else is the user's. unlinked are the
// run's unlinked named project keys and projects counts its named project
// keys: with several, a printed --pool would apply to every one of them
// and could be refused again, so the fix links each unlinked key with
// config and runs without --pool (as the unlinked refusal does).
func linkNeedsUser(stateDir string, reg *machine.Registry, req Request, key string, s Suggestion, unlinked []string, projects int) *machine.Refusal {
	switch {
	case s.Pattern == "":
		return refusal([]string{fmt.Sprintf("link-needs-user: %q has no suggested pools; ask the user; they run: incoda link %s", key, key)})
	case !s.Usable:
		return refusal([]string{fmt.Sprintf("link-needs-user: %q has no usable suggestion (%s); ask the user; they run: incoda link %s", key, s.Why, key)})
	}
	lines := []string{fmt.Sprintf("link-needs-user: %q suggests %s; a first link from run must equal it", key, strings.Join(s.Pools, ","))}
	if projects > 1 {
		cfg, set, quiet, ok := configFirst(reg, unlinked)
		if !ok {
			return refusal(append(lines, cfg...))
		}
		lead := "link it to the suggestion instead (stored; every later run on this queue takes these pools)"
		if len(unlinked) > 1 {
			lead = "link them to the suggestions instead (stored; every later run on these queues takes these pools)"
		}
		lines = append(lines, lead+", then run without --pool, which applies to every named project queue:")
		lines = append(lines, cfg...)
		run, why := fixFor(stateDir, reg, req, namedOrder(reg, req.Named), nil, set, quiet)
		lines = append(lines, fixline.RunLines(fixline.Native(), run, why, "run it")...)
		if quiet && !req.WaitGiven {
			lines = append(lines, "(--wait 5m added: quiet-machine holds every pool it has drained while it waits for the rest)")
		}
		return refusal(append(lines, "ask the user for anything else; they run: incoda link "+key))
	}
	lines = append(lines, "run it with the suggestion instead (stored; every later run on this queue takes these pools):")
	run, why := fixFor(stateDir, reg, req, namedOrder(reg, req.Named), s.Pools, map[string][]string{key: s.Pools}, s.QuietMachine)
	lines = append(lines, fixline.RunLines(fixline.Native(), run, why, "run it")...)
	if s.QuietMachine && !req.WaitGiven {
		lines = append(lines, "(--wait 5m added: quiet-machine holds every pool it has drained while it waits for the rest)")
	}
	return refusal(append(lines, "ask the user for anything else; they run: incoda link "+key))
}

// configFirst is the config line of each unlinked key, which links it to
// its suggestion, the links those lines set, and whether any suggestion
// carries quiet_machine. When a
// key has no usable suggestion there is no runnable line: ok is false and
// the one line returned asks the user to link every unlinked key.
func configFirst(reg *machine.Registry, unlinked []string) (lines []string, set map[string][]string, quiet, ok bool) {
	sh := fixline.Native()
	set = map[string][]string{}
	for _, k := range unlinked {
		s := Suggest(reg, k)
		if !s.Usable {
			links := make([]string, len(unlinked))
			for i, u := range unlinked {
				links[i] = "incoda link " + u
			}
			return []string{fmt.Sprintf("no runnable line: %q has no usable suggestion (%s); ask the user; they run: %s", k, s.Why, strings.Join(links, ", "))}, nil, false, false
		}
		lines = append(lines, "  "+configLine(sh, k, s))
		set[k] = s.Pools
		quiet = quiet || s.QuietMachine
	}
	return lines, set, quiet, true
}

// poolMismatch refuses a --pool set that is not part of key's link (spec
// 4.2). The fix holds the extra pools for this run only, by naming them
// next to the queue, and keeps the part of the set the link allows. With
// several named project keys a --pool would apply to all of them, so the
// line has none, and each unlinked key is linked to its suggestion first.
func poolMismatch(stateDir string, reg *machine.Registry, req Request, key string, link, unlinked []string, projects int) *machine.Refusal {
	in := map[string]bool{}
	for _, k := range link {
		in[k] = true
	}
	var extra, keep []string
	for _, k := range req.Pool {
		if in[k] {
			keep = append(keep, k)
		} else {
			extra = append(extra, k)
		}
	}
	lines := []string{fmt.Sprintf("pool-mismatch: %q is linked to %s; --pool %s is not part of it", key, strings.Join(link, ","), strings.Join(extra, ","))}
	hold := fmt.Sprintf("to also hold %s for this run only, name it next to the queue (no link change):", strings.Join(extra, ","))
	quiet := false
	var set map[string][]string
	if projects > 1 {
		keep = nil
		hold = fmt.Sprintf("to also hold %s for this run only, name it next to the queue (no link change; without --pool, which applies to every named project queue):", strings.Join(extra, ","))
		if len(unlinked) > 0 {
			cfg, s, q, ok := configFirst(reg, unlinked)
			if !ok {
				return refusal(append(append(lines, cfg...), "changing the link is the user's call; ask them."))
			}
			lead := fmt.Sprintf("link %s to its suggestion first (stored; every later run on this queue takes these pools):", unlinked[0])
			if len(unlinked) > 1 {
				lead = fmt.Sprintf("link %s to their suggestions first (stored; every later run on these queues takes these pools):", strings.Join(unlinked, ", "))
			}
			lines = append(append(lines, lead), cfg...)
			quiet, set = q, s
		}
	}
	lines = append(lines, hold)
	// A pool both named in --queue and in extra goes into the line once.
	queue := namedOrder(reg, machine.SortedSet(append(append([]string(nil), req.Named...), extra...)))
	run, why := fixFor(stateDir, reg, req, queue, keep, set, quiet)
	lines = append(lines, fixline.RunLines(fixline.Native(), run, why, "run it")...)
	if quiet && !req.WaitGiven {
		lines = append(lines, "(--wait 5m added: quiet-machine holds every pool it has drained while it waits for the rest)")
	}
	return refusal(append(lines, "changing the link is the user's call; ask them."))
}

// noProjectRefusal refuses --pool on a run that names only pools (spec
// 4.2).
func noProjectRefusal(stateDir string, reg *machine.Registry, req Request, pools []string) *machine.Refusal {
	what := fmt.Sprintf("%q is a pool", pools[0])
	if len(pools) > 1 {
		what = quoted(pools) + " are pools"
	}
	lines := []string{fmt.Sprintf("pool-mismatch: --pool needs a project key; %s", what), "rerun without --pool:"}
	run, why := fixFor(stateDir, reg, req, namedOrder(reg, req.Named), nil, nil, false)
	return refusal(append(lines, fixline.RunLines(fixline.Native(), run, why, "run it")...))
}
