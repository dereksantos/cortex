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
// router returns extract_overview for every language family.
//
// Rationale: the 12-chunk A/B against Qwen3-Coder-30B (recorded in
// docs/eval-journal.md, 2026-05-21) scored overview 24/24 vs insight
// 11/24 across the full panel (Go/Python/TS × source/config/test/doc),
// at 1.15× token cost (under the 1.2× threshold). The insight prompt
// is calibrated for session-event extraction ("durable, actionable,
// teachable" decisions/corrections) — on source files it surfaces
// tangential patterns instead of the "what is this file's job" answer
// bootstrap needs. The go.mod case was the strongest signal: insight
// invented an architectural recommendation from a plain dependency
// declaration, overview produced a structured config summary.
//
// The lang parameter is kept (rather than removed) so future evals
// with revised prompts can re-introduce per-language routing without
// touching the call sites.
//
// Default-when-empty: the empty string maps to "auto" so callers that
// forget to set ExtractOp don't get a degenerate result.
func ChooseExtractOp(cfgExtractOp, lang string) string {
	switch cfgExtractOp {
	case ExtractOpInsight, ExtractOpOverview:
		return "maintain." + cfgExtractOp
	}
	_ = lang // reserved for future per-language routing; see A/B journal entry.
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
