//go:build !windows

package paired

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	eval "github.com/dereksantos/cortex/internal/eval/v2"
)

// Harness runs one (Condition, scenario) pair end-to-end in workdir
// and returns a structured outcome. The default implementation
// (DefaultHarness) wraps eval.CortexHarness + ScoreGoLFrames; tests
// inject fakes so the package is exercisable without LLM calls.
type Harness interface {
	Run(ctx context.Context, c Condition, scenario *eval.CodingScenario, workdir string) (Outcome, error)
}

// Outcome is the harness-level snapshot the runner converts to a Result.
type Outcome struct {
	TokensIn   int
	TokensOut  int
	CostUSD    float64
	LatencyMs  int64
	AgentTurns int
	Frames     eval.FrameDiffResult
	JudgePass  bool
	Notes      string
}

// Run executes the scenario across all conditions, copying the seed
// into a fresh workdir per condition, scoring with frame diff, and
// writing one JSONL row per condition to outPath. The slice of
// Results is also returned so callers can inspect in-process.
//
// outPath may be empty: in that case nothing is written to disk and
// the caller is responsible for persisting results elsewhere (this is
// how the test exercises the runner without polluting the repo).
//
// Errors from a single condition are recorded on its Result.Err and
// do not abort the remaining conditions — paired comparison stays
// meaningful even when one row degrades to a hard failure.
func Run(ctx context.Context, scenarioPath string, conditions []Condition, h Harness, outPath string) ([]Result, error) {
	if h == nil {
		return nil, errors.New("paired.Run: harness is required")
	}
	if len(conditions) == 0 {
		return nil, errors.New("paired.Run: at least one condition is required")
	}
	scenario, err := eval.LoadCodingScenario(scenarioPath)
	if err != nil {
		return nil, fmt.Errorf("load scenario: %w", err)
	}

	root, err := os.MkdirTemp("", "cortex-paired-"+sanitize(scenario.ID)+"-*")
	if err != nil {
		return nil, fmt.Errorf("mkdir loop root: %w", err)
	}

	results := make([]Result, 0, len(conditions))
	for i, c := range conditions {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		workdir := filepath.Join(root, fmt.Sprintf("%s-%02d", sanitize(c.Name), i+1))
		if err := os.MkdirAll(workdir, 0o755); err != nil {
			results = append(results, errResult(scenario, c, fmt.Errorf("mkdir workdir: %w", err)))
			continue
		}
		if err := copyDir(scenario.SeedDir, workdir); err != nil {
			results = append(results, errResult(scenario, c, fmt.Errorf("copy seed: %w", err)))
			continue
		}

		start := time.Now()
		out, runErr := h.Run(ctx, c, scenario, workdir)
		latency := time.Since(start).Milliseconds()
		// Trust the harness's latency when present; fall back to our
		// outer timing for harnesses that can't observe it.
		if out.LatencyMs == 0 {
			out.LatencyMs = latency
		}

		r := Result{
			SchemaVersion: ResultSchemaVersion,
			Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
			ScenarioID:    scenario.ID,
			Condition:     c.Name,
			Model:         c.Model,
			UseCortex:     c.UseCortex,
			TokensIn:      out.TokensIn,
			TokensOut:     out.TokensOut,
			CostUSD:       out.CostUSD,
			LatencyMs:     out.LatencyMs,
			AgentTurns:    out.AgentTurns,
			FramesPassed:  out.Frames.Passed,
			FramesFailed:  out.Frames.Failed,
			BuildOK:       out.Frames.BuildOK,
			Pass:          out.Frames.BuildOK && out.Frames.AllPassed && out.JudgePass,
			Notes:         out.Notes,
		}
		if c.Endpoint != nil {
			r.Endpoint = c.Endpoint.Name
		}
		if runErr != nil {
			r.Err = runErr.Error()
		}
		results = append(results, r)
	}

	if outPath != "" {
		if err := writeJSONL(outPath, results); err != nil {
			return results, fmt.Errorf("write %s: %w", outPath, err)
		}
	}
	return results, nil
}

// writeJSONL serializes results one row per line. Creates parent dirs.
func writeJSONL(path string, rows []Result) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for i := range rows {
		if err := enc.Encode(&rows[i]); err != nil {
			return err
		}
	}
	return nil
}

func errResult(s *eval.CodingScenario, c Condition, err error) Result {
	r := Result{
		SchemaVersion: ResultSchemaVersion,
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
		ScenarioID:    s.ID,
		Condition:     c.Name,
		Model:         c.Model,
		UseCortex:     c.UseCortex,
		Err:           err.Error(),
	}
	if c.Endpoint != nil {
		r.Endpoint = c.Endpoint.Name
	}
	return r
}

// copyDir is the package-local clone of internal/eval/v2.copyDir. It
// preserves file modes but skips .git and .cortex (they're
// regenerated per-attempt). Duplicated to keep paired importable
// without forcing eval to export the helper.
func copyDir(src, dst string) error {
	srcAbs, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcAbs, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Skip per-eval state directories — they accumulate cross-attempt
		// in the source and would contaminate a fresh paired-run condition.
		first := strings.SplitN(rel, string(filepath.Separator), 2)[0]
		if first == ".git" || first == ".cortex" {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			return os.MkdirAll(target, info.Mode().Perm())
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

func sanitize(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			out = append(out, c)
		case c == '-' || c == '_':
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}

// SortByCost returns rows sorted by cost ascending — the natural read
// order for inspecting the cost-quality Pareto frontier.
func SortByCost(rows []Result) []Result {
	cp := make([]Result, len(rows))
	copy(cp, rows)
	sort.SliceStable(cp, func(i, j int) bool { return cp[i].CostUSD < cp[j].CostUSD })
	return cp
}
