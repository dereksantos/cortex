package main

// context_pivot_eval_live_test.go is the ø (agentic) layer of the pivot eval
// (docs/eval-context-pivot.md): given a course change mid-session, does a
// live model actually reach for the context tools (context_evict/
// context_merge/context_adjust_watermarks), and does migrating causally
// improve post-pivot behavior versus not migrating? The Δ (deterministic)
// layer, context_pivot_eval_test.go, proves the harness CAN compose a
// migration with no model; this layer measures whether a model WILL, and
// whether it HELPS.
//
// Scenario (three needles, one pivot — the design's table):
//
//	PHASE A: plant A-dead + A-keep (codeword-style needles past the
//	  outline's 500-rune verbatim cap, so post-demotion each is reachable
//	  only via recall/outline), filler turns until both demote.
//	PIVOT: the old course is declared abandoned, a new task B is named, and
//	  A-keep is declared still-needed — worded per the L0-L3 volition ladder.
//	PHASE B: plant the B needle, filler turns until it demotes.
//	PROBES: B graded hard when its hint is live; A-keep graded hard when
//	  its hint is live; A-dead is a floor-check only (its absence/eviction
//	  is the win, not a failure to recall it).
//
// Two arms, same script: ARM-ON offers the default toolset; ARM-OFF strips
// the three context-tool declarations from Request.Tools (the config gate
// only refuses at dispatch and leaves the declaration on the wire, per the
// design's B1 finding — stripping the declaration is the only honest
// control). toolsExcept (cmd/cortex/session_core.go) is the existing
// production seam this reuses; no production code changes are made here.
//
//	CORTEX_LIVE_FLEET=1 go test ./cmd/cortex/ -run ContextPivotEval_Live -v -timeout 1800s
//
// Tunables (defaults target the fleet's 80b qwen, matching context_eval_live_test.go):
//
//	CORTEX_PIVOT_EVAL_ENDPOINT  backend base URL   (default http://localhost:4000)
//	CORTEX_PIVOT_EVAL_MODEL     coder model tag    (default qwen3-coder-q3)
//	CORTEX_PIVOT_EVAL_STUDY     summarizer tag     (default glm-4.7-flash)
//	CORTEX_PIVOT_EVAL_WINDOW    session window     (default 6000; smaller = tighter pressure, faster)
//	CORTEX_PIVOT_EVAL_TURNS     filler turns/phase (default 8)
//	CORTEX_PIVOT_EVAL_RUNG      L0|L1|L2|L3        (default L3 — the design's
//	  build-order step 3 recommends an L3 dispatch probe FIRST, a cheap flight
//	  check that answers the schema/dispatch question (H4) before spending
//	  fleet time walking the full ladder L3→L0 to find the conversion rung)

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
)

const (
	pivotArmOn  = "ARM-ON"
	pivotArmOff = "ARM-OFF"
)

// Needle codewords, distinct per role so a confabulated answer can never
// coincidentally match a different needle's value.
const (
	pivotADeadCodeword = "GLACIER-88"
	pivotAKeepCodeword = "COBALT-23"
	pivotBCodeword     = "ONYX-56"
)

const pivotTaskBDescription = "migrating the schema validator to the new config format"

// indexerShardInput plants the A-dead needle: an indexer shard count the
// pivot turn declares obsolete. Same past-outline-cap construction as
// factInputWithCodeword (context_eval_live_test.go), different topic so it
// stays textually distinct from A-keep/B in the transcript.
func indexerShardInput(codeword string) string {
	var b strings.Builder
	b.WriteString("Please note this indexer status brief; the current shard count is stated at the very end of this message and you may be asked for it later. ")
	for s := 0; s < 8; s++ {
		fmt.Fprintf(&b, "Indexer section %d: rebalancing pass %d completed on schedule with no dropped records. ", s, s)
	}
	fmt.Fprintf(&b, "The indexer shard count is %s. Acknowledge with just: OK — do not repeat the brief or the count.", codeword)
	return b.String()
}

// taskBInput plants the B needle: the new task's config token, introduced
// only after the pivot. Same past-outline-cap construction as factInput.
func taskBInput(codeword string) string {
	var b strings.Builder
	b.WriteString("Please note this new task's brief; the config token is stated at the very end of this message and you will be asked for it later. ")
	for s := 0; s < 8; s++ {
		fmt.Fprintf(&b, "Task section %d: the migration gates on schema validation, the dry-run report, and sign-off from the on-call reviewer. ", s)
	}
	fmt.Fprintf(&b, "The config token is %s. Acknowledge with just: OK — do not repeat the brief or the token.", codeword)
	return b.String()
}

