package cli

import (
	"runtime/debug"
	"testing"
)

func TestFromBuildInfo(t *testing.T) {
	settings := func(kv ...string) []debug.BuildSetting {
		var out []debug.BuildSetting
		for i := 0; i+1 < len(kv); i += 2 {
			out = append(out, debug.BuildSetting{Key: kv[i], Value: kv[i+1]})
		}
		return out
	}
	cases := []struct {
		name                              string
		mainVersion                       string
		settings                          []debug.BuildSetting
		wantVersion, wantCommit, wantDate string
	}{
		{
			name:        "clean checkout",
			mainVersion: "v0.2.1-0.20260902101500-6790b60a1b2c",
			settings: settings(
				"vcs", "git",
				"vcs.revision", "6790b60a1b2c3d4e5f60718293a4b5c6d7e8f901",
				"vcs.time", "2026-09-02T10:15:00Z",
				"vcs.modified", "false",
			),
			wantVersion: "v0.2.1-0.20260902101500-6790b60a1b2c",
			wantCommit:  "6790b60",
			wantDate:    "2026-09-02T10:15:00Z",
		},
		{
			name:        "dirty checkout marks the commit",
			mainVersion: "(devel)",
			settings: settings(
				"vcs.revision", "6790b60a1b2c3d4e5f60718293a4b5c6d7e8f901",
				"vcs.time", "2026-09-02T10:15:00Z",
				"vcs.modified", "true",
			),
			wantVersion: "",
			wantCommit:  "6790b60+dirty",
			wantDate:    "2026-09-02T10:15:00Z",
		},
		{
			name:        "module proxy install has a version but no vcs",
			mainVersion: "v0.2.0",
			settings:    settings("-trimpath", "true"),
			wantVersion: "v0.2.0",
		},
		{
			name:        "nothing known",
			mainVersion: "(devel)",
		},
		{
			name:       "short revision passes through untouched",
			settings:   settings("vcs.revision", "abc", "vcs.modified", "true"),
			wantCommit: "abc+dirty",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, commit, date := fromBuildInfo(c.mainVersion, c.settings)
			if v != c.wantVersion || commit != c.wantCommit || date != c.wantDate {
				t.Errorf("fromBuildInfo(%q, ...) = (%q, %q, %q), want (%q, %q, %q)",
					c.mainVersion, v, commit, date, c.wantVersion, c.wantCommit, c.wantDate)
			}
		})
	}
	if _, commit, _ := fromBuildInfo("", settings("vcs.modified", "true")); commit != "" {
		t.Errorf("dirty flag without a revision produced commit %q, want empty", commit)
	}
}

func TestVersionInfoPrefersLdflags(t *testing.T) {
	// Save and restore: the package-level vars are process-wide.
	oldV, oldC, oldD := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = oldV, oldC, oldD })

	Version, Commit, Date = "v9.9.9", "abcdef0", "2026-01-01T00:00:00Z"
	v, c, d := versionInfo()
	if v != "v9.9.9" || c != "abcdef0" || d != "2026-01-01T00:00:00Z" {
		t.Errorf("versionInfo() = (%q, %q, %q), want the stamped values untouched", v, c, d)
	}

	// With the defaults in place the fallback must never hand back an empty
	// string, whatever the test binary's own build info happens to contain.
	Version, Commit, Date = "dev", "none", "unknown"
	v, c, d = versionInfo()
	if v == "" || c == "" || d == "" {
		t.Errorf("versionInfo() with defaults = (%q, %q, %q), empty field", v, c, d)
	}
}
