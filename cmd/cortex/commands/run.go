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
	"flag"
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

// DescribeFlags surfaces run's flag set into tools.json.
func (c *RunCommand) DescribeFlags(fs *flag.FlagSet) {
	fs.String("type", "", "DAG type: turn | think | dream | capture | eval")
	fs.String("prompt", "", "User prompt (turn DAG)")
	fs.String("model", "", "Model id")
	fs.String("workdir", "", "Target workdir for harness-bound DAGs")
	fs.String("scenario", "", "Path to scenario file (--type=eval)")
	fs.String("strategy", "cortex", "Strategy: cortex | baseline (--type=eval)")
	fs.String("event", "", "JSON event payload (--type=capture)")
	fs.String("output", "human", "Output format: human | json")
	fs.Bool("verbose", false, "Verbose output")
}

func (c *RunCommand) Execute(ctx *Context) error {
	dagType := ""
	prompt := ""
	model := ""
	workdir := ""
	outputFormat := "human"
	verbose := false
	scenarioPath := ""
	strategy := "cortex"
	eventJSON := ""

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
		case "--event":
			if i+1 < len(ctx.Args) {
				eventJSON = ctx.Args[i+1]
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
			} else if strings.HasPrefix(arg, "--event=") {
				eventJSON = strings.TrimPrefix(arg, "--event=")
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
	case "capture":
		return runCaptureDAG(eventJSON, outputFormat, verbose)
	case "think":
		return runThinkDAG(model, outputFormat, verbose)
	case "dream":
		return runDreamDAG(model, outputFormat, verbose)
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

// runCaptureDAG (Stage 5-B) runs a capture-type DAG. Hook payloads
// arrive as JSON on stdin (when --event is empty) or as an --event
// argument; the seed is sense.hook_event with the parsed payload as
// attrs; spawn chain is maintain.capture → maintain.extract_insight
// (conditional, only when budget permits and the event looks like an
// edit/decision).
//
// Budget: DefaultCaptureBudget (100ms, 500 tokens, depth 3). The
// budget is tight by design — capture is per-hook and must not
// block. maintain.extract_insight self-modulates and skips its LLM
// call when budget is exhausted.
//
// Trace rows still emit to .cortex/db/dag_traces.jsonl per the
// existing dagtrace writer; capture-type rows are distinguished by
// the sense.hook_event qualified name on the seed.
func runCaptureDAG(eventJSON, outputFormat string, verbose bool) error {
	turnID := fmt.Sprintf("capture-%d", time.Now().UnixNano())

	if eventJSON == "" {
		bb, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		eventJSON = strings.TrimSpace(string(bb))
	}
	if eventJSON == "" {
		return fmt.Errorf("--type=capture requires a JSON event via --event=JSON or stdin")
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(eventJSON), &event); err != nil {
		return fmt.Errorf("parse event JSON: %w", err)
	}

	tw, twErr := dagtrace.NewWriter("")
	var traceCB dag.TraceCallback
	if twErr == nil {
		traceCB = tw.Callback(turnID)
	} else if verbose {
		fmt.Fprintf(os.Stderr, "[capture] trace writer init failed: %v\n", twErr)
	}

	reg := dag.NewRegistry()
	registerCaptureChain(reg, event)

	ex := dag.NewExecutor(reg, traceCB)
	// Capture is per-hook and must not block the AI tool that fired
	// the hook. Sequential walking guarantees the per-axis budget
	// semantic the V0 hook contract assumes.
	ex.SetSequential(true)

	seed := []dag.NodeSpec{{Function: dag.FuncSense, Op: "hook_event", ID: "h1"}}
	trace, err := ex.Run(context.Background(), turnID, seed, dag.DefaultCaptureBudget())
	if err != nil {
		return fmt.Errorf("run capture DAG: %w", err)
	}

	if outputFormat == "json" {
		payload := map[string]any{
			"turn_id":        trace.TurnID,
			"total_executed": trace.TotalExecuted,
			"exhausted":      trace.Exhausted,
			"exhausted_axis": trace.ExhaustedAxis,
			"final_budget":   trace.FinalBudget,
			"trace_ops":      qualifiedOps(trace.Entries),
		}
		bb, _ := json.MarshalIndent(payload, "", "  ")
		fmt.Println(string(bb))
	} else {
		fmt.Printf("=== capture DAG (%s) ===\n", turnID)
		fmt.Printf("Executed %d nodes; exhausted=%v\n", trace.TotalExecuted, trace.Exhausted)
		for _, e := range trace.Entries {
			parent := e.ParentID
			if parent == "" {
				parent = "(seed)"
			}
			fmt.Printf("  %s [%s] parent=%s ok=%v cost={lat=%d tok=%d}\n",
				e.NodeID, e.QualifiedName, parent, e.OK,
				e.CostConsumed.LatencyMS, e.CostConsumed.Tokens)
		}
		fmt.Printf("Final budget: %s\n", trace.FinalBudget)
	}
	return nil
}

// registerCaptureChain wires the capture-type 3-node chain:
//
//	sense.hook_event → maintain.capture → maintain.extract_insight
//
// Each handler is intentionally small — capture's budget is too tight
// for LLM calls. extract_insight is conditional: it only spawns when
// the event payload contains an "intent" field that names a write
// operation (Edit, Write, MultiEdit). For everything else we skip
// the LLM call and leave it to the daemon's think/dream cycles to
// pick up the slack.
func registerCaptureChain(reg *dag.Registry, event map[string]any) {
	// sense.hook_event seeds the chain with the event payload and
	// spawns maintain.capture.
	mustRegister(reg, dag.NodeSpec{
		Function:    dag.FuncSense,
		Op:          "hook_event",
		Description: "ingress: AI-tool hook payload arrives; spawns maintain.capture",
		Cost:        dag.Cost{LatencyMS: 5, Tokens: 0},
		Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			return dag.NodeResult{
				Out: map[string]any{"event": event},
				Spawn: []dag.NodeSpec{{
					Function: dag.FuncMaintain, Op: "capture", ID: "h2",
					Attrs: map[string]any{"event": event},
				}},
				CostConsumed: dag.Cost{LatencyMS: 5, Tokens: 0},
			}, nil
		},
	})

	// maintain.capture persists the event. V0: mechanical write to
	// the existing capture journal would happen here; for the DAG
	// surface we record that we processed the event and conditionally
	// spawn extract_insight.
	mustRegister(reg, dag.NodeSpec{
		Function:    dag.FuncMaintain,
		Op:          "capture",
		Description: "persist hook event to journal; conditionally spawn extract_insight",
		Cost:        dag.Cost{LatencyMS: 20, Tokens: 0},
		Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			result := dag.NodeResult{
				Out:          map[string]any{"captured": true},
				CostConsumed: dag.Cost{LatencyMS: 20, Tokens: 0},
			}
			// Only spawn extract_insight on edit-shaped events AND
			// when the remaining budget can plausibly afford it.
			// extract_insight's registered Cost is set conservatively
			// below; the executor's pre-spawn CanAfford check refuses
			// when the post-capture budget is too tight.
			ev, _ := in["event"].(map[string]any)
			if isEditEvent(ev) {
				result.Spawn = []dag.NodeSpec{{
					Function: dag.FuncMaintain, Op: "extract_insight", ID: "h3",
					Attrs: map[string]any{"content": eventContent(ev)},
				}}
			}
			return result, nil
		},
	})

	// maintain.extract_insight — registered with conservative Cost so
	// the pre-spawn budget check actually fires on tight budgets. The
	// real handler (when ops.RegisterDefaults wires a provider) does
	// the LLM call; for the capture-type chain we keep it as a stub
	// because the budget is too tight to safely run an LLM in the
	// hook path.
	mustRegister(reg, dag.NodeSpec{
		Function:    dag.FuncMaintain,
		Op:          "extract_insight",
		Description: "best-effort insight extraction from edit-shaped events (stub on tight budget)",
		Cost:        dag.Cost{LatencyMS: 50, Tokens: 50},
		Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			// Self-modulate: if budget is below 30ms, skip the
			// (would-be) LLM call entirely. Hook path stays bounded.
			if b.LatencyMS < 30 {
				return dag.NodeResult{
					Out:          map[string]any{"skipped": true, "reason": "budget_too_tight_for_llm"},
					CostConsumed: dag.Cost{LatencyMS: 1, Tokens: 0},
				}, nil
			}
			return dag.NodeResult{
				Out:          map[string]any{"insight_stub": true},
				CostConsumed: dag.Cost{LatencyMS: 30, Tokens: 30},
			}, nil
		},
	})
}

