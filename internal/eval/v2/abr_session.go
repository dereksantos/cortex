package eval

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/harness"
	"github.com/dereksantos/cortex/pkg/llm"
)

// ABRSessionOptions controls a single ABR measurement session. The
// session is replayed twice (Fast pass + Full pass) against fresh REPL
// invocations, then per-turn responses are judged independently and
// the ABR = score(fast) / score(full) ratio is computed at each turn
// index — recovering the trajectory shape the deleted cognition
// session evaluator measured under the old harness.
type ABRSessionOptions struct {
	// ScenarioID names the prompt sequence; lands on every emitted cell.
	ScenarioID string

	// REPLBinary is the cortex CLI to spawn. Typically "bin/cortex".
	REPLBinary string

	// Model is passed to the REPL via --model.
	Model string

	// Workdir is the parent dir; each pass gets a subdir (<Workdir>/fast,
	// <Workdir>/full) so per-eval Cortex stores stay isolated.
	Workdir string

	// Prompts are turn inputs in order. One line per prompt; embedded
	// newlines are not supported (the REPL stdin scanner splits on \n).
	Prompts []string

	// JudgeCriteria is the free-form rubric passed to
	// ScoreWithJudgeCriteria. Should reference what counts as a good
	// answer in the scenario's domain.
	JudgeCriteria string

	// Judge scores each turn's response. Required.
	Judge llm.Provider

	// Persister, when non-nil, gets one CellResult per (turn, strategy)
	// via PersistCell — rides the existing journal + cell_results.jsonl
	// + cell_results SQLite fan-out.
	Persister *Persister

	// Provider names the CellResult.Provider field (e.g. "openrouter").
	// Must be one of the existing ProviderXxx constants.
	Provider string

	// CortexVersion lands on every cortex-* cell. Required when
	// Persister is non-nil (Validate would reject the rows otherwise).
	CortexVersion string

	// PerPassTimeout caps each REPL invocation. Zero → no timeout.
	PerPassTimeout time.Duration
}

// ABRTurn is one (Fast, Full) pair at a given turn index.
type ABRTurn struct {
	Index        int     `json:"index"`
	Prompt       string  `json:"prompt"`
	FastResponse string  `json:"fast_response"`
	FullResponse string  `json:"full_response"`
	FastScore    float64 `json:"fast_score"`
	FullScore    float64 `json:"full_score"`
	// FastInjected / FullInjected are the cortex_search tool's
	// observedInjectedTokens counter for each pass — the proxy for
	// "how much retrieved context did the agent actually see this
	// turn." 0 in BOTH passes means the agent didn't call
	// cortex_search successfully, which is the key signal for "ABR
	// is 1.0 because nothing was retrieved" vs "ABR is 1.0 because
	// retrieval converged."
	FastInjected int `json:"fast_injected_tokens"`
	FullInjected int `json:"full_injected_tokens"`
	// ABR is FastScore/FullScore. Zero when FullScore == 0; analytics
	// should filter on FullScore > 0 before averaging to avoid mis-
	// counting "Full also failed" turns as ABR=0.
	ABR float64 `json:"abr"`
}

// ABRSessionResult is the summary produced by RunABRSession.
type ABRSessionResult struct {
	SessionID        string    `json:"session_id"`
	ScenarioID       string    `json:"scenario_id"`
	Turns            []ABRTurn `json:"turns"`
	MeanABR          float64   `json:"mean_abr"`            // mean over turns where FullScore > 0
	MeanFastScore    float64   `json:"mean_fast_score"`     // simple mean across turns
	MeanFullScore    float64   `json:"mean_full_score"`     // simple mean across turns
	TurnsScored      int       `json:"turns_scored"`        // turns where both responses + judge succeeded
	TurnsFullNonzero int       `json:"turns_full_nonzero"`  // denominator for MeanABR
	FastPassExitCode int       `json:"fast_pass_exit_code"` // 0 if REPL exited clean
	FullPassExitCode int       `json:"full_pass_exit_code"`
	FastSessionPath  string    `json:"fast_session_path"`
	FullSessionPath  string    `json:"full_session_path"`
}

