//go:build windows

package pty

import (
	"strings"
	"testing"
	"unicode/utf16"
)

// parseEnvBlock reads a block the way CreateProcess does: NUL-terminated
// entries until an empty one ends the block. A block that double-terminates
// an entry early truncates the child's environment at that entry, which is
// how a job once launched with a single variable in it.
func parseEnvBlock(block []uint16) []string {
	var entries []string
	cur := make([]uint16, 0, 64)
	for _, u := range block {
		if u == 0 {
			if len(cur) == 0 {
				return entries
			}
			entries = append(entries, utf16ToString(cur))
			cur = cur[:0]
			continue
		}
		cur = append(cur, u)
	}
	return entries // malformed (no final empty entry); tests reject this shape
}

func utf16ToString(u []uint16) string { return string(utf16.Decode(u)) }

func TestEnvironmentBlockFeedsCreateProcessWhole(t *testing.T) {
	block, err := environmentBlock([]string{"ONE=first", "TWO=second", "THREE=third"})
	if err != nil {
		t.Fatal(err)
	}
	got := parseEnvBlock(block)
	want := []string{"ONE=first", "TWO=second", "THREE=third"}
	if len(got) != len(want) {
		t.Fatalf("CreateProcess would see %d entries (%q), want %d: a double NUL inside the block truncates the environment", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}
	// The block itself must end with the final empty entry.
	n := len(block)
	if n < 2 || block[n-1] != 0 || block[n-2] != 0 {
		t.Fatalf("block must end with two NULs (last entry's terminator plus the empty final entry), tail = %v", block[max(0, n-4):])
	}
}

func TestEnvironmentBlockDedupsLastWins(t *testing.T) {
	block, err := environmentBlock([]string{"PATH=a", "path=b", "OTHER=c"})
	if err != nil {
		t.Fatal(err)
	}
	got := parseEnvBlock(block)
	if len(got) != 2 {
		t.Fatalf("dedup should leave 2 entries, got %q", got)
	}
	seen := make(map[string]bool)
	pathVal := ""
	for _, kv := range got {
		k, v, _ := strings.Cut(kv, "=")
		uk := strings.ToUpper(k)
		if seen[uk] {
			t.Fatalf("duplicate key %q survived dedup in %q", k, got)
		}
		seen[uk] = true
		if uk == "PATH" {
			pathVal = v
		}
	}
	if pathVal != "b" {
		t.Fatalf("the later value must win case-insensitively, PATH=%q", pathVal)
	}
}