// isEditEvent reports whether the event payload looks like a tool
// invocation worth extracting insight from. Hook payloads from
// Claude Code / Cursor put the tool name under "tool_name"; we treat
// Edit / Write / MultiEdit as worthy and skip the rest.
func isEditEvent(event map[string]any) bool {
	if event == nil {
		return false
	}
	name, _ := event["tool_name"].(string)
	switch name {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		return true
	}
	return false
}

// eventContent extracts a substring of the event payload suitable for
// passing as insight-extraction input. Best-effort: walk the common
// hook payload shapes; fall back to a JSON dump.
func eventContent(event map[string]any) string {
	if event == nil {
		return ""
	}
	if s, ok := event["new_string"].(string); ok && s != "" {
		return s
	}
	if s, ok := event["content"].(string); ok && s != "" {
		return s
	}
	bb, _ := json.Marshal(event)
	return string(bb)
}

// runThinkDAG runs a think-type DAG via the CLI; daemon scheduler
// integration calls runThinkDAGSilent below (no stdout prints).
func runThinkDAG(model, outputFormat string, verbose bool) error {
	trace, err := runThinkDAGSilent(context.Background())
	if err != nil {
		return err
	}
	emitBackgroundSummary(outputFormat, "think", trace)
	return nil
}

// runDreamDAG runs a dream-type DAG via the CLI; daemon scheduler
// integration calls runDreamDAGSilent below (no stdout prints).
func runDreamDAG(model, outputFormat string, verbose bool) error {
	trace, err := runDreamDAGSilent(context.Background())
	if err != nil {
		return err
	}
	emitBackgroundSummary(outputFormat, "dream", trace)
	return nil
}