// RunABRSession spawns the REPL twice over the same prompt list (once
// with cortex_search defaulted to Fast, once to Full), parses each
// pass's session.jsonl for per-turn responses, judges each response,
// and emits per-turn CellResults under the cortex-fast / cortex-full
// strategy split. Returns the trajectory summary.
//
// The two passes are isolated in separate workdir subtrees so neither
// pass's per-eval Cortex store contaminates the other's. Think
// accumulates across turns within a pass (one REPL process, one
// long-lived cognition.Cortex instance), which is exactly the session
// trajectory ABR was designed to measure.
func RunABRSession(ctx context.Context, opts ABRSessionOptions) (ABRSessionResult, error) {
	if err := opts.validate(); err != nil {
		return ABRSessionResult{}, err
	}

	sessionID := fmt.Sprintf("abr-%s", time.Now().UTC().Format("20060102T150405Z"))

	fastDir := filepath.Join(opts.Workdir, "fast")
	fullDir := filepath.Join(opts.Workdir, "full")
	for _, d := range []string{fastDir, fullDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return ABRSessionResult{}, fmt.Errorf("create pass dir %s: %w", d, err)
		}
	}

	fastTurns, fastExit, fastPath, err := runREPLPass(ctx, opts, fastDir, "fast")
	if err != nil {
		return ABRSessionResult{}, fmt.Errorf("fast pass: %w", err)
	}
	fullTurns, fullExit, fullPath, err := runREPLPass(ctx, opts, fullDir, "full")
	if err != nil {
		return ABRSessionResult{}, fmt.Errorf("full pass: %w", err)
	}

	result := ABRSessionResult{
		SessionID:        sessionID,
		ScenarioID:       opts.ScenarioID,
		FastPassExitCode: fastExit,
		FullPassExitCode: fullExit,
		FastSessionPath:  fastPath,
		FullSessionPath:  fullPath,
	}

	// Walk paired turns. If passes drifted in length (e.g. one model
	// hit a budget and stopped early), trim to the shorter — judging
	// an unmatched turn against nothing would just produce noise.
	n := len(fastTurns)
	if len(fullTurns) < n {
		n = len(fullTurns)
	}

	var sumABR, sumFast, sumFull float64
	for i := 0; i < n; i++ {
		fastRow := fastTurns[i]
		fullRow := fullTurns[i]

		fastScore, err := judgeQuality(ctx, opts, fastRow)
		if err != nil {
			return result, fmt.Errorf("judge fast turn %d: %w", i, err)
		}
		fullScore, err := judgeQuality(ctx, opts, fullRow)
		if err != nil {
			return result, fmt.Errorf("judge full turn %d: %w", i, err)
		}

		abr := 0.0
		if fullScore > 0 {
			abr = fastScore / fullScore
			result.TurnsFullNonzero++
			sumABR += abr
		}

		result.Turns = append(result.Turns, ABRTurn{
			Index:        i,
			Prompt:       fastRow.UserMessage,
			FastResponse: fastRow.FinalText,
			FullResponse: fullRow.FinalText,
			FastScore:    fastScore,
			FullScore:    fullScore,
			FastInjected: fastRow.InjectedContextTokens,
			FullInjected: fullRow.InjectedContextTokens,
			ABR:          abr,
		})
		result.TurnsScored++
		sumFast += fastScore
		sumFull += fullScore

		if opts.Persister != nil {
			if err := emitCell(ctx, opts, sessionID, i, StrategyCortexFast, fastRow, fastScore); err != nil {
				return result, fmt.Errorf("persist fast cell turn %d: %w", i, err)
			}
			if err := emitCell(ctx, opts, sessionID, i, StrategyCortexFull, fullRow, fullScore); err != nil {
				return result, fmt.Errorf("persist full cell turn %d: %w", i, err)
			}
		}
	}

	if result.TurnsScored > 0 {
		result.MeanFastScore = sumFast / float64(result.TurnsScored)
		result.MeanFullScore = sumFull / float64(result.TurnsScored)
	}
	if result.TurnsFullNonzero > 0 {
		result.MeanABR = sumABR / float64(result.TurnsFullNonzero)
	}
	return result, nil
}

