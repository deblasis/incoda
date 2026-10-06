package cli

import (
	"os"
	"runtime"
	"strings"
)

// startEnv is the environment incoda was started with, captured during
// package initialisation, before any code can change the process
// environment. Decisions about inherited lanes read this copy and the
// child's environment is built from it, so incoda never calls os.Setenv on
// itself. Setting INCODA_HELD on the process once made the process-group
// decision see a value that only the child was meant to have, and no child
// got its own group from v0.3.0 on.
var startEnv = os.Environ()

// startGetenv is os.Getenv against startEnv: the first entry for key wins,
// as it does for the real environment.
func startGetenv(key string) string {
	for _, kv := range startEnv {
		k, v, ok := strings.Cut(kv, "=")
		if ok && envKeyEqual(k, key) {
			return v
		}
	}
	return ""
}

// childEnv returns base without any INCODA_HELD entry, plus
// INCODA_HELD=held when held is not empty.
func childEnv(base []string, held string) []string {
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if envKeyEqual(k, "INCODA_HELD") {
			continue
		}
		out = append(out, kv)
	}
	if held != "" {
		out = append(out, "INCODA_HELD="+held)
	}
	return out
}

// envKeyEqual compares variable names the way the platform does: Windows
// names are case-insensitive.
func envKeyEqual(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
