package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/dereksantos/cortex/pkg/llm"
)

// summarize.go is the free-text study path (study-navigator direction doc): the
// "summarizer" half of the navigator/summarizer split. Compaction (the session
// transcript) and shell-output study (a command's spill) have no structural map
// to navigate, so they use a sequential chunk-and-fold summarizer here instead
// of the byte-grid sampling engine — read in order, summarize each window-sized
// chunk toward the goal, then fold the partials into one digest. No RNG
// sampling, no coverage targets, no curator/director.

const (
	// summaryCharsPerToken is the rough bytes-per-token estimate used to turn a
	// token window into a character budget (matches the study engine's 4).
	summaryCharsPerToken = 4
	// summaryFillFraction sizes each chunk's INPUT to a fraction of the window,
	// leaving the rest for the summary the model writes back.
	summaryFillFraction = 0.5
	// summaryMaxTokens caps each summarization call's output.
	summaryMaxTokens = 1024
	// summaryMaxFoldRounds bounds the reduce phase so a pathological input can't
	// loop forever folding partials that never shrink.
	summaryMaxFoldRounds = 4
)

const summarizeSystem = `You are a summarizer. You are given a chunk of text (a session transcript or command output) and a GOAL. Produce a faithful, concise summary grounded ONLY in what the chunk shows — surface concrete details (names, files, errors, decisions, outcomes) over generic description. Lead with errors, failures, and anomalies when present. Do not invent anything not in the text.`

// Summarize reduces the file at path to a digest toward goal, returning the
// digest and whether folding was needed (compressed=false means the content fit
// a single chunk — for compaction, "nothing to compress"). window is the
// consumer's token budget; <=0 falls back to the study window.
func (cs *CortexSession) Summarize(ctx context.Context, path, goal string, window int) (digest string, compressed bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, fmt.Errorf("summarize read %s: %w", path, err)
	}
	return cs.SummarizeText(ctx, string(data), goal, window)
}

// SummarizeText reduces content (already in memory) to a digest toward goal,
// returning the digest and whether folding was needed (compressed=false means
// the content fit a single chunk — for compaction, "nothing to compress").
// window is the consumer's token budget; <=0 falls back to the study window.
func (cs *CortexSession) SummarizeText(ctx context.Context, content, goal string, window int) (digest string, compressed bool, err error) {
	if strings.TrimSpace(content) == "" {
		return "", false, nil
	}
	if window <= 0 {
		window = cs.studyWindow()
	}
	chunkChars := int(float64(window) * summaryFillFraction * summaryCharsPerToken)
	if chunkChars < 1024 {
		chunkChars = 1024
	}
	provider := cs.newStudyProvider(summaryMaxTokens)

	chunks := splitChunks(content, chunkChars)
	// Single chunk → one summarization call. compressed=false: it fit.
	if len(chunks) <= 1 {
		s, err := summarizeChunk(ctx, provider, goal, content, 1, 1)
		return s, false, err
	}

	// Map: summarize each chunk in order.
	if !cs.quiet {
		fmt.Println(withColor(fmt.Sprintf("  ▸ summarize via %s (%d chunks)", cs.Study.Model, len(chunks)), green))
	}
	partials := make([]string, 0, len(chunks))
	for i, ch := range chunks {
		s, err := summarizeChunk(ctx, provider, goal, ch, i+1, len(chunks))
		if err != nil {
			return "", false, err
		}
		if strings.TrimSpace(s) != "" {
			partials = append(partials, s)
		}
		if ctx.Err() != nil {
			return "", false, ctx.Err()
		}
	}

	// Reduce: fold the partials down to a budget-sized digest.
	folded, err := cs.foldSummaries(ctx, provider, goal, partials, chunkChars)
	return folded, true, err
}

// foldSummaries reduces a set of partial summaries to one digest. While the
// joined partials exceed the chunk budget, it summarizes them in groups (a
// reduce tree); once they fit, a final synthesis pass produces the digest.
func (cs *CortexSession) foldSummaries(ctx context.Context, p llm.Provider, goal string, partials []string, chunkChars int) (string, error) {
	for round := 0; round < summaryMaxFoldRounds; round++ {
		joined := strings.Join(partials, "\n\n")
		if len(joined) <= chunkChars {
			return summarizeChunk(ctx, p, goal, joined, 1, 1)
		}
		groups := splitChunks(joined, chunkChars)
		next := make([]string, 0, len(groups))
		for _, g := range groups {
			s, err := summarizeChunk(ctx, p, goal, g, 1, 1)
			if err != nil {
				return "", err
			}
			next = append(next, s)
		}
		partials = next
	}
	// Folding didn't converge under the round cap: return the best we have.
	return strings.Join(partials, "\n\n"), nil
}

// summarizeChunk runs one summarization call. n>1 frames the chunk as part i of
// n so the model knows it's seeing a slice, not the whole.
func summarizeChunk(ctx context.Context, p llm.Provider, goal, chunk string, i, n int) (string, error) {
	if strings.TrimSpace(goal) == "" {
		goal = "Summarize this text faithfully and concisely."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "GOAL: %s\n\n", goal)
	if n > 1 {
		fmt.Fprintf(&b, "This is part %d of %d of the text.\n\n", i, n)
	}
	b.WriteString("TEXT:\n")
	b.WriteString(chunk)
	out, err := p.GenerateWithSystem(ctx, b.String(), summarizeSystem)
	if err != nil {
		return "", fmt.Errorf("summarize call: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// splitChunks divides s into pieces of at most maxChars, breaking only at line
// boundaries so a chunk never starts mid-line. A single line longer than
// maxChars becomes its own (oversized) chunk rather than being cut.
func splitChunks(s string, maxChars int) []string {
	if len(s) <= maxChars {
		return []string{s}
	}
	var chunks []string
	var cur strings.Builder
	for _, line := range strings.SplitAfter(s, "\n") {
		if line == "" {
			continue
		}
		if cur.Len() > 0 && cur.Len()+len(line) > maxChars {
			chunks = append(chunks, cur.String())
			cur.Reset()
		}
		cur.WriteString(line)
	}
	if cur.Len() > 0 {
		chunks = append(chunks, cur.String())
	}
	return chunks
}
