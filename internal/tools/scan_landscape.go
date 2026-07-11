package tools

import (
	"fmt"
	"os"
	"strings"

	"github.com/dereksantos/cortex/internal/landscape"
)

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
// tool-call arguments; ToolCall carries none for this tool.
func scanLandscape() (string, error) {
	home, err := homeDirFunc()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	printToolAction("scan_landscape()")
	found, err := landscape.ScanHarnesses(home, landscape.Caps{})
	if err != nil {
		return "", fmt.Errorf("scan harnesses: %w", err)
	}
	runtimes, err := landscape.ScanRuntimes(home, landscape.Caps{})
	if err != nil {
		return "", fmt.Errorf("scan runtimes: %w", err)
	}
	return renderLandscapeSurvey(found, runtimes), nil
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
