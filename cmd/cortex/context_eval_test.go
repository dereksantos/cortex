package main

// Context eval — the cohesive acceptance test for the two-zone working set
// (docs/context-architecture.md). It drives real Turn() calls against a
// scripted backend and asserts the architecture's invariants at every seam:
//
//   1. Wire completeness (losslessness): every message the session has seen is
//      either on the wire verbatim or reachable through a citation the wire
//      carries (outline entry or folded digest) that recall resolves.
//   2. Bounded prompt: the wire payload never exceeds
//      envelope + outline cap (W/8) + tail high watermark (W/2) + slack.
//   3. Cache economics: on turns where zone A did not change, the re-prefilled
//      suffix is just the previous reply + the new input (pure append); on
//      demotion turns it is bounded by the zone A + tail budgets.
//   4. Outline integrity: turn labels stay unique across folds; citations
//      survive a lossy fold summarizer.
//   5. Seams: Compact's summary rides the wire and the post-Compact transcript
//      resumes demotable; legacy unstamped transcripts resume fully hydrated.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/dereksantos/cortex/internal/cache"
)

// contextEvalBackend records every wire payload and answers each request with
// a one-line scripted reply carrying a per-request marker.
type contextEvalBackend struct {
	mu    sync.Mutex
	wires [][]Message
	srv   *httptest.Server
}

func newContextEvalBackend(t *testing.T) *contextEvalBackend {
	t.Helper()
	b := &contextEvalBackend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		b.mu.Lock()
		b.wires = append(b.wires, req.Messages)
		n := len(b.wires)
		b.mu.Unlock()
		// Quiet sessions take the blocking (non-SSE) send path.
		fmt.Fprintf(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"reply-%d-marker done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`, n)
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *contextEvalBackend) lastWire() []Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.wires) == 0 {
		return nil
	}
	return b.wires[len(b.wires)-1]
}

func (b *contextEvalBackend) allWires() [][]Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([][]Message, len(b.wires))
	copy(out, b.wires)
	return out
}

