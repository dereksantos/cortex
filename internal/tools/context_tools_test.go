package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/agent"
)

// fakeContextDeps embeds headlessDeps for everything Execute needs and
// overrides just the context-tool seams — a minimal double, not a session.
type fakeContextDeps struct {
	headlessDeps
	recalled   string
	digest     string
	removed    map[string]bool
	mergedTo   string
	mergeErr   error
	outlineLen int
	disabled   map[string]bool
	rejectMsg  string
}

func (f *fakeContextDeps) Recall(citation string) (string, error) {
	if f.recalled == "" {
		return "", errors.New("no such citation")
	}
	return f.recalled, nil
}

func (f *fakeContextDeps) SummarizeText(_ context.Context, content, goal string, budget int) (string, bool, error) {
	return f.digest, true, nil
}

func (f *fakeContextDeps) RemoveOutlineEntry(citation string) bool {
	return f.removed[citation]
}

func (f *fakeContextDeps) MergeOutlineEntries(start, end string) (string, error) {
	if f.mergeErr != nil {
		return "", f.mergeErr
	}
	return f.mergedTo, nil
}

func (f *fakeContextDeps) OutlineLen() int { return f.outlineLen }

func (f *fakeContextDeps) AdjustWatermarks(highDelta, lowDelta int) (int, int, int, int, error) {
	return 1000, 600, 1000 + highDelta, 600 + lowDelta, nil
}

func (f *fakeContextDeps) IsToolEnabled(name string) bool { return !f.disabled[name] }

func (f *fakeContextDeps) ValidateToolCall(tc ToolCall) (bool, string) {
	if f.rejectMsg != "" {
		return false, f.rejectMsg
	}
	return true, ""
}

func contextCall(name, args string) agent.ToolCall {
	return agent.ToolCall{Function: agent.FunctionCall{Name: name, Arguments: args}}
}

func TestContextToolsExecute(t *testing.T) {
	const citation = "@session/20260701-143210#m12-19"

	tests := []struct {
		name    string
		call    agent.ToolCall
		deps    *fakeContextDeps
		want    []string // substrings the observation must contain
		wantErr string   // substring the error must contain ("" = no error)
	}{
		// "SHOULD NOT APPEAR" digests guard the paths that must skip the summarizer.
		{
			name: "recall with budget digests and keeps the citation",
			call: contextCall(FunctionRecall, `{"citation":"`+citation+`","budget":8}`),
			deps: &fakeContextDeps{recalled: strings.Repeat("raw turn text ", 20), digest: "the digest"},
			want: []string{"digest of " + citation, "the digest", "[" + citation + "]"}, // digest lacks the citation → appended mechanically
		},
		{
			name: "recall with budget passes through content already within it",
			call: contextCall(FunctionRecall, `{"citation":"`+citation+`","budget":100}`),
			deps: &fakeContextDeps{recalled: "short raw", digest: "SHOULD NOT APPEAR"},
			want: []string{"short raw"},
		},
		{
			name: "recall without budget returns the raw messages",
			call: contextCall(FunctionRecall, `{"citation":"`+citation+`"}`),
			deps: &fakeContextDeps{recalled: strings.Repeat("raw turn text ", 20), digest: "SHOULD NOT APPEAR"},
			want: []string{"raw turn text"},
		},
		{
			name: "evict reports a removed entry",
			call: contextCall(FunctionContextEvict, `{"citation":"`+citation+`"}`),
			deps: &fakeContextDeps{removed: map[string]bool{citation: true}, outlineLen: 4},
			want: []string{"evicted", "4 entries"},
		},
		{
			name: "evict reports a missing entry without erroring",
			call: contextCall(FunctionContextEvict, `{"citation":"`+citation+`"}`),
			deps: &fakeContextDeps{},
			want: []string{"not found"},
		},
		{
			name: "merge reports the spanning citation",
			call: contextCall(FunctionContextMerge, `{"range_start":"`+citation+`","range_end":"@session/20260701-143210#m30-42"}`),
			deps: &fakeContextDeps{mergedTo: "@session/20260701-143210#m12-42", outlineLen: 2},
			want: []string{"@session/20260701-143210#m12-42", "2 entries"},
		},
		{
			name:    "merge surfaces seam errors",
			call:    contextCall(FunctionContextMerge, `{"range_start":"a","range_end":"b"}`),
			deps:    &fakeContextDeps{mergeErr: errors.New("start citation a not found")},
			wantErr: "merge failed",
		},
		{
			name:    "merge requires both citations",
			call:    contextCall(FunctionContextMerge, `{"range_start":"`+citation+`"}`),
			deps:    &fakeContextDeps{},
			wantErr: "range_end is required",
		},
		{
			name: "adjust watermarks reports old and new values",
			call: contextCall(FunctionContextAdjustWatermarks, `{"high_delta":100,"low_delta":-50}`),
			deps: &fakeContextDeps{},
			want: []string{"1000→1100", "600→550"},
		},
		{
			name: "disabled tool is refused via config",
			call: contextCall(FunctionContextEvict, `{"citation":"`+citation+`"}`),
			deps: &fakeContextDeps{disabled: map[string]bool{FunctionContextEvict: true}},
			want: []string{"disabled in .cortex/config.json"},
		},
		{
			name: "validator rejection becomes the observation",
			call: contextCall(FunctionContextAdjustWatermarks, `{"high_delta":999999,"low_delta":0}`),
			deps: &fakeContextDeps{rejectMsg: "high_delta 999999 is out of bounds"},
			want: []string{"out of bounds"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Execute(context.Background(), tt.call, tt.deps)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Execute error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			for _, w := range tt.want {
				if !strings.Contains(out, w) {
					t.Errorf("observation %q missing %q", out, w)
				}
			}
			if strings.Contains(out, "SHOULD NOT APPEAR") {
				t.Errorf("observation %q includes a digest from a path that must not summarize", out)
			}
		})
	}
}

// The headless (no-session) path must refuse the stateful context tools
// rather than pretend to act.
func TestContextToolsHeadless(t *testing.T) {
	for name, args := range map[string]string{
		FunctionContextMerge:            `{"range_start":"@session/x#m1-2","range_end":"@session/x#m3-4"}`,
		FunctionContextAdjustWatermarks: `{"high_delta":10,"low_delta":10}`,
	} {
		if _, err := Execute(context.Background(), contextCall(name, args), nil); err == nil {
			t.Errorf("%s with no session should error", name)
		}
	}
	// Evict degrades gracefully: nothing to remove, honest "not found".
	out, err := Execute(context.Background(), contextCall(FunctionContextEvict, `{"citation":"@session/x#m1-2"}`), nil)
	if err != nil || !strings.Contains(out, "not found") {
		t.Errorf("headless evict = (%q, %v), want a not-found observation", out, err)
	}
}