func (o *ABRSessionOptions) validate() error {
	if o.ScenarioID == "" {
		return fmt.Errorf("ScenarioID required")
	}
	if o.REPLBinary == "" {
		return fmt.Errorf("REPLBinary required")
	}
	if o.Model == "" {
		return fmt.Errorf("model required")
	}
	if o.Workdir == "" {
		return fmt.Errorf("workdir required")
	}
	if len(o.Prompts) == 0 {
		return fmt.Errorf("prompts must be non-empty")
	}
	for i, p := range o.Prompts {
		if strings.ContainsRune(p, '\n') {
			return fmt.Errorf("prompt %d contains a newline; REPL stdin scanner splits on \\n", i)
		}
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("prompt %d is empty", i)
		}
	}
	if o.JudgeCriteria == "" {
		return fmt.Errorf("JudgeCriteria required (free-form rubric for ScoreWithJudgeCriteria)")
	}
	if o.Judge == nil {
		return fmt.Errorf("judge required")
	}
	if o.Persister != nil && o.CortexVersion == "" {
		return fmt.Errorf("CortexVersion required when Persister is set (CellResult.Validate rejects cortex-* rows without it)")
	}
	return nil
}

// runREPLPass spawns one REPL subprocess, pipes Prompts to stdin,
// waits for completion, and returns the parsed turn rows from the
// resulting session.jsonl plus the exit code. The strategy string
// becomes CORTEX_SEARCH_DEFAULT_MODE for this pass — that's the
// invisible knob that flips Fast vs Full without touching prompts.
func runREPLPass(parentCtx context.Context, opts ABRSessionOptions, workdir, strategy string) ([]replTurnRow, int, string, error) {
	ctx := parentCtx
	var cancel context.CancelFunc
	if opts.PerPassTimeout > 0 {
		ctx, cancel = context.WithTimeout(parentCtx, opts.PerPassTimeout)
		defer cancel()
	}

	// Bare `cortex` (no subcommand) enters the interactive REPL. See
	// `cortex --help`: "Bare cortex with no subcommand enters an
	// interactive coding REPL in the current directory."
	//
	// --full-tools is mandatory for ABR: when routed to local Ollama
	// the REPL auto-drops cortex_search from the tool registry for
	// tiny models (repl.go:899 SetMinimalTools). Without cortex_search
	// the agent literally cannot use the retrieval pipeline this
	// adapter is trying to measure. The flag is a no-op for OpenRouter
	// / Anthropic-direct paths where the auto-drop never fires.
	args := []string{"--workdir", workdir, "--model", opts.Model, "--full-tools"}
	cmd := exec.CommandContext(ctx, opts.REPLBinary, args...)

	// Inherit parent env so OPENROUTER_API_KEY, ANTHROPIC_API_KEY, PATH
	// etc. propagate. The default-mode override is appended last so it
	// wins over anything the parent shell might have set.
	env := os.Environ()
	env = append(env, fmt.Sprintf("%s=%s", harness.CortexSearchDefaultModeEnv, strategy))
	cmd.Env = env

	cmd.Stdin = strings.NewReader(strings.Join(opts.Prompts, "\n") + "\n")
	// Discard REPL stdout/stderr — the session.jsonl is the data path,
	// the human-facing prints are noise from this adapter's POV. The
	// REPL prints "session saved → <path>" on exit but we don't need
	// it (we glob for the session dir under workdir below).
	cmd.Stdout = os.Stderr // mirror so the user sees progress
	cmd.Stderr = os.Stderr

	runErr := cmd.Run()
	exitCode := 0
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			return nil, -1, "", fmt.Errorf("spawn repl: %w", runErr)
		}
	}

	sessionPath, err := latestSessionJSONL(workdir)
	if err != nil {
		return nil, exitCode, "", fmt.Errorf("locate session.jsonl: %w", err)
	}

	rows, err := readSessionJSONL(sessionPath)
	if err != nil {
		return nil, exitCode, sessionPath, fmt.Errorf("parse session.jsonl: %w", err)
	}
	return rows, exitCode, sessionPath, nil
}