// pivotTurnInput builds the pivot turn's wording at the given ladder rung.
// Wording stays principle-shaped through L2 (never a tool recipe); L3 is
// deliberately a recipe because its job is to isolate the mechanical layer
// (docs/eval-context-pivot.md's volition ladder).
func pivotTurnInput(rung string) string {
	base := fmt.Sprintf("The indexer work is abandoned; the new task is %s. We'll still need the deploy codeword from before.", pivotTaskBDescription)
	switch rung {
	case "L0":
		return base
	case "L1":
		return base + " Curate your working context for the new task before we start."
	case "L2":
		return base + " Curate your working context for the new task before we start. You have context tools available for evicting or merging outline entries you no longer need."
	case "L3":
		return base + " Evict the outline entries about the indexer work from your context."
	default:
		return base
	}
}

// pivotToolsForArm returns the toolset to offer: the default toolset for
// ARM-ON, or the default toolset with the three context-tool declarations
// stripped for ARM-OFF. toolsExcept is the existing production helper
// (session_core.go) also used to gate FunctionRemove/FunctionScanLandscape —
// this is the cleanest existing seam for an honest declaration-level strip
// (the config gate, tools.enable_context_*, only refuses at dispatch and
// leaves the declaration on the wire).
func pivotToolsForArm(arm string) []Tool {
	if arm != pivotArmOff {
		return toolSet
	}
	ts := toolsExcept(toolSet, tools.FunctionContextEvict)
	ts = toolsExcept(ts, tools.FunctionContextMerge)
	ts = toolsExcept(ts, tools.FunctionContextAdjustWatermarks)
	return ts
}

// turnBoundary records where a turn's messages start, so a tool call found
// later in cs.Request.Messages can be attributed back to the turn label that
// issued it.
type turnBoundary struct {
	label string
	start int
}

// labelFor returns the label of the last turn boundary at or before idx.
func labelFor(bounds []turnBoundary, idx int) string {
	label := "?"
	for _, b := range bounds {
		if idx >= b.start {
			label = b.label
		}
	}
	return label
}

// contextToolActivity reports every context_evict/context_merge/
// context_adjust_watermarks/recall call in cs.Request.Messages, attributed to
// the turn that issued it — "which tools fired and when relative to the
// pivot."
func contextToolActivity(cs *CortexSession, bounds []turnBoundary) []string {
	var lines []string
	for i, m := range cs.Request.Messages {
		for _, call := range m.ToolCalls {
			switch call.Function.Name {
			case tools.FunctionContextEvict, tools.FunctionContextMerge, tools.FunctionContextAdjustWatermarks, "recall":
				lines = append(lines, fmt.Sprintf("%s: %s(%s)", labelFor(bounds, i), call.Function.Name, call.Function.Arguments))
			}
		}
	}
	return lines
}

// pivotArmResult is what one arm's run reports — the raw material for the
// final t.Logf summary and the ARM-ON vs ARM-OFF comparison.
type pivotArmResult struct {
	arm             string
	rung            string
	stats           []turnStat
	migratedAtPivot bool // a context tool fired during the pivot turn itself
	migratedByProbe bool // a context tool fired anywhere between pivot and the probes
	toolActivity    []string
	bHintVisible    bool
	bReply          string
	bRecall         bool
	aKeepHintVis    bool
	aKeepReply      string
	aKeepRecall     bool
	aDeadReply      string
	aDeadRecall     bool
	foldedByProbe   bool
}

