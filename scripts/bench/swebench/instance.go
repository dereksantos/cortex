package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// instance.go loads SWE-bench task instances and chooses which ones a run
// attempts.
//
// Only the fields a leaderboard submission may use reach this struct:
// the issue text, the repository, its base commit, and the prebuilt image.
// The gold patch, the test patch, FAIL_TO_PASS/PASS_TO_PASS and hints_text
// are in the exported dataset file (the official harness needs them to
// grade), but they are never decoded here — so they cannot leak into a
// prompt by accident.

// Instance is one SWE-bench task, restricted to the inference-safe fields.
type Instance struct {
	InstanceID       string `json:"instance_id"`
	Repo             string `json:"repo"`
	BaseCommit       string `json:"base_commit"`
	ProblemStatement string `json:"problem_statement"`
	Image            string `json:"image"`
	Version          string `json:"version"`
	Difficulty       string `json:"difficulty"`
}

// LoadInstances reads the dataset export written by dataset.py.
func LoadInstances(path string) ([]Instance, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open dataset %s: %w", path, err)
	}
	defer f.Close()

	var out []Instance
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var in Instance
		if err := json.Unmarshal([]byte(line), &in); err != nil {
			return nil, fmt.Errorf("failed to parse dataset row: %w", err)
		}
		if in.InstanceID == "" || in.BaseCommit == "" || in.Image == "" {
			return nil, fmt.Errorf("dataset row missing instance_id/base_commit/image: %.80s", line)
		}
		out = append(out, in)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("failed to read dataset %s: %w", path, err)
	}
	return out, nil
}

// SelectInstances chooses the instances a run attempts.
//
// Explicit ids win and keep the given order. Otherwise the n instances with
// the smallest sha256(seed + ":" + instance_id) are chosen, in that order —
// a selection that depends only on the seed and the id set, so anyone can
// reproduce it (in any language) without our RNG. n <= 0 means all
// instances, in that same seeded order.
func SelectInstances(all []Instance, ids []string, n int, seed string) ([]Instance, error) {
	byID := make(map[string]Instance, len(all))
	for _, in := range all {
		byID[in.InstanceID] = in
	}
	if len(ids) > 0 {
		out := make([]Instance, 0, len(ids))
		for _, id := range ids {
			in, ok := byID[id]
			if !ok {
				return nil, fmt.Errorf("unknown instance %q", id)
			}
			out = append(out, in)
		}
		return out, nil
	}
	ranked := append([]Instance(nil), all...)
	keys := make(map[string]string, len(ranked))
	for _, in := range ranked {
		keys[in.InstanceID] = seedKey(seed, in.InstanceID)
	}
	sort.Slice(ranked, func(i, j int) bool {
		ki, kj := keys[ranked[i].InstanceID], keys[ranked[j].InstanceID]
		if ki != kj {
			return ki < kj
		}
		return ranked[i].InstanceID < ranked[j].InstanceID
	})
	if n > 0 && n < len(ranked) {
		ranked = ranked[:n]
	}
	return ranked, nil
}

func seedKey(seed, id string) string {
	sum := sha256.Sum256([]byte(seed + ":" + id))
	return hex.EncodeToString(sum[:])
}

// BuildPrompt is the single user message a cortex turn receives: the issue
// text verbatim plus a short statement of the task. No hints, no test names,
// no knowledge of the grading tests — only what a developer reading the
// issue would have.
func BuildPrompt(in Instance) string {
	var b strings.Builder
	b.WriteString("<issue>\n")
	b.WriteString(strings.TrimSpace(in.ProblemStatement))
	b.WriteString("\n</issue>\n\n")
	b.WriteString("The repository this issue was filed against (")
	b.WriteString(in.Repo)
	b.WriteString(") is checked out in the current directory at the commit where the issue was reported, ")
	b.WriteString("with its development dependencies installed. Resolve the issue by changing the repository's ")
	b.WriteString("non-test source files so the described behavior is fixed in general, not only for the example given. ")
	b.WriteString("When you are done, reply with a brief summary of what you changed.")
	return b.String()
}

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

// containerName is the docker name for one instance's inference container.
// Every container this driver creates carries the "cortex-swebench-" prefix,
// and it only ever removes containers it created by name.
func containerName(runID, instanceID string) string {
	return "cortex-swebench-" + unsafeName.ReplaceAllString(runID+"-"+instanceID, "_")
}
