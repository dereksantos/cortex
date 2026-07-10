package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// confine.go holds the study subagent's two read-path guards, applied in the
// study dispatcher (cmd/cortex) before a tool executes — once, for every
// path-taking tool — so the tools themselves stay caller-agnostic. See
// docs/study-subagent.md §1 and §4.

// maxReadLines is the ONE knob governing both "what counts as a small file" (a
// no-range read at or under this is returned whole) and "the maximum span" (a
// ranged read is clamped to this). ~200 lines ≈ one screenful.
const maxReadLines = 200

// ConfinePath is the door guard: it vets the path arg of every path-taking tool
// (outline, grep, read_file) exactly once, rejecting absolute paths and `..`
// escapes and requiring the resolved path stay within the workspace root. It is
// SEPARATE from remove_path's delete-protection: reads must ALLOW .cortex/journal
// (that's how journal recall works) — it lives inside the root, so "stays within
// root" admits it while still blocking escapes. A call with no path arg passes
// through untouched.
func ConfinePath(call ToolCall, root string) (ToolCall, error) {
	path, err := call.StringArg("path")
	if err != nil || strings.TrimSpace(path) == "" {
		return call, nil // no path to confine (e.g. grep with the default ".")
	}
	if filepath.IsAbs(path) {
		return call, fmt.Errorf("path %q must be relative to the workspace, not absolute", path)
	}
	if root == "" {
		root, _ = os.Getwd()
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return call, fmt.Errorf("resolve workspace root: %w", err)
	}
	abs := filepath.Join(rootAbs, filepath.Clean(path))
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return call, fmt.Errorf("path %q escapes the workspace", path)
	}
	return call, nil
}

// TargetedRead enforces the span contract on a read_file call (a no-op for every
// other tool). One constant, three behaviors:
//   - a no-range read of a file ≤ maxReadLines is left whole (small files read in
//     one shot);
//   - a no-range read of a larger file is REFUSED with a non-empty message (the
//     model must outline first, then read a span);
//   - a ranged read is clamped to maxReadLines, visibly (read_file's @path:a-b
//     header shows the actual window returned).
//
// The refusal is returned as the second value (empty when the call should
// proceed) so the dispatcher can short-circuit without executing.
func TargetedRead(call ToolCall) (ToolCall, string) {
	if call.Function.Name != FunctionReadFile {
		return call, ""
	}
	path, _ := call.StringArg("path")
	if start, ok := call.IntArg("start"); ok && start > 0 {
		end, hasEnd := call.IntArg("end")
		if !hasEnd || end < start || end-start+1 > maxReadLines {
			end = start + maxReadLines - 1
		}
		return withRange(call, path, start, end), ""
	}
	// No range: small files read whole, larger ones are refused.
	if n := countFileLines(path); n > maxReadLines {
		return call, fmt.Sprintf("%s is %d lines — too large to read whole. outline(%q) first, then read_file(%q, start, end) a span (≤%d lines).",
			path, n, path, path, maxReadLines)
	}
	return call, ""
}

// withRange rewrites a read_file call's arguments to the given clamped span.
func withRange(call ToolCall, path string, start, end int) ToolCall {
	args, err := json.Marshal(map[string]any{"path": path, "start": start, "end": end})
	if err != nil {
		return call
	}
	call.Function.Arguments = string(args)
	return call
}

// countFileLines counts the lines in a file (0 if unreadable). Used only for the
// small-file floor decision; the file is re-read by read_file if it proceeds.
func countFileLines(path string) int {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return 0
	}
	n := strings.Count(string(data), "\n")
	if data[len(data)-1] != '\n' {
		n++
	}
	return n
}