// runThinkDAGSilent is the daemon-scheduler entry point. Same chain
// as runThinkDAG but never writes to stdout — the caller decides
// what to do with the trace. Stage 5-C cleanup: the daemon's
// cognitiveTicker calls this every tick when activity is high,
// running the DAG-shaped think alongside the legacy MaybeThink so
// the trace rows accumulate even before think's V0 stub gets real
// model.predict_next / attend.warm_cache / value.rerank_session
// bodies.
//
// Budget: DefaultThinkBudget. V0 shape: single maintain.session_check
// seed.
func runThinkDAGSilent(ctx context.Context) (*dag.Trace, error) {
	turnID := fmt.Sprintf("think-%d", time.Now().UnixNano())
	tw, _ := dagtrace.NewWriter("")
	var traceCB dag.TraceCallback
	if tw != nil {
		traceCB = tw.Callback(turnID)
	}
	reg := dag.NewRegistry()
	mustRegister(reg, dag.NodeSpec{
		Function:    dag.FuncMaintain,
		Op:          "session_check",
		Description: "think: probe session topic weights + cache freshness (V0 stub)",
		Cost:        dag.Cost{LatencyMS: 10, Tokens: 0},
		Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			return dag.NodeResult{
				Out:          map[string]any{"checked": true, "stub": true},
				CostConsumed: dag.Cost{LatencyMS: 10, Tokens: 0},
			}, nil
		},
	})
	ex := dag.NewExecutor(reg, traceCB)
	return ex.Run(ctx, turnID,
		[]dag.NodeSpec{{Function: dag.FuncMaintain, Op: "session_check", ID: "t1"}},
		dag.DefaultThinkBudget())
}

// runDreamDAGSilent is the daemon-scheduler entry point. Same chain
// as runDreamDAG but never writes to stdout — the caller decides
// what to do with the trace. Stage 5-D cleanup: the daemon's
// cognitiveTicker calls this every tick when activity is low,
// running the DAG-shaped dream alongside the legacy MaybeDream so
// the trace rows accumulate even before dream's V0 stub gets real
// attend.sample / value.extract_insight / remember.embed_new
// bodies.
//
// Budget: DefaultDreamBudget. V0 shape: single maintain.idle_probe
// seed.
func runDreamDAGSilent(ctx context.Context) (*dag.Trace, error) {
	turnID := fmt.Sprintf("dream-%d", time.Now().UnixNano())
	tw, _ := dagtrace.NewWriter("")
	var traceCB dag.TraceCallback
	if tw != nil {
		traceCB = tw.Callback(turnID)
	}
	reg := dag.NewRegistry()
	mustRegister(reg, dag.NodeSpec{
		Function:    dag.FuncMaintain,
		Op:          "idle_probe",
		Description: "dream: idle-time substrate sampling (V0 stub)",
		Cost:        dag.Cost{LatencyMS: 10, Tokens: 0},
		Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			return dag.NodeResult{
				Out:          map[string]any{"probed": true, "stub": true},
				CostConsumed: dag.Cost{LatencyMS: 10, Tokens: 0},
			}, nil
		},
	})
	ex := dag.NewExecutor(reg, traceCB)
	return ex.Run(ctx, turnID,
		[]dag.NodeSpec{{Function: dag.FuncMaintain, Op: "idle_probe", ID: "d1"}},
		dag.DefaultDreamBudget())
}

// RunThinkDAGSilent is the exported daemon-facing wrapper for
// runThinkDAGSilent. Same shape; exported so daemon.go can call it
// without an underscore-prefixed name.
func RunThinkDAGSilent(ctx context.Context) (*dag.Trace, error) {
	return runThinkDAGSilent(ctx)
}

