package lane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestConfigRoundTripAndDefaults(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	// No file: every field at its zero value, and no error, because a
	// never-configured queue is the common case and must stay free to use.
	cfg, err := q.LoadConfig()
	if err != nil {
		t.Fatalf("missing config must not error: %v", err)
	}
	if !EqualConfig(cfg, Config{}) {
		t.Fatalf("missing config should be zero, got %+v", cfg)
	}

	want := Config{Slots: 2, Description: "CPU and RAM", RequireReason: true, Closed: "use wintty-build"}
	if err := q.SaveConfig(want); err != nil {
		t.Fatal(err)
	}
	got, err := q.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !EqualConfig(got, want) {
		t.Fatalf("config did not round-trip: got %+v want %+v", got, want)
	}

	// A malformed file is an error, not a silent reset to defaults: a
	// queue that quietly forgot it was closed would let the old key back in.
	if err := os.WriteFile(filepath.Join(q.Dir, configName), []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := q.LoadConfig(); err == nil {
		t.Fatal("a corrupt config must be reported")
	}
}

func TestConfigKeepsUnknownFieldsAndStampsSchema(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	path := filepath.Join(q.Dir, configName)
	if err := os.WriteFile(path, []byte(`{"slots":2,"from_the_future":{"x":1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpdateConfig(func(c *Config) error { c.Description = "d"; return nil }); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	// The file is pretty-printed, so the preserved field's own whitespace
	// is reflowed along with everything else; compare it compacted, which
	// is a content comparison rather than a byte-for-byte one.
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, raw["from_the_future"]); err != nil {
		t.Fatal(err)
	}
	if compacted.String() != `{"x":1}` {
		t.Fatalf("unknown field lost: %s", b)
	}
	if string(raw["schema"]) != "2" || string(raw["slots"]) != "2" || string(raw["description"]) != `"d"` {
		t.Fatalf("unexpected file: %s", b)
	}
}

func TestConfigNewerSchemaFailsClosed(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if err := os.WriteFile(filepath.Join(q.Dir, configName), []byte(`{"schema":3}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = q.LoadConfig()
	var ns *NewerSchemaError
	if !errors.As(err, &ns) || ns.Schema != 3 {
		t.Fatalf("want NewerSchemaError, got %v", err)
	}
	if _, err := q.UpdateConfig(func(*Config) error { return nil }); !errors.As(err, &ns) {
		t.Fatalf("a write must refuse too, got %v", err)
	}
}

func TestConfigPoolsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if _, err := q.UpdateConfig(func(c *Config) error { c.Pools = []string{"tests", "builds"}; c.QuietMachine = true; return nil }); err != nil {
		t.Fatal(err)
	}
	got, err := q.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Pools) != 2 || got.Pools[0] != "tests" || !got.QuietMachine || got.Schema != ConfigSchema {
		t.Fatalf("got %+v", got)
	}
}

// TestConfigUpdatesDoNotLoseWrites: two processes' worth of concurrent
// updates on different fields must both land, which an unlocked load then
// store cannot guarantee.
func TestConfigUpdatesDoNotLoseWrites(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q, err := Open(dir, "unit")
			if err != nil {
				t.Error(err)
				return
			}
			defer q.Close()
			_, err = q.UpdateConfig(func(c *Config) error {
				c.Pools = append(c.Pools, fmt.Sprintf("p%02d", i))
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	q, _ := Open(dir, "unit")
	defer q.Close()
	got, err := q.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Pools) != 20 {
		t.Fatalf("lost updates: %d of 20 landed: %v", len(got.Pools), got.Pools)
	}
}

func TestConfigSlotsAreTheDefaultForTickets(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if err := q.SaveConfig(Config{Slots: 3}); err != nil {
		t.Fatal(err)
	}

	// Slots 0 on the ticket means "whatever the queue says".
	a, err := q.Enroll(Ticket{Command: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release(0)
	snap, err := q.Observe(0)
	if err != nil {
		t.Fatal(err)
	}
	if snap.EffectiveSlots != 3 || snap.Holders[0].Ticket.Slots != 3 {
		t.Fatalf("config slots should apply to an unset ticket, got effective %d ticket %d", snap.EffectiveSlots, snap.Holders[0].Ticket.Slots)
	}

	// An explicit --slots that agrees with the config is fine: the ticket
	// carries the same number it would have been stamped with.
	b, err := q.Enroll(Ticket{Slots: 3, Command: []string{"b"}})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Release(0)
	if got := b.Ticket().Slots; got != 3 {
		t.Fatalf("an agreeing --slots should pass through, got %d", got)
	}
	b.Release(0)

	// A disagreeing --slots is refused in both directions. Narrowing was
	// allowed once and one stray "--slots 1" dragged a configured queue down
	// to a single slot for everyone; widening past the config was clamped.
	// Refusing the disagreement is the only behavior a caller can not
	// misread: the queue's config is the number.
	for _, ask := range []int{1, 2, 5} {
		if _, err := q.Enroll(Ticket{Slots: ask, Command: []string{"x"}}); err == nil {
			t.Fatalf("--slots %d on a 3-slot queue should be refused", ask)
		}
	}
}

// TestConfiguredSlotsFloorAdmission covers the defense behind the refusal: a
// ticket that carries a smaller or unset count anyway (a binary predating
// per-queue config, or one written during a rolling upgrade) must not narrow a
// configured queue. The effective width floors at the config, and admission
// (Acquire through Position) uses the same resolution.
func TestConfiguredSlotsFloorAdmission(t *testing.T) {
	// Resolution level: an unstamped or narrowed ticket rides at the
	// configured width; an exclusive one still forces 1; without a config
	// the minimum rules as it always did.
	narrow := []Entry{{Ticket: Ticket{Slots: 5}}, {Ticket: Ticket{Slots: 1}}}
	if got := effectiveSlots(narrow, 5); got != 5 {
		t.Fatalf("effectiveSlots on a 5-config queue with a slots=1 ticket = %d, want 5", got)
	}
	unstamped := []Entry{{Ticket: Ticket{Slots: 5}}, {Ticket: Ticket{}}}
	if got := effectiveSlots(unstamped, 5); got != 5 {
		t.Fatalf("effectiveSlots on a 5-config queue with an unstamped ticket = %d, want 5", got)
	}
	excl := []Entry{{Ticket: Ticket{Slots: 1, Exclusive: true}}}
	if got := effectiveSlots(excl, 5); got != 1 {
		t.Fatalf("exclusive must still force 1 on a configured queue, got %d", got)
	}
	if got := effectiveSlots(narrow, 0); got != 1 {
		t.Fatalf("without a config the minimum must rule, got %d", got)
	}

	// Admission level: plant the ticket a stale binary would leave (payload
	// says slots 1, lock held so it reads as live) and prove a configured
	// queue still admits a second holder alongside it.
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if err := q.SaveConfig(Config{Slots: 2}); err != nil {
		t.Fatal(err)
	}
	stale, err := q.Enroll(Ticket{Slots: 2, Command: []string{"stale"}})
	if err != nil {
		t.Fatal(err)
	}
	// Rewrite the payload the way an old binary would have written it: the
	// count is the only field that differs, and the lock stays ours.
	stale.ticket.Slots = 1
	b, err := json.Marshal(stale.ticket)
	if err != nil {
		t.Fatal(err)
	}
	if err := stale.lock.Truncate(b); err != nil {
		t.Fatal(err)
	}
	defer stale.Release(0)

	if err := stale.Acquire(context.Background(), AcquireOptions{Wait: 0}); err != nil {
		t.Fatal(err)
	}
	second, err := q.Enroll(Ticket{Command: []string{"second"}})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release(0)
	if err := second.Acquire(context.Background(), AcquireOptions{Wait: 0}); err != nil {
		t.Fatalf("a slots=1 ticket must not narrow a 2-slot configured queue: %v", err)
	}
}

// TestConfiguredSlotsCapAboveConfig is the other half of the clamp: a live
// ticket carrying a count ABOVE the config (a ticket written before the
// config was narrowed) must not widen the queue past what it now allows.
func TestConfiguredSlotsCapAboveConfig(t *testing.T) {
	wide := []Entry{{Ticket: Ticket{Slots: 8}}, {Ticket: Ticket{Slots: 5}}}
	if got := effectiveSlots(wide, 3); got != 3 {
		t.Fatalf("effectiveSlots with above-config tickets on a 3-slot queue = %d, want 3", got)
	}

	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if err := q.SaveConfig(Config{Slots: 2}); err != nil {
		t.Fatal(err)
	}
	// Plant the pre-narrowing ticket: enrolled at the configured count, then
	// rewritten to the wider count an older config would have stamped.
	stale, err := q.Enroll(Ticket{Slots: 2, Command: []string{"stale"}})
	if err != nil {
		t.Fatal(err)
	}
	stale.ticket.Slots = 8
	b, err := json.Marshal(stale.ticket)
	if err != nil {
		t.Fatal(err)
	}
	if err := stale.lock.Truncate(b); err != nil {
		t.Fatal(err)
	}
	defer stale.Release(0)
	if err := stale.Acquire(context.Background(), AcquireOptions{Wait: 0}); err != nil {
		t.Fatal(err)
	}

	first, err := q.Enroll(Ticket{Command: []string{"first"}})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release(0)
	if err := first.Acquire(context.Background(), AcquireOptions{Wait: 0}); err != nil {
		t.Fatal(err)
	}
	third, err := q.Enroll(Ticket{Command: []string{"third"}})
	if err != nil {
		t.Fatal(err)
	}
	defer third.Release(0)
	if err := third.Acquire(context.Background(), AcquireOptions{Wait: 0}); err != ErrTimeout {
		t.Fatalf("a slots=8 ticket must not widen a 2-slot configured queue: %v", err)
	}
	// And the observer agrees with admission: the count is the config's.
	snap, err := q.Observe(0)
	if err != nil {
		t.Fatal(err)
	}
	if snap.EffectiveSlots != 2 {
		t.Fatalf("snapshot effective slots = %d, want 2", snap.EffectiveSlots)
	}
}

// TestSlotsDisagreementError pins the one message and the two advice shapes
// every refusal path shares: a plain caller is pointed at --exclusive, a
// caller that already passed --exclusive is told to drop --slots.
func TestSlotsDisagreementError(t *testing.T) {
	plain := NewSlotsDisagreement("builds", 5, 1, false)
	msg := plain.Error()
	for _, want := range []string{
		`queue "builds" is configured for 5 slot(s)`,
		"--slots 1 is not allowed to disagree",
		"pass --exclusive if the job needs the queue alone",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("plain refusal missing %q:\n%s", want, msg)
		}
	}
	excl := NewSlotsDisagreement("builds", 5, 1, true).Error()
	if !strings.Contains(excl, "--exclusive already holds the queue alone") {
		t.Fatalf("exclusive refusal should tell the caller to drop --slots:\n%s", excl)
	}
	if strings.Contains(excl, "pass --exclusive") {
		t.Fatalf("exclusive refusal must not advise passing --exclusive:\n%s", excl)
	}

	// Enroll returns the typed error, so the CLI can map it to the usage
	// exit instead of a state error.
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if err := q.SaveConfig(Config{Slots: 3}); err != nil {
		t.Fatal(err)
	}
	_, err = q.Enroll(Ticket{Slots: 2, Command: []string{"x"}})
	var sd *SlotsDisagreement
	if !errors.As(err, &sd) || sd.Configured != 3 || sd.Asked != 2 {
		t.Fatalf("Enroll should refuse with a typed SlotsDisagreement, got %v", err)
	}
}

func TestAcquiredHolderStaysAHolderWhenExclusiveArrives(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if err := q.SaveConfig(Config{Slots: 2}); err != nil {
		t.Fatal(err)
	}
	var holders []*Enrollment
	for _, name := range []string{"a", "b"} {
		en, err := q.Enroll(Ticket{Command: []string{name}})
		if err != nil {
			t.Fatal(err)
		}
		if err := en.Acquire(context.Background(), AcquireOptions{Wait: 0}); err != nil {
			t.Fatal(err)
		}
		holders = append(holders, en)
	}
	x, err := q.Enroll(Ticket{Command: []string{"x"}, Exclusive: true})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Release(0)
	snap, err := q.Observe(0)
	if err != nil {
		t.Fatal(err)
	}
	// The exclusive ticket narrows the count to 1 for newcomers, but the
	// second holder is still running and must be reported as holding.
	if len(snap.Holders) != 2 || len(snap.Waiting) != 1 {
		t.Fatalf("want 2 holders and 1 waiter, got %d/%d", len(snap.Holders), len(snap.Waiting))
	}
	for _, h := range holders {
		h.Release(0)
	}
}

func TestExclusiveTicketForcesOneSlot(t *testing.T) {
	// An exclusive participant needs the queue to itself: while it is live
	// the effective slot count is 1 whatever anyone else asked for, and it
	// is not a "disagreement" worth warning about.
	live := []Entry{
		{Ticket: Ticket{Slots: 4}},
		{Ticket: Ticket{Slots: 4, Exclusive: true}},
	}
	if got := effectiveSlots(live, 0); got != 1 {
		t.Fatalf("effectiveSlots with an exclusive ticket = %d, want 1", got)
	}
	if SlotsDisagree(live) {
		t.Fatal("an exclusive ticket is not a slots disagreement")
	}

	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if err := q.SaveConfig(Config{Slots: 2}); err != nil {
		t.Fatal(err)
	}
	a, err := q.Enroll(Ticket{Command: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Acquire(context.Background(), AcquireOptions{Wait: 0}); err != nil {
		t.Fatal(err)
	}
	x, err := q.Enroll(Ticket{Command: []string{"x"}, Exclusive: true})
	if err != nil {
		t.Fatal(err)
	}
	// Two slots, one holder: a plain ticket would acquire now. The
	// exclusive one must wait for the queue to drain.
	if err := x.Acquire(context.Background(), AcquireOptions{Wait: 0}); err != ErrTimeout {
		t.Fatalf("exclusive ticket acquired alongside a holder: %v", err)
	}
	a.Release(0)
	if err := x.Acquire(context.Background(), AcquireOptions{Wait: 0}); err != nil {
		t.Fatalf("exclusive ticket should acquire an empty queue: %v", err)
	}
	// And nobody joins it while it holds, even though the queue says 2.
	b, err := q.Enroll(Ticket{Command: []string{"b"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Acquire(context.Background(), AcquireOptions{Wait: 0}); err != ErrTimeout {
		t.Fatalf("a ticket joined an exclusive holder: %v", err)
	}
	x.Release(0)
	b.Release(0)
}

func TestEnqueueLogsExclusive(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	en, err := q.Enroll(Ticket{Command: []string{"x"}, Exclusive: true})
	if err != nil {
		t.Fatal(err)
	}
	defer en.Release(0)
	if got := lastLog(t, q); !strings.Contains(got, "exclusive=true") {
		t.Fatalf("enqueue line should say exclusive, got %q", got)
	}
}

// TestEnrollRefusesClosedReasonlessAndNewer: Enroll checks the lane's rules
// under the registry lock (spec 2.5, 4.5), so a run that read the config
// before it changed is still refused, and creates no ticket. A config
// written by a newer incoda fails closed.
func TestEnrollRefusesClosedReasonlessAndNewer(t *testing.T) {
	q, err := Open(t.TempDir(), "rules")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	tickets := func() int {
		n := 0
		entries, _ := os.ReadDir(q.Dir)
		for _, e := range entries {
			if _, ok := parseTicketName(e.Name()); ok {
				n++
			}
		}
		return n
	}
	if err := q.SaveConfig(Config{Closed: "maintenance"}); err != nil {
		t.Fatal(err)
	}
	var ce *ClosedError
	if _, err := q.Enroll(Ticket{Reason: "r"}); !errors.As(err, &ce) || err.Error() != `queue "rules" is closed: maintenance` {
		t.Fatalf("closed: %v", err)
	}
	if err := q.SaveConfig(Config{RequireReason: true}); err != nil {
		t.Fatal(err)
	}
	var re *ReasonRequiredError
	if _, err := q.Enroll(Ticket{Reason: "  "}); !errors.As(err, &re) || !strings.HasPrefix(err.Error(), `queue "rules" requires --reason: `) {
		t.Fatalf("reason: %v", err)
	}
	if n := tickets(); n != 0 {
		t.Fatalf("a refused enrollment left %d ticket(s)", n)
	}
	en, err := q.Enroll(Ticket{Reason: "nightly"})
	if err != nil {
		t.Fatalf("with a reason it enrolls: %v", err)
	}
	en.Release(0)
	if err := os.WriteFile(filepath.Join(q.Dir, "config.json"), []byte(`{"schema":9}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var ns *NewerSchemaError
	if _, err := q.Enroll(Ticket{Reason: "r"}); !errors.As(err, &ns) {
		t.Fatalf("newer: %v", err)
	}
}
