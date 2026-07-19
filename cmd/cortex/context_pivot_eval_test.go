package main

// context_pivot_eval_test.go is the Δ (deterministic) layer of the pivot eval
// (docs/eval-context-pivot.md): does the harness mechanically support a
// composed migration — evict + merge + watermark shift, through real Turn()
// calls — with the architecture's invariants from context_eval_test.go still
// holding jointly afterward? No model: a scripted backend plays the ideal
// migrator with canned context_evict/context_merge/context_adjust_watermarks
// tool calls at a "pivot" turn. Per the design, this layer is expected GREEN
// from the first run — the tools are shipped; any red here is a real
// composition bug, not a model-volition question (that's the live ø layer,
// context_pivot_eval_live_test.go).
//
// Note on wire timing: cs.Request.OutlineBlock is (re-)rendered from
// cs.outline once, at the top of turn() (cmd/cortex/turn.go), before that
// turn's tool loop runs — it is NOT refreshed mid-turn. So a mid-turn
// context_evict/context_merge is visible in cs.outline immediately (asserted
// directly below) but only reaches the WIRE starting with the NEXT turn.
// Invariants stated over "the wire" are therefore checked against the first
// post-migration turn, not the migration turn's own last request.
//
//	go test ./cmd/cortex -run ContextPivotEval

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dereksantos/cortex/internal/cache"
	"github.com/dereksantos/cortex/internal/tools"
)

// pivotScript is one canned backend response for the pivot-eval Sender: either
// a tool-call round (calls non-empty) or a plain text reply.
type pivotScript struct {
	calls []ToolCall
	text  string
}

// pivotEvalBackend is contextEvalBackend (context_eval_test.go) extended with
// a response queue: the Δ layer needs the canned assistant to emit specific
// context_evict/context_merge/context_adjust_watermarks calls at the pivot
// turn (not just a numbered marker reply), so a composed migration can be
// asserted deterministically. Absent a queued script, it falls back to
// contextEvalBackend's numbered marker reply so ordinary filler turns need no
// scripting at all.
type pivotEvalBackend struct {
	mu    sync.Mutex
	wires [][]Message
	queue []pivotScript
	srv   *httptest.Server
}

