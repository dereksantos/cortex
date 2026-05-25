package dag

import (
	"strings"
	"testing"
)

func TestSplitLines(t *testing.T) {
	tests := map[string]int{
		"":                       0,
		"a\nb\nc":                3,
		"a\nb\nc\n":              3,
		"\n":                     1,
		"single line no newline": 1,
	}
	for input, wantCount := range tests {
		got := splitLines(input)
		if len(got) != wantCount {
			t.Errorf("splitLines(%q): got %d lines, want %d (%+v)", input, len(got), wantCount, got)
		}
		// Round-trip: joining all parts byte-for-byte must equal input.
		joined := strings.Join(got, "")
		if joined != input {
			t.Errorf("splitLines(%q): round-trip mismatch — got %q", input, joined)
		}
	}
}

func TestBuildChunksByLine_FitsInSingleChunk(t *testing.T) {
	lines := []string{"line a\n", "line b\n", "line c\n"}
	chunks := buildChunksByLine(lines, 1000)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk for small input, got %d", len(chunks))
	}
	if chunks[0].startLine != 1 || chunks[0].endLine != 3 {
		t.Errorf("chunk bounds: got %d-%d want 1-3", chunks[0].startLine, chunks[0].endLine)
	}
}

func TestBuildChunksByLine_SplitsByCap(t *testing.T) {
	// Each line is ~10 chars = ~3 tokens. cap=5 → 1-2 lines per chunk.
	lines := []string{
		"line one\n",   // 9 chars ≈ 2 tokens
		"line two\n",   // 9 chars ≈ 2 tokens
		"line three\n", // 11 chars ≈ 2 tokens
		"line four\n",  // 10 chars ≈ 2 tokens
	}
	chunks := buildChunksByLine(lines, 5)
	if len(chunks) < 2 {
		t.Fatalf("expected ≥2 chunks for cap=5 vs 4 lines, got %d", len(chunks))
	}
	// Round-trip: concatenating all chunk contents must equal original.
	joined := ""
	for _, c := range chunks {
		joined += c.content
	}
	want := strings.Join(lines, "")
	if joined != want {
		t.Errorf("chunks round-trip mismatch:\n got: %q\nwant: %q", joined, want)
	}
	// Line bounds must tile [1, N] without gaps or overlaps.
	expectedStart := 1
	for i, c := range chunks {
		if c.startLine != expectedStart {
			t.Errorf("chunk %d startLine: got %d want %d", i, c.startLine, expectedStart)
		}
		if c.endLine < c.startLine {
			t.Errorf("chunk %d: endLine %d < startLine %d", i, c.endLine, c.startLine)
		}
		expectedStart = c.endLine + 1
	}
	if expectedStart-1 != len(lines) {
		t.Errorf("chunks span %d lines, want %d", expectedStart-1, len(lines))
	}
}

func TestBuildChunksByLine_OversizedSingleLineStandsAlone(t *testing.T) {
	// A single line larger than the cap should not be split mid-line —
	// it stays as its own chunk. Splitting source code mid-line would
	// corrupt it.
	bigLine := strings.Repeat("x", 4000) + "\n"
	smallLine := "small\n"
	lines := []string{bigLine, smallLine}
	chunks := buildChunksByLine(lines, 100)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks (big alone, small alone), got %d", len(chunks))
	}
	if chunks[0].content != bigLine {
		t.Errorf("first chunk should be the whole big line; got len=%d want %d", len(chunks[0].content), len(bigLine))
	}
}

func TestJoinChunks_LocationHeaders(t *testing.T) {
	chunks := []chunkRange{
		{startLine: 1, endLine: 10, content: "first chunk content\n"},
		{startLine: 11, endLine: 20, content: "second chunk content\n"},
	}
	out := joinChunks(chunks, 2)
	if !strings.Contains(out, "[chunk 1/2, lines 1-10]") {
		t.Errorf("missing chunk 1 header in:\n%s", out)
	}
	if !strings.Contains(out, "[chunk 2/2, lines 11-20]") {
		t.Errorf("missing chunk 2 header in:\n%s", out)
	}
	if !strings.Contains(out, "first chunk content") {
		t.Errorf("missing chunk 1 content")
	}
	if !strings.Contains(out, "second chunk content") {
		t.Errorf("missing chunk 2 content")
	}
}

