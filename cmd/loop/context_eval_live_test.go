package main

// context_eval_live_test.go is the live counterpart of the deterministic
// context eval (context_eval_test.go). The deterministic eval proves the
// mechanical invariants; this one answers the model- and backend-dependent
// questions against a real fleet:
//
//   - Retention: after a fact demotes out of the hydrated tail (planted past
//     the outline's verbatim cap, so the outline holds only a hint), does the
//     model bridge outline → recall and answer correctly?
//   - Prefill economics: does the provider's prompt cache actually reuse the
//     two-zone prefix (usage.prompt_tokens_details.cached_tokens, which
//     llama.cpp reports through LiteLLM)?
//   - Bounded prompt: does prompt_tokens stay flat once demotion engages?
//
//	CORTEX_LIVE_FLEET=1 go test ./cmd/loop/ -run ContextEval_Live -v -timeout 1800s
//
// Tunables (defaults target the fleet's 80b qwen):
//
//	CORTEX_CTX_EVAL_ENDPOINT  backend base URL   (default http://localhost:4000)
//	CORTEX_CTX_EVAL_MODEL     coder model tag    (default qwen3-coder-q3)
//	CORTEX_CTX_EVAL_STUDY     summarizer tag     (default glm-4.7-flash)
//	CORTEX_CTX_EVAL_WINDOW    session window     (default 4096; smaller = faster)
//	CORTEX_CTX_EVAL_TURNS     filler turns       (default 6)

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/cache"
)

const liveCodeword = "FROSTBITE-17"

// codewordish spots a confabulated answer: something codeword-shaped that
// isn't the planted one.
var codewordish = regexp.MustCompile(`\b[A-Z]{3,}-\d+\b`)

// cacheOutlineText renders only the LIVE outline entries (not the folded
// digest) — the region where a graded hint can still appear.
func cacheOutlineText(cs *CortexSession) string {
	return cache.RenderOutline(cs.outline)
}