func newPivotEvalBackend(t *testing.T) *pivotEvalBackend {
	t.Helper()
	b := &pivotEvalBackend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		b.mu.Lock()
		b.wires = append(b.wires, req.Messages)
		n := len(b.wires)
		var script *pivotScript
		if len(b.queue) > 0 {
			s := b.queue[0]
			b.queue = b.queue[1:]
			script = &s
		}
		b.mu.Unlock()

		if script != nil && len(script.calls) > 0 {
			callsJSON, _ := json.Marshal(script.calls)
			fmt.Fprintf(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":%s},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`, callsJSON)
			return
		}
		text := fmt.Sprintf("reply-%d-marker done", n)
		if script != nil && script.text != "" {
			text = script.text
		}
		textJSON, _ := json.Marshal(text)
		fmt.Fprintf(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`, textJSON)
	}))
	t.Cleanup(b.srv.Close)
	return b
}

// script queues one scripted tool-call round for the NEXT request the backend
// answers.
func (b *pivotEvalBackend) script(calls ...ToolCall) {
	b.mu.Lock()
	b.queue = append(b.queue, pivotScript{calls: calls})
	b.mu.Unlock()
}

func (b *pivotEvalBackend) lastWire() []Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.wires) == 0 {
		return nil
	}
	return b.wires[len(b.wires)-1]
}

func (b *pivotEvalBackend) allWires() [][]Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([][]Message, len(b.wires))
	copy(out, b.wires)
	return out
}

func newPivotEvalSession(t *testing.T, backend *pivotEvalBackend, window int) *CortexSession {
	t.Helper()
	cs := &CortexSession{Window: window, quiet: true, Request: &AgentRequest{
		Model:    "m",
		BaseURL:  backend.srv.URL,
		Messages: []Message{{Role: RoleSystem, Content: "sys"}},
		Tools:    tools.All,
	}}
	cs.ws = cs.newWorkingSet(1)
	cs.StartTranscript()
	if cs.transcript == nil {
		t.Fatal("StartTranscript failed to open a transcript")
	}
	t.Cleanup(func() {
		if cs.transcript != nil {
			cs.transcript.Close()
		}
	})
	return cs
}

// evictCall/mergeCall/adjustWatermarksCall build a canned tool call for the
// scripted backend — the same wire shape a real model's tool_calls carry
// (Arguments is a JSON-encoded *string*, per internal/agent.FunctionCall).
func evictCall(id, citation string) ToolCall {
	args, _ := json.Marshal(map[string]any{"citation": citation})
	return ToolCall{ID: id, Type: "function", Function: FunctionCall{Name: tools.FunctionContextEvict, Arguments: string(args)}}
}

func mergeCall(id, start, end string) ToolCall {
	args, _ := json.Marshal(map[string]any{"range_start": start, "range_end": end})
	return ToolCall{ID: id, Type: "function", Function: FunctionCall{Name: tools.FunctionContextMerge, Arguments: string(args)}}
}

func adjustWatermarksCall(id string, highDelta, lowDelta int) ToolCall {
	args, _ := json.Marshal(map[string]any{"high_delta": highDelta, "low_delta": lowDelta})
	return ToolCall{ID: id, Type: "function", Function: FunctionCall{Name: tools.FunctionContextAdjustWatermarks, Arguments: string(args)}}
}

// pivotOutlineEntry finds the first live outline entry whose User text
// contains head, failing loudly (setup error) if the scenario never got the
// planted turn into the outline.
func pivotOutlineEntry(t *testing.T, cs *CortexSession, head string) cache.OutlineEntry {
	t.Helper()
	for _, e := range cs.outline {
		if strings.Contains(e.User, head) {
			return e
		}
	}
	t.Fatalf("setup error: no outline entry found for %q (scenario never tightened?)", head)
	return cache.OutlineEntry{}
}

const (
	pivotADeadHead   = "ADEAD-marker"
	pivotADeadNeedle = "adead-deep-needle-secret"
	pivotAKeepHead   = "AKEEP-marker"
	pivotAKeepNeedle = "akeep-deep-needle-secret"
)

// plantPivotTurns runs the Δ scenario's setup: two "fact" turns beyond the
// outline's verbatim cap (the same past-500-rune trick as context_eval_test.go
// and the live eval's factInput), plus two SHORT filler turns (f1/f2 — kept
// small so ContextMergeTool's "several small related turns" merge is
// exercised the way it is documented to be used, staying under the outline's
// per-entry cap after joining), padded with more filler turns until all four
// have demoted into the outline — so the scripted migration always has real
// outline entries to target regardless of exactly where the hysteresis batch
// boundary falls.
func plantPivotTurns(t *testing.T, cs *CortexSession) {
	t.Helper()
	filler := strings.Repeat("filler ", 400) // ~2800 chars ≈ 700 tokens/turn, as in TestContextEvalSteadyState

	turn := func(label, input string) {
		t.Helper()
		if _, err := cs.Turn(context.Background(), input); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}

	turn("a-dead", fmt.Sprintf("%s %s %s", pivotADeadHead, filler, pivotADeadNeedle))
	turn("a-keep", fmt.Sprintf("%s %s %s", pivotAKeepHead, filler, pivotAKeepNeedle))
	turn("f1", "f1-marker short scheduling note, nothing else to see here.")
	turn("f2", "f2-marker short staging note, nothing else to see here.")
	for i := 1; i <= 8 && cs.ws.Demoted() < 4; i++ {
		turn(fmt.Sprintf("pad-%d", i), fmt.Sprintf("pad-%d-marker %s", i, filler))
	}
	if cs.ws.Demoted() < 4 {
		t.Fatalf("setup error: only %d turns demoted after 4 planted + 8 pad turns; scenario never tightened", cs.ws.Demoted())
	}
}

// TestContextPivotEvalComposedMigration drives the composed migration — evict
// the A-dead entry, merge the two filler entries into one spanning citation,
// and nudge the watermarks, all in one scripted tool round at the pivot
// turn — and checks the invariants Deliverable B2 enumerates from
// docs/eval-context-pivot.md: the wire stays complete (assertWireComplete)
// and bounded (assertWireBounded) after the migration, a merged citation's
// recall still returns the verbatim original messages, and the A-dead entry
// is evicted while A-keep survives.
func TestContextPivotEvalComposedMigration(t *testing.T) {
	quickRetries(t)
	t.Chdir(t.TempDir())
	backend := newPivotEvalBackend(t)
	cs := newPivotEvalSession(t, backend, 4000)

	plantPivotTurns(t, cs)

	aDead := pivotOutlineEntry(t, cs, pivotADeadHead)
	aKeep := pivotOutlineEntry(t, cs, pivotAKeepHead)
	f1 := pivotOutlineEntry(t, cs, "f1-marker")
	f2 := pivotOutlineEntry(t, cs, "f2-marker")

	// The scripted migration: evict the dead entry, merge the two (unrelated,
	// small) filler entries into one spanning citation, and shift the
	// watermarks by a small amount — small enough to stay inside
	// assertWireBounded's existing hysteresis-overshoot slack (the design
	// notes there is no honest way to make a large shift causally necessary in
	// a scripted scenario; here a shift only needs to compose without breaking
	// boundedness).
	backend.script(
		evictCall("c1", aDead.Citation),
		mergeCall("c2", f1.Citation, f2.Citation),
		adjustWatermarksCall("c3", 100, -50),
	)
	if _, err := cs.Turn(context.Background(), "pivot-marker migrating now"); err != nil {
		t.Fatalf("pivot turn: %v", err)
	}

	// The mutations land in cs.outline/cs.ws immediately (no model, no wire
	// involved) — check those directly before ever touching the wire.
	for _, e := range cs.outline {
		if e.Citation == aDead.Citation {
			t.Errorf("A-dead entry %q survived the scripted context_evict", aDead.Citation)
		}
	}
	merged := pivotOutlineEntry(t, cs, "f1-marker")
	if merged.Citation == f1.Citation || merged.Citation == f2.Citation {
		t.Errorf("merged entry citation %q is not a new spanning citation", merged.Citation)
	}
	if !strings.Contains(merged.User, "f1-marker") || !strings.Contains(merged.User, "f2-marker") {
		t.Errorf("merged outline entry lost a head: %q", merged.User)
	}

	// citations resolvable: recall of the merged span still returns the
	// verbatim original messages (Δ2 — merge is lossless).
	if raw, err := cs.Recall(merged.Citation); err != nil || !strings.Contains(raw, "f1-marker") || !strings.Contains(raw, "f2-marker") {
		t.Errorf("merged citation %s does not resolve both original turns: err=%v raw=%q", merged.Citation, err, raw)
	}
	// A-dead entry evicted (wire-only loss, Δ1): its raw messages stay in the
	// transcript and remain recallable via its original citation even though
	// the outline entry that pointed to it is gone.
	if raw, err := cs.Recall(aDead.Citation); err != nil || !strings.Contains(raw, pivotADeadNeedle) {
		t.Errorf("evicted entry no longer recallable via its original citation: err=%v raw=%q", err, raw)
	}
	if aKeep.Citation == "" {
		t.Errorf("setup error: A-keep citation never captured")
	}

	// The migration turn itself doesn't refresh cs.Request.OutlineBlock (that
	// only happens at the top of the NEXT turn) — run one more small turn so
	// the migrated outline actually reaches the wire, then check the
	// wire-level invariants against THAT request.
	if _, err := cs.Turn(context.Background(), "f3-marker outline refresh check"); err != nil {
		t.Fatalf("post-migration refresh turn: %v", err)
	}
	refreshWire := backend.lastWire()

	if strings.Contains(outlineZone(refreshWire), aDead.Citation) {
		t.Errorf("evicted citation %s is still present in the outline zone", aDead.Citation)
	}
	// A-keep survives: still present and reachable straight off the wire.
	assertWireComplete(t, cs, refreshWire, []string{
		pivotAKeepHead, pivotAKeepNeedle, "f1-marker", "f2-marker",
	}, "post-migration turn")
	assertWireBounded(t, cs, refreshWire, "post-migration turn")

	// Cache re-stabilizes: the migration turn breaks the prefix once (the
	// refresh turn above IS that one break); a further ordinary turn with no
	// more outline/index change must be pure append again.
	postInput := "f4-marker post-migration check"
	if _, err := cs.Turn(context.Background(), postInput); err != nil {
		t.Fatalf("post-refresh turn: %v", err)
	}
	wires := backend.allWires()
	if len(wires) < 2 {
		t.Fatal("expected at least two requests to compare re-prefill")
	}
	got := rePrefillChars(wires[len(wires)-2], wires[len(wires)-1])
	limit := len(postInput) + 300 // prior reply + new input + slack
	if got > limit {
		t.Errorf("post-refresh ordinary turn re-prefills %d chars, want ≤ %d (pure append; a migration must break the cache only once)", got, limit)
	}
}
