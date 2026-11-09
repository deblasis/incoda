package lane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestConfigLimitsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	if _, err := q.UpdateConfig(func(c *Config) error {
		c.MaxRun, c.Stall = 90*time.Minute, 20*time.Minute
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err := q.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxRun != 90*time.Minute || got.Stall != 20*time.Minute {
		t.Fatalf("limits did not round-trip: %+v", got)
	}
	if EqualConfig(got, Config{}) {
		t.Fatal("a config with limits is not equal to the zero config")
	}
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), `"1h30m0s"`) {
		t.Fatalf("limits are stored as readable durations: %s", b)
	}

	// Zero clears and omits.
	if _, err := q.UpdateConfig(func(c *Config) error { c.MaxRun, c.Stall = 0, 0; return nil }); err != nil {
		t.Fatal(err)
	}
	got, _ = q.LoadConfig()
	b, _ = json.Marshal(got)
	if got.MaxRun != 0 || got.Stall != 0 || strings.Contains(string(b), "max_run") || strings.Contains(string(b), "stall") {
		t.Fatalf("zero limits are absent: %+v %s", got, b)
	}
}

// A limit that is not a duration must not take the lane's config down with
// it: the value is ignored (no limit, the safe direction), reported by
// Warnings, and everything else in the file still reads.
func TestConfigBadLimitIsIgnoredNotFatal(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(`{"slots":2,"max_run":"soon","stall":"-5s"}`), &c); err != nil {
		t.Fatalf("a bad limit must not fail the read: %v", err)
	}
	if c.Slots != 2 || c.MaxRun != 0 || c.Stall != 0 {
		t.Fatalf("slots survive, limits are off: %+v", c)
	}
	w := c.Warnings()
	if len(w) != 2 || !strings.Contains(w[0], "max_run") || !strings.Contains(w[1], "stall") {
		t.Fatalf("both bad values are reported: %q", w)
	}
	var ok Config
	if err := json.Unmarshal([]byte(`{"max_run":"5m"}`), &ok); err != nil || len(ok.Warnings()) != 0 {
		t.Fatalf("a good value has no warnings: %v %q", err, ok.Warnings())
	}
}

// A lane carrying a limit is stamped with a schema that an older incoda
// refuses by name, instead of running the lane with no limit. A lane with no
// limit keeps the schema older binaries read.
func TestConfigSchemaFollowsLimits(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	schema := func() int {
		c, err := q.LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		return c.Schema
	}
	if _, err := q.UpdateConfig(func(c *Config) error { c.Slots = 2; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := schema(); got != 2 {
		t.Fatalf("no limit, schema 2, got %d", got)
	}
	if _, err := q.UpdateConfig(func(c *Config) error { c.Stall = time.Minute; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := schema(); got != 3 {
		t.Fatalf("a limit makes it schema 3, got %d", got)
	}
	if _, err := q.UpdateConfig(func(c *Config) error { c.Stall = 0; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := schema(); got != 2 {
		t.Fatalf("clearing the limit returns to schema 2, got %d", got)
	}
}

func TestReleaseLogsTheLimitThatFired(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	en, err := q.Enroll(Ticket{Command: []string{"wedged"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := en.Acquire(context.Background(), AcquireOptions{Wait: 0}); err != nil {
		t.Fatal(err)
	}
	en.Stats = Stats{CPU: time.Second, HaveCPU: true, Limit: "stall"}
	en.Release(132)
	got := lastLog(t, q)
	for _, want := range []string{"event=release", "rc=132", "limit=stall", "cpu=1s"} {
		if !strings.Contains(got, want) {
			t.Fatalf("release line missing %q: %q", want, got)
		}
	}
	if s := (Stats{}).logFields(); strings.Contains(s, "limit") {
		t.Fatalf("no limit, no field: %q", s)
	}
}
