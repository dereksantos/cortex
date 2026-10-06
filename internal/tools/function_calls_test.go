// function_calls_test.go — issue #132's text-form tool-call recovery, at the
// parser level: ParseFunctionCallsTags (Hermes-style <function_calls> JSON
// tags, the shapes an open model actually emits), StripToolMarkup (the
// display strip, closed or orphaned tags), and the edit_file identical
// old/new no-op message (the stuck-hint class the loop's redirect keys on).
package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestParseFunctionCallsTags covers every shape ParseFunctionCallsTags
// tolerates (and rejects) — the parser's contract is "recover what an open
// model meant, and never sink the parse on trailing prose, a second tag, or
// a truncated closer."
func TestParseFunctionCallsTags(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantNil bool
		want    []struct {
			id   string
			name string
			args string
		}
	}{
		{
			name:    "closed tag, one call",
			content: `Let me look. <function_calls>{"name":"read_file","arguments":{"path":"f"}}</function_calls>`,
			want: []struct {
				id   string
				name string
				args string
			}{{id: "fc-1", name: "read_file", args: `{"path":"f"}`}},
		},
		{
			name:    "truncated tag, no closer",
			content: `Checking the config. <function_calls>{"name":"bash","arguments":{"command":"ls"}}`,
			want: []struct {
				id   string
				name string
				args string
			}{{id: "fc-1", name: "bash", args: `{"command":"ls"}`}},
		},
		{
			name:    "trailing prose with a brace",
			content: `<function_calls>{"name":"grep","arguments":{"pattern":"x"}}</function_calls> done — the } above is part of the prose, not JSON.`,
			want: []struct {
				id   string
				name string
				args string
			}{{id: "fc-1", name: "grep", args: `{"pattern":"x"}`}},
		},
		{
			name:    "two objects in one block",
			content: `<function_calls>{"name":"read_file","arguments":{"path":"a"}}{"name":"read_file","arguments":{"path":"b"}}</function_calls>`,
			want: []struct {
				id   string
				name string
				args string
			}{
				{id: "fc-1", name: "read_file", args: `{"path":"a"}`},
				{id: "fc-2", name: "read_file", args: `{"path":"b"}`},
			},
		},
		{
			name:    "JSON array",
			content: `<function_calls>[{"name":"read_file","arguments":{"path":"a"}},{"name":"grep","arguments":{"pattern":"y"}}]</function_calls>`,
			want: []struct {
				id   string
				name string
				args string
			}{
				{id: "fc-1", name: "read_file", args: `{"path":"a"}`},
				{id: "fc-2", name: "grep", args: `{"pattern":"y"}`},
			},
		},
		{
			name:    "two separate tags",
			content: `<function_calls>{"name":"read_file","arguments":{"path":"a"}}</function_calls> now this: <function_calls>{"name":"grep","arguments":{"pattern":"y"}}</function_calls>`,
			want: []struct {
				id   string
				name string
				args string
			}{
				{id: "fc-1", name: "read_file", args: `{"path":"a"}`},
				{id: "fc-2", name: "grep", args: `{"pattern":"y"}`},
			},
		},
		{
			name:    "singular function_call tag",
			content: `<function_call>{"name":"bash","arguments":{"command":"ls"}}</function_call>`,
			want: []struct {
				id   string
				name string
				args string
			}{{id: "fc-1", name: "bash", args: `{"command":"ls"}`}},
		},
		{
			name:    "missing arguments becomes {}",
			content: `<function_calls>{"name":"bash"}</function_calls>`,
			want: []struct {
				id   string
				name string
				args string
			}{{id: "fc-1", name: "bash", args: `{}`}},
		},
		{
			name:    "null arguments becomes {}",
			content: `<function_calls>{"name":"bash","arguments":null}</function_calls>`,
			want: []struct {
				id   string
				name string
				args string
			}{{id: "fc-1", name: "bash", args: `{}`}},
		},
		{
			name:    "malformed JSON is not a call",
			content: `<function_calls>{"name": "bash",</function_calls>`,
			wantNil: true,
		},
		{
			name:    "plain prose is not a call",
			content: "All done — the tests pass.",
			wantNil: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseFunctionCallsTags(tc.content)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("ParseFunctionCallsTags = %+v, want nil", got)
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d calls, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				if got[i].ID != w.id || got[i].Function.Name != w.name {
					t.Errorf("call %d = %s %q, want %s %q", i+1, got[i].ID, got[i].Function.Name, w.id, w.name)
				}
				// Arguments is the JSON-encoded shape the wire uses; the parser
				// re-marshals the arguments object, so compare decoded objects
				// (key order is not the contract).
				var g, wa map[string]any
				if err := json.Unmarshal([]byte(got[i].Function.Arguments), &g); err != nil {
					t.Fatalf("call %d arguments %q are not a JSON object: %v", i+1, got[i].Function.Arguments, err)
				}
				if err := json.Unmarshal([]byte(w.args), &wa); err != nil {
					t.Fatalf("want arguments %q: %v", w.args, err)
				}
				if len(g) != len(wa) {
					t.Errorf("call %d arguments = %v, want %v", i+1, g, wa)
				} else {
					for k, v := range wa {
						if g[k] != v {
							t.Errorf("call %d argument %q = %v, want %v", i+1, k, g[k], v)
						}
					}
				}
			}
		})
	}
}

