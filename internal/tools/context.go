package tools

const (
	FunctionContextSummarize        = "context_summarize"
	FunctionContextEvict            = "context_evict"
	FunctionContextMerge            = "context_merge"
	FunctionContextReorder          = "context_reorder"
	FunctionContextAdjustWatermarks = "context_adjust_watermarks"
)

// ContextSummarizeTool compresses a demoted turn into a compact digest.
// Uses the existing Summarizer interface (sequential chunk-and-fold)
// and preserves citations mechanically.
var ContextSummarizeTool = newTool(FunctionContextSummarize,
	"Compresses a demoted turn into a compact digest using the existing summarizer interface. "+
		"The summary preserves citations so the model can trace back to the original messages. "+
		"Use this when the hydrated tail is growing too large and you want to reduce its token count.",
	objectSchema(map[string]any{
		"citation": stringProp("The citation to summarize, e.g., @session/20260701-143210#t1"),
		"goal":     stringProp("Optional: What should the summary focus on? Default: summarize the turn."),
		"budget":   map[string]any{"type": "integer", "description": "Token budget for the summary (default: half window)"},
	}, "citation"))

// ContextEvictTool removes an outline entry from the working set.
// The entry is already demoted (in the journal), so this only affects the hydrated tail and outline.
var ContextEvictTool = newTool(FunctionContextEvict,
	"Removes an outline entry from the working set. The entry is already demoted (in the journal), "+
		"so this only affects the hydrated tail and outline. Use this to manually evict low-value "+
		"entries when the automatic demotion isn't keeping up.",
	objectSchema(map[string]any{
		"citation": stringProp("The citation to evict, e.g., @session/20260701-143210#t1"),
	}, "citation"))

// ContextMergeTool merges consecutive demoted turns into a single outline entry.
// Deterministic (no LLM) and preserves citations.
var ContextMergeTool = newTool(FunctionContextMerge,
	"Merges consecutive demoted turns into a single outline entry. This is deterministic (no LLM) "+
		"and preserves all citations. Use this to reduce outline clutter when multiple small turns "+
		"can be grouped together.",
	objectSchema(map[string]any{
		"range_start": stringProp("Start citation of range, e.g., @session/20260701-143210#t1"),
		"range_end":   stringProp("End citation of range, e.g., @session/20260701-143210#t5"),
	}, "range_start", "range_end"))

// ContextReorderTool reorders the hydrated tail based on the given metric.
// Only rearranges order—it does not evict or compress.
var ContextReorderTool = newTool(FunctionContextReorder,
	"Reorders the hydrated tail based on the given metric. Only rearranges order—it does not evict or compress. "+
		"Use this when certain turns are more relevant to the current goal and should be kept closer to the front.",
	objectSchema(map[string]any{
		"by": stringProp("Metric to sort by: 'salience', 'recency', or 'task-relevance'"),
	}, "by"))

// ContextAdjustWatermarksTool dynamically adjusts the working set watermarks.
// Bounded (±W/4) to prevent abuse.
var ContextAdjustWatermarksTool = newTool(FunctionContextAdjustWatermarks,
	"Dynamically adjusts the working set watermarks by the given deltas. "+
		"Bounded (±W/4) to prevent abuse. Use this when you need more buffer space "+
		"for a large operation or want to be more aggressive about demotion.",
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
		ContextReorderTool,
		ContextAdjustWatermarksTool,
	}
}
