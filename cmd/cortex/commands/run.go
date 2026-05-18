// Package commands — cortex run --type=<dag-type> entry point.
//
// CLI surface for the DAG protocol per docs/dag-build-plan.md. Routes
// to the seed-and-grow executor with a per-type seed and initial
// budget. Telemetry from each node lands in
// .cortex/db/dag_traces.jsonl via the trace callback.
//
// Stage 2 scope:
//   - --type=turn only (other types route to "not implemented" stubs)
//   - 11 registered ops via ops.RegisterDefaults: sense.prompt (stub),
//     represent.embed, remember.vector_search, attend.rerank,
//     value.score, value.detect_contradiction, decide.inject,
//     decide.should_capture, model.predict_next,
//     maintain.extract_insight, maintain.capture (stub).
//   - decide.coding_turn handler (wraps the LLM agent loop, ADR-001
//     V0 inline form) is registered separately because it crosses the
//     pkg/cognition/dag/ops vs internal/harness/dagnode boundary.
//   - Default chain for --type=turn:
//     sense.prompt → represent.embed → remember.vector_search
//     → attend.rerank → decide.inject → decide.coding_turn
//     → maintain.extract_insight → maintain.capture
//     Each step's output flows to the next via Attrs population in
//     wrapper closures (proper $node.out resolution lands in Stage 4
//     when the executor gains data-edge handling).
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/eval/dagtrace"
	evalv2 "github.com/dereksantos/cortex/internal/eval/v2"
	"github.com/dereksantos/cortex/internal/harness/dagnode"
	"github.com/dereksantos/cortex/pkg/cognition"
	"github.com/dereksantos/cortex/pkg/cognition/dag"
	"github.com/dereksantos/cortex/pkg/cognition/dag/ops"
)

func init() {
	Register(&RunCommand{})
}

// RunCommand executes a DAG of a given type with a user-supplied
// prompt (or other trigger).
type RunCommand struct{}

func (c *RunCommand) Name() string        { return "run" }
func (c *RunCommand) Description() string { return "Run a DAG by type (turn|think|dream|capture|eval)" }

func (c *RunCommand) Execute(ctx *Context) error {
	dagType := ""
	prompt := ""
	model := ""
	workdir := ""
	outputFormat := "human"
	verbose := false
	scenarioPath := ""
	strategy := "cortex"

	for i := 0; i < len(ctx.Args); i++ {
		arg := ctx.Args[i]
		switch arg {
		case "--type":
			if i+1 < len(ctx.Args) {
				dagType = ctx.Args[i+1]
				i++
			}
		case "--prompt":
			if i+1 < len(ctx.Args) {
				prompt = ctx.Args[i+1]
				i++
			}
		case "--model", "-m":
			if i+1 < len(ctx.Args) {
				model = ctx.Args[i+1]
				i++
			}
		case "--workdir":
			if i+1 < len(ctx.Args) {
				workdir = ctx.Args[i+1]
				i++
			}
		case "--scenario":
			if i+1 < len(ctx.Args) {
				scenarioPath = ctx.Args[i+1]
				i++
			}
		case "--strategy":
			if i+1 < len(ctx.Args) {
				strategy = ctx.Args[i+1]
				i++
			}
		case "-o", "--output":
			if i+1 < len(ctx.Args) {
				outputFormat = ctx.Args[i+1]
				i++
			}
		case "-v", "--verbose":
			verbose = true
		case "-h", "--help":
			printRunHelp()
			return nil
		default:
			if strings.HasPrefix(arg, "--type=") {
				dagType = strings.TrimPrefix(arg, "--type=")
			} else if strings.HasPrefix(arg, "--prompt=") {
				prompt = strings.TrimPrefix(arg, "--prompt=")
			} else if strings.HasPrefix(arg, "--model=") {
				model = strings.TrimPrefix(arg, "--model=")
			} else if strings.HasPrefix(arg, "--workdir=") {
				workdir = strings.TrimPrefix(arg, "--workdir=")
			} else if strings.HasPrefix(arg, "--scenario=") {
				scenarioPath = strings.TrimPrefix(arg, "--scenario=")
			} else if strings.HasPrefix(arg, "--strategy=") {
				strategy = strings.TrimPrefix(arg, "--strategy=")
			}
		}
	}

	if dagType == "" {
		return fmt.Errorf("--type required (turn|think|dream|capture|eval)")
	}

	switch dagType {
	case "turn":
		return runTurnDAG(prompt, model, workdir, outputFormat, verbose)
	case "eval":
		if scenarioPath == "" {
			return fmt.Errorf("--type=eval requires --scenario=PATH")
		}
		return runEvalDAG(scenarioPath, model, workdir, strategy, outputFormat, verbose)
	case "think", "dream", "capture":
		return fmt.Errorf("--type=%s not yet implemented (Stage 5 B/C/D of dag-build-plan.md)", dagType)
	default:
		return fmt.Errorf("unknown --type=%s (known: turn, think, dream, capture, eval)", dagType)
	}
}

