package machine

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/deblasis/incoda/internal/textsafe"
)

const (
	// probeOutputCap is the most output doctor reads from a version probe.
	probeOutputCap = 4096
	// probeWaitDelay bounds the wait for the probe's output pipe once the
	// probe itself has exited (a descendant may hold it open).
	probeWaitDelay = 500 * time.Millisecond
)

// probeTimeout is how long doctor lets `<path> version` run before it
// kills the probe's process group (a seam for tests).
var probeTimeout = 5 * time.Second

// errProbeTimeout means the probe did not finish within probeTimeout.
var errProbeTimeout = errors.New("did not answer within the time limit")

// cappedBuffer keeps the first probeOutputCap bytes written to it and
// accepts (and drops) the rest, so the probe never blocks on a full pipe.
type cappedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := probeOutputCap - c.b.Len(); room > 0 {
		if len(p) > room {
			c.b.Write(p[:room])
		} else {
			c.b.Write(p)
		}
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.b.String()
}

// Version is what a version probe found.
type Version struct {
	// Text is the version as printed ("v0.6.0", "dev"), escaped.
	Text         string
	Major, Minor int
	// Known is set when Text parsed as major.minor.patch.
	Known bool
}

// parseVersion reads the first line of `incoda version` output: "incoda
// <version>", where <version> is v0.6.0, 0.6.0, a pre-release such as
// v0.7.0-rc1, or "dev" for a build without a stamped version.
func parseVersion(out string) (Version, error) {
	line, _, _ := strings.Cut(out, "\n")
	f := strings.Fields(line)
	if len(f) != 2 || f[0] != "incoda" {
		return Version{}, fmt.Errorf("unexpected output %q", textsafe.Escape(truncate(line, 60)))
	}
	v := Version{Text: textsafe.Escape(f[1])}
	core := strings.TrimPrefix(f[1], "v")
	if i := strings.IndexAny(core, "-+"); i >= 0 {
		core = core[:i]
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, nil
	}
	nums := make([]int, 3)
	for i, s := range parts {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return v, nil
		}
		nums[i] = n
	}
	v.Major, v.Minor, v.Known = nums[0], nums[1], true
	return v, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// PathVersionLines are doctor's lines for the incodas on PATH (spec 5.5):
// one "on PATH:" line each, and an attention line for one older than 0.7,
// one whose version is dev or does not parse, and one that does not answer
// within the time limit. This binary is listed, not executed.
func PathVersionLines(path, self, selfVersion string) (lines, attention []string) {
	entries := IncodasOnPath(path, self)
	if len(entries) == 0 {
		return []string{"none"}, nil
	}
	for _, e := range entries {
		p := textsafe.Escape(e.Path)
		if e.Self {
			lines = append(lines, fmt.Sprintf("%s %s (this binary)", p, textsafe.Escape(selfVersion)))
			continue
		}
		out, err := ProbeVersion(e.Path)
		if errors.Is(err, errProbeTimeout) {
			lines = append(lines, p+" (no answer)")
			attention = append(attention, fmt.Sprintf("%s did not answer \"version\" within %s; if it is older than 0.7 it stops with \"not a directory\" on this state directory", p, probeTimeout))
			continue
		}
		v, perr := parseVersion(out)
		switch {
		case perr != nil:
			if err != nil {
				perr = fmt.Errorf("%s; %s", textsafe.Escape(err.Error()), perr)
			}
			lines = append(lines, p+" (version unknown)")
			attention = append(attention, fmt.Sprintf("%s: cannot read its version (%s); if it is older than 0.7 it stops with \"not a directory\" on this state directory", p, perr))
		case !v.Known:
			lines = append(lines, fmt.Sprintf("%s %s", p, v.Text))
			attention = append(attention, fmt.Sprintf("%s reports version %q: cannot tell whether it is older than 0.7; if it is, it stops with \"not a directory\" on this state directory", p, v.Text))
		case v.Major == 0 && v.Minor < 7:
			lines = append(lines, fmt.Sprintf("%s %s (older than 0.7)", p, v.Text))
			attention = append(attention, fmt.Sprintf("%s is incoda %s, older than 0.7: it stops with \"not a directory\" (exit 122) on this state directory and runs nothing; upgrade it (brew upgrade incoda, or the install script)", p, v.Text))
		default:
			lines = append(lines, fmt.Sprintf("%s %s", p, v.Text))
		}
	}
	return lines, attention
}
