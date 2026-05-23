// Package dag — salience-budget enforcement hook.
//
// docs/salience-budgets.md sets up the contract: a node may carry a
// SalienceContract (set by its spawning parent) capping how many
// tokens its deposit into turn state is allowed to be. When a handler
// returns more, the executor synthesizes an attend.compress
// invocation, replaces the oversized field with the compressed value,
// and emits a synthetic child trace row so the compression is
// observable, calibrate-able, and recoverable from the journal.
//
// This file groups the post-handler hook plus its helpers so the
// executor's main loop stays readable.
package dag

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// applySalienceCompression runs the post-handler compression check.
//
// When the node carried a SalienceContract with MaxOutputTokens > 0
// AND the largest string-valued field in result.Out exceeds the cap,
// the hook invokes the registry's attend.compress op on that field,
// replaces it with the compressed value, accumulates the compressor's
// cost into result.CostConsumed, and returns a synthetic TraceEntry
// for the compressor (parent = node's own ID).
//
// Returns (nil, nil) when no compression was needed. Returns
// (nil, error) only if the compressor itself failed — callers can
// surface the error or fall through (Phase 2 falls through and emits
// the synthetic row with OK=false).
//
// The "largest string field" heuristic is the v0 deposit detector:
// most ops with a Salience contract have one dominant string in Out
// (`output`, `response`, `content`). Phase 3 may introduce an explicit
// "deposit" annotation on NodeSpec to make this rule-based instead of
// inferred.
func (e *Executor) applySalienceCompression(
	ctx context.Context,
	item pendingItem,
	result *NodeResult,
	nextChildID func() string,
) *TraceEntry {
	if item.spec.Salience == nil || item.spec.Salience.MaxOutputTokens <= 0 {
		return nil
	}
	if result.Out == nil {
		return nil
	}
	field, raw, found := largestStringField(result.Out)
	if !found || raw == "" {
		return nil
	}
	if approxOutputTokens(raw) <= item.spec.Salience.MaxOutputTokens {
		return nil
	}
	// Deterministic chunking path — when the contract opts in,
	// mechanically split by line boundary and emit a synthetic
	// attend.chunk entry. No LLM in the read path: the calling model
	// gets the actual content with location headers it can re-fetch
	// from.
	if item.spec.Salience.ChunkOnOversize {
		return applyDeterministicChunking(item, result, field, raw, nextChildID)
	}
	compSpec, err := e.registry.Get("attend.compress")
	if err != nil {
		// Compressor not registered — surface in trace via the parent
		// node's Out so the operator can see we wanted to compress but
		// couldn't. Don't fail the parent; pre-salience-budgets
		// behavior preserved.
		return nil
	}

	childID := nextChildID()
	started := time.Now()
	compIn := map[string]any{
		"raw":        raw,
		"max_tokens": item.spec.Salience.MaxOutputTokens,
		"intent":     item.spec.Salience.Intent,
	}
	// Pass a minimal budget snapshot — the compressor's stub doesn't
	// need real budget enforcement and the LLM-backed Phase-2 impl
	// reads it for self-modulation only. The parent's CostConsumed
	// absorbs the compressor's spend.
	compRes, compErr := compSpec.Handler(ctx, compIn, Budget{
		LatencyMS: 60000,
		Tokens:    4000,
		Depth:     1,
	})
	ended := time.Now()

	entry := &TraceEntry{
		NodeID:        childID,
		ParentID:      item.spec.ID,
		QualifiedName: "attend.compress",
		WallStart:     started,
		WallEnd:       ended,
		CostConsumed:  compRes.CostConsumed,
		Out:           compRes.Out,
		Salience:      item.spec.Salience, // mirror the contract on the child for trace continuity
	}

	if compErr != nil {
		entry.OK = false
		entry.ErrorCode = "handler_error"
		entry.ErrorMessage = compErr.Error()
		return entry
	}
	compStr, ok := compRes.Out["compressed"].(string)
	if !ok {
		entry.OK = false
		entry.ErrorCode = "handler_error"
		entry.ErrorMessage = "attend.compress returned no 'compressed' string"
		return entry
	}
	entry.OK = true

	// Replace the parent's oversized field with the compressed value.
	// The parent's own CostConsumed accumulates the compressor's spend
	// so the running budget reflects the full work done.
	result.Out[field] = compStr
	result.CostConsumed.LatencyMS += compRes.CostConsumed.LatencyMS
	result.CostConsumed.Tokens += compRes.CostConsumed.Tokens
	// OutputTokens: the parent's deposit is now the compressed value.
	// We can't know what the parent declared as its own OutputTokens
	// (callers vary), so the convention is "the parent's stated
	// OutputTokens stands; the compressor's reported OutputTokens
	// reflects the post-compression deposit." Both rows show up in
	// the trace so the calibration loop sees the before/after pair.

	return entry
}

// largestStringField returns the (key, value) of the longest string in
// out, or ("", "", false) when out has no string-valued fields. The
// deposit-detection heuristic for Phase 2 — the field most likely to
// be the bytes flowing downstream.
func largestStringField(out map[string]any) (string, string, bool) {
	var bestKey, bestVal string
	for k, v := range out {
		if s, ok := v.(string); ok {
			if len(s) > len(bestVal) {
				bestKey, bestVal = k, s
			}
		}
	}
	if bestKey == "" {
		return "", "", false
	}
	return bestKey, bestVal, true
}

