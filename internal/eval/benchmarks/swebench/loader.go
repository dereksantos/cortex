package swebench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
)

// hfDatasetsServerBase is the read-API HuggingFace exposes for any
// public dataset. It auto-converts Parquet → JSON so we don't need
// arrow/parquet libs in this binary.
const hfDatasetsServerBase = "https://datasets-server.huggingface.co/rows"

// hfDatasetSlug is the upstream dataset path.
const hfDatasetSlug = "princeton-nlp/SWE-bench_Verified"

// hfSplit is the split name on the dataset card.
const hfSplit = "test"

// hfConfig is the dataset config name. Verified ships a single
// "default" config.
const hfConfig = "default"

// pageSize matches the upstream cap (100 rows per request as of the
// dataset-server contract; we'll loop until offset+length covers Limit
// or returns an empty page).
const pageSize = 100

// LoadInstances fetches SWE-bench Verified instances via the
// HuggingFace datasets-server JSON endpoint and returns at most
// opts.Limit of them.
//
// Filtering:
//   - opts.Subset must equal "verified" (the only supported value);
//     other subsets (lite, full) are not in scope for this loop.
//   - opts.Filter["repo"] (comma- or pipe-separated) restricts to
//     matching upstream repo slugs (e.g. "django/django,sympy/sympy").
//   - opts.Limit caps the number of returned instances; 0 means
//     "load everything" (be careful — the full split is 500 rows).
//
// Network failures bubble up as errors; the caller is expected to
// retry or abort. Cached responses are saved under the benchmark
// cache root per page so repeated runs in CI are deterministic and
// don't re-hit HF.
func LoadInstances(ctx context.Context, opts benchmarks.LoadOpts) ([]Instance, error) {
	if opts.Subset != "" && opts.Subset != "verified" {
		return nil, fmt.Errorf("swebench: unsupported subset %q (only \"verified\" is wired)", opts.Subset)
	}

	want := opts.Limit
	if want < 0 {
		return nil, fmt.Errorf("swebench: limit must be >= 0, got %d", want)
	}
	repoFilter := parseRepoFilter(opts.Filter["repo"])

	var (
		out    []Instance
		offset int
	)
	for {
		page, err := fetchPage(ctx, offset, pageSize)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		for _, inst := range page {
			if len(repoFilter) > 0 && !repoFilter[strings.ToLower(inst.Repo)] {
				continue
			}
			out = append(out, inst)
			if want > 0 && len(out) >= want {
				return out, nil
			}
		}
		offset += len(page)
		if len(page) < pageSize {
			break
		}
	}
	return out, nil
}

// parseRepoFilter splits a comma/pipe/space-separated filter string
// into a lowercase set. Empty input returns nil so callers can branch
// on len(set)==0 to skip filtering entirely.
func parseRepoFilter(raw string) map[string]bool {
	if raw == "" {
		return nil
	}
	out := map[string]bool{}
	for _, tok := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '|' || r == ' '
	}) {
		out[strings.ToLower(strings.TrimSpace(tok))] = true
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// fetchPage returns one page of instances starting at offset. Pages
// are cached on disk by (offset,length) so reruns are deterministic
// and offline-friendly.
func fetchPage(ctx context.Context, offset, length int) ([]Instance, error) {
	rel := fmt.Sprintf("hf-rows-offset-%d-length-%d.json", offset, length)
	q := url.Values{}
	q.Set("dataset", hfDatasetSlug)
	q.Set("config", hfConfig)
	q.Set("split", hfSplit)
	q.Set("offset", strconv.Itoa(offset))
	q.Set("length", strconv.Itoa(length))
	full := hfDatasetsServerBase + "?" + q.Encode()

	path, err := benchmarks.EnsureCached("swebench", rel, full)
	if err != nil {
		return nil, fmt.Errorf("fetch swebench rows offset=%d: %w", offset, err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read cached swebench rows: %w", err)
	}
	return parseRowsResponse(body)
}

// parseRowsResponse parses the HF datasets-server `/rows` JSON shape
// into Instances. The relevant envelope:
//
//	{
//	  "features": [...],
//	  "rows": [ { "row_idx": 0, "row": { ...fields... }, "truncated_cells": [] }, ... ]
//	}
//
// Only the `row` object matters for us; we re-marshal it through the
// Instance JSON tags so type coercion (FAIL_TO_PASS may arrive as a
// JSON-encoded string or as a native list, depending on the upstream
// schema) is handled uniformly.
func parseRowsResponse(body []byte) ([]Instance, error) {
	var env struct {
		Rows []struct {
			RowIdx int             `json:"row_idx"`
			Row    json.RawMessage `json:"row"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decode rows envelope: %w", err)
	}

	out := make([]Instance, 0, len(env.Rows))
	for _, r := range env.Rows {
		inst, err := decodeInstance(r.Row)
		if err != nil {
			return nil, fmt.Errorf("decode row %d: %w", r.RowIdx, err)
		}
		out = append(out, inst)
	}
	// Stable ordering by instance_id so two runs over the same cache
	// produce identical Limit slices.
	sort.SliceStable(out, func(i, j int) bool { return out[i].InstanceID < out[j].InstanceID })
	return out, nil
}

// decodeInstance handles the FAIL_TO_PASS / PASS_TO_PASS string-or-list
// quirk: the upstream dataset stores them as JSON-encoded strings
// (e.g. "[\"test_a\", \"test_b\"]"), but some tooling rehydrates them
// as native arrays. We accept both shapes.
func decodeInstance(raw json.RawMessage) (Instance, error) {
	var loose map[string]json.RawMessage
	if err := json.Unmarshal(raw, &loose); err != nil {
		return Instance{}, fmt.Errorf("row object: %w", err)
	}

	inst := Instance{}
	inst.InstanceID = stringOrEmpty(loose["instance_id"])
	inst.Repo = stringOrEmpty(loose["repo"])
	inst.BaseCommit = stringOrEmpty(loose["base_commit"])
	inst.ProblemStatement = stringOrEmpty(loose["problem_statement"])
	inst.Patch = stringOrEmpty(loose["patch"])
	inst.TestPatch = stringOrEmpty(loose["test_patch"])
	inst.HintsText = stringOrEmpty(loose["hints_text"])
	inst.CreatedAt = stringOrEmpty(loose["created_at"])
	inst.Version = stringOrEmpty(loose["version"])
	inst.EnvironmentSetupCommit = stringOrEmpty(loose["environment_setup_commit"])
	inst.FailToPass = stringOrListField(loose["FAIL_TO_PASS"])
	inst.PassToPass = stringOrListField(loose["PASS_TO_PASS"])
	return inst, nil
}

// stringOrEmpty returns the string value of raw, or "" if raw is null
// or not a string. Non-string values for these fields would indicate
// upstream schema drift; we surface "" rather than failing loudly so
// loader-level smoke tests don't break the moment HF adds an optional
// numeric field.
func stringOrEmpty(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return ""
}

// stringOrListField unwraps the two on-wire shapes for FAIL_TO_PASS /
// PASS_TO_PASS: a JSON list of strings, or a JSON-encoded string
// containing a JSON list of strings.
func stringOrListField(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil
		}
		var inner []string
		if err := json.Unmarshal([]byte(s), &inner); err == nil {
			return inner
		}
	}
	return nil
}
