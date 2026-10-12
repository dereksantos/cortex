package benchstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeRow(t *testing.T) {
	tests := []struct {
		name          string
		row           string
		status, class string
		detail        string
	}{
		{"polyglot pass", `{"exercise":"wordy","pass":true,"tokens_in":10,"agent_turns":2}`, StatusPass, "", ""},
		{"polyglot early finalize", `{"exercise":"x","pass":false,"failure_class":"early_finalize"}`, StatusFail, FailNoChange, "early_finalize"},
		{"swebench wrong patch", `{"instance_id":"x","resolved":false,"failure_class":"wrong_patch"}`, StatusFail, FailWrongChange, "wrong_patch"},
		{"swebench env error", `{"instance_id":"x","failure_class":"env_error"}`, StatusError, FailInfra, "env_error"},
		{"unscored prediction", `{"instance_id":"x","eval_status":"error_no_report"}`, StatusError, FailInfra, "error_no_report"},
		{"unknown class is kept", `{"exercise":"x","failure_class":"novel"}`, StatusFail, "novel", "novel"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var row map[string]any
			if err := json.Unmarshal([]byte(tt.row), &row); err != nil {
				t.Fatal(err)
			}
			r := NormalizeRow(row)
			if r.Schema != ResultSchemaV1 || r.TaskID != "x" && r.TaskID != "wordy" {
				t.Errorf("schema/task = %q/%q", r.Schema, r.TaskID)
			}
			if r.Status != tt.status || r.FailureClass != tt.class || r.FailureDetail != tt.detail {
				t.Errorf("got status=%s class=%s detail=%s, want %s/%s/%s", r.Status, r.FailureClass, r.FailureDetail, tt.status, tt.class, tt.detail)
			}
		})
	}
	t.Run("runner fields beyond v1 ride in extra", func(t *testing.T) {
		r := NormalizeRow(map[string]any{"instance_id": "x", "resolved": true, "patch_bytes": 12.0})
		if r.Extra["patch_bytes"] != 12.0 {
			t.Errorf("extra = %v", r.Extra)
		}
	})
}

func TestNormalizeRun(t *testing.T) {
	polyglot := map[string]any{"run_id": "r", "model": "q", "study_model": "s", "polyglot_repo": "repo", "polyglot_commit": "abc",
		"language": "go", "exercises": []any{"a", "b"}, "cortex_commit": "c1", "cortex_dirty": true, "os": "darwin", "go_version": "go1"}
	r := NormalizeRun("polyglot", "r", polyglot, map[string]string{"arm": "x"})
	if r.Harness != "cortex" || r.Models["code"] != "q" || r.Models["study"] != "s" || r.Dataset.Revision != "abc" ||
		len(r.Tasks) != 2 || !r.Cortex.Dirty || r.Host.OS != "darwin" || r.Extra["go_version"] != "go1" || r.Labels["arm"] != "x" {
		t.Errorf("polyglot run = %+v", r)
	}
	mini := map[string]any{"harness": "mini-swe-agent", "mini_version": "2.4.6", "ids": "a,b,c", "dataset_revision": "rev"}
	m := NormalizeRun("swebench", "p/mini", mini, nil)
	if m.Harness != "mini-swe-agent" || m.HarnessVersion != "2.4.6" || len(m.Tasks) != 3 || m.Dataset.Name != "SWE-bench/SWE-bench_Verified" {
		t.Errorf("mini run = %+v", m)
	}
}

func TestNormalizeMiniFromReports(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "run.json"), `{"harness":"mini-swe-agent"}`)
	writeFile(t, filepath.Join(dir, "mini_overlay_litellm.yaml"), "model:\n  model_class: litellm\n  model_name: openai/qwen3.8-27b\n")
	writeFile(t, filepath.Join(dir, "predictions.jsonl"),
		`{"instance_id":"a","model_patch":"diff"}`+"\n"+`{"instance_id":"b","model_patch":"diff"}`+"\n"+`{"instance_id":"c","model_patch":""}`+"\n")
	writeFile(t, filepath.Join(dir, "eval", "x", "report.json"), `{"a":{"resolved":true}}`)
	writeFile(t, filepath.Join(dir, "eval", "y", "report.json"), `{"c":{"resolved":false}}`)
	if err := Normalize("swebench", "p/mini", dir, nil); err != nil {
		t.Fatal(err)
	}
	s, err := Summarize(dir)
	if err != nil {
		t.Fatal(err)
	}
	// a resolved; b has no report (scoring failed → infra error); c empty patch → no_change fail.
	if s.Pass != 1 || s.Skipped != 1 || s.Fail != 1 || s.Classes[FailNoChange] != 1 {
		t.Errorf("summary = %+v", s)
	}
	b, err := os.ReadFile(filepath.Join(dir, RunFileV1))
	if err != nil {
		t.Fatalf("run.v1.json not written: %v", err)
	}
	var run RunV1
	if err := json.Unmarshal(b, &run); err != nil || run.Models["code"] != "qwen3.8-27b" {
		t.Errorf("model from the overlay = %v (err %v), want qwen3.8-27b", run.Models, err)
	}
}
