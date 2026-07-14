// firstrun.go — first-run detection (Phase 1 / M1.4).
//
// GOAL.md §3 P1 first-run (binding, outranks docs/cortex-web.md's older
// "no prior sessions" text): first-run ⇔ no config.json under the user
// home AND no greeting marker under the user home. The greeting turn
// (M1.5) writes the marker when it fires. Sessions are project-scoped and
// play NO part in this determination.
//
// This file defines the predicate and the marker's path only; wiring the
// greeting turn (which writes the marker) into NewCortexSession/main.go
// lands in M1.5.
package main

import (
	"os"

	"github.com/dereksantos/cortex/internal/userhome"
)

// greetingMarkerName is the file under the user home whose presence
// records that the greeting turn has already fired once.
const greetingMarkerName = "greeted"

// greetingMarkerPath resolves the greeting marker's path under the user
// home (see userhome.Path). Empty on resolution failure, matching
// userConfigPath's degrade-to-empty convention.
func greetingMarkerPath() string {
	p, err := userhome.Path(greetingMarkerName)
	if err != nil {
		return ""
	}
	return p
}

// pathExists reports whether path is non-empty and names a file that
// exists (stat succeeds).
func pathExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// IsFirstRun reports whether this is the user's first run of cortex:
// no config.json under the user home AND no greeting marker under the
// user home (GOAL.md §3 P1 first-run). Sessions are project-scoped and
// play no part in this determination.
func IsFirstRun() bool {
	return !pathExists(userConfigPath()) && !pathExists(greetingMarkerPath())
}
