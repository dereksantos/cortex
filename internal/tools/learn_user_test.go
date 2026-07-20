package tools

import "testing"

// TestLearnUserProfileShape covers docs/cross-source-learning.md piece 1's
// promotion half: bounds are set (the mandatory runaway backstop, same
// invariant as Study/Agent/Learn), DepthCap is 0 (no recursion), and the
// profile's Role is "learn_user" so cmd/cortex's
// subagentBounds/subagentRequest can special-case it independently of
// "study"/"agent"/"learn".
func TestLearnUserProfileShape(t *testing.T) {
	if LearnUser.Name != "learn_user" || LearnUser.Role != "learn_user" {
		t.Errorf("got Name=%q Role=%q, want both %q", LearnUser.Name, LearnUser.Role, "learn_user")
	}
	if LearnUser.Bounds.MaxTokens == 0 {
		t.Error("LearnUser.Bounds.MaxTokens is unset — mandatory runaway backstop, see Study/Agent/Learn")
	}
	if LearnUser.Bounds.MaxIter == 0 {
		t.Error("LearnUser.Bounds.MaxIter is unset")
	}
	if LearnUser.Bounds.ReadBudgetBytes == 0 {
		t.Error("LearnUser.Bounds.ReadBudgetBytes is unset")
	}
	if LearnUser.DepthCap != 0 {
		t.Errorf("LearnUser.DepthCap = %d, want 0 (no recursion)", LearnUser.DepthCap)
	}
}

// TestLearnUserNotCoderCallable mirrors TestLearnNotCoderCallable: LearnUser
// is never offered to the coder as a callable tool mid-conversation — its
// only entry points are `cortex learn --user` and the loop scheduler's
// kind:"learn"+scope:"user" firing (both cmd/cortex/learn_user.go), which
// call RunSubagent directly.
func TestLearnUserNotCoderCallable(t *testing.T) {
	if _, ok := Lookup("learn_user"); ok {
		t.Error(`Lookup("learn_user") found a registered profile — LearnUser must not be Register()'d (it is not coder-callable)`)
	}
	for _, tl := range All {
		if tl.Function.Name == "learn_user" {
			t.Error(`tools.All contains a "learn_user" tool declaration — LearnUser must not be offered to the coder`)
		}
	}
}

// TestLearnUserToolsetMatchesDesignDoc covers the exact toolset
// docs/cross-source-learning.md piece 1 specifies: memory_read/
// memory_search/memory_write ONLY — no filesystem tools at all (unlike
// Learn, LearnUser never needs outline/grep/read_file: its input is
// candidate note bodies handed to it in the seed, not code), no
// memory_forget (retraction stays a human/coder judgment call), no
// recall/context_*/study/agent (session-scoped coder-only tools).
func TestLearnUserToolsetMatchesDesignDoc(t *testing.T) {
	allow := AllowOf(LearnUser.Tools)

	want := []string{FunctionMemoryRead, FunctionMemorySearch, FunctionMemoryWrite}
	if len(allow) != len(want) {
		t.Errorf("LearnUser toolset has %d entries, want exactly %d: %v", len(allow), len(want), want)
	}
	for _, name := range want {
		if !allow[name] {
			t.Errorf("LearnUser toolset missing %q", name)
		}
	}

	excluded := []string{
		FunctionBash, FunctionWriteFile, FunctionEditFile, FunctionRemove,
		FunctionOutline, FunctionGrep, FunctionReadFile,
		FunctionMemoryForget, FunctionRecall,
		FunctionContextEvict, FunctionContextMerge, FunctionContextAdjustWatermarks,
		FunctionStudy, FunctionAgent,
	}
	for _, name := range excluded {
		if allow[name] {
			t.Errorf("LearnUser toolset should exclude %q (docs/cross-source-learning.md piece 1)", name)
		}
	}
}