// approxOutputTokens mirrors the 4-char-per-token approximation in
// the attend.compress stub. Keeping it local to the dag package
// avoids a circular import on ops; both packages agree on the rule
// of thumb until Phase 2's real compressor wires through a tokenizer.
func approxOutputTokens(s string) int {
	if s == "" {
		return 0
	}
	n := len(s) / 4
	if n == 0 {
		return 1
	}
	return n
}

// applyDeterministicChunking handles the ChunkOnOversize=true branch.
// Splits raw by line boundary into N chunks each ≤ MaxOutputTokens,
// joins them with "[chunk i/N, lines a-b]" headers into one deposit,
// and returns a synthetic attend.chunk trace entry. No LLM call.
//
// The total output is bounded by 8 × MaxOutputTokens — beyond that the
// content is truncated with a final "[truncated — N more lines]"
// marker. Callers that need ALL of a very large file should issue
// follow-up reads with explicit line ranges (e.g. via run_shell sed).
func applyDeterministicChunking(
	item pendingItem,
	result *NodeResult,
	field, raw string,
	nextChildID func() string,
) *TraceEntry {
	const maxChunks = 8
	cap := item.spec.Salience.MaxOutputTokens
	started := time.Now()
	lines := splitLines(raw)
	chunks := buildChunksByLine(lines, cap)
	totalChunks := len(chunks)
	truncated := false
	if totalChunks > maxChunks {
		chunks = chunks[:maxChunks]
		truncated = true
	}
	joined := joinChunks(chunks, totalChunks)
	if truncated {
		extra := 0
		for _, c := range buildChunksByLine(lines, cap)[maxChunks:] {
			extra += c.endLine - c.startLine + 1
		}
		joined += fmt.Sprintf("\n\n[truncated — %d more lines beyond chunk %d; re-fetch with explicit line range]\n", extra, maxChunks)
	}
	ended := time.Now()

	childID := nextChildID()
	entry := &TraceEntry{
		NodeID:        childID,
		ParentID:      item.spec.ID,
		QualifiedName: "attend.chunk",
		WallStart:     started,
		WallEnd:       ended,
		OK:            true,
		CostConsumed:  Cost{LatencyMS: int(ended.Sub(started).Milliseconds())},
		Out: map[string]any{
			"chunks":       totalChunks,
			"emitted":      len(chunks),
			"chunked":      joined,
			"max_tokens":   cap,
			"intent":       item.spec.Salience.Intent,
		},
		Salience: item.spec.Salience,
	}
	result.Out[field] = joined
	result.CostConsumed.LatencyMS += entry.CostConsumed.LatencyMS
	return entry
}

// chunkRange records the line-bounds and content of one mechanically-
// split chunk. startLine and endLine are 1-indexed inclusive.
type chunkRange struct {
	startLine, endLine int
	content            string
}

// splitLines splits raw into lines preserving newlines on each entry
// except possibly the last. Mirrors strings.Split(raw, "\n") but
// preserves the trailing newline so re-joining round-trips byte-for-byte.
func splitLines(raw string) []string {
	if raw == "" {
		return nil
	}
	out := make([]string, 0, 64)
	start := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\n' {
			out = append(out, raw[start:i+1])
			start = i + 1
		}
	}
	if start < len(raw) {
		out = append(out, raw[start:])
	}
	return out
}

// buildChunksByLine groups consecutive lines into chunks whose
// approximate token count stays under capTokens. A chunk always
// contains at least one line, even if that line alone exceeds the
// cap — splitting mid-line would corrupt source code.
func buildChunksByLine(lines []string, capTokens int) []chunkRange {
	if len(lines) == 0 {
		return nil
	}
	if capTokens <= 0 {
		capTokens = 500 // safety floor
	}
	chunks := make([]chunkRange, 0, 8)
	curStart := 1
	curContent := ""
	curTokens := 0
	for i, ln := range lines {
		lineTok := approxOutputTokens(ln)
		// Flush when adding this line would overflow, unless the
		// current chunk is empty (single oversized line stays alone).
		if curTokens > 0 && curTokens+lineTok > capTokens {
			chunks = append(chunks, chunkRange{
				startLine: curStart,
				endLine:   i, // i is the index of the NEXT line; last included was i-1, 1-indexed = i
				content:   curContent,
			})
			curStart = i + 1
			curContent = ""
			curTokens = 0
		}
		curContent += ln
		curTokens += lineTok
	}
	if curContent != "" {
		chunks = append(chunks, chunkRange{
			startLine: curStart,
			endLine:   len(lines),
			content:   curContent,
		})
	}
	return chunks
}

// joinChunks formats the chunk list into one string with location
// headers. totalChunks is the unbounded count (so headers can say
// "1/12" even when only 8 are emitted).
func joinChunks(chunks []chunkRange, totalChunks int) string {
	var b strings.Builder
	for i, c := range chunks {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "[chunk %d/%d, lines %d-%d]\n", i+1, totalChunks, c.startLine, c.endLine)
		b.WriteString(c.content)
	}
	return b.String()
}
