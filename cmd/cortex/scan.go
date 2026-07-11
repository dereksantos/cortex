// scan.go — M2.5: `cortex scan [--json] [--root <path>]`. Wires
// internal/landscape into a CLI subcommand per GOAL.md §3 P2 scanner and
// docs/cortex-web.md Phase 2: harnesses/runtimes probe the OS home
// directory (os.UserHomeDir(), matching their "well-known paths under
// $HOME" design); projects walk the persisted scan.roots config key
// (M1.7's PersistScanRoots) or an explicit --root override — never a
// blind $HOME sweep for project discovery. Neither present ⇒ typed
// refusal (ErrNoScanRoots), per GOAL.md M2.5.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/landscape"
)

// ErrNoScanRoots is returned by resolveScanRoots when neither an
// explicit --root nor a persisted scan.roots config entry is
// available. Typed so callers can distinguish "no roots configured
// yet" from any other resolution failure.
var ErrNoScanRoots = errors.New("no scan roots configured — run cortex and answer where your code lives, or pass --root <path>")

// defaultScanCaps bounds cortex scan's project walk: generous enough
// for a real project tree, bounded so a runaway filesystem can't hang
// the CLI. Harness/runtime probes don't recurse (landscape.go) so caps
// only matter for the project walk.
var defaultScanCaps = landscape.Caps{
	MaxDepth:   12,
	MaxEntries: 20000,
	Timeout:    10 * time.Second,
}

// resolveScanRoots picks the project-scan roots per GOAL.md M2.5: an
// explicit --root wins outright; otherwise the persisted scan.roots
// config key at configPath is used; if neither yields anything,
// ErrNoScanRoots refuses rather than silently sweeping $HOME.
func resolveScanRoots(configPath, rootFlag string) ([]string, error) {
	if rootFlag != "" {
		return []string{rootFlag}, nil
	}
	roots, err := readScanRoots(configPath)
	if err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		return nil, ErrNoScanRoots
	}
	return roots, nil
}

// readScanRoots reads the scan.roots array persisted by
// PersistScanRoots (scanroots.go). A missing file, or a file with no
// scan.roots key, yields an empty, non-error result — the "not
// configured yet" case resolveScanRoots turns into ErrNoScanRoots.
func readScanRoots(path string) ([]string, error) {
	doc, err := readJSONDoc(path)
	if err != nil {
		return nil, err
	}
	raw, ok := doc["scan"]
	if !ok || len(raw) == 0 {
		return nil, nil
	}
	var scan struct {
		Roots []string `json:"roots"`
	}
	if err := json.Unmarshal(raw, &scan); err != nil {
		return nil, fmt.Errorf("failed to parse scan.roots in %s: %w", path, err)
	}
	return scan.Roots, nil
}

// ScanReport aggregates a landscape scan across the OS home (harnesses,
// runtimes) and one or more project roots (projects) — the CLI-level
// composition GOAL.md M2.5's Decisions note calls for, reconciling
// landscape.Scan's single-root signature with production's two
// distinct real root kinds. Field names double as the JSON shape for
// `cortex scan --json`.
type ScanReport struct {
	Roots     []string            `json:"roots"`
	Tools     []landscape.Tool    `json:"tools"`
	Runtimes  []landscape.Runtime `json:"runtimes"`
	Projects  []landscape.Project `json:"projects"`
	Truncated bool                `json:"truncated"`
}

// buildScanReport runs the three scanner families — harnesses and
// runtimes against homeDir, projects against every entry in roots in
// order — and aggregates the results into one ScanReport.
func buildScanReport(homeDir string, roots []string, caps landscape.Caps) (ScanReport, error) {
	tools, err := landscape.ScanHarnesses(homeDir, caps)
	if err != nil {
		return ScanReport{}, fmt.Errorf("failed to scan harnesses: %w", err)
	}
	runtimes, err := landscape.ScanRuntimes(homeDir, caps)
	if err != nil {
		return ScanReport{}, fmt.Errorf("failed to scan runtimes: %w", err)
	}
	var projects []landscape.Project
	truncated := false
	for _, root := range roots {
		found, trunc, err := landscape.ScanProjects(root, caps)
		if err != nil {
			return ScanReport{}, fmt.Errorf("failed to scan projects under %s: %w", root, err)
		}
		projects = append(projects, found...)
		truncated = truncated || trunc
	}
	return ScanReport{Roots: roots, Tools: tools, Runtimes: runtimes, Projects: projects, Truncated: truncated}, nil
}

