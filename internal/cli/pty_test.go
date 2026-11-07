package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/lane"
)

// TestConfigPtyFlagRoundTrip: --pty sets the queue default, config shows it,
// --pty=false clears it, and the file says "pty": true only while set.
func TestConfigPtyFlagRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INCODA_DIR", dir)
	if code := Main([]string{"config", "seed"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("migrate: %d", code)
	}
	var out bytes.Buffer
	if code := Main([]string{"config", "seed", "--pty"}, &out, io.Discard); code != 0 {
		t.Fatalf("config --pty: %d", code)
	}
	if !strings.Contains(out.String(), "pty default: yes") {
		t.Fatalf("config display should say pty default: yes:\n%s", out.String())
	}
	b, err := os.ReadFile(filepath.Join(lane.LaneDir(dir, "seed"), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["pty"]) != "true" {
		t.Fatalf("config.json should carry pty:true, got %s", b)
	}
	out.Reset()
	if code := Main([]string{"config", "seed", "--pty=false"}, &out, io.Discard); code != 0 {
		t.Fatalf("config --pty=false: %d", code)
	}
	if !strings.Contains(out.String(), "pty default: no") {
		t.Fatalf("config display should say pty default: no:\n%s", out.String())
	}
	cfg, err := lane.ReadConfig(lane.LaneDir(dir, "seed"))
	if err != nil || cfg.PTY {
		t.Fatalf("after --pty=false the config should be off: %+v %v", cfg, err)
	}
}

// TestRunPtyRefusesOffWindows: a queue or flag asking for pty mode on a
// system without ConPTY is refused with a named reason before any ticket is
// taken.
func TestRunPtyRefusesOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the refusal is the non-Windows behavior")
	}
	dir := t.TempDir()
	t.Setenv("INCODA_DIR", dir)
	if code := Main([]string{"config", "seed"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("migrate: %d", code)
	}
	var stderr bytes.Buffer
	code := Main([]string{"run", "--queue", "seed", "--pty", "--", "true"}, io.Discard, &stderr)
	if code != ExitUsage || !strings.Contains(stderr.String(), "not implemented") {
		t.Fatalf("want 120 naming the missing implementation, got %d:\n%s", code, stderr.String())
	}
}