// runTurnDAG seeds a turn-type DAG with the Stage 2 chain (8 nodes)
// and runs it through the executor. Demonstrates real ops walking
// end-to-end with telemetry. The decide.coding_turn node uses the
// real LLM agent loop (via internal/harness/dagnode) when --model is
// provided; otherwise runs in stub mode (no provider) so developers
// can exercise the protocol without API keys.
//
// LLM-backed ops (attend.rerank, decide.inject, etc.) self-modulate
// when no provider is configured: they take the mechanical-fallback
// path and emit "fallback": true in their output. This means
// `cortex run --type=turn --prompt X` works without API keys, just
// with reduced-quality results.
func runTurnDAG(prompt, model, workdir, outputFormat string, verbose bool) error {
	turnID := fmt.Sprintf("turn-%d", time.Now().UnixNano())

	// Wire structured trace writes to .cortex/db/dag_traces.jsonl.
	// Tolerant: if the writer can't be constructed (e.g., no writable
	// cwd), continue without telemetry rather than abort.
	tw, twErr := dagtrace.NewWriter("")
	var traceCB dag.TraceCallback
	if twErr == nil {
		traceCB = tw.Callback(turnID)
	} else if verbose {
		fmt.Fprintf(os.Stderr, "[run] trace writer init failed: %v (continuing without dag_traces.jsonl)\n", twErr)
	}

	// Stage 3: build the chain registry with trace callback wiring so
	// coding_turn can fabricate per-tool act.* rows alongside its own
	// row in dag_traces.jsonl.
	reg := buildTurnRegistry(prompt, model, workdir, traceCB)

	// Stage 4-C: warm registry Cost fields from the prior process's
	// calibration snapshot, if one exists. Tolerant: missing file is
	// a cold start; corrupt file is logged in verbose mode.
	if _, err := dag.LoadCalibrationSnapshot(reg, ""); err != nil && verbose {
		fmt.Fprintf(os.Stderr, "[run] calibration load failed: %v (using registered defaults)\n", err)
	}

	ex := dag.NewExecutor(reg, traceCB)

	seed := []dag.NodeSpec{
		{
			Function: dag.FuncSense,
			Op:       "prompt",
			ID:       "n1",
			Attrs:    map[string]any{"prompt": prompt},
		},
	}
	budget := dag.DefaultTurnBudget()
	trace, err := ex.Run(context.Background(), turnID, seed, budget)
	if err != nil {
		return fmt.Errorf("run turn DAG: %w", err)
	}

	if outputFormat == "json" {
		// Render trace as JSON envelope.
		out := map[string]any{
			"turn_id":         trace.TurnID,
			"initial_budget":  trace.InitialBudget,
			"final_budget":    trace.FinalBudget,
			"total_executed":  trace.TotalExecuted,
			"exhausted":       trace.Exhausted,
			"exhausted_axis":  trace.ExhaustedAxis,
			"spawn_refusals":  trace.SpawnRefusals,
			"trace_entry_ops": qualifiedOps(trace.Entries),
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	} else {
		fmt.Printf("=== turn DAG (%s) ===\n", turnID)
		fmt.Printf("Initial budget: %s\n", trace.InitialBudget)
		fmt.Printf("Executed %d nodes; exhausted=%v\n\n", trace.TotalExecuted, trace.Exhausted)
		for _, e := range trace.Entries {
			parent := e.ParentID
			if parent == "" {
				parent = "(seed)"
			}
			fmt.Printf("  %s [%s] parent=%s ok=%v cost={lat=%d tok=%d}\n",
				e.NodeID, e.QualifiedName, parent, e.OK,
				e.CostConsumed.LatencyMS, e.CostConsumed.Tokens)
			if verbose && e.ErrorMessage != "" {
				fmt.Printf("    err: %s\n", e.ErrorMessage)
			}
		}
		fmt.Printf("\nFinal budget: %s\n", trace.FinalBudget)
		if trace.Exhausted {
			fmt.Printf("Stopped on: %s exhaustion\n", trace.ExhaustedAxis)
		}
	}
	return nil
}

// runEvalDAG (Stage 5-A) loads a v2 scenario YAML, builds the prompt
// (optionally prepended with CortexContext when strategy=cortex),
// optionally seeds a workdir from the scenario's SeedDir, executes
// the same turn-shaped DAG as `cortex run --type=turn`, then runs
// the scenario's Verify command. Emits a one-line summary (or JSON
// envelope) and exits non-zero if verify fails.
//
// This unifies the eval path with the production runtime — the same
// executor, the same chain, the same calibration + rollover + parallel
// behavior. The grid runner that bulk-runs scenarios can layer atop
// `cortex run --type=eval` by invoking it per cell, keeping
// cell_results.jsonl emission as the grid's single source of truth
// (no duplicate sink here).
func runEvalDAG(scenarioPath, model, workdir, strategy, outputFormat string, verbose bool) error {
	scn, err := evalv2.Load(scenarioPath)
	if err != nil {
		return fmt.Errorf("load scenario %s: %w", scenarioPath, err)
	}

	if len(scn.Tests) == 0 || scn.Tests[0].Query == "" {
		return fmt.Errorf("scenario %s has no tests[0].query — nothing to prompt with", scn.ID)
	}
	prompt := scn.Tests[0].Query

	// Stage 5-A: same prefix logic the grid runner uses, so a single
	// `cortex run --type=eval` produces the same prompt that
	// strategy=cortex would produce in a grid cell.
	if strategy == "cortex" && len(scn.CortexContext) > 0 {
		bullets := []string{}
		for _, b := range scn.CortexContext {
			b = strings.TrimSpace(b)
			if b != "" {
				bullets = append(bullets, b)
			}
		}
		if len(bullets) > 0 {
			prompt = "Hints: " + strings.Join(bullets, " ") + "\n\n" + prompt
		}
	}

	// Seed the workdir if the scenario provides one. scn.SeedDir is
	// stored as-given in the YAML; convention (per the
	// internal/eval/v2/scenario.go doc + how grid.go consumes it) is
	// "relative to cwd of the eval invocation." A scenario file
	// committed under test/evals/coding/sqlx-insert-user.yaml with
	// seed_dir: test/evals/coding/seeds/sqlx-insert-user works when
	// `cortex run --type=eval` is invoked from the repo root.
	if scn.SeedDir != "" {
		seedDir := scn.SeedDir
		if workdir == "" {
			tmp, terr := os.MkdirTemp("", "cortex-eval-"+scn.ID+"-")
			if terr != nil {
				return fmt.Errorf("mktemp workdir: %w", terr)
			}
			workdir = tmp
		}
		if cerr := copyDir(seedDir, workdir); cerr != nil {
			return fmt.Errorf("seed workdir from %s: %w", seedDir, cerr)
		}
		if verbose {
			fmt.Fprintf(os.Stderr, "[eval] seeded workdir %s from %s\n", workdir, seedDir)
		}
	}

	// Delegate to runTurnDAG — same executor, same chain, same
	// rollover/calibration. The eval wrapper adds prompt construction
	// + verify pre/post hooks; everything else flows through.
	if err := runTurnDAG(prompt, model, workdir, outputFormat, verbose); err != nil {
		return fmt.Errorf("turn DAG: %w", err)
	}

	// Verify. The scenario's Verify command runs in the workdir; exit
	// 0 = pass, non-zero = fail. Empty Verify = scenario reports no
	// programmatic pass/fail signal (the trace alone is the artifact).
	if scn.Verify == "" {
		if outputFormat != "json" {
			fmt.Printf("\n[eval] %s: no verify command — trace is the artifact\n", scn.ID)
		}
		return nil
	}

	cmd := exec.Command("bash", "-c", scn.Verify)
	if workdir != "" {
		cmd.Dir = workdir
	}
	out, verifyErr := cmd.CombinedOutput()
	pass := verifyErr == nil

	if outputFormat == "json" {
		payload := map[string]any{
			"scenario_id": scn.ID,
			"strategy":    strategy,
			"workdir":     workdir,
			"verify_pass": pass,
			"verify_out":  string(out),
		}
		bb, _ := json.MarshalIndent(payload, "", "  ")
		fmt.Println(string(bb))
	} else {
		status := "PASS"
		if !pass {
			status = "FAIL"
		}
		fmt.Printf("\n[eval] %s verify: %s\n", scn.ID, status)
		if !pass {
			fmt.Printf("  command: %s\n", scn.Verify)
			tail := strings.TrimSpace(string(out))
			if len(tail) > 400 {
				tail = "..." + tail[len(tail)-400:]
			}
			if tail != "" {
				fmt.Printf("  output: %s\n", tail)
			}
		}
	}
	if !pass {
		return fmt.Errorf("verify failed")
	}
	return nil
}

// copyDir recursively copies srcDir's contents into dstDir. Files
// already present in dstDir are overwritten. Directory permissions
// are preserved from src; file permissions are preserved from src.
// Used by the eval-type DAG to seed a workdir from the scenario's
// SeedDir.
func copyDir(srcDir, dstDir string) error {
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(dstDir, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, info.Mode())
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

// _ keeps the strconv import alive if no other code in this file
// uses it (defensive against future flag-parsing edits).
var _ = strconv.Atoi

// buildTurnRegistry registers the Stage 2 op set via
// ops.RegisterDefaults, then overlays chain-wiring wrappers so a
// `cortex run --type=turn` walk produces a sensible 8-node tree.
//
// Wrappers convert each step's typed Out into the next step's Attrs,
// then declare the spawn relationship. This is the Stage 2 pattern;
// Stage 4 will replace explicit wrapper wiring with executor-level
// $node.out data-edge resolution.
//
// LLM deps (provider, embedder, storage) are nil here for the
// general-developer-friendly path — every LLM op's mechanical
// fallback handles nil deps gracefully. A future iteration will
// resolve provider/embedder via llm.NewLLMClient and storage via the
// project ContextDir.
func buildTurnRegistry(prompt, model, workdir string, traceCB dag.TraceCallback) *dag.Registry {
	reg := dag.NewRegistry()

	// Phase 1: register the canonical op set with nil deps. Each op's
	// mechanical fallback handles missing providers/embedders/storage.
	// Production paths that wire real deps will override these by
	// re-registering with non-nil DefaultsConfig (or this function
	// will grow a Config param).
	if _, err := ops.RegisterDefaults(reg, ops.DefaultsConfig{}); err != nil {
		panic(fmt.Sprintf("ops.RegisterDefaults: %v", err))
	}

	// Phase 2: re-register the chain nodes with spawn-wiring wrappers
	// around their underlying handlers. Last-write-wins on the
	// registry, so this swaps in the chain-aware variants.
	chain := buildTurnChain(prompt, model, workdir, reg, traceCB)
	for _, spec := range chain {
		if err := reg.Register(spec); err != nil {
			panic(fmt.Sprintf("chain register %s: %v", spec.QualifiedName(), err))
		}
	}

	return reg
}

// buildTurnChain returns NodeSpecs whose handlers wrap the
// ops-package handlers with chain-wiring spawn logic. Each wrapper:
//
//   - reads the underlying op's Out
//   - constructs the next node's Attrs from that Out
//   - sets NodeResult.Spawn to the next node
//
// Nodes whose underlying op doesn't exist in the ops package
// (sense.prompt, decide.coding_turn, maintain.capture) get inline
// implementations.
func buildTurnChain(prompt, model, workdir string, reg *dag.Registry, traceCB dag.TraceCallback) []dag.NodeSpec {
	// Reuse the underlying handlers from a fresh registry so the
	// wrappers can call them via Get() without doing the construction
	// again. Cleaner than capturing each spec individually.
	innerReg := dag.NewRegistry()
	if _, err := ops.RegisterDefaults(innerReg, ops.DefaultsConfig{}); err != nil {
		panic(err)
	}
	get := func(qname string) dag.Handler {
		spec, err := innerReg.Get(qname)
		if err != nil {
			panic(fmt.Sprintf("inner registry missing %s: %v", qname, err))
		}
		return spec.Handler
	}

	// Stage 3 wiring: when act ops are registered on reg, coding_turn
	// routes its tool calls through them and emits per-tool trace rows
	// via traceCB. When no act ops are registered, coding_turn falls
	// back to the V0 inline path. The default chain doesn't register
	// act ops yet (the harness flag opts that in); leaving cfg.ActRegistry
	// nil here preserves V0 behavior for `cortex run --type=turn`.
	// Callers that want Stage 3 dispatch (cortex code, REPL) build
	// their own chain with ActRegistry set.
	codingTurnHandler := dagnode.NewCodingTurnHandler(dagnode.CodingTurnConfig{
		Model:       model,
		Workdir:     workdir,
		ActRegistry: nil, // V0 behavior; Stage-3-aware callers override
		TraceCB:     traceCB,
	})

	// sense.prompt — captures the trigger prompt; spawns represent.embed.
	senseSpec := dag.NodeSpec{
		Function:    dag.FuncSense,
		Op:          "prompt",
		Description: "ingress: user prompt arrives; spawns represent.embed",
		Cost:        dag.Cost{LatencyMS: 5, Tokens: 0},
		Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			return dag.NodeResult{
				Out: map[string]any{"prompt": prompt},
				Spawn: []dag.NodeSpec{
					{
						Function: dag.FuncRepresent, Op: "embed", ID: "n2",
						Attrs: map[string]any{"text": prompt},
					},
				},
				CostConsumed: dag.Cost{LatencyMS: 5, Tokens: 0},
			}, nil
		},
	}

	// represent.embed — embed the prompt; spawns remember.vector_search.
	embedInner := get("represent.embed")
	embedSpec := dag.NodeSpec{
		Function:    dag.FuncRepresent,
		Op:          "embed",
		Description: "embed the prompt into a vector; spawns remember.vector_search",
		Cost:        dag.Cost{LatencyMS: 10, Tokens: 0},
		Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			res, err := embedInner(ctx, in, b)
			if err != nil {
				// No embedder configured (the common path without deps wired) —
				// skip vector_search by spawning attend.rerank with empty
				// candidates so the chain still completes.
				res = dag.NodeResult{
					Out:          map[string]any{"vector": nil, "skipped": true},
					CostConsumed: res.CostConsumed,
				}
			}
			vec, _ := res.Out["vector"].([]float32)
			res.Spawn = []dag.NodeSpec{
				{
					Function: dag.FuncRemember, Op: "vector_search", ID: "n3",
					Attrs: map[string]any{"query_vector": vec, "limit": 10, "threshold": 0.0},
				},
			}
			// Suppress error so the chain keeps walking; mechanical
			// fallback is part of the contract.
			return res, nil
		},
	}

	// remember.vector_search — find similar context; spawns attend.rerank.
	searchInner := get("remember.vector_search")
	searchSpec := dag.NodeSpec{
		Function:    dag.FuncRemember,
		Op:          "vector_search",
		Description: "find similar context via vector similarity; spawns attend.rerank",
		Cost:        dag.Cost{LatencyMS: 15, Tokens: 0},
		Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			res, err := searchInner(ctx, in, b)
			if err != nil {
				// No storage or no query vector — pass through with no
				// candidates so attend.rerank gets an empty set.
				res = dag.NodeResult{
					Out:          map[string]any{"results": []any{}, "count": 0, "skipped": true},
					CostConsumed: res.CostConsumed,
				}
			}
			candidates := vectorSearchResultsToCognitionResults(res.Out["results"])
			res.Spawn = []dag.NodeSpec{
				{
					Function: dag.FuncAttend, Op: "rerank", ID: "n4",
					Attrs: map[string]any{"query": prompt, "candidates": candidates},
				},
			}
			return res, nil
		},
	}

	// attend.rerank — rerank candidates; spawns decide.inject.
	rerankInner := get("attend.rerank")
	rerankSpec := dag.NodeSpec{
		Function:    dag.FuncAttend,
		Op:          "rerank",
		Description: "rerank candidates by relevance; spawns decide.inject",
		Cost:        dag.Cost{LatencyMS: 800, Tokens: 250},
		Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			res, err := rerankInner(ctx, in, b)
			if err != nil {
				return res, err
			}
			reranked, _ := res.Out["reranked"].([]cognition.Result)
			res.Spawn = []dag.NodeSpec{
				{
					Function: dag.FuncDecide, Op: "inject", ID: "n5",
					Attrs: map[string]any{"query": prompt, "candidates": reranked},
				},
			}
			return res, nil
		},
	}

	// decide.inject — decide inject/wait/queue; spawns decide.coding_turn.
	injectInner := get("decide.inject")
	injectSpec := dag.NodeSpec{
		Function:    dag.FuncDecide,
		Op:          "inject",
		Description: "decide inject/wait/queue; spawns decide.coding_turn",
		Cost:        dag.Cost{LatencyMS: 700, Tokens: 150},
		Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			res, err := injectInner(ctx, in, b)
			if err != nil {
				return res, err
			}
			res.Spawn = []dag.NodeSpec{
				{
					Function: dag.FuncDecide, Op: "coding_turn", ID: "n6",
					Attrs: map[string]any{"prompt": prompt},
				},
			}
			return res, nil
		},
	}

	// decide.coding_turn — REAL agent-loop wrapper (per ADR-001 V0
	// inline form). Spawns maintain.extract_insight.
	codingTurnSpec := dag.NodeSpec{
		Function:    dag.FuncDecide,
		Op:          "coding_turn",
		Description: "wraps existing LLM agent loop (ADR-001 V0 inline form); spawns maintain.extract_insight",
		// Cost hint = 0 to skip the pre-spawn budget check — stub mode
		// is ~10ms, real LLM is seconds. Actual CostConsumed determines
		// whether downstream nodes get refused.
		Cost: dag.Cost{LatencyMS: 0, Tokens: 0},
		Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			if in == nil {
				in = map[string]any{}
			}
			if _, has := in["prompt"]; !has {
				in["prompt"] = prompt
			}
			res, err := codingTurnHandler(ctx, in, b)
			// Spawn maintain.extract_insight with the response content.
			response, _ := res.Out["response"].(string)
			res.Spawn = []dag.NodeSpec{
				{
					Function: dag.FuncMaintain, Op: "extract_insight", ID: "n7",
					Attrs: map[string]any{"content": response, "source": "coding_turn"},
				},
			}
			return res, err
		},
	}

	// maintain.extract_insight — extract insights; spawns maintain.capture.
	insightInner := get("maintain.extract_insight")
	insightSpec := dag.NodeSpec{
		Function:    dag.FuncMaintain,
		Op:          "extract_insight",
		Description: "extract insights from the turn output; spawns maintain.capture",
		Cost:        dag.Cost{LatencyMS: 900, Tokens: 200},
		Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			res, err := insightInner(ctx, in, b)
			if err != nil {
				// Empty content path — extract_insight requires content;
				// skip downstream insight extraction gracefully.
				res = dag.NodeResult{
					Out:          map[string]any{"insights": []ops.Insight{}, "count": 0, "skipped": true},
					CostConsumed: res.CostConsumed,
				}
			}
			res.Spawn = []dag.NodeSpec{
				{Function: dag.FuncMaintain, Op: "capture", ID: "n8"},
			}
			return res, nil
		},
	}

	// maintain.capture stub — Stage 3 will give it real journal-write logic.
	captureSpec := dag.NodeSpec{
		Function:    dag.FuncMaintain,
		Op:          "capture",
		Description: "persist turn outcome to journal (Stage 2 stub; real impl Stage 3)",
		Cost:        dag.Cost{LatencyMS: 20, Tokens: 10},
		Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			return dag.NodeResult{
				Out:          map[string]any{"captured": true},
				CostConsumed: dag.Cost{LatencyMS: 20, Tokens: 10},
			}, nil
		},
	}

	return []dag.NodeSpec{
		senseSpec, embedSpec, searchSpec, rerankSpec,
		injectSpec, codingTurnSpec, insightSpec, captureSpec,
	}
}

