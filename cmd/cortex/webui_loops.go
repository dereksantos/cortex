// webui_loops.go — M6.7a: the loops screen view-model (GOAL.md §6 M6.7,
// split into M6.7a-e per STATE.md's 2026-07-13 split; GOAL.md §2 names
// cmd/cortex/webui*.go as home for the web UI's Go view-model builders).
// Per GOAL.md §3 P5, rendering logic lives in golden-tested Go view-models
// — this file is pure data assembly, no HTML/JS (the actual loops-screen
// render is M6.7f, owner amendment A4, not part of M6.7's literal DoD —
// see STATE.md's 2026-07-13 Decisions entries).
//
// The loops screen is "every registered loop, plus its run history"
// (docs/cortex-web.md Phase 6): internal/loops.Store.List() for the specs,
// composed with loops.JournalRunHistory (this increment's new production
// journal reader, internal/loops/history.go) grouped by name from the
// user-level loop.run journal.
package main

import (
	"fmt"
	"sort"

	"github.com/dereksantos/cortex/internal/loops"
)

// loopView is one registered loop's spec plus its full run history.
// Embeds loops.Spec directly rather than duplicating its fields — mirrors
// M5.2c's landscape view-model precedent of not wrapping a shape that
// already has exactly the fields a screen needs.
type loopView struct {
	loops.Spec
	Runs []loops.RunRecord `json:"runs"`
}

// loopsViewModel is the full loops screen.
type loopsViewModel struct {
	Loops []loopView `json:"loops"`
}

// buildLoopsViewModel composes internal/loops.Store.List() (specs) with
// each loop's run history (loops.JournalRunHistory) grouped by name,
// sorted by name for a deterministic render order — mirrors
// buildDashboardViewModel's registry-sort precedent since Store is an
// interface a fake test double need not sort.
func buildLoopsViewModel(store loops.Store) (loopsViewModel, error) {
	specs, err := store.List()
	if err != nil {
		return loopsViewModel{}, fmt.Errorf("failed to list loops: %w", err)
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })

	vm := loopsViewModel{Loops: make([]loopView, 0, len(specs))}
	for _, s := range specs {
		runs, err := loops.JournalRunHistory(s.Name)
		if err != nil {
			return loopsViewModel{}, fmt.Errorf("failed to read run history for loop %q: %w", s.Name, err)
		}
		if runs == nil {
			runs = []loops.RunRecord{}
		}
		vm.Loops = append(vm.Loops, loopView{Spec: s, Runs: runs})
	}
	return vm, nil
}
