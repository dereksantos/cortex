// Package userhome resolves the single Cortex user-home directory:
// $CORTEX_HOME if set, else ~/.cortex under the current user's home
// directory. Every machine-level path — user config, user journal,
// projects.json, loops.json, serve.token — should resolve through Dir (or
// Path) so tests can redirect the whole machine-state tree with one
// t.Setenv("CORTEX_HOME", tmp) instead of stubbing each caller separately.
package userhome

import (
	"fmt"
	"os"
	"path/filepath"
)

// Dir returns the resolved Cortex user-home directory. If $CORTEX_HOME is
// set (to any non-empty value) it is returned verbatim; otherwise Dir
// resolves to ~/.cortex under os.UserHomeDir().
func Dir() (string, error) {
	if h := os.Getenv("CORTEX_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve user home directory: %w", err)
	}
	return filepath.Join(home, ".cortex"), nil
}

// Path joins the resolved user-home directory (see Dir) with the given
// path elements — a convenience for callers building a path under the
// user home, e.g. userhome.Path("config.json").
func Path(elem ...string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{dir}, elem...)...), nil
}
