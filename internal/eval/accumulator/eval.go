// Package accumulator hosts the bounded-emergence proof-of-concept
// eval: a small model + attend.accumulate chain answering a
// synthesis question whose source observations don't fit the
// model's context window.
//
// The eval is intentionally tiny — 10 observations, one final
// question, two paths (naive baseline vs accumulator chain).
// Pass/fail is judged by:
//
//   - Did the prompt fit the model's context window?
//   - Does the answer contain all the synthesis facts the question
//     requires? (string-presence, not semantic equivalence — keeps
//     the eval cheap and grep-friendly.)
//
// The eval is run via `go test ./internal/eval/accumulator/... -v`
// with CORTEX_EVAL_ENDPOINT + CORTEX_EVAL_MODEL set; skips
// otherwise so the unit suite stays hermetic.
package accumulator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/cognition/dag/ops"
	"github.com/dereksantos/cortex/pkg/llm"
)

// Observation is one piece of context fed sequentially into the
// accumulator. Filler is repeated padding that bloats the
// observation past what a naive "stuff them all in" baseline can
// fit; the Fact substring is what the final question needs to
// recover.
type Observation struct {
	Step   int
	Fact   string // the load-bearing string the synthesis question depends on
	Filler string // padding text — the noise an accumulator should drop
}

// Body renders the observation as it would be presented to a model.
// Concatenates Filler + Fact with markers so the LLM can tell them
// apart; in practice models don't always honor the markers, which
// is exactly the noise an accumulator + intent prompt has to filter.
func (o Observation) Body() string {
	return fmt.Sprintf(
		"[step %d filler] %s\n[step %d fact] %s",
		o.Step, o.Filler, o.Step, o.Fact,
	)
}

// Scenario is one demo: a sequence of observations and the question
// that requires synthesizing facts from all of them.
type Scenario struct {
	Name          string
	Intent        string
	Observations  []Observation
	Question      string
	RequiredFacts []string // substrings the answer must contain to pass
}

// DefaultScenario builds the canonical 10-observation synthesis
// demo. The Facts are fictional repo state — fields, file paths,
// numbers — chosen so the synthesis question has one right answer
// constructible from them.
//
// Filler is sized so the 10 observations concatenated overflow a
// 4096-token n_ctx by ~30% (the model can't fit them all even with
// generous instruction overhead). Adjust fillerScale to tune.
func DefaultScenario() Scenario {
	fillerScale := 200 // ~200 reps of the boilerplate per observation
	filler := strings.Repeat(
		"This is filler text describing project history that is unrelated to the question. ",
		fillerScale,
	)
	obs := []Observation{
		{1, "The user table is named `accounts` (not `users`).", filler},
		{2, "Primary key column is `account_id` (UUID v7).", filler},
		{3, "Created-at column is `created_at_utc` stored as bigint epoch millis.", filler},
		{4, "Soft-deletes use `deleted_at_utc` — NULL means active.", filler},
		{5, "Auth tokens table is named `session_tokens` and lives in schema `auth`.", filler},
		{6, "Token TTL is 86400 seconds (24 hours).", filler},
		{7, "Refresh tokens are stored hashed with argon2id.", filler},
		{8, "Token rotation triggers when `last_seen_at_utc` is older than 3600 seconds.", filler},
		{9, "Logout marks the token as revoked via `revoked_at_utc`.", filler},
		{10, "The logout endpoint lives at POST /v1/auth/logout in handlers/auth.go.", filler},
	}
	return Scenario{
		Name:   "auth-schema-synthesis",
		Intent: "Identify the schema + endpoint a logout feature must touch.",
		Observations: obs,
		Question: "List every database column and the HTTP endpoint a logout feature must read or write to revoke a session token. Be specific: name the columns, name the endpoint path.",
		RequiredFacts: []string{
			"session_tokens",
			"revoked_at_utc",
			"/v1/auth/logout",
		},
	}
}

// Result captures the outcome of running one path (naive or
// accumulator) against a Scenario. Token + latency numbers are
// approximate (4-char heuristic) but consistent across both paths
// so the comparison is fair.
type Result struct {
	Path                 string // "naive" | "accumulator"
	PromptTokens         int    // estimated tokens in the final answer-prompt
	AnswerTokens         int
	TotalLLMCalls        int
	TotalLatencyMS       int
	FinalAnswer          string
	FactsFound           []string
	FactsMissing         []string
	OverflowedNCtx       bool // PromptTokens > NCtxBudget
	AccumulatorTrajectoy []int // snapshot_tokens at each step (accumulator path only)
}

// RunNaive concatenates all observations into a single prompt and
// asks the model the question. The prompt MAY exceed the model's
// n_ctx; that's the failure mode this eval exists to surface.
func RunNaive(ctx context.Context, p llm.Provider, s Scenario, nctxBudget int) (Result, error) {
	var sb strings.Builder
	sb.WriteString("Observations gathered so far:\n\n")
	for _, o := range s.Observations {
		fmt.Fprintf(&sb, "--- step %d ---\n%s\n\n", o.Step, o.Body())
	}
	sb.WriteString("Question: ")
	sb.WriteString(s.Question)
	sb.WriteString("\n\nAnswer concisely; reference specific names from the observations.")
	prompt := sb.String()
	tok := approxTokens(prompt)

	started := time.Now()
	out, stats, err := p.GenerateWithStats(ctx, prompt)
	latency := int(time.Since(started).Milliseconds())
	r := Result{
		Path:           "naive",
		PromptTokens:   tok,
		TotalLLMCalls:  1,
		TotalLatencyMS: latency,
		OverflowedNCtx: nctxBudget > 0 && tok > nctxBudget,
	}
	if err != nil {
		return r, err
	}
	r.FinalAnswer = strings.TrimSpace(out)
	r.AnswerTokens = stats.OutputTokens
	r.FactsFound, r.FactsMissing = scoreAnswer(r.FinalAnswer, s.RequiredFacts)
	return r, nil
}

