package main

import "testing"

func TestLogicalRun(t *testing.T) {
	tests := []struct {
		name                 string
		runID                string
		tasks                []string
		harness              string
		wantID, wantB, wantA string
	}{
		{"per-instance mini (pair layout)", "pair-20261008/mini-astropy__astropy-7166/mini", []string{"astropy__astropy-7166"}, "mini-swe-agent",
			"pair-20261008/mini", "pair-20261008", "mini"},
		{"per-instance cortex (baseline layout)", "baseline-sv50/cortex-pytest-dev__pytest-5787", []string{"pytest-dev__pytest-5787"}, "cortex",
			"baseline-sv50/cortex", "baseline-sv50", "cortex"},
		{"a run still going has no results yet", "baseline-sv50/mini-scikit-learn__scikit-learn-1/mini", nil, "mini-swe-agent",
			"baseline-sv50/mini", "baseline-sv50", "mini"},
		{"A/B arm", "ab-editfmt-20261013/patch", []string{"a__b-1", "c__d-2"}, "cortex",
			"ab-editfmt-20261013/patch", "ab-editfmt-20261013", "patch"},
		{"dashed arm name is kept", "ab-20261009/new-reason", []string{"a__b-1", "c__d-2"}, "cortex",
			"ab-20261009/new-reason", "ab-20261009", "new-reason"},
		{"single-segment run: the arm is the harness", "smoke-seed106", []string{"a__b-1", "c__d-2"}, "cortex",
			"smoke-seed106", "smoke-seed106", "cortex"},
		{"one-task run not named after its task", "probe-local-n1", []string{"astropy__astropy-7166"}, "cortex",
			"probe-local-n1", "probe-local-n1", "cortex"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, b, a := logicalRun(tc.runID, tc.tasks, tc.harness)
			if id != tc.wantID || b != tc.wantB || a != tc.wantA {
				t.Errorf("logicalRun(%q) = (%q, %q, %q), want (%q, %q, %q)", tc.runID, id, b, a, tc.wantID, tc.wantB, tc.wantA)
			}
		})
	}
}
