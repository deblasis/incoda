// Package held reads and writes INCODA_HELD, the lanes an ancestor incoda
// holds on a process's behalf. Each entry names the exact ticket
// (KEY=TICKET), so a nested run can check that the ticket is alive and that
// its holder is really an ancestor before it trusts the entry.
package held

import (
	"sort"
	"strings"

	"github.com/deblasis/incoda/internal/lane"
)

// Entry is one held lane: its key and the holder's ticket file name.
type Entry struct {
	Key    string
	Ticket string
}

func (e Entry) String() string { return e.Key + "=" + e.Ticket }

// Bad is an entry that does not parse: a pre-0.7 bare key, a missing part,
// a key that fails ValidateKey, or a ticket that is not a ticket file name.
type Bad struct{ Raw string }

// Parse splits raw into entries. Both parts are validated before anything
// touches the filesystem. The first entry for a key wins.
func Parse(raw string) ([]Entry, []Bad) {
	var out []Entry
	var bad []Bad
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, t, ok := strings.Cut(part, "=")
		if !ok || lane.ValidateKey(k) != nil || !lane.ValidTicketName(t) {
			bad = append(bad, Bad{Raw: part})
			continue
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, Entry{Key: k, Ticket: t})
	}
	return out, bad
}

// Format renders entries sorted by key, the value a child receives.
func Format(entries []Entry) string {
	s := append([]Entry(nil), entries...)
	sort.Slice(s, func(i, j int) bool { return s[i].Key < s[j].Key })
	parts := make([]string, len(s))
	for i, e := range s {
		parts[i] = e.String()
	}
	return strings.Join(parts, ",")
}

// Merge returns a plus b, one entry per key, b winning on a shared key.
func Merge(a, b []Entry) []Entry {
	byKey := map[string]Entry{}
	for _, e := range a {
		byKey[e.Key] = e
	}
	for _, e := range b {
		byKey[e.Key] = e
	}
	out := make([]Entry, 0, len(byKey))
	for _, e := range byKey {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