func newContextEvalSession(t *testing.T, backend *contextEvalBackend, window int) *CortexSession {
	t.Helper()
	cs := &CortexSession{Window: window, quiet: true, Request: &AgentRequest{
		Model:    "m",
		BaseURL:  backend.srv.URL,
		Messages: []Message{{Role: RoleSystem, Content: "sys"}},
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

var evalCitationRe = regexp.MustCompile(`@session/[A-Za-z0-9-]+#m\d+-\d+`)

func wireText(wire []Message) string {
	var b strings.Builder
	for _, m := range wire {
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// assertWireComplete checks the losslessness invariant: each marker is on the
// wire verbatim, or recall of a citation the wire carries returns it.
func assertWireComplete(t *testing.T, cs *CortexSession, wire []Message, markers []string, label string) {
	t.Helper()
	text := wireText(wire)
	citations := evalCitationRe.FindAllString(text, -1)
	for _, mk := range markers {
		if strings.Contains(text, mk) {
			continue
		}
		found := false
		for _, c := range citations {
			raw, err := cs.Recall(c)
			if err == nil && strings.Contains(raw, mk) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: %q is neither on the wire nor recallable via any of the wire's %d citations", label, mk, len(citations))
		}
	}
}

// assertWireBounded checks the flat-prompt invariant: wire tokens stay within
// envelope + outline cap + tail high watermark (+ slack for headers/labels).
func assertWireBounded(t *testing.T, cs *CortexSession, wire []Message, label string) {
	t.Helper()
	budget := cs.windowSize()/8 + cs.windowSize()/2 + cache.TokensOf(len("sys")) + 300
	if got := cache.TokensOf(len(wireText(wire))); got > budget {
		t.Errorf("%s: wire is ~%d tokens, over the zone budget %d", label, got, budget)
	}
}

var outlineLabelRe = regexp.MustCompile(`(?m)^t(\d+) · user:`)

// assertOutlineLabelsUnique checks that no two visible outline entries carry
// the same turn label (folds must not make numbering regress).
func assertOutlineLabelsUnique(t *testing.T, wire []Message, label string) {
	t.Helper()
	seen := map[string]bool{}
	for _, m := range wire {
		if !strings.HasPrefix(m.Content, outlineHeader) {
			continue
		}
		for _, match := range outlineLabelRe.FindAllStringSubmatch(m.Content, -1) {
			if seen[match[1]] {
				t.Errorf("%s: duplicate outline label t%s", label, match[1])
			}
			seen[match[1]] = true
		}
	}
}

// assertNewestLabelIsDemotedCount checks that outline numbering tracks the
// demotion sequence: the newest live entry's label must equal the total number
// of demoted turns (folds must not make the counter regress).
func assertNewestLabelIsDemotedCount(t *testing.T, cs *CortexSession) {
	t.Helper()
	if len(cs.outline) == 0 {
		return
	}
	if got, want := cs.outline[len(cs.outline)-1].Turn, cs.ws.Demoted(); got != want {
		t.Errorf("newest outline entry is labeled t%d, want t%d (one per demoted turn)", got, want)
	}
}

// outlineZone returns the outline message's content, or "" if none is on the wire.
func outlineZone(wire []Message) string {
	for _, m := range wire {
		if strings.HasPrefix(m.Content, outlineHeader) {
			return m.Content
		}
	}
	return ""
}

// rePrefillChars measures the suffix a longest-common-prefix cache would have
// to re-evaluate: everything after the first message that differs from the
// previous request.
func rePrefillChars(prev, cur []Message) int {
	j := 0
	for j < len(prev) && j < len(cur) && prev[j].Role == cur[j].Role && prev[j].Content == cur[j].Content {
		j++
	}
	total := 0
	for _, m := range cur[j:] {
		total += len(m.Content)
	}
	return total
}

// TestContextEvalSteadyState runs a scripted session long enough to demote
// several turns and checks completeness, boundedness, label uniqueness, and
// the cache economics claim on every turn.
func TestContextEvalSteadyState(t *testing.T) {
	quickRetries(t)
	t.Chdir(t.TempDir())
	// A well-behaved fold summarizer: compresses but keeps every citation.
	// (The lossy-summarizer case is TestContextEvalFoldKeepsHistoryReachable.)
	origFold := foldSummarize
	foldSummarize = func(ctx context.Context, cs *CortexSession, content string, window int) (string, bool, error) {
		return "FOLDED-DIGEST " + strings.Join(evalCitationRe.FindAllString(content, -1), " "), true, nil
	}
	defer func() { foldSummarize = origFold }()
	backend := newContextEvalBackend(t)
	// Window 4000 → tail watermarks high=2000/low=1333 tokens, outline cap 500.
	cs := newContextEvalSession(t, backend, 4000)

	const nTurns = 8
	filler := strings.Repeat("filler ", 400) // ~2800 chars ≈ 700 tokens per turn
	var markers []string
	var inputs []string
	for i := 1; i <= nTurns; i++ {
		input := fmt.Sprintf("u%d-marker %s", i, filler)
		if i == 2 {
			// A long paste whose tail is beyond the outline's verbatim cap:
			// after demotion this marker is reachable only through recall.
			input += " deep-needle-2"
			markers = append(markers, "deep-needle-2")
		}
		markers = append(markers, fmt.Sprintf("u%d-marker", i))
		inputs = append(inputs, input)

		if _, err := cs.Turn(context.Background(), input); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}

		// The turn's own reply first appears on the NEXT request's wire, so it
		// joins the marker set only after this turn's assertions.
		label := fmt.Sprintf("turn %d", i)
		wire := backend.lastWire()
		assertWireComplete(t, cs, wire, markers, label)
		assertWireBounded(t, cs, wire, label)
		assertOutlineLabelsUnique(t, wire, label)
		markers = append(markers, fmt.Sprintf("reply-%d-marker", i))
	}

	if cs.ws.Demoted() == 0 {
		t.Fatal("scripted session never demoted a turn; the eval exercised nothing")
	}
	assertNewestLabelIsDemotedCount(t, cs)
	if cs.Request.TailFrom <= 1 {
		t.Errorf("TailFrom = %d, want > 1 after demotion", cs.Request.TailFrom)
	}

	// Cache economics: on turns where zone A did not change, re-prefill must be
	// just the previous reply + the new input (pure append). Demotion turns may
	// re-prefill up to the outline + tail budgets.
	wires := backend.allWires()
	demotions := 0
	for i := 1; i < len(wires); i++ {
		got := rePrefillChars(wires[i-1], wires[i])
		if outlineZone(wires[i]) != outlineZone(wires[i-1]) {
			demotions++
			budget := (cs.windowSize()/8 + cs.windowSize()/2 + 300) * cache.CharsPerToken
			if got > budget {
				t.Errorf("demotion turn %d re-prefills %d chars, over budget %d", i+1, got, budget)
			}
			continue
		}
		// prev reply ("reply-N-marker done") + this turn's input + slack.
		limit := len(inputs[i]) + 64
		if got > limit {
			t.Errorf("ordinary turn %d re-prefills %d chars, want ≤ %d (pure append)", i+1, got, limit)
		}
	}
	if demotions == 0 {
		t.Error("no demotion batch was observed on the wire")
	}
}

// TestContextEvalFoldKeepsHistoryReachable forces outline folds through a
// deliberately lossy summarizer and checks that citations survive mechanically
// and turn labels stay unique — losslessness must not depend on the model.
func TestContextEvalFoldKeepsHistoryReachable(t *testing.T) {
	quickRetries(t)
	t.Chdir(t.TempDir())
	origFold := foldSummarize
	foldSummarize = func(ctx context.Context, cs *CortexSession, content string, window int) (string, bool, error) {
		return "LOSSY-DIGEST (summarizer dropped every citation)", true, nil
	}
	defer func() { foldSummarize = origFold }()

	backend := newContextEvalBackend(t)
	// Window 800 → tail watermarks high=200/low=133 tokens, outline cap 100:
	// each demoted entry (~125 tokens) pushes the outline over cap, so folds
	// fire repeatedly once two entries accumulate.
	cs := newContextEvalSession(t, backend, 800)

	const nTurns = 8
	filler := strings.Repeat("pad ", 125) // ~500 chars ≈ 125 tokens per turn
	var markers []string
	demotedCitations := map[string]bool{}
	for i := 1; i <= nTurns; i++ {
		input := fmt.Sprintf("u%d-marker %s", i, filler)
		markers = append(markers, fmt.Sprintf("u%d-marker", i))
		if _, err := cs.Turn(context.Background(), input); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
		for _, e := range cs.outline {
			demotedCitations[e.Citation] = true
		}

		label := fmt.Sprintf("turn %d", i)
		wire := backend.lastWire()
		assertWireComplete(t, cs, wire, markers, label)
		assertOutlineLabelsUnique(t, wire, label)
	}

	if cs.outlineFolded == "" {
		t.Fatal("the outline never folded; the eval exercised nothing")
	}
	assertNewestLabelIsDemotedCount(t, cs)
	// Every citation ever rendered must still be visible in the outline zone —
	// folded entries included — or demoted turns become unreachable.
	block := cs.renderOutlineBlock()
	for c := range demotedCitations {
		if !strings.Contains(block, c) {
			t.Errorf("citation %s vanished from the outline zone after a fold", c)
		}
	}
}

// TestContextEvalCompactSeam checks that Compact's summary actually reaches
// the model, and that the transcript Compact writes resumes demotable.
func TestContextEvalCompactSeam(t *testing.T) {
	quickRetries(t)
	t.Chdir(t.TempDir())
	origCompact := compactSummarize
	compactSummarize = func(ctx context.Context, cs *CortexSession, content string, window int) (string, bool, error) {
		return "COMPACT-DIGEST-marker of the session so far", true, nil
	}
	defer func() { compactSummarize = origCompact }()

	backend := newContextEvalBackend(t)
	cs := newContextEvalSession(t, backend, 4000)

	for i := 1; i <= 2; i++ {
		if _, err := cs.Turn(context.Background(), fmt.Sprintf("pre%d-marker question", i)); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
	}
	if err := cs.Compact(context.Background()); err != nil {
		t.Fatalf("compact: %v", err)
	}

	if _, err := cs.Turn(context.Background(), "post1-marker question"); err != nil {
		t.Fatalf("post-compact turn: %v", err)
	}
	wire := backend.lastWire()
	if !strings.Contains(wireText(wire), "COMPACT-DIGEST-marker") {
		t.Error("the compacted summary never reached the model")
	}

	if _, err := cs.Turn(context.Background(), "post2-marker question"); err != nil {
		t.Fatalf("post-compact turn 2: %v", err)
	}

	// Resume the post-Compact transcript in a fresh session: the stamped spans
	// must replay (stamps do not start at 1 in a compacted transcript).
	id := cs.SessionID
	cs.transcript.Close()
	cs.transcript = nil
	cs2 := &CortexSession{Window: 4000, Request: &AgentRequest{Model: "m", BaseURL: backend.srv.URL, Messages: []Message{}}}
	if err := cs2.ResumeTranscript(id); err != nil {
		t.Fatalf("resume: %v", err)
	}
	defer cs2.transcript.Close()
	if cs2.ws.TailTokens() == 0 {
		t.Error("post-Compact transcript replayed no spans; its turns can never demote")
	}
	if _, err := cs2.Turn(context.Background(), "post3-marker question"); err != nil {
		t.Fatalf("resumed turn: %v", err)
	}
	assertWireComplete(t, cs2, backend.lastWire(), []string{
		"COMPACT-DIGEST-marker", "post1-marker", "post2-marker", "post3-marker",
	}, "resumed post-compact turn")
}

func TestTurnMemoryIndexKeepsSystemStable(t *testing.T) {
	quickRetries(t)
	t.Chdir(t.TempDir())
	backend := newContextEvalBackend(t)
	cs := newContextEvalSession(t, backend, 4000)
	cs.EnableMemory()
	if _, err := cs.MemoryWrite("cache-marker", "A durable cache marker.", ""); err != nil {
		t.Fatal(err)
	}
	system := cs.Request.Messages[0].Content
	var wires [][]Message

	for i := 0; i < 2; i++ {
		if _, err := cs.Turn(context.Background(), fmt.Sprintf("turn %d", i+1)); err != nil {
			t.Fatal(err)
		}
		if cs.Request.Messages[0].Content != system {
			t.Fatalf("turn %d mutated the cache-critical system message", i+1)
		}
		wire := backend.lastWire()
		wires = append(wires, wire)
		if len(wire) < 2 || !strings.Contains(wire[1].Content, "cache-marker") {
			t.Fatalf("turn %d did not put memory index in the fixed wire slot: %+v", i+1, wire)
		}
	}
	if got := rePrefillChars(wires[0], wires[1]); got > len("turn 2")+64 {
		t.Fatalf("unchanged memory index caused %d chars of re-prefill, want append-only suffix", got)
	}
}

func TestContextEvalResumeRestoresDemotionState(t *testing.T) {
	quickRetries(t)
	t.Chdir(t.TempDir())
	backend := newContextEvalBackend(t)
	cs := newContextEvalSession(t, backend, 1000)

	for i := 1; i <= 4; i++ {
		input := fmt.Sprintf("turn-%d %s", i, strings.Repeat("x", 700))
		if _, err := cs.Turn(context.Background(), input); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
	}
	// Demotion runs at turn start, so one more turn applies the threshold
	// crossed by the fourth completed turn.
	if _, err := cs.Turn(context.Background(), "trigger demotion"); err != nil {
		t.Fatal(err)
	}
	if cs.ws.Demoted() == 0 || len(cs.outline) == 0 {
		t.Fatalf("test setup did not demote: frontier=%d outline=%d", cs.ws.Demoted(), len(cs.outline))
	}
	wantDemoted := cs.ws.Demoted()
	wantOutline := cache.RenderOutline(cs.outline)
	id := cs.SessionID
	cs.transcript.Close()
	cs.transcript = nil

	resumed := &CortexSession{Window: 1000, Request: &AgentRequest{Model: "m", BaseURL: backend.srv.URL}}
	if err := resumed.ResumeTranscript(id); err != nil {
		t.Fatal(err)
	}
	defer resumed.transcript.Close()
	if resumed.ws.Demoted() != wantDemoted {
		t.Fatalf("restored frontier = %d, want %d", resumed.ws.Demoted(), wantDemoted)
	}
	if got := cache.RenderOutline(resumed.outline); got != wantOutline {
		t.Fatalf("restored outline mismatch\ngot:  %s\nwant: %s", got, wantOutline)
	}
	if resumed.Request.TailFrom != resumed.ws.FrontierMsg() || resumed.Request.OutlineBlock == "" {
		t.Fatal("restored wire layout was not initialized")
	}
}

// TestContextEvalLegacyResumeHydrates checks the fallback contract: a
// transcript without turn stamps resumes with its whole history on the wire.
func TestContextEvalLegacyResumeHydrates(t *testing.T) {
	quickRetries(t)
	t.Chdir(t.TempDir())
	dir := sessionsDir()
	os.MkdirAll(dir, 0755)
	lines := []string{
		`{"kind":"message","role":"system","content":"sys"}`,
		`{"kind":"message","role":"user","content":"legacy-first-marker question"}`,
		`{"kind":"message","role":"assistant","content":"legacy-answer-marker reply"}`,
	}
	if err := os.WriteFile(filepath.Join(dir, "legacy.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	backend := newContextEvalBackend(t)
	cs := &CortexSession{Window: 4000, Request: &AgentRequest{Model: "m", BaseURL: backend.srv.URL, Messages: []Message{}}}
	if err := cs.ResumeTranscript("legacy"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	defer cs.transcript.Close()

	if _, err := cs.Turn(context.Background(), "new question"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	text := wireText(backend.lastWire())
	for _, mk := range []string{"legacy-first-marker", "legacy-answer-marker"} {
		if !strings.Contains(text, mk) {
			t.Errorf("legacy history %q missing from the wire after resume", mk)
		}
	}
}
