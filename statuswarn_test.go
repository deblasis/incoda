package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/machine"
)

// TestStatusWarnsAboutUnpooledRunsAndAMissingFence: after a careless rm
// of the fence, plain status ends with the unpooled run and the missing
// fence, and still never re-fences. Every line before the warnings is
// what status printed before.
func TestStatusWarnsAboutUnpooledRunsAndAMissingFence(t *testing.T) {
	incoda, _ := binaries(t)
	state := t.TempDir()
	if out, code := runIncoda(t, incoda, state, "config", "seed"); code != 0 {
		t.Fatalf("migrate: %d\n%s", code, out)
	}
	clean, code := runIncoda(t, incoda, state, "status", "--queue", "seed", "--no-color")
	if code != 0 || strings.Contains(clean, "unpooled") || strings.Contains(clean, "fence missing") {
		t.Fatalf("a healthy layout has no warnings: %d\n%s", code, clean)
	}
	if err := os.Remove(filepath.Join(state, "queues")); err != nil {
		t.Fatal(err)
	}
	holdOldTicket(t, filepath.Join(state, "queues"), "oldjob", 999999, "zig", "build")
	out, code := runIncoda(t, incoda, state, "status", "--queue", "seed", "--no-color")
	want := "\nunpooled run by an older incoda: pid 999999, key oldjob\nfence missing: the next run re-places it (incoda doctor)\n"
	if code != 0 || !strings.HasSuffix(out, want) {
		t.Fatalf("want the warning block %q at the end, got %d:\n%s", want, code, out)
	}
	if machine.FencePlaced(state) {
		t.Fatal("status must never re-fence")
	}
}