// runPivotArm drives the full three-needle pivot scenario for one arm and
// reports (does not necessarily gate) every measurement the design calls for.
func runPivotArm(t *testing.T, arm, endpoint, model, study string, window, fillers int, rung string) pivotArmResult {
	t.Helper()
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
			Tools:       pivotToolsForArm(arm),
			MaxTokens:   codeMaxOutputTokens,
		},
	}
	cs.ws = cs.newWorkingSet(1)
	cs.StartTranscript()
	if cs.transcript == nil {
		t.Fatal("StartTranscript failed")
	}
	defer cs.transcript.Close()

	var stats []turnStat
	var bounds []turnBoundary
	runTurn := func(label, input string) string {
		before := len(cs.Request.Messages)
		reply := runLiveTurn(t, cs, &stats, label, input)
		bounds = append(bounds, turnBoundary{label, before})
		return reply
	}

	// --- Phase A: plant A-dead + A-keep, filler until both demote ------------
	runTurn("a-dead", indexerShardInput(pivotADeadCodeword))
	runTurn("a-keep", factInputWithCodeword(pivotAKeepCodeword))
	for i := 1; i <= fillers; i++ {
		runTurn(fmt.Sprintf("filler-a-%d", i), fillerInput(i))
		if cs.ws.Demoted() >= 2 && i >= 2 {
			break // both fact turns are out of the tail; more fillers only add runtime
		}
	}
	if cs.ws.Demoted() < 2 {
		t.Fatalf("setup error: A-dead/A-keep never both demoted after %d filler turns (window %d too large for the script?)", fillers, window)
	}

	// --- Pivot turn ------------------------------------------------------------
	prePivot := len(cs.Request.Messages)
	runTurn("pivot", pivotTurnInput(rung))
	migratedAtPivot := false
	for _, c := range toolCallsSince(cs, prePivot) {
		switch c.Function.Name {
		case tools.FunctionContextEvict, tools.FunctionContextMerge, tools.FunctionContextAdjustWatermarks:
			migratedAtPivot = true
		}
	}

	// --- Phase B: plant B, filler until it demotes ------------------------------
	demotedBeforeB := cs.ws.Demoted()
	runTurn("b-needle", taskBInput(pivotBCodeword))
	for i := 1; i <= fillers; i++ {
		runTurn(fmt.Sprintf("filler-b-%d", i), fillerInput(fillers+i))
		if cs.ws.Demoted() > demotedBeforeB && i >= 2 {
			break // the B turn is out of the tail; more fillers only add runtime
		}
	}
	if cs.ws.Demoted() <= demotedBeforeB {
		t.Fatalf("setup error: B needle turn never demoted after %d filler turns", fillers)
	}

	migratedByProbe := false
	for _, c := range toolCallsSince(cs, prePivot) {
		switch c.Function.Name {
		case tools.FunctionContextEvict, tools.FunctionContextMerge, tools.FunctionContextAdjustWatermarks:
			migratedByProbe = true
		}
	}

	// The sizing rule (docs/eval-context-pivot.md): without curation, A+B
	// entries must exceed the outline cap so the fold fires before the B
	// probe. Pre-flight, as designed: if no migration happened AND the
	// outline never folded, the scenario never actually tightened — pad with
	// more filler (bounded) before declaring a setup error, so a slightly
	// loose window doesn't spuriously invalidate the run.
	if !migratedByProbe && cs.outlineFolded == "" {
		for i := 1; i <= 8 && cs.outlineFolded == ""; i++ {
			runTurn(fmt.Sprintf("filler-b-pad-%d", i), fillerInput(2*fillers+i))
		}
		if cs.outlineFolded == "" {
			t.Fatalf("setup error: no migration occurred and the outline never folded by probe time (window %d too large for this script? tighten CORTEX_PIVOT_EVAL_WINDOW)", window)
		}
	}
	foldedByProbe := cs.outlineFolded != ""

	outlineNow := cacheOutlineText(cs)
	bHintVisible := strings.Contains(outlineNow, "config token")
	aKeepHintVis := strings.Contains(outlineNow, "deploy codeword")

	// --- Probes: B, then A-keep, then A-dead (the design's order) --------------
	preProbe := len(cs.Request.Messages)
	bReply := runTurn("probe-b", "What is the config token from the new task's brief?")
	bRecall := recallCalledSince(cs, preProbe)

	preProbe = len(cs.Request.Messages)
	aKeepReply := runTurn("probe-a-keep", "What is the deploy codeword from earlier in this session?")
	aKeepRecall := recallCalledSince(cs, preProbe)

	preProbe = len(cs.Request.Messages)
	aDeadReply := runTurn("probe-a-dead", "What was the indexer shard count from earlier in this session?")
	aDeadRecall := recallCalledSince(cs, preProbe)

	result := pivotArmResult{
		arm: arm, rung: rung, stats: stats,
		migratedAtPivot: migratedAtPivot, migratedByProbe: migratedByProbe,
		toolActivity: contextToolActivity(cs, bounds),
		bHintVisible: bHintVisible, bReply: bReply, bRecall: bRecall,
		aKeepHintVis: aKeepHintVis, aKeepReply: aKeepReply, aKeepRecall: aKeepRecall,
		aDeadReply: aDeadReply, aDeadRecall: aDeadRecall,
		foldedByProbe: foldedByProbe,
	}

	// --- report ------------------------------------------------------------------
	reportTurnStats(t, model, window, stats)
	t.Logf("[%s] rung=%s migrated-at-pivot=%v migrated-by-probe=%v outline-folded=%v", arm, rung, migratedAtPivot, migratedByProbe, foldedByProbe)
	if len(result.toolActivity) == 0 {
		t.Logf("[%s] context/recall tool activity: none", arm)
	} else {
		for _, line := range result.toolActivity {
			t.Logf("[%s] activity: %s", arm, line)
		}
	}
	t.Logf("[%s] B probe: %q (hint visible: %v, recall called: %v)", arm, strings.TrimSpace(bReply), bHintVisible, bRecall)
	t.Logf("[%s] A-keep probe: %q (hint visible: %v, recall called: %v)", arm, strings.TrimSpace(aKeepReply), aKeepHintVis, aKeepRecall)
	t.Logf("[%s] A-dead probe (floor-check): %q (recall called: %v)", arm, strings.TrimSpace(aDeadReply), aDeadRecall)

	// --- gates ---------------------------------------------------------------------
	// G1 (plumbing rung): only hard-gated at L3 — the design's floor. Failing
	// to convert at L0-L2 is an "expected red today" measurement, not a bug;
	// failing at L3 (direct command) implicates schema/dispatch friction (H4).
	if rung == "L3" && arm == pivotArmOn && !migratedByProbe {
		t.Errorf("G1 FAILED: no context tool fired even at L3 (direct command) — schema/dispatch friction or model floor (H4)")
	}

	// G2/G3 (retention): gated hard only "at the conversion rung" — i.e. this
	// run actually converted AND the needle's hint was live at probe time.
	// Otherwise reported via the accepted floor, per the design (a
	// non-converting run is informative, not a bug).
	gradeNeedle := func(label, reply, codeword string, hintVisible, gateHard bool) {
		if hintVisible && gateHard {
			if !strings.Contains(reply, codeword) {
				t.Errorf("%s FAILED (hard — hint was live, migration converted): reply %q missing %s", label, strings.TrimSpace(reply), codeword)
			}
			return
		}
		floorGrade(t, label+" (reported floor)", reply, codeword)
	}
	gradeNeedle(fmt.Sprintf("[%s] B retention", arm), bReply, pivotBCodeword, bHintVisible, arm == pivotArmOn && migratedByProbe)
	gradeNeedle(fmt.Sprintf("[%s] A-keep retention", arm), aKeepReply, pivotAKeepCodeword, aKeepHintVis, arm == pivotArmOn && migratedByProbe)

	// G4 (A-dead): always a floor-check — its absence/eviction is the win,
	// never gated hard for exact recall.
	floorGrade(t, fmt.Sprintf("[%s] A-dead floor", arm), aDeadReply, pivotADeadCodeword)

	// G5 (bounded): applies uniformly to both arms — mechanical demotion is
	// unaffected by whether the model curates.
	assertPromptBounded(t, stats, window)

	// Anti-vacuity: ARM-OFF passing the B graded probe with the hint live
	// means the scenario failed to make migration causally necessary — a
	// setup issue (retune CORTEX_PIVOT_EVAL_WINDOW/TURNS), not a real result.
	if arm == pivotArmOff && bHintVisible && strings.Contains(bReply, pivotBCodeword) {
		t.Logf("WARNING [%s]: B graded probe passed with no possible migration (tools stripped) — sizing may be too loose; this run does not prove the benefit arm", arm)
	}

	return result
}

