package lane

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// StateDir resolves the machine-local, per-user directory that holds every
// queue's state. Resolution order is INCODA_DIR, then the platform's
// conventional per-user state location.
func StateDir() (string, error) {
	if d := strings.TrimSpace(os.Getenv("INCODA_DIR")); d != "" {
		return filepath.Clean(d), nil
	}
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "incoda"), nil
		}
		return "", fmt.Errorf("LOCALAPPDATA is unset; set INCODA_DIR to choose a state directory")
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "incoda"), nil
	default:
		if d := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); d != "" {
			return filepath.Join(d, "incoda"), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "state", "incoda"), nil
	}
}

// QueuesDir is the parent of every per-key directory.
func QueuesDir(stateDir string) string { return filepath.Join(stateDir, "queues") }

// QueueDir is the per-key state directory. key must already be validated.
func QueueDir(stateDir, key string) string { return filepath.Join(QueuesDir(stateDir), key) }

// maxKeyLen keeps a key comfortably inside every filesystem's component limit
// even after the ticket-name suffix is appended.
const maxKeyLen = 64

// reservedNames are Windows device names. A directory called NUL or COM1 cannot
// be created there, so a key that looks like one is rejected everywhere for
// portability of the state directory.
var reservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// ValidateKey checks that key is safe to use as a single directory name. A key
// becomes a path component, so anything that could escape the state directory
// or confuse a filesystem is refused rather than sanitised: silently rewriting
// a key would let two callers that meant different queues share one.
func ValidateKey(key string) error {
	if key == "" {
		return fmt.Errorf("queue key is empty")
	}
	if len(key) > maxKeyLen {
		return fmt.Errorf("queue key %q is %d bytes; the limit is %d", key, len(key), maxKeyLen)
	}
	if key == "." || key == ".." {
		return fmt.Errorf("queue key %q is a path element, not a name", key)
	}
	if reservedNames[strings.ToLower(key)] {
		return fmt.Errorf("queue key %q is a reserved Windows device name", key)
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return fmt.Errorf("queue key %q contains %q; use letters, digits, '-', '_' or '.'", key, string(r))
		}
	}
	return nil
}
