// cli_test.go — `cortex turn` flag parsing (parseTurnArgs), isolated from a
// live session so the --plan switch (#150) and the legacy flags are covered
// by a table-driven test.
package main

import (
	"bytes"
	"reflect"
	"strings"
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

// TestTurnCLIReceiptRouting (issue #219): reportTurnText's non-JSON branch
// must print the turn's measurement receipt to stderr — the same surface as
// the test-loss / lint receipts (printTestReceipt / printLintReceipt) — and
// never to stdout (issue #118's answer-only contract: stdout holds exactly
// the reply). The receipt is a multi-line block (files changed / verification
// sections), so the routing under test is the receipt block's destination, not
// just the presence of a line.
func TestTurnCLIReceiptRouting(t *testing.T) {
	receipt := "files changed:\n  go.mod | 2 +\nverification:\n  test: go test ./... (exit 0, 1s)"
	res := TurnResult{Reply: "the answer", Receipt: receipt}

	var outBuf, errBuf bytes.Buffer
	reportTurnText(&outBuf, &errBuf, nil, res, "sess-1")

	stdout, stderr := outBuf.String(), errBuf.String()
	// issue #118: stdout is the reply only.
	if stdout != res.Reply+"\n" {
		t.Errorf("stdout = %q, want exactly the reply only (%q) — issue #118's answer-only contract", stdout, res.Reply+"\n")
	}
	// The receipt block rides on stderr, whole and intact.
	if !strings.Contains(stderr, receipt) {
		t.Errorf("stderr = %q, want the turn receipt %q printed (issue #219)", stderr, receipt)
	}
	if strings.Contains(stdout, "files changed") || strings.Contains(stdout, "verification:") {
		t.Errorf("the receipt leaked to stdout — it belongs on stderr (issue #118)\nstdout: %q", stdout)
	}
}

// TestTurnCLIReceiptRoutingNoReceipt is the companion case: a turn that
// measured nothing (empty receipt) prints no receipt block on stderr — the
// non-JSON branch must not emit a stray "turn receipt: " header. The
// session line (the only other stderr output reportTurnText can produce
// for a clean turn) is still present.
func TestTurnCLIReceiptRoutingNoReceipt(t *testing.T) {
	res := TurnResult{Reply: "the answer"}

	var outBuf, errBuf bytes.Buffer
	reportTurnText(&outBuf, &errBuf, nil, res, "sess-1")

	if outBuf.String() != res.Reply+"\n" {
		t.Errorf("stdout = %q, want exactly the reply only", outBuf.String())
	}
	stderr := errBuf.String()
	if strings.Contains(stderr, "files changed") || strings.Contains(stderr, "verification") {
		t.Errorf("stderr = %q, want no receipt block (no receipt measured)", stderr)
	}
	if stderr != "session: sess-1\n" {
		t.Errorf("stderr = %q, want exactly the session line (%q)", stderr, "session: sess-1\n")
	}
}

// TestReportTurnTextSummaryIssue (issue #230): a turn whose final reply the
// engine's sanitizer could not repair carries TurnResult.SummaryIssue, and
// reportTurnText (runTurnCLI's non-JSON branch) must print it as the stderr
// line "summary issue: <value>" — the headless surface a driver that feeds
// the reply into a commit message checks. The reply itself still goes to
// stdout (issue #118's answer-only contract); the flag is a stderr fact.
//
// The --json surface — runTurnCLI's out-map builder, turnJSON — is covered
// by TestTurnJSONSummaryIssue below.
func TestReportTurnTextSummaryIssue(t *testing.T) {
	res := TurnResult{Reply: "the reply", SummaryIssue: "truncated"}

	var outBuf, errBuf bytes.Buffer
	reportTurnText(&outBuf, &errBuf, nil, res, "sess-1")

	if outBuf.String() != "the reply\n" {
		t.Errorf("stdout = %q, want exactly the reply only", outBuf.String())
	}
	stderr := errBuf.String()
	if !strings.Contains(stderr, "summary issue: truncated\n") {
		t.Errorf("stderr = %q, want the line %q", stderr, "summary issue: truncated\n")
	}
	if strings.Contains(outBuf.String(), "summary issue") {
		t.Errorf("the summary-issue flag leaked to stdout — it belongs on stderr (issue #118)")
	}
}

// TestTurnJSONSummaryIssue (issue #230): turnJSON — the out-map builder of
// runTurnCLI's --json branch — carries a final reply the engine's sanitizer
// could not repair under the "summary_issue" key, and omits the key for a
// clean turn. The key is what the issue names: a commit-step driver reads
// `cortex turn --json` and checks it before writing the reply to git
// history.
func TestTurnJSONSummaryIssue(t *testing.T) {
	tests := []struct {
		name    string
		res     TurnResult
		want    string
		wantKey bool
	}{
		{name: "flagged truncated reply", res: TurnResult{Reply: "the reply", SummaryIssue: "truncated"}, want: "truncated", wantKey: true},
		{name: "clean reply omits the key", res: TurnResult{Reply: "the reply"}, want: "", wantKey: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := turnJSON(tt.res, "sess-1", nil)
			got, ok := out["summary_issue"].(string)
			if ok != tt.wantKey {
				if tt.wantKey {
					t.Fatalf("turnJSON out map has no summary_issue key, want %q", tt.want)
				}
				t.Fatalf("turnJSON out map has a summary_issue key = %v, want the key absent", got)
			}
			if tt.wantKey && got != tt.want {
				t.Errorf("summary_issue = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestReportTurnTextSummaryIssueAbsent is the companion case: a clean turn
// (no SummaryIssue) prints NO "summary issue:" line on stderr.
func TestReportTurnTextSummaryIssueAbsent(t *testing.T) {
	res := TurnResult{Reply: "the reply"}

	var outBuf, errBuf bytes.Buffer
	reportTurnText(&outBuf, &errBuf, nil, res, "sess-1")

	if outBuf.String() != "the reply\n" {
		t.Errorf("stdout = %q, want exactly the reply only", outBuf.String())
	}
	if strings.Contains(errBuf.String(), "summary issue:") {
		t.Errorf("stderr = %q, want no summary-issue line for a clean turn", errBuf.String())
	}
}