// recordLandscapeScan persists a landscape.scan event to the user-level
// journal (userhome-rooted, independent of any project's .cortex/journal)
// for one completed scan. Names/counts only, per LandscapeScanPayload's own
// content-non-leak doc. Deliberately does NOT touch internal/memory —
// headless `cortex scan` writes no memory note (GOAL.md M2.7; the coder-only
// scan_landscape tool is the one that does, see internal/tools/scan_landscape.go).
func recordLandscapeScan(r ScanReport) error {
	return journal.AppendLandscapeScan(journal.LandscapeScanPayload{
		Roots:        r.Roots,
		ToolCount:    len(r.Tools),
		RuntimeCount: len(r.Runtimes),
		ProjectCount: len(r.Projects),
		Truncated:    r.Truncated,
	})
}

// renderScanReport renders a ScanReport as the text report `cortex
// scan` prints by default (golden-pinned by TestRenderScanReportGolden
// — treat any change to this layout as a Decisions-Log-worthy
// intentional change, same convention as greetingPrompt).
func renderScanReport(r ScanReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Cortex landscape scan\n")
	fmt.Fprintf(&b, "Roots: %s\n\n", strings.Join(r.Roots, ", "))

	fmt.Fprintf(&b, "Harnesses & editors (%d):\n", len(r.Tools))
	for _, t := range r.Tools {
		fmt.Fprintf(&b, "  - %s (%s)\n", t.Name, t.Path)
	}
	if len(r.Tools) == 0 {
		fmt.Fprintf(&b, "  (none found)\n")
	}

	fmt.Fprintf(&b, "\nLocal model runtimes (%d):\n", len(r.Runtimes))
	for _, rt := range r.Runtimes {
		fmt.Fprintf(&b, "  - %s (%s)\n", rt.Name, rt.Path)
	}
	if len(r.Runtimes) == 0 {
		fmt.Fprintf(&b, "  (none found)\n")
	}

	fmt.Fprintf(&b, "\nProjects (%d):\n", len(r.Projects))
	for _, p := range r.Projects {
		fmt.Fprintf(&b, "  - %s [%s]\n", p.Path, strings.Join(p.Markers, ", "))
	}
	if len(r.Projects) == 0 {
		fmt.Fprintf(&b, "  (none found)\n")
	}

	if r.Truncated {
		fmt.Fprintf(&b, "\n(scan truncated by caps — results may be incomplete)\n")
	}
	return b.String()
}

// runScanCLI is the `cortex scan` entry point (dispatched from
// main.go). Argument parsing is manual (matching runTurnCLI's style)
// rather than the flag package, since this is the only case switch in
// main's dispatch and no other cmd/cortex file uses flag.
func runScanCLI(args []string) {
	asJSON := false
	root := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			asJSON = true
		case "--root":
			if i+1 < len(args) {
				root = args[i+1]
				i++
			}
		}
	}

	roots, err := resolveScanRoots(userConfigPath(), root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "scan:", err)
		os.Exit(1)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "scan:", err)
		os.Exit(1)
	}

	report, err := buildScanReport(homeDir, roots, defaultScanCaps)
	if err != nil {
		fmt.Fprintln(os.Stderr, "scan:", err)
		os.Exit(1)
	}

	// Best-effort telemetry: a journal write failure (e.g. an unwritable
	// user home) must not block the scan result itself.
	if err := recordLandscapeScan(report); err != nil {
		fmt.Fprintln(os.Stderr, "scan: warning: failed to record landscape.scan event:", err)
	}

	if asJSON {
		b, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "scan:", err)
			os.Exit(1)
		}
		fmt.Println(string(b))
		return
	}
	fmt.Print(renderScanReport(report))
}
