package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// sink.go is the structured record of a run. results.jsonl and
// predictions.jsonl are appended and fsynced as each instance finishes —
// never reconstructed at the end — so a run killed halfway still leaves a
// valid record of everything that finished.

// Row is one instance's result, written to results.jsonl.
type Row struct {
	InstanceID   string `json:"instance_id"`
	Resolved     bool   `json:"resolved"`
	FailureClass string `json:"failure_class"`
	TurnEnd      string `json:"turn_end"`
	EvalStatus   string `json:"eval_status"`

	TokensIn        int `json:"tokens_in"`
	TokensOut       int `json:"tokens_out"`
	ReasoningTokens int `json:"reasoning_tokens"`
	// CostUSD is cortex's own total of the per-call usage.cost OpenRouter
	// returned (from the headless metrics row). AccountCostUSD is the
	// OpenRouter key's usage delta across this instance — independent of
	// cortex's accounting, and the number the spend cap is enforced on.
	CostUSD        float64 `json:"cost_usd"`
	AccountCostUSD float64 `json:"account_cost_usd"`

	WallMs  int64 `json:"wall_ms"`
	SetupMs int64 `json:"setup_ms"`
	AgentMs int64 `json:"agent_ms"`
	EvalMs  int64 `json:"eval_ms"`

	ToolCalls     int            `json:"tool_calls"`
	MutatingCalls int            `json:"mutating_calls"`
	ToolCounts    map[string]int `json:"tool_counts,omitempty"`
	AgentTurns    int            `json:"agent_turns"`
	MetricsFound  bool           `json:"metrics_found"`

	PatchBytes int `json:"patch_bytes"`
	PatchFiles int `json:"patch_files"`

	SessionID     string `json:"session_id,omitempty"`
	TrajPath      string `json:"traj_path,omitempty"`
	PatchPath     string `json:"patch_path,omitempty"`
	LogDir        string `json:"log_dir"`
	EvalReport    string `json:"eval_report,omitempty"`
	BaseCommitOK  bool   `json:"base_commit_ok"`
	ImagePulledMs int64  `json:"image_pull_ms"`
	// SecretRedacted: the API key was found in an artifact and replaced.
	// Should never be true; if it is, that is a finding.
	SecretRedacted bool   `json:"secret_redacted"`
	Error          string `json:"error,omitempty"`
}

// Prediction is one line of the official predictions.jsonl format.
type Prediction struct {
	InstanceID      string `json:"instance_id"`
	ModelNameOrPath string `json:"model_name_or_path"`
	ModelPatch      string `json:"model_patch"`
}

// JSONLSink appends JSON values to a file, fsyncing each one.
type JSONLSink struct{ f *os.File }

// NewJSONLSink opens path for append, creating it. Appending (not
// truncating) lets a resumed run keep what an interrupted one recorded.
func NewJSONLSink(path string) (*JSONLSink, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create dir for %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", path, err)
	}
	return &JSONLSink{f: f}, nil
}

// Append writes one value as a line and fsyncs it.
func (s *JSONLSink) Append(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("failed to encode row: %w", err)
	}
	if _, err := s.f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("failed to append row: %w", err)
	}
	if err := s.f.Sync(); err != nil {
		return fmt.Errorf("failed to flush row: %w", err)
	}
	return nil
}

func (s *JSONLSink) Close() error { return s.f.Close() }

// RunMeta is run.json: everything needed to say what produced these numbers.
type RunMeta struct {
	RunID     string `json:"run_id"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at,omitempty"`

	ModelNameOrPath string         `json:"model_name_or_path"`
	Model           string         `json:"model"`
	StudyModel      string         `json:"study_model"`
	Provider        map[string]any `json:"provider_routing"`
	Window          int            `json:"window"`
	Temperature     float64        `json:"temperature"`
	Endpoint        string         `json:"endpoint"`
	WorkspaceConfig map[string]any `json:"workspace_config"`
	// Auth names where the key came from, never the key.
	Auth string `json:"auth"`

	CortexCommit    string `json:"cortex_commit"`
	CortexDirty     bool   `json:"cortex_dirty"`
	CortexBin       string `json:"cortex_bin"`
	SwebenchVersion string `json:"swebench_version"`
	Dataset         string `json:"dataset"`
	DatasetRevision string `json:"dataset_revision"`
	DatasetFile     string `json:"dataset_file"`
	Split           string `json:"split"`

	Seed      string   `json:"seed"`
	Instances []string `json:"instances"`
	// SkippedForBudget lists selected instances not attempted because the
	// next one could have pushed spend past --budget.
	SkippedForBudget []string `json:"skipped_for_budget,omitempty"`

	BudgetUSD         float64 `json:"budget_usd"`
	InstanceCapUSD    float64 `json:"instance_cap_usd"`
	AccountUsageStart float64 `json:"account_usage_start"`
	AccountUsageEnd   float64 `json:"account_usage_end,omitempty"`

	TurnTimeout string `json:"turn_timeout"`
	EvalTimeout string `json:"eval_timeout"`

	Host         string `json:"host"`
	HostOS       string `json:"host_os"`
	HostArch     string `json:"host_arch"`
	DockerServer string `json:"docker_server"`
	// Emulated: instance images are linux/amd64; on an arm64 docker server
	// they run under emulation (Rosetta/qemu), which slows test execution.
	Emulated bool `json:"emulated"`

	ResultsPath     string `json:"results_path"`
	PredictionsPath string `json:"predictions_path"`
}

