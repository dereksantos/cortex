package bootstrap

import "strings"

// Op names used by the extract router. Keep these as constants rather
// than free-form strings so misspellings fail at compile time.
const (
	ExtractOpAuto     = "auto"
	ExtractOpInsight  = "extract_insight"
	ExtractOpOverview = "extract_overview"
)

// ChooseExtractOp resolves the configured ExtractOp + chunk metadata
// to the qualified op name the controller should call.
//
// When cfgExtractOp is "extract_insight" or "extract_overview", that
// choice wins unconditionally. When it's "auto" (the default), the
// router maps per-language:
//
//   - source-like (.go, .py, .js, .ts, .rs, .java, .c family, .cs, .swift,
//     .kt, .scala, .rb, .sh, .lua, .sql) → extract_overview, since the
//     prompt is tuned for "what is this file's job + what does it expose".
//   - config-like (.toml, .yaml, .ini, .tf) → extract_overview.
//   - prose (.md, .txt, .rst, unknown / no-extension) → extract_insight,
//     since the prompt is tuned for "what durable insight is here".
//
// Default-when-empty: the empty string maps to "auto" so callers that
// forget to set ExtractOp don't get a degenerate result.
//
// The mapping is a v1 default. Step 7 of docs/bootstrap-dag-plan.md
// runs a 12-chunk A/B that may revise it; the revision lands here as
// table edits (single-file change).
func ChooseExtractOp(cfgExtractOp, lang string) string {
	switch cfgExtractOp {
	case ExtractOpInsight, ExtractOpOverview:
		return "maintain." + cfgExtractOp
	}
	// auto (or unset) — route per language family.
	switch lang {
	case "go", "py", "js", "ts", "rs", "java", "c", "cs", "swift", "kt", "scala",
		"rb", "sh", "lua", "sql", "hs",
		"toml", "yaml", "ini", "tf":
		return "maintain." + ExtractOpOverview
	case "md", "txt", "rst", "unknown", "":
		return "maintain." + ExtractOpInsight
	}
	return "maintain." + ExtractOpOverview
}

// IsValidExtractOp reports whether op is a recognized configuration
// value (one of "auto", "extract_insight", "extract_overview", or
// empty for the auto default).
func IsValidExtractOp(op string) bool {
	switch strings.TrimSpace(op) {
	case "", ExtractOpAuto, ExtractOpInsight, ExtractOpOverview:
		return true
	}
	return false
}
