// Package cliout owns the tool-surface contract for the cortex CLI:
//   - manifest.go: tools.json shape + generator (axis 1, Contract)
//   - envelope.go: uniform {ok, data, error, meta} wrapper (axis 4, Result)
//   - telemetry.go: per-invocation cell_results.jsonl row (axis 6, Observability)
//
// See docs/tool-surface.md for the six axes and docs/prompts/loop-phase-1-tool-surface.md
// for the work this package lands.
package cliout

// ManifestVersion is the schema version of tools.json itself. Bump when
// the manifest shape changes (not when an individual tool's surface
// changes — that's the per-entry Version field). CI diff compares the
// generated manifest against the committed one byte-for-byte, so a
// schema change is a deliberate two-step: bump this constant, regenerate.
const ManifestVersion = "1.0.0"

// ToolManifest is the top-level shape of tools.json. Sorted Tools list
// makes the file deterministic across regenerations (no map iteration
// order leaking into the output).
type ToolManifest struct {
	SchemaVersion string      `json:"schema_version"`
	Generated     string      `json:"generated_from"` // "cobra-style command registry" — descriptive, not load-bearing
	Tools         []ToolEntry `json:"tools"`
}

// ToolEntry is one command's manifest entry. Name and Description come
// from the Command interface; Args and Flags come from optional
// describer interfaces (NamedArgsDescriber, FlagDescriber). Version is
// the per-tool surface version — bump in the command itself when
// flags/args change in a way callers should notice.
type ToolEntry struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Version     string     `json:"version"`
	Args        []ArgSpec  `json:"args,omitempty"`
	Flags       []FlagSpec `json:"flags,omitempty"`
}

// ArgSpec describes one positional argument. Order in the slice matches
// the order the command consumes them on stdin.
type ArgSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Variadic    bool   `json:"variadic,omitempty"` // last arg may be repeated (e.g. query terms)
}

// FlagSpec describes one named flag. Type is the Go zero-value type
// ("string", "int", "bool", "float64") so callers can validate
// without rebuilding a FlagSet. Default is the flag's default value
// as a string (matches `flag.Flag.DefValue`), or empty when the flag
// has no default.
type FlagSpec struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Default     string `json:"default,omitempty"`
	Description string `json:"description"`
}
