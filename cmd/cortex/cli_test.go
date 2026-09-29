// cli_test.go — `cortex turn` flag parsing (parseTurnArgs), isolated from a
// live session so the --plan switch (#150) and the legacy flags are covered
// by a table-driven test.
package main

import (
	"reflect"
	"testing"
)

func TestParseTurnArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want turnArgs
	}{
		{
			name: "bare input",
			args: []string{"build the feature"},
			want: turnArgs{input: "build the feature"},
		},
		{
			name: "plan flag only",
			args: []string{"--plan", "do it"},
			want: turnArgs{plan: true, input: "do it"},
		},
		{
			name: "json flag only",
			args: []string{"--json", "do it"},
			want: turnArgs{asJSON: true, input: "do it"},
		},
		{
			name: "session long form",
			args: []string{"--session", "abc123", "hello"},
			want: turnArgs{sessionID: "abc123", input: "hello"},
		},
		{
			name: "session short form",
			args: []string{"-s", "abc123", "hello"},
			want: turnArgs{sessionID: "abc123", input: "hello"},
		},
		{
			name: "project flag",
			args: []string{"--project", "blog", "hello"},
			want: turnArgs{project: "blog", input: "hello"},
		},
		{
			name: "plan plus json plus project plus session, multi-word input",
			args: []string{"--plan", "--json", "--project", "blog", "--session", "abc", "build", "the", "feature"},
			want: turnArgs{
				sessionID: "abc",
				asJSON:    true,
				plan:      true,
				project:   "blog",
				input:     "build the feature",
			},
		},
		{
			name: "no args yields empty input (runTurnCLI then falls back to stdin)",
			args: nil,
			want: turnArgs{},
		},
		{
			name: "unknown flag falls through to input (historical behavior)",
			args: []string{"--bogus", "hello"},
			want: turnArgs{input: "--bogus hello"},
		},
		{
			name: "valueless flag at end of args does not consume the input",
			args: []string{"hello", "--session"},
			want: turnArgs{input: "hello"},
		},
		{
			name: "input is trimmed",
			args: []string{"  spaced out  "},
			want: turnArgs{input: "spaced out"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTurnArgs(tt.args)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseTurnArgs(%q) = %+v, want %+v", tt.args, got, tt.want)
			}
		})
	}
}