func TestJoinChunks_TruncatedTotalShowsRealCount(t *testing.T) {
	// When the executor truncates to maxChunks (8), the joined headers
	// should still display the REAL total so the calling model knows
	// content was withheld.
	chunks := []chunkRange{
		{startLine: 1, endLine: 100, content: "c1\n"},
		{startLine: 101, endLine: 200, content: "c2\n"},
	}
	out := joinChunks(chunks, 12) // total 12 even though we emit 2
	if !strings.Contains(out, "[chunk 1/12, lines 1-100]") {
		t.Errorf("header should show real total of 12, got:\n%s", out)
	}
}

// fakeChunkableContent builds a string with `lines` lines of `lineLen`
// chars each plus a newline. Useful for forcing buildChunksByLine into
// any specific (chunks-per-cap, total-tokens) shape the test needs.
func fakeChunkableContent(lines, lineLen int) string {
	var b strings.Builder
	for i := 0; i < lines; i++ {
		b.WriteString(strings.Repeat("x", lineLen))
		b.WriteString("\n")
	}
	return b.String()
}

// TestChunkOversize_LegacyChunkCountCap pins the backward-compat path:
// when maxEmittedTokens == 0, ChunkOversize truncates at
// MaxEmittedChunks (8). Existing dispatcher callers that haven't been
// migrated to the token-budget shape keep their pre-2026-05-25
// behavior verbatim.
func TestChunkOversize_LegacyChunkCountCap(t *testing.T) {
	// 100 lines × 40 chars ≈ 1K tokens total. Per-chunk cap 40 forces
	// ~25 chunks. With maxEmittedTokens=0, the legacy MaxEmittedChunks=8
	// cap should kick in.
	raw := fakeChunkableContent(100, 40)
	joined, total, emitted := ChunkOversize(raw, 40, 0)
	if emitted != MaxEmittedChunks {
		t.Errorf("legacy mode: emitted=%d, want %d (MaxEmittedChunks)", emitted, MaxEmittedChunks)
	}
	if total <= MaxEmittedChunks {
		t.Errorf("test setup wrong — need total > MaxEmittedChunks to exercise truncation; got %d", total)
	}
	if !strings.Contains(joined, "[truncated") {
		t.Errorf("expected truncation marker when total > MaxEmittedChunks")
	}
}

// TestChunkOversize_TokenBudgetReplacesChunkCap pins the new
// intent-aware behavior: when maxEmittedTokens > 0, ChunkOversize walks
// chunks accumulating tokens and truncates at the budget — NOT at the
// 8-chunk legacy cap. The point of Piece 1 in
// docs/handoff-2026-05-25.md: at a 500-token per-chunk cap, 8 chunks
// emit only ~22% of a moderate file; raising the budget to 16K closes
// the gap.
func TestChunkOversize_TokenBudgetReplacesChunkCap(t *testing.T) {
	// 200 lines × 40 chars ≈ 2K tokens total split into ~50 chunks at
	// per-chunk cap 40. Budget=600 ≈ 15 chunks — beyond the legacy 8.
	raw := fakeChunkableContent(200, 40)
	joined, total, emitted := ChunkOversize(raw, 40, 600)
	if emitted <= MaxEmittedChunks {
		t.Errorf("token-budget mode (cap=600 tokens, ~40 tok/chunk) should emit > %d chunks; got %d (total=%d)",
			MaxEmittedChunks, emitted, total)
	}
	if emitted > total {
		t.Errorf("emitted=%d > total=%d makes no sense", emitted, total)
	}
	// Sanity: emitted chunks should accumulate to roughly the budget.
	// Allow slack (the loop stops BEFORE the budget would be exceeded
	// by the next chunk, so usage can be a chunk's worth under budget).
	if emitted < total && !strings.Contains(joined, "[truncated") {
		t.Errorf("expected truncation marker when emitted < total")
	}
}

// TestChunkOversize_TokenBudgetEmitsAllWhenFits pins the non-truncation
// case: when total tokens fit under the budget, every chunk emits and
// no "[truncated …]" marker appears.
func TestChunkOversize_TokenBudgetEmitsAllWhenFits(t *testing.T) {
	// 20 lines × 40 chars ≈ 200 tokens — comfortably under a 4K budget.
	raw := fakeChunkableContent(20, 40)
	joined, total, emitted := ChunkOversize(raw, 40, 4000)
	if emitted != total {
		t.Errorf("budget=4000 ≫ content tokens — should emit all %d chunks; got %d", total, emitted)
	}
	if strings.Contains(joined, "[truncated") {
		t.Errorf("no truncation marker expected when all chunks fit: %q", joined[:min(200, len(joined))])
	}
}