func TestContextPivotEval_Live(t *testing.T) {
	if os.Getenv("CORTEX_LIVE_FLEET") == "" {
		t.Skip("set CORTEX_LIVE_FLEET=1 to run the live pivot eval")
	}
	endpoint := liveEnv("CORTEX_PIVOT_EVAL_ENDPOINT", "http://localhost:4000")
	model := liveEnv("CORTEX_PIVOT_EVAL_MODEL", "qwen3-coder-q3")
	study := liveEnv("CORTEX_PIVOT_EVAL_STUDY", "glm-4.7-flash")
	window := liveEnvInt("CORTEX_PIVOT_EVAL_WINDOW", 6000)
	fillers := liveEnvInt("CORTEX_PIVOT_EVAL_TURNS", 8)
	rung := liveEnv("CORTEX_PIVOT_EVAL_RUNG", "L3")
	switch rung {
	case "L0", "L1", "L2", "L3":
	default:
		t.Logf("unrecognized CORTEX_PIVOT_EVAL_RUNG %q, falling back to L3", rung)
		rung = "L3"
	}

	var onResult, offResult pivotArmResult
	t.Run(pivotArmOn, func(t *testing.T) {
		onResult = runPivotArm(t, pivotArmOn, endpoint, model, study, window, fillers, rung)
	})
	t.Run(pivotArmOff, func(t *testing.T) {
		offResult = runPivotArm(t, pivotArmOff, endpoint, model, study, window, fillers, rung)
	})

	// G6 (reported, not gated until n≥3 shows the separation is stable, per
	// the design): does ARM-ON hold B's hint live at least as long/well as
	// ARM-OFF?
	t.Logf("comparison: rung=%s ARM-ON[migrated=%v B-hint=%v B-hit=%v] ARM-OFF[migrated=%v B-hint=%v B-hit=%v]",
		rung,
		onResult.migratedByProbe, onResult.bHintVisible, strings.Contains(onResult.bReply, pivotBCodeword),
		offResult.migratedByProbe, offResult.bHintVisible, strings.Contains(offResult.bReply, pivotBCodeword))
}
