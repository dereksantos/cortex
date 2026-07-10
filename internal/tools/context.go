package tools

const (
	FunctionContextEvict            = "context_evict"
	FunctionContextMerge            = "context_merge"
	FunctionContextAdjustWatermarks = "context_adjust_watermarks"
)

// ContextEvictTool removes an outline entry from the working set.
// The entry is already demoted (in the journal), so this only affects the hydrated tail and outline.
var ContextEvictTool = newTool(FunctionContextEvict,
	"Removes an entry from the session outline. The turn's raw messages stay in the transcript "+
		"(a later recall of the citation still works), so this is safe — use it to drop outline "+
		"entries that are clearly irrelevant to the ongoing work. Reverts on session resume.",
	objectSchema(map[string]any{
		"citation": stringProp("The citation to evict, exactly as shown in the outline, e.g., @session/20260701-143210#m12-19"),
	}, "citation"))

// ContextMergeTool merges consecutive demoted turns into a single outline entry.
// Deterministic (no LLM) and preserves citations.
var ContextMergeTool = newTool(FunctionContextMerge,
	"Merges a contiguous range of outline entries into one entry with a single spanning citation "+
		"(recall still resolves every original message). Deterministic, no LLM. Use this to reduce "+
		"outline clutter when several small related turns can be grouped.",
	objectSchema(map[string]any{
		"range_start": stringProp("Citation of the first entry in the range, exactly as shown in the outline, e.g., @session/20260701-143210#m12-19"),
		"range_end":   stringProp("Citation of the last entry in the range, e.g., @session/20260701-143210#m30-42"),
	}, "range_start", "range_end"))

// ContextAdjustWatermarksTool dynamically adjusts the working set watermarks.
// Bounded (±W/4) to prevent abuse.
var ContextAdjustWatermarksTool = newTool(FunctionContextAdjustWatermarks,
	"Adjusts the working-set watermarks (token thresholds where old turns demote to the outline) "+
		"by the given deltas, bounded to ±W/4. Raise them to keep more recent turns verbatim during "+
		"a dense task; lower them to demote more aggressively. Reverts on session resume.",
	objectSchema(map[string]any{
		"high_delta": map[string]any{"type": "integer", "description": "Change to high watermark (default: 0)"},
		"low_delta":  map[string]any{"type": "integer", "description": "Change to low watermark (default: 0)"},
	}, "high_delta", "low_delta"))

// AllContextTools returns all context window modification tools.
// This is a helper for tests and documentation generation.
func AllContextTools() []Tool {
	return []Tool{
		ContextEvictTool,
		ContextMergeTool,
		ContextAdjustWatermarksTool,
	}
}
