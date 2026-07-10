package tools

const (
	FunctionContextSummarize        = "context_summarize"
	FunctionContextEvict            = "context_evict"
	FunctionContextMerge            = "context_merge"
	FunctionContextAdjustWatermarks = "context_adjust_watermarks"
)

// ContextSummarizeTool compresses a demoted turn into a compact digest.
// Uses the existing Summarizer interface (sequential chunk-and-fold)
// and preserves citations mechanically.
var ContextSummarizeTool = newTool(FunctionContextSummarize,
	"Compresses a demoted turn into a compact digest. The digest keeps the citation so the "+
		"original messages stay reachable. Use this instead of recall when you need the gist of "+
		"an old turn without pulling its full raw messages into context.",
	objectSchema(map[string]any{
		"citation": stringProp("The citation to summarize, exactly as shown in the outline, e.g., @session/20260701-143210#m12-19"),
		"goal":     stringProp("Optional: What should the summary focus on? Default: the turn's key facts and decisions."),
		"budget":   map[string]any{"type": "integer", "description": "Token budget for the summary (default 512)"},
	}, "citation"))

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
		ContextSummarizeTool,
		ContextEvictTool,
		ContextMergeTool,
		ContextAdjustWatermarksTool,
	}
}
