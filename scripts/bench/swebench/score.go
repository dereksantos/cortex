package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// score.go runs the OFFICIAL SWE-bench evaluation harness
// (`python -m swebench.harness.run_evaluation`) on one prediction and reads
// its verdict from the report.json it writes. There is no other scorer in
// this driver: resolved means the harness said resolved.

// ScoreResult is the harness outcome for one prediction.
type ScoreResult struct {
	Status     string // one of the Eval* constants
	Resolved   bool
	ReportPath string
	LogPath    string
	Err        string
}

// harnessReport is the per-instance report.json shape (swebench 5.x):
// {"<instance_id>": {"patch_successfully_applied": bool, "resolved": bool, ...}}
type harnessReport map[string]struct {
	PatchIsNone              bool `json:"patch_is_None"`
	PatchExists              bool `json:"patch_exists"`
	PatchSuccessfullyApplied bool `json:"patch_successfully_applied"`
	Resolved                 bool `json:"resolved"`
}

// harnessLogDir mirrors the harness's own layout:
// logs/run_evaluation/<run_id>/<model_name_or_path with / → __>/<instance_id>.
func harnessLogDir(evalDir, runID, model, instanceID string) string {
	return filepath.Join(evalDir, "logs", "run_evaluation", runID,
		strings.ReplaceAll(model, "/", "__"), instanceID)
}

// ParseScore turns the harness's per-instance log directory into a result.
func ParseScore(logDir, instanceID string) ScoreResult {
	res := ScoreResult{
		ReportPath: filepath.Join(logDir, "report.json"),
		LogPath:    filepath.Join(logDir, "run_instance.log"),
	}
	if b, err := os.ReadFile(res.ReportPath); err == nil {
		var rep harnessReport
		if err := json.Unmarshal(b, &rep); err != nil {
			res.Status, res.Err = EvalError, "unparseable report.json: "+err.Error()
			return res
		}
		r, ok := rep[instanceID]
		if !ok {
			res.Status, res.Err = EvalError, "report.json has no entry for "+instanceID
			return res
		}
		switch {
		case !r.PatchSuccessfullyApplied:
			res.Status = EvalApplyFailed
		case r.Resolved:
			res.Status, res.Resolved = EvalResolved, true
		default:
			res.Status = EvalUnresolved
		}
		return res
	}
	res.ReportPath = ""
	// No report: the harness raises before grading when the patch does not
	// apply ("Patch Apply Failed" in run_instance.log), or on an infra error.
	if b, err := os.ReadFile(res.LogPath); err == nil && strings.Contains(string(b), "Patch Apply Failed") {
		res.Status = EvalApplyFailed
		return res
	}
	res.Status, res.Err = EvalError, "harness produced no report.json"
	return res
}

// ScoreOne evaluates a single prediction with the official harness.
func ScoreOne(ctx context.Context, python, datasetFile, evalDir, runID string, pred Prediction, timeout time.Duration) ScoreResult {
	if strings.TrimSpace(pred.ModelPatch) == "" {
		return ScoreResult{Status: EvalSkippedEmpty}
	}
	if err := os.MkdirAll(evalDir, 0o755); err != nil {
		return ScoreResult{Status: EvalError, Err: err.Error()}
	}
	predPath := filepath.Join(evalDir, "preds", pred.InstanceID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(predPath), 0o755); err != nil {
		return ScoreResult{Status: EvalError, Err: err.Error()}
	}
	b, err := json.Marshal(pred)
	if err != nil {
		return ScoreResult{Status: EvalError, Err: err.Error()}
	}
	if err := os.WriteFile(predPath, append(b, '\n'), 0o644); err != nil {
		return ScoreResult{Status: EvalError, Err: err.Error()}
	}

	cctx, cancel := context.WithTimeout(ctx, timeout+15*time.Minute) // + image pull headroom
	defer cancel()
	cmd := exec.CommandContext(cctx, python, "-m", "swebench.harness.run_evaluation",
		"--dataset_name", datasetFile,
		"--instance_ids", pred.InstanceID,
		"--predictions_path", predPath,
		"--run_id", runID,
		"--max_workers", "1",
		"--timeout", fmt.Sprintf("%d", int(timeout.Seconds())),
		"--report_dir", evalDir,
	)
	cmd.Dir = evalDir
	out, runErr := cmd.CombinedOutput()
	_ = os.WriteFile(filepath.Join(evalDir, "harness-"+pred.InstanceID+".log"), out, 0o644)

	res := ParseScore(harnessLogDir(evalDir, runID, pred.ModelNameOrPath, pred.InstanceID), pred.InstanceID)
	if res.Status == EvalError {
		switch {
		case errors.Is(cctx.Err(), context.DeadlineExceeded):
			res.Err = "harness timed out"
		case runErr != nil:
			res.Err = strings.TrimSpace(res.Err + "; harness exit: " + runErr.Error())
		}
	}
	return res
}
