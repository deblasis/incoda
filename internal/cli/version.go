package cli

import "runtime/debug"

// Version information, stamped at build time with -ldflags -X (see `just
// dist`). A plain `go install` leaves the defaults, and versionInfo then fills
// in whatever the module's embedded build info knows instead.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// versionInfo returns the version, commit and build time to report. The ldflags
// values win whenever they were set; only a value still at its default is taken
// from runtime/debug's build info.
func versionInfo() (version, commit, date string) {
	version, commit, date = Version, Commit, Date
	if version != "dev" && commit != "none" && date != "unknown" {
		return version, commit, date
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return version, commit, date
	}
	v, c, d := fromBuildInfo(bi.Main.Version, bi.Settings)
	if version == "dev" && v != "" {
		version = v
	}
	if commit == "none" && c != "" {
		commit = c
	}
	if date == "unknown" && d != "" {
		date = d
	}
	return version, commit, date
}

// fromBuildInfo extracts what the build info knows, returning "" for anything
// it does not. The commit is shortened to the same 7 characters the release
// build stamps, with "+dirty" appended when the tree had uncommitted changes.
func fromBuildInfo(mainVersion string, settings []debug.BuildSetting) (version, commit, date string) {
	if mainVersion != "" && mainVersion != "(devel)" {
		version = mainVersion
	}
	dirty := false
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			commit = s.Value
		case "vcs.time":
			date = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if len(commit) > 7 {
		commit = commit[:7]
	}
	if commit != "" && dirty {
		commit += "+dirty"
	}
	return version, commit, date
}
