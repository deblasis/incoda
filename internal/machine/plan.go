package machine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/deblasis/incoda/internal/atomicfile"
	"github.com/deblasis/incoda/internal/lane"
	"github.com/deblasis/incoda/internal/textsafe"
)

const planName = "migration.json"

func planPath(stateDir string) string { return filepath.Join(stateDir, planName) }

// migrationPlan is migration.json: proof that a migration is under way, and
// what it found. It never carries links.
type migrationPlan struct {
	TargetLayout int      `json:"target_layout"`
	Pools        []string `json:"pools"`
	Unreadable   []string `json:"unreadable_configs"`
	PID          int      `json:"pid"`
	PlannedAt    string   `json:"planned_at"`
}

// writePlan is M3. It is recomputed every time it is reached, from every
// lane's config as it is now; a plan from before a crash is never reused.
func writePlan(stateDir string) error {
	p := migrationPlan{TargetLayout: Layout, Pools: BootstrapPools(), Unreadable: []string{},
		PID: os.Getpid(), PlannedAt: time.Now().UTC().Format(time.RFC3339)}
	root := lane.QueuesDir(stateDir)
	if kindOf(root) == aDir {
		keys, err := lane.ListIn(root)
		if err != nil {
			return stateErrorf("cannot list queues/: %s", textsafe.Escape(err.Error()))
		}
		for _, k := range keys {
			if _, err := lane.ReadConfig(filepath.Join(root, k)); err != nil {
				p.Unreadable = append(p.Unreadable, k)
			}
		}
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return stateErrorf("cannot encode migration.json: %v", err)
	}
	if err := atomicfile.Write(planPath(stateDir), append(b, '\n'), 0o644); err != nil {
		return stateErrorf("cannot write migration.json: %s", textsafe.Escape(err.Error()))
	}
	return nil
}