func liveEnv(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func liveEnvInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// fillerInput builds a distinct ~1600-char note the model is asked to ack —
// enough tokens per turn to push the tail over its watermark within a few turns.
func fillerInput(i int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Reference note %d for the session log. ", i)
	for s := 0; s < 16; s++ {
		fmt.Fprintf(&b, "Batch indexer pass %d-%02d processed shard group %d with %d records and no integrity faults. ", i, s, s*7+i, 1200+s*31+i)
	}
	b.WriteString("No action needed. Reply with just: ACK")
	return b.String()
}

// factInput plants the codeword beyond the outline's verbatim cap (500 runes):
// after demotion, the outline shows only the preamble hint, and the codeword is
// reachable solely through recall.
func factInput() string {
	var b strings.Builder
	b.WriteString("Please note this project brief; the deploy codeword is stated at the very end of this message and you will be asked for it later. ")
	for s := 0; s < 8; s++ {
		fmt.Fprintf(&b, "Brief section %d: the rollout gates on the staging soak, the canary dashboards, and the signed manifest from the release captain. ", s)
	}
	fmt.Fprintf(&b, "The deploy codeword is %s. Acknowledge with just: OK — do not repeat the brief or the codeword.", liveCodeword)
	return b.String()
}

func TestContextEval_Live(t *testing.T) {
	if os.Getenv("CORTEX_LIVE_FLEET") == "" {
		t.Skip("set CORTEX_LIVE_FLEET=1 to run the live context eval")
	}
	endpoint := liveEnv("CORTEX_CTX_EVAL_ENDPOINT", "http://localhost:4000")
	model := liveEnv("CORTEX_CTX_EVAL_MODEL", "qwen3-coder-q3")
	study := liveEnv("CORTEX_CTX_EVAL_STUDY", "glm-4.7-flash")
	// Defaults sized so the first demotion batch (~4 outline entries) fits the
	// outline cap (W/8) with room to spare: the graded probe must land while
	// the fact's outline hint is still live, before any fold.
	window := liveEnvInt("CORTEX_CTX_EVAL_WINDOW", 6000)
	fillers := liveEnvInt("CORTEX_CTX_EVAL_TURNS", 8)

	t.Chdir(t.TempDir())
	temp := 0.1
	cs := &CortexSession{
		Window: window,
		quiet:  true,
		Study:  ModelSpec{Endpoint: endpoint, Model: study, Window: 8192},
		Request: &AgentRequest{
			Model:       model,
			BaseURL:     endpoint,
			Temperature: temp,
			Messages:    []Message{{Role: RoleSystem, Content: SystemPrompt}},
			Tools:       toolSet,
			MaxTokens:   codeMaxOutputTokens,
		},
	}
	cs.ws = cs.newWorkingSet(1)
	cs.StartTranscript()
	if cs.transcript == nil {
		t.Fatal("StartTranscript failed")
	}
	defer cs.transcript.Close()

	type turnStat struct {
		label     string
		prompt    int
		cached    int
		demoted   int
		elapsedMS int64
	}
	var stats []turnStat
	runTurn := func(label, input string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		start := time.Now()
		res, err := cs.Turn(ctx, input)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		stats = append(stats, turnStat{
			label:     label,
			prompt:    cs.LastPromptTokens,
			cached:    cs.LastCachedTokens,
			demoted:   cs.ws.Demoted(),
			elapsedMS: time.Since(start).Milliseconds(),
		})
		return res.Reply
	}

	recallCalledSince := func(from int) bool {
		for _, m := range cs.Request.Messages[from:] {
			for _, call := range m.ToolCalls {
				if call.Function.Name == "recall" {
					return true
				}
			}
		}
		return false
	}

	// --- phase 1: plant the fact, filler turns until it demotes --------------
	runTurn("fact", factInput())
	for i := 1; i <= fillers; i++ {
		runTurn(fmt.Sprintf("filler-%d", i), fillerInput(i))
		if cs.ws.Demoted() >= 1 && i >= 2 {
			break // fact turn is out of the tail; more fillers only add runtime
		}
	}
	if cs.ws.Demoted() < 1 {
		t.Fatalf("fact turn never demoted after %d filler turns (window %d too large for the script?)", fillers, window)
	}

	// The graded probe requires the outline to still signal relevance: the
	// fact entry's head (which names the deploy codeword) must be live, and
	// the codeword itself must not have leaked into the outline.
	outlineNow := cs.renderOutlineBlock()
	hintVisible := strings.Contains(outlineNow, "deploy codeword")
	leak := strings.Contains(outlineNow, liveCodeword)
	if leak {
		t.Logf("WARNING: codeword leaked into the outline zone; the graded probe measures outline reading, not recall")
	}
	if !hintVisible {
		t.Logf("WARNING: the fact's outline hint already folded away; the graded probe degrades to a blind one")
	}

	// --- phase 1 probe: graded retention (outline hint → recall) -------------
	preProbe := len(cs.Request.Messages)
	gradedReply := runTurn("probe-graded", "What is the deploy codeword from the project brief earlier in this session?")
	gradedRecall := recallCalledSince(preProbe)

	// --- phase 2: keep filling until the fact entry folds into the digest ----
	folded := false
	for i := fillers + 1; i <= fillers+8; i++ {
		if cs.outlineFolded != "" && !strings.Contains(cacheOutlineText(cs), "deploy codeword") {
			folded = true
			break
		}
		runTurn(fmt.Sprintf("filler-%d", i), fillerInput(i))
	}
	var postReply string
	var postRecall bool
	if folded {
		preProbe = len(cs.Request.Messages)
		postReply = runTurn("probe-postfold", "Earlier in this session I gave you a deploy codeword. What was it? Answer with just the codeword.")
		postRecall = recallCalledSince(preProbe)
	}

	// --- report ---------------------------------------------------------------
	t.Logf("model=%s window=%d turns=%d", model, window, len(stats))
	t.Logf("%-14s %8s %8s %10s %8s %9s", "turn", "prompt", "cached", "evaluated", "demoted", "elapsed")
	for _, s := range stats {
		t.Logf("%-14s %8d %8d %10d %8d %8dms", s.label, s.prompt, s.cached, s.prompt-s.cached, s.demoted, s.elapsedMS)
	}
	t.Logf("graded probe: %q (recall called: %v, hint visible: %v, leak: %v)", strings.TrimSpace(gradedReply), gradedRecall, hintVisible, leak)
	if folded {
		t.Logf("post-fold probe: %q (recall called: %v)", strings.TrimSpace(postReply), postRecall)
	} else {
		t.Logf("post-fold probe skipped: the fact entry never folded within the turn budget")
	}

	// floorGrade applies the design's accepted floor when the outline gave no
	// hint: correct answers and honest misses pass, confabulation fails.
	floorGrade := func(label, reply string) {
		if strings.Contains(reply, liveCodeword) {
			return
		}
		if fake := codewordish.FindString(reply); fake != "" {
			t.Errorf("%s FAILED: confabulated codeword %q (want %s or an honest miss)", label, fake, liveCodeword)
		} else {
			t.Logf("%s: honest miss (accepted floor)", label)
		}
	}

	// --- gates ------------------------------------------------------------------
	// G1 graded retention (hard when the hint was actually visible): the
	// demoted fact must come back correct. If the hint folded away before the
	// probe, the probe was blind and only the floor applies.
	if hintVisible {
		if !strings.Contains(gradedReply, liveCodeword) {
			t.Errorf("graded retention FAILED: probe reply %q does not contain the codeword %s", strings.TrimSpace(gradedReply), liveCodeword)
		}
	} else {
		floorGrade("graded-degraded-to-blind retention", gradedReply)
	}

	// G1b post-fold retention (floor): once the hint is folded away, a correct
	// answer or an honest miss both pass — the accepted floor per the design.
	if folded {
		floorGrade("post-fold retention", postReply)
	}

	// G2 bounded: prompt_tokens stays within envelope + tail high watermark +
	// outline cap + one turn of hysteresis overshoot (demotion runs at turn
	// START, so the tail may exceed the watermark by the last completed turn).
	envelope := stats[0].prompt
	overshoot := 900 // ≈ one filler turn (~450 tok) + probe turns + estimate error
	budget := envelope + window/2 + window/8 + overshoot
	for _, s := range stats {
		if s.prompt > budget {
			t.Errorf("bounded FAILED: %s prompt=%d tokens, over budget %d (envelope %d + W/2 + W/8 + overshoot)", s.label, s.prompt, budget, envelope)
		}
	}

	// G3 cache sanity: the backend must reuse the prefix on at least one
	// post-first turn — zero reuse everywhere means the two-zone layout is not
	// prefix-stable on the real wire. (Exact ratios are reported, not gated:
	// a shared fleet can evict between requests.)
	reused := false
	for _, s := range stats[1:] {
		if s.cached > 0 {
			reused = true
		}
	}
	if !reused {
		t.Errorf("cache sanity FAILED: no request after the first reported cached prompt tokens (prefix never reused)")
	}
}