// RunAccumulator threads each observation through attend.accumulate
// in sequence. After all 10, the snapshot is the model's working
// understanding — bounded by snapshotMaxTokens. The final answer
// call only sees the snapshot, not the raw observations.
//
// When journalDir is non-empty, each accumulator step writes a
// think.accumulator_update entry under <journalDir>/think/. The
// caller (test driver) is responsible for choosing the dir — the
// eval is happy with either a temp dir (for hermetic runs) or a
// real .cortex/journal/think (to inspect cross-session).
func RunAccumulator(ctx context.Context, p llm.Provider, s Scenario, snapshotMaxTokens, nctxBudget int, journalDir string) (Result, error) {
	sessionID := fmt.Sprintf("acc-%s-%d", s.Name, time.Now().UnixNano())
	step := 0
	prevSnapshotID := ""
	var jw *journal.Writer
	if journalDir != "" {
		var err error
		jw, err = journal.NewWriter(journal.WriterOpts{
			ClassDir: journalDir,
			Fsync:    journal.FsyncPerBatch,
		})
		if err != nil {
			return Result{Path: "accumulator"}, fmt.Errorf("open journal writer: %w", err)
		}
		defer jw.Close()
	}

	journalHook := func(snapshot string, snapshotTokens int, observation string, fallback bool) {
		if jw == nil {
			return
		}
		op := "attend.accumulate"
		if fallback {
			op = "attend.accumulate.fallback"
		}
		entry, err := journal.NewThinkAccumulatorUpdateEntry(journal.ThinkAccumulatorUpdatePayload{
			SessionID:         sessionID,
			Step:              step,
			PrevSnapshotID:    prevSnapshotID,
			Snapshot:          snapshot,
			SourceObservation: observation,
			SnapshotTokens:    snapshotTokens,
			MaxTokens:         snapshotMaxTokens,
			CompressorOp:      op,
		})
		if err != nil {
			return
		}
		if _, err := jw.Append(entry); err == nil {
			prevSnapshotID = fmt.Sprintf("%s#%d", sessionID, step)
		}
	}

	spec := ops.AccumulateSpec(ops.AccumulateConfig{
		Provider:     p,
		JournalWrite: journalHook,
	})
	h := spec.Handler
	var (
		snapshot   string
		latency    int
		llmCalls   int
		trajectory []int
	)

	for _, o := range s.Observations {
		step = o.Step
		stepStart := time.Now()
		res, err := h(ctx, map[string]any{
			"prev_snapshot": snapshot,
			"observation":   o.Body(),
			"max_tokens":    snapshotMaxTokens,
			"intent":        s.Intent,
		}, dag.DefaultTurnBudget())
		if err != nil {
			return Result{Path: "accumulator"}, fmt.Errorf("step %d: %w", o.Step, err)
		}
		snap, _ := res.Out["snapshot"].(string)
		snapshot = snap
		stepTok, _ := res.Out["snapshot_tokens"].(int)
		trajectory = append(trajectory, stepTok)
		latency += int(time.Since(stepStart).Milliseconds())
		llmCalls++ // each accumulate may LLM, may passthrough; we count as a call attempted
	}

	// Final synthesis call — model sees only the snapshot.
	finalPrompt := fmt.Sprintf(
		"You have been maintaining the working memory below across 10 observations. Use ONLY this memory to answer the question.\n\nWorking memory:\n\"\"\"\n%s\n\"\"\"\n\nQuestion: %s\n\nAnswer concisely; reference specific names from the working memory.",
		snapshot, s.Question,
	)
	finalTok := approxTokens(finalPrompt)
	finalStart := time.Now()
	out, stats, err := p.GenerateWithStats(ctx, finalPrompt)
	latency += int(time.Since(finalStart).Milliseconds())
	llmCalls++

	r := Result{
		Path:                 "accumulator",
		PromptTokens:         finalTok,
		TotalLLMCalls:        llmCalls,
		TotalLatencyMS:       latency,
		OverflowedNCtx:       nctxBudget > 0 && finalTok > nctxBudget,
		AccumulatorTrajectoy: trajectory,
	}
	if err != nil {
		return r, err
	}
	r.FinalAnswer = strings.TrimSpace(out)
	r.AnswerTokens = stats.OutputTokens
	r.FactsFound, r.FactsMissing = scoreAnswer(r.FinalAnswer, s.RequiredFacts)
	return r, nil
}

// scoreAnswer does a case-insensitive substring check for each
// required fact. Coarse but deterministic — the eval rewards
// presence of the named identifiers, the small thing the prompt
// asked for. Semantic-equivalence judging would let the model off
// the hook for hallucinating plausible-sounding alternatives.
func scoreAnswer(answer string, required []string) (found, missing []string) {
	lower := strings.ToLower(answer)
	for _, f := range required {
		if strings.Contains(lower, strings.ToLower(f)) {
			found = append(found, f)
		} else {
			missing = append(missing, f)
		}
	}
	return
}

func approxTokens(s string) int {
	if s == "" {
		return 0
	}
	n := len(s) / 4
	if n == 0 {
		return 1
	}
	return n
}