// replTurnRow mirrors cmd/cortex/commands/repl.go's turnRow shape for
// the fields this adapter uses. We deliberately don't import the
// upstream type — staying decoupled keeps the cmd package from
// becoming a public surface for internal/eval.
type replTurnRow struct {
	Turn                  int     `json:"turn"`
	SessionID             string  `json:"session_id"`
	UserMessage           string  `json:"user_message"`
	FinalText             string  `json:"final_text"`
	TokensIn              int     `json:"tokens_in"`
	TokensOut             int     `json:"tokens_out"`
	InjectedContextTokens int     `json:"injected_context_tokens"`
	CostUSD               float64 `json:"cost_usd"`
	LatencyMs             int64   `json:"latency_ms"`
}

func latestSessionJSONL(workdir string) (string, error) {
	dir := filepath.Join(workdir, ".cortex", "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) == 0 {
		return "", fmt.Errorf("no session dirs under %s", dir)
	}
	// Session dirs are UTC timestamps `20060102T150405Z`, lexicographic
	// sort = chronological. Take the newest.
	sort.Strings(dirs)
	return filepath.Join(dir, dirs[len(dirs)-1], "session.jsonl"), nil
}

func readSessionJSONL(path string) ([]replTurnRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var rows []replTurnRow
	scanner := bufio.NewScanner(f)
	// turnRow records can carry full retry transcripts; bump buffer
	// past the 64 KiB default so we don't choke on long verifier
	// outputs.
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var row replTurnRow
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, fmt.Errorf("decode line: %w", err)
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

func judgeQuality(ctx context.Context, opts ABRSessionOptions, row replTurnRow) (float64, error) {
	if strings.TrimSpace(row.FinalText) == "" {
		return 0, nil // no response = zero quality, no judge call
	}
	jr, err := ScoreWithJudgeCriteria(ctx, row.FinalText, row.UserMessage, opts.JudgeCriteria, opts.Judge)
	if err != nil {
		return 0, err
	}
	return CompositeQuality(jr), nil
}

func emitCell(ctx context.Context, opts ABRSessionOptions, sessionID string, turnIdx int, strategy string, row replTurnRow, score float64) error {
	turn := turnIdx // local copy so &turn is valid past the call
	cell := &CellResult{
		SchemaVersion:         CellResultSchemaVersion,
		RunID:                 fmt.Sprintf("%s-%s-t%d", sessionID, strategy, turnIdx),
		Timestamp:             time.Now().UTC().Format(time.RFC3339),
		ScenarioID:            opts.ScenarioID,
		SessionID:             sessionID,
		TurnIndex:             &turn,
		Harness:               HarnessCortex,
		Provider:              opts.Provider,
		Model:                 opts.Model,
		ContextStrategy:       strategy,
		CortexVersion:         opts.CortexVersion,
		Temperature:           0,
		TokensIn:              row.TokensIn,
		TokensOut:             row.TokensOut,
		InjectedContextTokens: row.InjectedContextTokens,
		CostUSD:               row.CostUSD,
		LatencyMs:             row.LatencyMs,
		TaskSuccess:           score >= 0.5, // soft threshold; the real signal is the score, captured in Notes
		TaskSuccessCriterion:  CriterionJudgeLLM,
		Notes:                 fmt.Sprintf(`{"quality_score":%.4f}`, score),
	}
	return opts.Persister.PersistCell(ctx, cell)
}
