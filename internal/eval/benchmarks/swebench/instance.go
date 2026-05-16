// Package swebench wires SWE-bench Verified into Cortex's benchmark
// registry. Each instance is one human-curated GitHub issue from
// princeton-nlp/SWE-bench_Verified; Cortex runs the in-process coding
// harness against the repo at base_commit, extracts the resulting
// patch via `git diff`, and scores it by applying the patch inside the
// canonical SWE-bench Docker image and running the F2P/P2P test sets.
//
// Scoring runs out-of-loop in Docker (see score.go) — the in-loop
// harness's shell allowlist is NOT extended for Python tooling. The
// agent reads code and writes code; verification happens against the
// authoritative upstream environment.
//
// See docs/benchmarks/swebench.md for the user-facing description and
// docs/prompts/benchmarks/04-swebench.md for the engineering brief.
package swebench

// Instance is one SWE-bench Verified item. Fields mirror the upstream
// dataset rows; the runner and scorer use them verbatim.
//
// NOTE: Patch is the human-authored gold patch. It is kept here so the
// scorer can sanity-check against a known-good solution when needed,
// but it MUST NEVER be passed to the model. The runner is responsible
// for that contract.
type Instance struct {
	InstanceID             string   `json:"instance_id"`
	Repo                   string   `json:"repo"`
	BaseCommit             string   `json:"base_commit"`
	ProblemStatement       string   `json:"problem_statement"`
	Patch                  string   `json:"patch"`
	TestPatch              string   `json:"test_patch"`
	HintsText              string   `json:"hints_text"`
	CreatedAt              string   `json:"created_at"`
	Version                string   `json:"version"`
	EnvironmentSetupCommit string   `json:"environment_setup_commit"`
	FailToPass             []string `json:"FAIL_TO_PASS"`
	PassToPass             []string `json:"PASS_TO_PASS"`
}