// TestStripToolMarkup covers the display strip: closed and orphaned (truncated)
// <function_calls> tags — with surrounding prose kept, the raw markup never
// leaking into the print path.
func TestStripToolMarkup(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		keep []string // substrings that MUST survive
		drop []string // substrings that must NOT survive
	}{
		{
			name: "closed tag stripped, prose kept",
			in:   `Let me look.<function_calls>{"name":"read_file","arguments":{"path":"f"}}</function_calls> Now I know.`,
			want: "Let me look. Now I know.",
			keep: []string{"Let me look.", "Now I know."},
			drop: []string{"<function_calls>", "read_file"},
		},
		{
			name: "orphaned (truncated) tag: opener and everything after dropped",
			in:   `Checking. <function_calls>{"name":"bash","arguments":{"command":"go test ./..."}}`,
			want: "Checking.",
			keep: []string{"Checking."},
			drop: []string{"<function_calls>", "go test"},
		},
		{
			name: "plain prose untouched",
			in:   "All done.",
			want: "All done.",
			keep: []string{"All done."},
			drop: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StripToolMarkup(tc.in)
			if got != tc.want {
				t.Errorf("StripToolMarkup = %q, want %q", got, tc.want)
			}
			for _, sub := range tc.keep {
				if !strings.Contains(got, sub) {
					t.Errorf("prose %q must survive; got %q", sub, got)
				}
			}
			for _, sub := range tc.drop {
				if strings.Contains(got, sub) {
					t.Errorf("markup %q must not leak; got %q", sub, got)
				}
			}
		})
	}
}

// TestEditFileIdenticalOldNewReturnsReReadMessage pins the no-op edit
// message: old_string == new_string is a no-op, and the error the model
// receives says so AND steers it to re-read the span — the exact class the
// loop's stuck-hint redirect keys on ("identical; nothing to change" → the
// "change is most likely ALREADY APPLIED" off-ramp).
func TestEditFileIdenticalOldNewReturnsReReadMessage(t *testing.T) {
	path := seedEditFile(t, "f.go", "package main\nfunc f() int { return 1 }\n")

	_, _, err := Execute(context.Background(), editArgs(t, map[string]any{
		"path":       path,
		"old_string": "return 1",
		"new_string": "return 1",
	}), headlessDeps{})
	if err == nil {
		t.Fatal("identical old/new edit should error, got none")
	}
	got := err.Error()
	for _, sub := range []string{"identical; nothing to change", "Re-read the span"} {
		if !strings.Contains(got, sub) {
			t.Errorf("error should contain %q; got: %q", sub, got)
		}
	}
}