// TestChunkOversize_TokenBudgetAlwaysEmitsAtLeastOne pins the floor:
// even with an absurdly small token budget, ChunkOversize emits at
// least one chunk so the calling model has *something* to inspect.
// Emitting zero chunks would tell the model "this file has content
// but you can't see any of it" — strictly worse than oversized first
// chunk + truncation marker.
func TestChunkOversize_TokenBudgetAlwaysEmitsAtLeastOne(t *testing.T) {
	raw := fakeChunkableContent(200, 40)
	joined, _, emitted := ChunkOversize(raw, 40, 1) // 1 token: forces "first chunk only"
	if emitted < 1 {
		t.Errorf("budget=1 should still emit ≥1 chunk; got %d", emitted)
	}
	if !strings.Contains(joined, "[chunk 1/") {
		t.Errorf("first chunk header missing")
	}
}

// TestEmittedTokensCap_IntentSwitch pins the intent → emission-budget
// mapping. code (the safe default) keeps the focus 4K; review / recall
// / meta unlock 16K so read-heavy intents can see whole files.
func TestEmittedTokensCap_IntentSwitch(t *testing.T) {
	tests := []struct {
		intent string
		want   int
	}{
		{"code", 4000},
		{"", 4000},
		{"unknown", 4000},
		{"review", 16000},
		{"recall", 16000},
		{"meta", 16000},
	}
	for _, tc := range tests {
		t.Run(tc.intent, func(t *testing.T) {
			b := Budget{Intent: tc.intent}
			if got := b.EmittedTokensCap(); got != tc.want {
				t.Errorf("Budget{Intent:%q}.EmittedTokensCap() = %d, want %d", tc.intent, got, tc.want)
			}
		})
	}
}

// TestChunkOversize_TruncationMarkerNamesConcreteRange pins Piece 2's
// honesty fix: when the chunker truncates, the marker MUST cite the
// next-line and last-line numbers the model needs to feed to
// act.read_file's start_line / end_line params. Without this, the
// previous "re-fetch with explicit line range" advice was unactionable
// — act.read_file didn't even accept a range. The pair-up of marker
// + tool surface is what closes the re-read loop.
func TestChunkOversize_TruncationMarkerNamesConcreteRange(t *testing.T) {
	// Force truncation: small per-chunk cap (40 tok) over 200 lines of
	// 40 chars → ~50 chunks; emission budget 600 → ~15 chunks emit.
	raw := fakeChunkableContent(200, 40)
	joined, total, emitted := ChunkOversize(raw, 40, 600)
	if emitted >= total {
		t.Fatalf("test setup wrong — need partial emission to exercise marker; got emitted=%d total=%d", emitted, total)
	}
	if !strings.Contains(joined, "start_line=") {
		t.Errorf("truncation marker MUST cite start_line=N so the model can paginate; got tail %q",
			joined[max(0, len(joined)-200):])
	}
	if !strings.Contains(joined, "end_line=") {
		t.Errorf("truncation marker should cite end_line=N too so the model knows the file's last line; got tail %q",
			joined[max(0, len(joined)-200):])
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// TestEmittedTokensCap_CtxWindowCeiling pins the safety ceiling: even
// on intents that ask for a big budget, the per-deposit cap can't
// exceed 30% of the model's context window. Protects small-context
// models from a single deposit crowding out everything else.
func TestEmittedTokensCap_CtxWindowCeiling(t *testing.T) {
	// Review wants 16K. On a 8K-ctx model, 30% = 2400 → clamp.
	b := Budget{Intent: "review", MaxContextTokens: 8000}
	if got, want := b.EmittedTokensCap(), 2400; got != want {
		t.Errorf("review @ ctx=8K: EmittedTokensCap() = %d, want %d (30%% ceiling)", got, want)
	}
	// On a 65K-ctx model, 30% = 19500 → review's 16K fits, no clamp.
	b = Budget{Intent: "review", MaxContextTokens: 65000}
	if got, want := b.EmittedTokensCap(), 16000; got != want {
		t.Errorf("review @ ctx=65K: EmittedTokensCap() = %d, want %d (no clamp)", got, want)
	}
	// MaxContextTokens=0 (unknown) → no ceiling applied.
	b = Budget{Intent: "review"}
	if got, want := b.EmittedTokensCap(), 16000; got != want {
		t.Errorf("review @ ctx=unknown: EmittedTokensCap() = %d, want %d (no ceiling)", got, want)
	}
}
