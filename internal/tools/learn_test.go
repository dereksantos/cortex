package tools

import "testing"

// TestLearnProfileShape covers docs/learning-loop.md's Learn subagent
// profile: bounds are set (the mandatory runaway backstop, same invariant
// as Study/Agent — see their own tests' identical comment), DepthCap is 0
// (no recursion), and the profile's Role is "learn" so cmd/cortex's
// subagentBounds/subagentRequest can special-case it independently of
// "study"/"agent".
func TestLearnProfileShape(t *testing.T) {
	if Learn.Name != "learn" || Learn.Role != "learn" {
		t.Errorf("got Name=%q Role=%q, want both %q", Learn.Name, Learn.Role, "learn")
	}
	if Learn.Bounds.MaxTokens == 0 {
		t.Error("Learn.Bounds.MaxTokens is unset — mandatory runaway backstop, see Study/Agent")
	}
	if Learn.Bounds.MaxIter == 0 {
		t.Error("Learn.Bounds.MaxIter is unset")
	}
	if Learn.Bounds.ReadBudgetBytes == 0 {
		t.Error("Learn.Bounds.ReadBudgetBytes is unset")
	}
	if Learn.DepthCap != 0 {
		t.Errorf("Learn.DepthCap = %d, want 0 (no recursion)", Learn.DepthCap)
	}
}

// TestLearnNotCoderCallable covers the design decision (docs/learning-loop.md):
// unlike Study and Agent, Learn is never offered to the coder as a callable
// tool mid-conversation — its only entry points are `cortex learn` and the
// loop scheduler's kind:"learn" firing (both cmd/cortex), which call
// RunSubagent directly. It must therefore be absent from BOTH the shared
// subagent registry (Lookup) and the coder's declared toolset (All).
func TestLearnNotCoderCallable(t *testing.T) {
	if _, ok := Lookup("learn"); ok {
		t.Error(`Lookup("learn") found a registered profile — Learn must not be Register()'d (it is not coder-callable)`)
	}
	for _, tl := range All {
		if tl.Function.Name == "learn" {
			t.Error(`tools.All contains a "learn" tool declaration — Learn must not be offered to the coder`)
		}
	}
}

// TestLearnToolsetMatchesDesignDoc covers the exact toolset docs/learning-loop.md
// specifies: outline/grep/read_file/memory_read/memory_search/memory_write —
// read-only on the filesystem EXCEPT memory writes. No bash/write_file/
// edit_file (Learn never touches the filesystem beyond reading it), no
// memory_forget (retracting a note stays the coder's judgment call, not a
// background pass's), no recall/context_*/study/agent (session-scoped
// coder-only tools, same exclusion Study and Agent already apply).
func TestLearnToolsetMatchesDesignDoc(t *testing.T) {
	allow := AllowOf(Learn.Tools)

	want := []string{FunctionOutline, FunctionGrep, FunctionReadFile, FunctionMemoryRead, FunctionMemorySearch, FunctionMemoryWrite}
	if len(allow) != len(want) {
		t.Errorf("Learn toolset has %d entries, want exactly %d: %v", len(allow), len(want), want)
	}
	for _, name := range want {
		if !allow[name] {
			t.Errorf("Learn toolset missing %q", name)
		}
	}

	excluded := []string{
		FunctionBash, FunctionWriteFile, FunctionEditFile, FunctionRemove,
		FunctionMemoryForget, FunctionRecall,
		FunctionContextEvict, FunctionContextMerge, FunctionContextAdjustWatermarks,
		FunctionStudy, FunctionAgent,
	}
	for _, name := range excluded {
		if allow[name] {
			t.Errorf("Learn toolset should exclude %q (docs/learning-loop.md)", name)
		}
	}
}
