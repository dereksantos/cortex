package benchstore

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The v1 result format: every run directory carries run.v1.json and
// results.v1.jsonl beside its runner's native run.json/results.jsonl. The
// native files keep everything a runner records; the v1 files are the one
// shape every trend view, comparison, and summary reads, whatever the
// benchmark. Fields a runner records beyond v1 ride in "extra", verbatim.
const (
	RunSchemaV1    = "cortex.bench.run/v1"
	ResultSchemaV1 = "cortex.bench.result/v1"
	RunFileV1      = "run.v1.json"
	ResultsFileV1  = "results.v1.jsonl"
)

// Status values. Only an infrastructure failure is an "error" (excluded from
// the pass rate, but counted and shown); everything the agent caused —
// including producing no change at all — is a "fail".
const (
	StatusPass  = "pass"
	StatusFail  = "fail"
	StatusError = "error"
)

// The shared failure taxonomy (failure_class). failure_detail keeps the
// runner's own, finer class.
const (
	FailChatMode    = "chat_mode"    // answered in prose: no tool calls, no change
	FailNoChange    = "no_change"    // worked with tools but left no change on disk
	FailWrongChange = "wrong_change" // changed the code; the tests still fail
	FailTimeout     = "timeout"      // hit the wall-clock limit
	FailBudget      = "budget_stop"  // hit the spend limit
	FailAgentError  = "agent_error"  // the agent process errored
	FailInfra       = "infra_error"  // the harness/environment failed, not the agent
)

// nativeClass maps each runner's failure_class to the shared taxonomy.
var nativeClass = map[string]string{
	"chat_mode":          FailChatMode,
	"early_finalize":     FailNoChange, // polyglot
	"empty_patch":        FailNoChange, // swebench
	"wrong_code":         FailWrongChange,
	"wrong_patch":        FailWrongChange,
	"timeout":            FailTimeout,
	"budget_stop":        FailBudget,
	"error":              FailAgentError,
	"env_error":          FailInfra,
	"eval_error":         FailInfra,
	"patch_apply_failed": FailWrongChange, // the agent's diff doesn't apply: unresolved, as SWE-bench scores it
}