// WriteRunMeta writes (or rewrites) run.json.
func WriteRunMeta(path string, m RunMeta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode run metadata: %w", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}

// Summary is the end-of-run aggregate.
type Summary struct {
	Total, Resolved int
	ClassCounts     map[string]int
	TokensIn        int
	TokensOut       int
	CostUSD         float64
	AccountCostUSD  float64
	WallMs          int64
	MedianWallMs    int64
	MedianCostUSD   float64
	// AgentRows counts rows where the agent actually ran (not env_error);
	// per-instance means below are over these rows.
	AgentRows int
}

// Summarize aggregates rows.
func Summarize(rows []Row) Summary {
	s := Summary{Total: len(rows), ClassCounts: map[string]int{}}
	var walls []int64
	var costs []float64
	for _, r := range rows {
		if r.Resolved {
			s.Resolved++
		} else {
			s.ClassCounts[r.FailureClass]++
		}
		s.TokensIn += r.TokensIn
		s.TokensOut += r.TokensOut
		s.CostUSD += r.CostUSD
		s.AccountCostUSD += r.AccountCostUSD
		s.WallMs += r.WallMs
		if r.FailureClass != ClassEnv {
			s.AgentRows++
			walls = append(walls, r.WallMs)
			costs = append(costs, r.AccountCostUSD)
		}
	}
	sort.Slice(walls, func(i, j int) bool { return walls[i] < walls[j] })
	sort.Float64s(costs)
	if len(walls) > 0 {
		s.MedianWallMs = walls[(len(walls)-1)/2]
		s.MedianCostUSD = costs[(len(costs)-1)/2]
	}
	return s
}

var classOrder = []string{ClassWrongPatch, ClassApplyFailed, ClassEmptyPatch, ClassChatMode,
	ClassTimeout, ClassBudget, ClassError, ClassEvalError, ClassEnv}

// PrintSummary renders the end-of-run report.
func PrintSummary(w io.Writer, m RunMeta, rows []Row) {
	s := Summarize(rows)
	bar := strings.Repeat("=", 96)
	fmt.Fprintf(w, "\n%s\nSWE-bench Verified — run %s\n%s @ %v | dataset %s@%s | seed %q\n%s\n",
		bar, m.RunID, m.Model, m.Provider["order"], m.Dataset, shortSHA(m.DatasetRevision), m.Seed, bar)
	fmt.Fprintf(w, "  %-36s %-8s %-18s %6s %9s %8s %7s %7s\n",
		"instance", "resolved", "class", "tools", "tok_in", "tok_out", "$", "wall")
	for _, r := range rows {
		class := r.FailureClass
		if class == "" {
			class = "-"
		}
		fmt.Fprintf(w, "  %-36s %-8v %-18s %6d %9d %8d %7.3f %6.0fs\n",
			r.InstanceID, r.Resolved, class, r.ToolCalls, r.TokensIn, r.TokensOut,
			r.AccountCostUSD, float64(r.WallMs)/1000)
	}
	fmt.Fprintf(w, "%s\n", strings.Repeat("-", 96))
	pct := 0.0
	if s.Total > 0 {
		pct = 100 * float64(s.Resolved) / float64(s.Total)
	}
	fmt.Fprintf(w, "resolved      %d/%d (%.1f%%)\n", s.Resolved, s.Total, pct)
	for _, c := range classOrder {
		if n := s.ClassCounts[c]; n > 0 {
			fmt.Fprintf(w, "  %-20s %d\n", c, n)
		}
	}
	fmt.Fprintf(w, "tokens        %d in / %d out\n", s.TokensIn, s.TokensOut)
	fmt.Fprintf(w, "cost          $%.4f account delta ($%.4f cortex-reported); median $%.4f/instance\n",
		s.AccountCostUSD, s.CostUSD, s.MedianCostUSD)
	fmt.Fprintf(w, "wall clock    %s total, %s median\n",
		(time.Duration(s.WallMs) * time.Millisecond).Round(time.Second),
		(time.Duration(s.MedianWallMs) * time.Millisecond).Round(time.Second))
	if len(m.SkippedForBudget) > 0 {
		fmt.Fprintf(w, "skipped       %d instance(s) for budget: %s\n", len(m.SkippedForBudget), strings.Join(m.SkippedForBudget, ", "))
	}
	fmt.Fprintf(w, "results       %s\npredictions   %s\n", m.ResultsPath, m.PredictionsPath)
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
