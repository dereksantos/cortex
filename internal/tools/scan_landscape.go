package tools

import (
	"fmt"
	"os"
	"strings"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/internal/landscape"
)

// landscapeMemoryNoteName is the fixed note name the scan_landscape tool
// writes to the CURRENT project's memory store (GOAL.md §3 P2: "only the
// scan_landscape coder tool writes one (fixed name `landscape`)").
const landscapeMemoryNoteName = "landscape"

// FunctionScanLandscape is the tool name for the coder-only landscape survey
// (GOAL.md M2.6). It is home-scoped and read-only: it checks only well-known
// harness/runtime paths under the user's home directory (existence-only, via
// internal/landscape) and never walks for projects — walking would be the
// blind-$HOME-sweep GOAL.md's D3 forbids. Project discovery stays behind
// persisted scan roots / --root (cmd/cortex's `cortex scan`, M2.5).
const FunctionScanLandscape = "scan_landscape"

// homeDirFunc resolves the OS home directory; overridable in tests so
// scanLandscape can be pointed at a fixture tree instead of the real home.
var homeDirFunc = os.UserHomeDir

var ScanLandscapeTool = newTool(FunctionScanLandscape,
	"Survey the user's local AI landscape: detected agent harnesses/editor "+
		"integrations (Claude, Cursor, Codex, ...) and local model runtimes "+
		"(Ollama, ...) under the user's home directory. Read-only and "+
		"existence-only (file contents are never read); confined to well-known "+
		"home-directory paths — it never walks the user's projects. Only call "+
		"this after the user has consented to a landscape survey.",
	objectSchema(map[string]any{}))

// scanLandscape probes the resolved home directory for known agent harnesses
// and local model runtimes and renders a compact text summary. It takes no
// tool-call arguments; ToolCall carries none for this tool. deps is
// MemoryStore + Quieter (Interface Segregation, per this package's doc
// comment) — scanLandscape's only needs are MemoryWrite and printToolAction's
// display gate, not the full ToolDeps surface.
//
// Unlike headless `cortex scan` (cmd/cortex/scan.go), this coder-only tool
// additionally writes a fixed-name "landscape" note to the CURRENT project's
// memory store — GOAL.md §3 P2's "only the scan_landscape coder tool writes
// one" — via the same deps.MemoryWrite path the memory_write tool uses, so
// the note is written identically to a model-driven write.
func scanLandscape(deps interface {
	MemoryStore
	Quieter
}) (string, error) {
	home, err := homeDirFunc()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	printToolAction(deps, "scan_landscape()")
	found, err := landscape.ScanHarnesses(home, landscape.Caps{})
	if err != nil {
		return "", fmt.Errorf("scan harnesses: %w", err)
	}
	runtimes, err := landscape.ScanRuntimes(home, landscape.Caps{})
	if err != nil {
		return "", fmt.Errorf("scan runtimes: %w", err)
	}
	result := renderLandscapeSurvey(found, runtimes)

	// Best-effort telemetry: a journal write failure (e.g. an unwritable
	// user home) must not block the tool result.
	_ = journal.AppendLandscapeScan(journal.LandscapeScanPayload{
		ToolCount:    len(found),
		RuntimeCount: len(runtimes),
	})

	// Explicit "project": the landscape note is always written to THIS
	// project's memory store, never the user tier (docs/cross-source-
	// learning.md piece 1's scope arg) — a home-directory survey is
	// per-machine-per-project context, not a cross-project fact.
	if _, err := deps.MemoryWrite(landscapeMemoryNoteName, result, "project"); err != nil {
		return "", fmt.Errorf("write landscape memory note: %w", err)
	}

	return result, nil
}

// renderLandscapeSurvey formats the harness/runtime findings as compact text
// for the tool-call result. Names and paths only, never content.
func renderLandscapeSurvey(harnesses []landscape.Tool, runtimes []landscape.Runtime) string {
	var b strings.Builder
	b.WriteString("AI landscape (home-scoped):\n")
	b.WriteString("  harnesses:")
	if len(harnesses) == 0 {
		b.WriteString(" none detected\n")
	} else {
		b.WriteString("\n")
		for _, h := range harnesses {
			fmt.Fprintf(&b, "    - %s (%s)\n", h.Name, h.Path)
		}
	}
	b.WriteString("  runtimes:")
	if len(runtimes) == 0 {
		b.WriteString(" none detected\n")
	} else {
		b.WriteString("\n")
		for _, r := range runtimes {
			fmt.Fprintf(&b, "    - %s (%s)\n", r.Name, r.Path)
		}
	}
	return b.String()
}