// RunV1 is run.v1.json.
type RunV1 struct {
	Schema         string            `json:"schema"`
	Bench          string            `json:"bench"`
	RunID          string            `json:"run_id"`
	Harness        string            `json:"harness"` // the agent under test: cortex, mini-swe-agent, …
	HarnessVersion string            `json:"harness_version,omitempty"`
	StartedAt      string            `json:"started_at,omitempty"`
	EndedAt        string            `json:"ended_at,omitempty"`
	Models         map[string]string `json:"models,omitempty"` // role → model id (code, study)
	Backend        string            `json:"backend,omitempty"`
	Endpoint       string            `json:"endpoint,omitempty"`
	Window         int               `json:"window,omitempty"`
	Temperature    *float64          `json:"temperature,omitempty"`
	Cortex         SourceV1          `json:"cortex"`
	Dataset        DatasetV1         `json:"dataset"`
	Tasks          []string          `json:"tasks,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	Host           HostV1            `json:"host"`
	Extra          map[string]any    `json:"extra,omitempty"`
}

// SourceV1 is the cortex tree the run was built from.
type SourceV1 struct {
	Commit string `json:"commit,omitempty"`
	Dirty  bool   `json:"dirty,omitempty"`
}

// DatasetV1 pins the task corpus; results compare only within one pin.
type DatasetV1 struct {
	Name     string `json:"name,omitempty"`
	Revision string `json:"revision,omitempty"`
	Split    string `json:"split,omitempty"`
}

// HostV1 is where the run executed.
type HostV1 struct {
	Name string `json:"name,omitempty"`
	OS   string `json:"os,omitempty"`
	Arch string `json:"arch,omitempty"`
}

// ResultV1 is one results.v1.jsonl row.
type ResultV1 struct {
	Schema        string         `json:"schema"`
	TaskID        string         `json:"task_id"`
	Status        string         `json:"status"` // pass | fail | error
	FailureClass  string         `json:"failure_class,omitempty"`
	FailureDetail string         `json:"failure_detail,omitempty"`
	TokensIn      int64          `json:"tokens_in,omitempty"`
	TokensOut     int64          `json:"tokens_out,omitempty"`
	CostUSD       float64        `json:"cost_usd,omitempty"`
	WallMs        int64          `json:"wall_ms,omitempty"`
	ToolCalls     int            `json:"tool_calls,omitempty"`
	Turns         int            `json:"turns,omitempty"`
	Extra         map[string]any `json:"extra,omitempty"`
}

// NormalizeRow converts one native results row to v1.
func NormalizeRow(row map[string]any) ResultV1 {
	r := ResultV1{Schema: ResultSchemaV1}
	used := map[string]bool{}
	take := func(keys ...string) any {
		for _, k := range keys {
			if v, ok := row[k]; ok {
				used[k] = true
				return v
			}
		}
		return nil
	}
	r.TaskID = str(take("task_id", "exercise", "instance_id"))
	pass := truthy(take("pass")) || truthy(take("resolved"))
	native := str(take("failure_class"))
	evalStatus := str(take("eval_status"))
	r.TokensIn = int64(num(take("tokens_in")))
	r.TokensOut = int64(num(take("tokens_out")))
	r.CostUSD = num(take("cost_usd"))
	r.WallMs = int64(num(take("wall_ms")))
	r.ToolCalls = int(num(take("tool_calls")))
	r.Turns = int(num(take("agent_turns", "turns")))

	switch {
	case pass:
		r.Status = StatusPass
	default:
		r.FailureDetail = native
		r.FailureClass = nativeClass[native]
		if native == "" && strings.HasPrefix(evalStatus, "error") {
			r.FailureClass, r.FailureDetail = FailInfra, evalStatus
		}
		if r.FailureClass == "" && native != "" {
			r.FailureClass = native // an unmapped class is kept, never dropped
		}
		if r.FailureClass == FailInfra {
			r.Status = StatusError
		} else {
			r.Status = StatusFail
		}
	}
	for k, v := range row {
		if !used[k] {
			if r.Extra == nil {
				r.Extra = map[string]any{}
			}
			r.Extra[k] = v
		}
	}
	if evalStatus != "" {
		if r.Extra == nil {
			r.Extra = map[string]any{}
		}
		r.Extra["eval_status"] = evalStatus
	}
	return r
}

// NormalizeRun converts a native run.json (any runner here) to v1.
func NormalizeRun(bench, runID string, meta map[string]any, labels map[string]string) RunV1 {
	used := map[string]bool{}
	take := func(keys ...string) any {
		for _, k := range keys {
			if v, ok := meta[k]; ok {
				used[k] = true
				return v
			}
		}
		return nil
	}
	r := RunV1{Schema: RunSchemaV1, Bench: bench, RunID: runID, Labels: labels}
	take("run_id")
	r.Harness = str(take("harness"))
	if r.Harness == "" {
		r.Harness = "cortex"
	}
	r.HarnessVersion = str(take("mini_version"))
	r.StartedAt = str(take("started_at"))
	r.EndedAt = str(take("ended_at"))
	if m := str(take("model")); m != "" {
		r.Models = map[string]string{"code": m}
		if s := str(take("study_model")); s != "" {
			r.Models["study"] = s
		}
	}
	r.Backend = str(take("backend_type", "backend"))
	r.Endpoint = str(take("endpoint"))
	r.Window = int(num(take("window")))
	if t, ok := take("temperature").(float64); ok {
		r.Temperature = &t
	}
	r.Cortex = SourceV1{Commit: str(take("cortex_commit")), Dirty: truthy(take("cortex_dirty"))}
	switch {
	case meta["polyglot_commit"] != nil:
		r.Dataset = DatasetV1{Name: str(take("polyglot_repo")), Revision: str(take("polyglot_commit")), Split: str(take("language"))}
	default:
		r.Dataset = DatasetV1{Name: str(take("dataset")), Revision: str(take("dataset_revision")), Split: str(take("split"))}
		if r.Dataset.Name == "" && r.Dataset.Revision != "" && bench == "swebench" {
			r.Dataset.Name = "SWE-bench/SWE-bench_Verified"
		}
	}
	switch v := take("exercises", "instances", "ids").(type) {
	case []any:
		for _, x := range v {
			r.Tasks = append(r.Tasks, str(x))
		}
	case string:
		for _, x := range strings.Split(v, ",") {
			if x = strings.TrimSpace(x); x != "" {
				r.Tasks = append(r.Tasks, x)
			}
		}
	}
	r.Host = HostV1{Name: str(take("host")), OS: str(take("os", "host_os")), Arch: str(take("arch", "host_arch"))}
	for k, v := range meta {
		if !used[k] {
			if r.Extra == nil {
				r.Extra = map[string]any{}
			}
			r.Extra[k] = v
		}
	}
	return r
}

// Normalize writes run.v1.json and results.v1.jsonl for one run directory
// from its native files. A run with no results.jsonl but official SWE-bench
// predictions (the mini-SWE-agent side) gets rows from the harness reports:
// a prediction with no report is an infra error, not a guess. Re-running is
// safe — the v1 files are a pure function of the native ones.
func Normalize(bench, runID, dir string, labels map[string]string) error {
	var meta map[string]any
	if b, err := os.ReadFile(filepath.Join(dir, "run.json")); err == nil {
		_ = json.Unmarshal(b, &meta)
	}
	if meta == nil {
		meta = map[string]any{}
	}
	if meta["model"] == nil || meta["model"] == "" {
		// Runs from before run_mini.sh recorded the model name it only in
		// mini-SWE-agent's litellm overlay ("model_name: openai/<id>").
		if m := overlayModel(dir); m != "" {
			meta["model"] = m
		}
	}
	run := NormalizeRun(bench, runID, meta, labels)
	rows, err := nativeRows(dir)
	if err != nil {
		return err
	}
	var out strings.Builder
	for _, row := range rows {
		b, err := json.Marshal(NormalizeRow(row))
		if err != nil {
			return err
		}
		out.Write(b)
		out.WriteByte('\n')
	}
	if err := writeAtomic(filepath.Join(dir, ResultsFileV1), []byte(out.String())); err != nil {
		return err
	}
	b, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, RunFileV1), append(b, '\n'))
}

func nativeRows(dir string) ([]map[string]any, error) {
	rows, err := readJSONL(filepath.Join(dir, "results.jsonl"))
	if err == nil {
		return rows, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	preds, err := readJSONL(filepath.Join(dir, "predictions.jsonl"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	reports := map[string]map[string]any{}
	_ = filepath.WalkDir(filepath.Join(dir, "eval"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "report.json" {
			return nil
		}
		var rep map[string]map[string]any
		if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &rep) == nil {
			for k, v := range rep {
				reports[k] = v
			}
		}
		return nil
	})
	// usage.jsonl is run_mini.sh's per-instance ledger: the wall clock it
	// measured around the agent and whether --wall-limit cut it short.
	usage := map[string]map[string]any{}
	if ledger, err := readJSONL(filepath.Join(dir, "usage.jsonl")); err == nil {
		for _, u := range ledger {
			usage[str(u["instance_id"])] = u
		}
	}
	for _, p := range preds {
		id := str(p["instance_id"])
		row := map[string]any{"instance_id": id}
		u, hasUsage := usage[id]
		if hasUsage {
			if s, ok := u["wall_s"].(float64); ok {
				row["wall_ms"] = s * 1000
			}
		}
		emptyPatch := strings.TrimSpace(str(p["model_patch"])) == ""
		if rep, ok := reports[id]; !ok && emptyPatch {
			// The harness skips an empty patch and writes no report: the
			// agent changed nothing, which is not a scoring failure.
			row["eval_status"], row["failure_class"] = "skipped_empty", "empty_patch"
			if hasUsage && truthy(u["timed_out"]) {
				row["failure_class"] = "timeout"
			}
		} else if !ok {
			row["eval_status"] = "error_no_report"
		} else if truthy(rep["resolved"]) {
			row["resolved"], row["eval_status"] = true, "resolved"
		} else {
			row["eval_status"] = "unresolved"
			switch {
			case hasUsage && truthy(u["timed_out"]):
				row["failure_class"] = "timeout"
			case emptyPatch:
				row["failure_class"] = "empty_patch"
			default:
				row["failure_class"] = "wrong_patch"
			}
		}
		rows = append(rows, row)
	}
	// An instance the ledger ran but that left no prediction (mini killed at
	// --wall-limit, or crashed) is still an attempt the agent failed: leaving
	// it out would drop it from every paired comparison, so one harness's
	// timeouts would vanish while the other's count. A budget skip never ran.
	predicted := map[string]bool{}
	for _, p := range preds {
		predicted[str(p["instance_id"])] = true
	}
	ids := make([]string, 0, len(usage))
	for id := range usage {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		u := usage[id]
		if predicted[id] || truthy(u["skipped_for_budget"]) {
			continue
		}
		row := map[string]any{"instance_id": id, "eval_status": "no_prediction", "failure_class": "error"}
		if s, ok := u["wall_s"].(float64); ok {
			row["wall_ms"] = s * 1000
		}
		if truthy(u["timed_out"]) {
			row["failure_class"] = "timeout"
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// Summarize rolls up a run directory: from results.v1.jsonl when present,
// else from the native results.jsonl through the same normalization.
func Summarize(dir string) (Summary, error) {
	rows, err := readJSONL(filepath.Join(dir, ResultsFileV1))
	if errors.Is(err, os.ErrNotExist) {
		var native []map[string]any
		native, err = nativeRows(dir)
		for _, n := range native {
			b, _ := json.Marshal(NormalizeRow(n))
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			rows = append(rows, m)
		}
	}
	if err != nil {
		return Summary{}, err
	}
	var s Summary
	for _, row := range rows {
		s.N++
		switch str(row["status"]) {
		case StatusPass:
			s.Pass++
		case StatusError:
			s.Skipped++
		default:
			s.Fail++
		}
		if c := str(row["failure_class"]); c != "" {
			if s.Classes == nil {
				s.Classes = map[string]int{}
			}
			s.Classes[c]++
		}
		s.TokensIn += int64(num(row["tokens_in"]))
		s.TokensOut += int64(num(row["tokens_out"]))
		s.CostUSD += num(row["cost_usd"])
		s.WallSec += num(row["wall_ms"]) / 1000
	}
	return s, nil
}

func readJSONL(path string) ([]map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var rows []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			rows = append(rows, m)
		}
	}
	return rows, sc.Err()
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return os.Rename(tmp, path)
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

// overlayModel reads model_name from a mini-SWE-agent litellm overlay in dir,
// minus litellm's "openai/" routing prefix (the cortex side records the bare id).
func overlayModel(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "mini_overlay_litellm.yaml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && k == "model_name" {
			return strings.TrimPrefix(strings.Trim(strings.TrimSpace(v), `"'`), "openai/")
		}
	}
	return ""
}