// RunDreamDAGSilent is the exported daemon-facing wrapper for
// runDreamDAGSilent.
func RunDreamDAGSilent(ctx context.Context) (*dag.Trace, error) {
	return runDreamDAGSilent(ctx)
}

// emitBackgroundSummary prints a small summary for the think/dream
// DAG types. Human format mirrors capture/turn; JSON envelope is
// compact since callers are daemon scheduler hooks not humans.
func emitBackgroundSummary(outputFormat, label string, trace *dag.Trace) {
	if outputFormat == "json" {
		payload := map[string]any{
			"type":           label,
			"turn_id":        trace.TurnID,
			"total_executed": trace.TotalExecuted,
			"exhausted":      trace.Exhausted,
			"final_budget":   trace.FinalBudget,
			"trace_ops":      qualifiedOps(trace.Entries),
		}
		bb, _ := json.MarshalIndent(payload, "", "  ")
		fmt.Println(string(bb))
		return
	}
	fmt.Printf("=== %s DAG (%s) ===\n", label, trace.TurnID)
	fmt.Printf("Executed %d nodes; exhausted=%v\n", trace.TotalExecuted, trace.Exhausted)
	for _, e := range trace.Entries {
		fmt.Printf("  %s [%s] ok=%v cost={lat=%d tok=%d}\n",
			e.NodeID, e.QualifiedName, e.OK,
			e.CostConsumed.LatencyMS, e.CostConsumed.Tokens)
	}
	fmt.Printf("Final budget: %s\n", trace.FinalBudget)
}

// mustRegister registers spec or panics — used inline for type-specific
// chain construction where a registration failure is a programming
// bug (caller controls the spec).
func mustRegister(reg *dag.Registry, spec dag.NodeSpec) {
	if err := reg.Register(spec); err != nil {
		panic(fmt.Sprintf("register %s: %v", spec.QualifiedName(), err))
	}
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
	return buildTurnRegistryWithConfig(prompt, model, workdir, traceCB, dagnode.CodingTurnConfig{
		Model:   model,
		Workdir: workdir,
		TraceCB: traceCB,
	})
}

// buildTurnRegistryWithConfig is the chain-build entry that lets a
// caller override the CodingTurnConfig — used by the REPL to inject
// its preconfigured CortexHarness via HarnessFactory so all REPL
// state (notifier, system prompt, shared cortex, budget overrides,
// dispatcher) flows through the DAG path instead of being bypassed
// by the inline harness construction the default config does.
func buildTurnRegistryWithConfig(prompt, model, workdir string, traceCB dag.TraceCallback, codingCfg dagnode.CodingTurnConfig) *dag.Registry {
	reg := dag.NewRegistry()
	if _, err := ops.RegisterDefaults(reg, ops.DefaultsConfig{}); err != nil {
		panic(fmt.Sprintf("ops.RegisterDefaults: %v", err))
	}
	chain := buildTurnChainWithConfig(prompt, model, workdir, reg, traceCB, codingCfg)
	for _, spec := range chain {
		if err := reg.Register(spec); err != nil {
			panic(fmt.Sprintf("chain register %s: %v", spec.QualifiedName(), err))
		}
	}
	return reg
}

