package cli

import (
	"io"
	"os"
	"runtime"
	"testing"
)

func TestChildEnvReplacesHeld(t *testing.T) {
	base := []string{"A=1", "INCODA_HELD=old", "B=2"}
	got := childEnv(base, "k=00000000000000000001-7.ticket")
	want := []string{"A=1", "B=2", "INCODA_HELD=k=00000000000000000001-7.ticket"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if got := childEnv(base, ""); len(got) != 2 {
		t.Fatalf("an empty held value removes the variable, got %v", got)
	}
}

func TestStartGetenvReadsFirstMatch(t *testing.T) {
	saved := startEnv
	defer func() { startEnv = saved }()
	startEnv = []string{"X=first", "X=second", "Y="}
	if got := startGetenv("X"); got != "first" {
		t.Fatalf("got %q", got)
	}
	if got := startGetenv("Y"); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := startGetenv("Z"); got != "" {
		t.Fatalf("got %q", got)
	}
}

// TestRunLeavesOwnEnvironmentAlone: incoda must never set INCODA_HELD on its
// own process; only the child gets it.
func TestRunLeavesOwnEnvironmentAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /usr/bin/true")
	}
	t.Setenv("INCODA_DIR", t.TempDir())
	os.Unsetenv("INCODA_HELD")
	if code := Main([]string{"run", "--queue", "envq", "--quiet", "--", "true"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("run exited %d", code)
	}
	if v, ok := os.LookupEnv("INCODA_HELD"); ok {
		t.Fatalf("incoda set INCODA_HELD=%q on itself", v)
	}
}
