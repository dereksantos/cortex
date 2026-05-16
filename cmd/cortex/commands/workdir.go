package commands

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
)

// openWorkdirContext opens a config + storage rooted at <workdir>/.cortex.
// Used by --workdir flags on commands that benchmarks invoke against an
// isolated directory (avoiding the global ~/.cortex state the cwd-walking
// loadCaptureConfig/loadStorageConfig pair resolves to by default).
//
// Per eval-principles #6 (isolation): benchmark instances must never
// touch the user's real Cortex state. The --workdir flag is how that
// invariant is communicated from the benchmark to the CLI.
//
// Caller owns the returned storage and must Close it.
func openWorkdirContext(workdir string) (*config.Config, *storage.Storage, error) {
	if strings.TrimSpace(workdir) == "" {
		return nil, nil, fmt.Errorf("workdir is required")
	}
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return nil, nil, fmt.Errorf("workdir: %w", err)
	}
	cfg := &config.Config{
		ContextDir:  filepath.Join(abs, ".cortex"),
		ProjectRoot: abs,
	}
	store, err := storage.New(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("open storage at %s: %w", cfg.ContextDir, err)
	}
	return cfg, store, nil
}