// buildTurnChainWithConfig returns NodeSpecs whose handlers wrap the
// ops-package handlers with chain-wiring spawn logic. Each wrapper:
//
//   - reads the underlying op's Out
//   - constructs the next node's Attrs from that Out
//   - sets NodeResult.Spawn to the next node
//
// Nodes whose underlying op doesn't exist in the ops package
// (sense.prompt, decide.coding_turn, maintain.capture) get inline
// implementations.
//
// The caller-supplied CodingTurnConfig overrides the default
// coding_turn handler so the REPL (and any other caller) can inject
// a preconfigured CortexHarness via HarnessFactory + capture the
// full HarnessResult / LoopResult via ResultCallback. Callers that
// only need defaults route through buildTurnRegistry.
func buildTurnChainWithConfig(prompt, model, workdir string, reg *dag.Registry, traceCB dag.TraceCallback, codingCfg dagnode.CodingTurnConfig) []dag.NodeSpec {
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
	// Use the caller-supplied CodingTurnConfig so the REPL (and any
	// future caller) can inject a preconfigured CortexHarness via
	// HarnessFactory. Defaults still applied for fields the caller
	// left unset.
	if codingCfg.Model == "" {
		codingCfg.Model = model
	}
	if codingCfg.Workdir == "" {
		codingCfg.Workdir = workdir
	}
	if codingCfg.TraceCB == nil {
		codingCfg.TraceCB = traceCB
	}
	rawCodingTurnHandler := dagnode.NewCodingTurnHandler(codingCfg)
	// Wrap with the fetch-op re-attempt loop. After the LLM emits its
	// response, value.detect_unfamiliarity scans for the bleed pattern
	// (imports X but doesn't call X — the third-arm ABR prototype's
	// failure mode). When findings appear AND budget allows AND the
	// per-turn attempt cap isn't exhausted, remember.fetch_external
	// pulls API surface for each finding and the LLM is re-invoked
	// with the snippets prepended to the prompt.
	//
	// Bounded by maxReattempts (1 by default) so a model that
	// consistently emits bleed-pattern code can't unbounded-loop the
	// turn budget away.
	codingTurnHandler := wrapCodingTurnWithReattempt(rawCodingTurnHandler, codingCfg, traceCB)

	// readPrompt returns the runtime prompt for this chain step.
	// Cleanup (post-Stage-5/6): every step reads prompt from in["prompt"]
	// — the seed's Attrs set it once, then each spawn propagates it to
	// its child's Attrs. The closure-captured `prompt` argument stays
	// as a fallback so legacy callers that don't populate seed Attrs
	// keep working. Once all callers pass prompt via Attrs (they do
	// today — runTurnDAG + runREPLChainTurn both seed with
	// Attrs={prompt: prompt}), the closure can be dropped entirely
	// and the chain registry can be built once and reused across
	// turns (REPL multi-turn benefit).
	readPrompt := func(in map[string]any) string {
		if s, ok := in["prompt"].(string); ok && s != "" {
			return s
		}
		return prompt
	}

	// sense.prompt — captures the trigger prompt; spawns represent.embed.
	senseSpec := dag.NodeSpec{
		Function:    dag.FuncSense,
		Op:          "prompt",
		Description: "ingress: user prompt arrives; spawns represent.embed",
		Cost:        dag.Cost{LatencyMS: 5, Tokens: 0},
		Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			p := readPrompt(in)
			return dag.NodeResult{
				Out: map[string]any{"prompt": p},
				Spawn: []dag.NodeSpec{
					{
						Function: dag.FuncRepresent, Op: "embed", ID: "n2",
						Attrs: map[string]any{"text": p, "prompt": p},
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
			p := readPrompt(in)
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
					Attrs: map[string]any{
						"query_vector": vec, "limit": 10, "threshold": 0.0,
						"prompt": p,
					},
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
			p := readPrompt(in)
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
					Attrs: map[string]any{"query": p, "candidates": candidates, "prompt": p},
				},
			}
			return res, nil
		},
	}

	// attend.rerank — rerank candidates; spawns value.score on the top
	// candidate (audit K). value.score then spawns decide.inject.
	rerankInner := get("attend.rerank")
	rerankSpec := dag.NodeSpec{
		Function:    dag.FuncAttend,
		Op:          "rerank",
		Description: "rerank candidates by relevance; spawns value.score on the top candidate",
		Cost:        dag.Cost{LatencyMS: 800, Tokens: 250},
		Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			p := readPrompt(in)
			res, err := rerankInner(ctx, in, b)
			if err != nil {
				return res, err
			}
			reranked, _ := res.Out["reranked"].([]cognition.Result)
			topCandidate := ""
			if len(reranked) > 0 {
				topCandidate = reranked[0].Content
			}
			res.Spawn = []dag.NodeSpec{
				{
					Function: dag.FuncValue, Op: "score", ID: "n4a",
					Attrs: map[string]any{
						"query":      p,
						"candidate":  topCandidate,
						"candidates": reranked,
						"prompt":     p,
					},
				},
			}
			return res, nil
		},
	}

	// value.score — judge whether the top reranked candidate is
	// load-bearing for the query. Pure trace contribution: it does
	// not gate the downstream inject decision; the result is captured
	// for later analysis and the chain continues to
	// value.detect_contradiction.
	scoreInner := get("value.score")
	scoreSpec := dag.NodeSpec{
		Function:    dag.FuncValue,
		Op:          "score",
		Description: "judge load-bearing-ness of the top candidate; spawns value.detect_contradiction",
		Cost:        dag.Cost{LatencyMS: 9000, Tokens: 350},
		Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			p := readPrompt(in)
			reranked, _ := in["candidates"].([]cognition.Result)
			candidate, _ := in["candidate"].(string)
			nextSpawn := []dag.NodeSpec{
				{
					Function: dag.FuncValue, Op: "detect_contradiction", ID: "n4b",
					Attrs: map[string]any{
						"query":      p,
						"candidate":  candidate,
						"priors":     reranked,
						"candidates": reranked,
						"prompt":     p,
					},
				},
			}
			if candidate == "" {
				return dag.NodeResult{
					Out: map[string]any{
						"load_bearing": false,
						"confidence":   0.0,
						"why":          "no candidate to score",
						"fallback":     true,
					},
					Spawn:        nextSpawn,
					CostConsumed: dag.Cost{LatencyMS: 1, Tokens: 0},
				}, nil
			}
			res, err := scoreInner(ctx, in, b)
			if err != nil {
				res = dag.NodeResult{
					Out:          map[string]any{"load_bearing": false, "fallback": true, "error": err.Error()},
					CostConsumed: res.CostConsumed,
				}
			}
			res.Spawn = nextSpawn
			return res, nil
		},
	}

	// value.detect_contradiction — judge whether the top candidate
	// conflicts with any of the other reranked priors. Pure trace
	// contribution: result captured, chain continues to decide.inject.
	contradictionInner := get("value.detect_contradiction")
	contradictionSpec := dag.NodeSpec{
		Function:    dag.FuncValue,
		Op:          "detect_contradiction",
		Description: "flag conflicts between the top candidate and priors; spawns decide.inject",
		Cost:        dag.Cost{LatencyMS: 13000, Tokens: 550},
		Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			p := readPrompt(in)
			reranked, _ := in["candidates"].([]cognition.Result)
			candidate, _ := in["candidate"].(string)
			nextSpawn := []dag.NodeSpec{
				{
					Function: dag.FuncDecide, Op: "inject", ID: "n5",
					Attrs: map[string]any{"query": p, "candidates": reranked, "prompt": p},
				},
			}
			if candidate == "" {
				return dag.NodeResult{
					Out: map[string]any{
						"conflicts":      false,
						"conflicts_with": []string{},
						"why":            "no candidate to evaluate",
						"fallback":       true,
					},
					Spawn:        nextSpawn,
					CostConsumed: dag.Cost{LatencyMS: 1, Tokens: 0},
				}, nil
			}
			res, err := contradictionInner(ctx, in, b)
			if err != nil {
				res = dag.NodeResult{
					Out:          map[string]any{"conflicts": false, "fallback": true, "error": err.Error()},
					CostConsumed: res.CostConsumed,
				}
			}
			res.Spawn = nextSpawn
			return res, nil
		},
	}

	// decide.inject — decide inject/wait/queue; spawns model.predict_next
	// (which then spawns decide.coding_turn).
	injectInner := get("decide.inject")
	injectSpec := dag.NodeSpec{
		Function:    dag.FuncDecide,
		Op:          "inject",
		Description: "decide inject/wait/queue; spawns model.predict_next",
		Cost:        dag.Cost{LatencyMS: 700, Tokens: 150},
		Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			p := readPrompt(in)
			res, err := injectInner(ctx, in, b)
			if err != nil {
				return res, err
			}
			res.Spawn = []dag.NodeSpec{
				{
					Function: dag.FuncModel, Op: "predict_next", ID: "n5a",
					Attrs: map[string]any{"current": p, "prompt": p},
				},
			}
			return res, nil
		},
	}

	// model.predict_next — forward-simulation hint: top-3 likely
	// follow-up queries given the current prompt. Pure trace
	// contribution today; future slices can use the predictions to
	// warm caches before decide.coding_turn runs. Always continues
	// the chain to decide.coding_turn.
	predictNextInner := get("model.predict_next")
	predictNextSpec := dag.NodeSpec{
		Function:    dag.FuncModel,
		Op:          "predict_next",
		Description: "forward-simulate likely follow-ups; spawns decide.coding_turn",
		Cost:        dag.Cost{LatencyMS: 11000, Tokens: 350},
		Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			p := readPrompt(in)
			res, err := predictNextInner(ctx, in, b)
			if err != nil {
				res = dag.NodeResult{
					Out:          map[string]any{"predictions": []string{}, "count": 0, "fallback": true, "error": err.Error()},
					CostConsumed: res.CostConsumed,
				}
			}
			res.Spawn = []dag.NodeSpec{
				{
					Function: dag.FuncDecide, Op: "coding_turn", ID: "n6",
					Attrs: map[string]any{"prompt": p},
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
			// Prompt flows in via the chain's runtime Attrs propagation
			// (cleanup post-Stage-5/6). Fallback to closure for legacy
			// callers that seed without Attrs.
			if p, ok := in["prompt"].(string); !ok || p == "" {
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

	// maintain.extract_insight — extract insights; spawns
	// decide.should_capture (which gates whether maintain.capture fires).
	insightInner := get("maintain.extract_insight")
	insightSpec := dag.NodeSpec{
		Function:    dag.FuncMaintain,
		Op:          "extract_insight",
		Description: "extract insights from the turn output; spawns decide.should_capture",
		Cost:        dag.Cost{LatencyMS: 900, Tokens: 200},
		Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			res, err := insightInner(ctx, in, b)
			if err != nil {
				res = dag.NodeResult{
					Out:          map[string]any{"insights": []ops.Insight{}, "count": 0, "skipped": true},
					CostConsumed: res.CostConsumed,
				}
			}
			// Pass the response content (or a synthetic placeholder) down
			// so decide.should_capture has an `event` to evaluate.
			event, _ := in["content"].(string)
			if event == "" {
				event = "(empty turn content)"
			}
			res.Spawn = []dag.NodeSpec{
				{
					Function: dag.FuncDecide, Op: "should_capture", ID: "n7a",
					Attrs: map[string]any{"event": event},
				},
			}
			return res, nil
		},
	}

	// decide.should_capture — gate maintain.capture on a Y/N journal
	// decision. capture=false short-circuits the chain (no maintain.capture
	// spawn); capture=true continues to maintain.capture.
	shouldCaptureInner := get("decide.should_capture")
	shouldCaptureSpec := dag.NodeSpec{
		Function:    dag.FuncDecide,
		Op:          "should_capture",
		Description: "Y/N gate: should this turn be journaled? Conditionally spawns maintain.capture",
		Cost:        dag.Cost{LatencyMS: 16000, Tokens: 350},
		Handler: func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
			res, err := shouldCaptureInner(ctx, in, b)
			if err != nil {
				// On error, default to NOT capturing — safer than
				// capturing on broken metadata. Chain terminates here.
				res = dag.NodeResult{
					Out:          map[string]any{"capture": false, "tag": "none", "fallback": true, "error": err.Error()},
					CostConsumed: res.CostConsumed,
				}
				return res, nil
			}
			if capture, _ := res.Out["capture"].(bool); capture {
				res.Spawn = []dag.NodeSpec{
					{Function: dag.FuncMaintain, Op: "capture", ID: "n8"},
				}
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
		scoreSpec, contradictionSpec,
		injectSpec, predictNextSpec, codingTurnSpec, insightSpec,
		shouldCaptureSpec, captureSpec,
	}
}

// maxReattempts caps how many fetch-and-re-invoke cycles a single
// coding turn can run. 1 attempt is the default: if the first LLM
// pass shows the bleed pattern, fetch the missing API surface and
// retry once. Anything more risks ratholing — if a model still bleed-
// patterns after one fetch, it likely needs a different model not
// another fetch cycle.
const maxReattempts = 1

// wrapCodingTurnWithReattempt returns a handler that runs the inner
// coding_turn, scans its response with value.detect_unfamiliarity,
// and (if findings + budget + attempt cap permit) fetches snippets
// via remember.fetch_external and re-invokes the inner handler with
// the snippets prepended to the prompt. Re-attempts emit a synthetic
// dag.TraceEntry so the trace shows the loop firing.
//
// V0 scope: detection runs on the response text. Falsified positives
// possible (the model's response includes example code in a code
// fence that imports without calling). The eval target —
// sqlx-insert-user — uses code-as-response so the detector hits
// cleanly. Detecting on actually-written files is a future iteration
// once the harness exposes per-file new-content cleanly.
func wrapCodingTurnWithReattempt(inner dag.Handler, codingCfg dagnode.CodingTurnConfig, traceCB dag.TraceCallback) dag.Handler {
	detect := ops.NewDetectUnfamiliarityHandler(ops.DetectUnfamiliarityConfig{})
	fetch := ops.NewFetchExternalHandler(ops.FetchExternalConfig{})

	return func(ctx context.Context, in map[string]any, b dag.Budget) (dag.NodeResult, error) {
		res, err := inner(ctx, in, b)
		if err != nil {
			return res, err
		}
		response, _ := res.Out["response"].(string)
		if response == "" {
			return res, nil
		}

		attempts := 0
		for attempts < maxReattempts {
			detectRes, detectErr := detect(ctx, map[string]any{"code": response, "language": "go"}, b)
			if detectErr != nil {
				break
			}
			findings, _ := detectRes.Out["findings"].([]ops.UnfamiliarityFinding)
			if len(findings) == 0 {
				break // clean code; nothing to refetch
			}

			// Fetch each finding's API surface. Skip on fetch error
			// or empty snippet — best-effort augmentation.
			var snippets []string
			for _, f := range findings {
				if !b.CanAfford(dag.Cost{LatencyMS: 300, Tokens: 0}) {
					break // not enough budget for one more fetch
				}
				fetchRes, fetchErr := fetch(ctx, map[string]any{"package": f.Package, "language": "go"}, b)
				if fetchErr != nil {
					continue
				}
				snippet, _ := fetchRes.Out["snippet"].(string)
				if snippet == "" {
					continue
				}
				snippets = append(snippets, "Reference for "+f.Package+":\n"+snippet)
				// Account for the fetch cost on the turn budget. Read
				// it back from the fetch's CostConsumed so the budget
				// stays accurate.
				b.Consume(fetchRes.CostConsumed)
				res.CostConsumed.LatencyMS += fetchRes.CostConsumed.LatencyMS
				res.CostConsumed.Tokens += fetchRes.CostConsumed.Tokens
			}

			if len(snippets) == 0 {
				break // all fetches failed; no point retrying with no new context
			}

			// Emit a synthetic trace entry so the re-attempt is visible.
			parentNodeID := dag.NodeIDFromContext(ctx)
			if traceCB != nil {
				traceCB(dag.TraceEntry{
					NodeID:        fmt.Sprintf("%s-reattempt-%d", parentNodeID, attempts+1),
					ParentID:      parentNodeID,
					QualifiedName: "decide.reattempt",
					OK:            true,
					WallStart:     time.Now(),
					WallEnd:       time.Now(),
					Out: map[string]any{
						"findings_count": len(findings),
						"snippets_count": len(snippets),
						"attempt":        attempts + 1,
					},
				})
			}

			// Augment prompt and re-invoke.
			origPrompt, _ := in["prompt"].(string)
			augmented := origPrompt + "\n\n--- Reference examples for unfamiliar APIs ---\n\n" + strings.Join(snippets, "\n\n") + "\n\n--- end references ---\n\nReattempt the task following these examples."
			retryIn := map[string]any{}
			for k, v := range in {
				retryIn[k] = v
			}
			retryIn["prompt"] = augmented

			retryRes, retryErr := inner(ctx, retryIn, b)
			if retryErr != nil {
				break // keep the original result if retry errors
			}
			// Roll forward to the retry's response + accumulate cost.
			retryRes.CostConsumed.LatencyMS += res.CostConsumed.LatencyMS
			retryRes.CostConsumed.Tokens += res.CostConsumed.Tokens
			res = retryRes
			response, _ = res.Out["response"].(string)
			attempts++
		}
		return res, nil
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
  --type TYPE          DAG type: turn | eval | capture | think | dream
  --prompt TEXT        User prompt (for --type=turn)
  --scenario PATH      Scenario YAML (for --type=eval); see
                       test/evals/coding/ + test/evals/v2/
  --strategy NAME      Eval prompt strategy: cortex | baseline
                       (default: cortex; prepends CortexContext bullets
                       as "Hints: ..." prefix)
  --event JSON         Hook event payload (for --type=capture); omit to
                       read JSON from stdin
  --workdir DIR        Workdir for the turn/eval (seeded from scenario
                       SeedDir when set; auto-mktemp when omitted)
  --model NAME         LLM model id (--type=turn / --type=eval). Empty =
                       stub mode (run the DAG with no real LLM call).
  -o, --output FMT     Output format: human | json (default: human)
  -v, --verbose        Verbose trace output
  -h, --help           Show this help

Per-type shapes (docs/dag-build-plan.md + docs/dag-protocol.md):
  - --type=turn       Stage 2 chain (8 ops); Stage 3 act-op dispatch on
                      tool calls; Stage 4 parallelism + rollover +
                      calibration; loads .cortex/db/op_cost_hints.json
                      at construction
  - --type=eval       Stage 5-A: loads scenario, builds prompt, seeds
                      workdir from scenario SeedDir, runs the same
                      turn chain, runs scenario.Verify post-hoc
  - --type=capture    Stage 5-B: hook payload → sense.hook_event →
                      maintain.capture → maintain.extract_insight
                      (conditional on edit-shaped events + remaining
                      budget). 100ms latency budget; sequential
  - --type=think      Stage 5-C: V0 stub — single maintain.session_check
                      seed under DefaultThinkBudget. Daemon scheduler
                      integration (mid-session timer, inverse activity
                      budget) lands when the daemon picks up DAG types
  - --type=dream      Stage 5-D: V0 stub — single maintain.idle_probe
                      seed under DefaultDreamBudget. Daemon scheduler
                      integration (idle-time trigger, growing budget)
                      lands alongside think

Examples:
  cortex run --type=turn --prompt "refactor the auth module"
  cortex run --type=eval --scenario=test/evals/coding/sqlx-insert-user.yaml
  echo '{"tool_name":"Edit","file_path":"foo.go"}' | cortex run --type=capture
  cortex run --type=think
  cortex run --type=dream

See docs/dag-protocol.md for the protocol semantics.`)
}
