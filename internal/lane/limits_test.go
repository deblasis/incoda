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

func TestConfigLimitsRejectGarbage(t *testing.T) {
	var c Config
	for _, bad := range []string{`{"max_run":"soon"}`, `{"stall":"-5s"}`} {
		if err := json.Unmarshal([]byte(bad), &c); err == nil {
			t.Fatalf("%s must not parse", bad)
		}
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