// vectorSearchResultsToCognitionResults converts storage's
// VectorSearchResult slice into cognition.Result so attend.rerank can
// consume it. Mapping: ContentID → ID, Content → Content, Similarity
// → Score. The handler accepts any interface (the underlying op
// returns []storage.VectorSearchResult); we re-shape here.
func vectorSearchResultsToCognitionResults(v any) []cognition.Result {
	// The underlying handler returns []storage.VectorSearchResult, but
	// to avoid an import dependency cycle the chain wrapper treats the
	// output via type assertion. When the storage type is wired in,
	// add a case for it.
	out := []cognition.Result{}
	switch x := v.(type) {
	case []cognition.Result:
		return x
	case []any:
		for _, e := range x {
			if m, ok := e.(map[string]any); ok {
				var r cognition.Result
				if id, ok := m["ContentID"].(string); ok {
					r.ID = id
				}
				if c, ok := m["Content"].(string); ok {
					r.Content = c
				}
				if s, ok := m["Similarity"].(float64); ok {
					r.Score = s
				}
				out = append(out, r)
			}
		}
	}
	return out
}

func qualifiedOps(entries []dag.TraceEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.QualifiedName
	}
	return out
}

func printRunHelp() {
	fmt.Fprintln(os.Stderr, `Usage: cortex run --type=<dag-type> [options]

Run a DAG of the given type through the seed-and-grow executor.

Options:
  --type TYPE         DAG type: turn | think | dream | capture | eval
                      (v0: only turn implemented)
  --prompt TEXT       User prompt (for --type=turn)
  -o, --output FMT    Output format: human | json (default: human)
  -v, --verbose       Verbose trace output
  -h, --help          Show this help

V0 scope (per docs/dag-build-plan.md Stage 1):
  - --type=turn only; other types route to "not implemented"
  - 4 stub ops (sense.prompt, attend.reflex, decide.inject, maintain.capture)
  - Demonstrates executor walks the seed, decays budget, emits trace
  - Real LLM handlers + decide.coding_turn integration land in Stage 2/3

See docs/dag-protocol.md for the protocol semantics.`)
}
