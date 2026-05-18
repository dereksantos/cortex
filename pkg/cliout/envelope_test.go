package cliout

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestEnvelopeOkShape verifies that a successful emit produces the
// {ok:true, data:..., meta:{trace_id, latency_ms}} canonical shape.
func TestEnvelopeOkShape(t *testing.T) {
	e := NewEmitter("")
	var buf bytes.Buffer
	if err := e.Ok(&buf, map[string]any{"hello": "world"}); err != nil {
		t.Fatalf("Ok: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v (raw: %s)", err, buf.String())
	}
	if !env.Ok {
		t.Errorf("Ok=false, want true")
	}
	if env.Error != nil {
		t.Errorf("Error=%+v, want nil", env.Error)
	}
	if env.Meta.TraceID == "" {
		t.Errorf("TraceID empty")
	}
	if env.Meta.LatencyMs < 0 {
		t.Errorf("LatencyMs=%d, want >= 0", env.Meta.LatencyMs)
	}
	var data map[string]string
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if data["hello"] != "world" {
		t.Errorf("data[hello]=%q, want world", data["hello"])
	}
}

// TestEnvelopeFailShape verifies error envelopes carry code + message
// and never claim ok=true.
func TestEnvelopeFailShape(t *testing.T) {
	e := NewEmitter("")
	var buf bytes.Buffer
	if err := e.Fail(&buf, ErrCodeInvalidArgs, "missing --workdir", nil); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Ok {
		t.Errorf("Ok=true on failure")
	}
	if env.Error == nil {
		t.Fatal("Error=nil on failure")
	}
	if env.Error.Code != ErrCodeInvalidArgs {
		t.Errorf("Code=%q, want %q", env.Error.Code, ErrCodeInvalidArgs)
	}
	if env.Error.Message != "missing --workdir" {
		t.Errorf("Message=%q", env.Error.Message)
	}
}

// TestEnvelopeTruncatedFlag verifies the meta.truncated marker is set
// when the command caps a result.
func TestEnvelopeTruncatedFlag(t *testing.T) {
	e := NewEmitter("")
	var buf bytes.Buffer
	if err := e.OkTruncated(&buf, []int{1, 2, 3}); err != nil {
		t.Fatalf("OkTruncated: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !env.Meta.Truncated {
		t.Errorf("Truncated=false, want true")
	}
}

// TestEnvelopeTraceIDUnique guards against the trace id collapsing to a
// constant (would break telemetry joining).
func TestEnvelopeTraceIDUnique(t *testing.T) {
	a := NewEmitter("")
	b := NewEmitter("")
	if a.TraceID() == b.TraceID() {
		t.Errorf("trace ids collided: %q", a.TraceID())
	}
	if len(a.TraceID()) < 16 {
		t.Errorf("trace id too short: %q", a.TraceID())
	}
}

// TestEnvelopeRedactPathsOutsideProject verifies absolute paths outside
// projectDir get rewritten to <redacted>. Paths inside the project root
// or ~/.cortex/ survive intact (callers need them for diagnostics).
func TestEnvelopeRedactPathsOutsideProject(t *testing.T) {
	e := NewEmitter("/repo")
	var buf bytes.Buffer
	if err := e.Ok(&buf, map[string]string{
		"in_repo":   "/repo/internal/foo.go",
		"outside":   "/etc/passwd",
		"in_cortex": "/some/path/.cortex/db/x.json",
		"relative":  "internal/foo.go",
		"home_dot":  ".cortex/journal/x",
	}); err != nil {
		t.Fatalf("Ok: %v", err)
	}
	s := buf.String()
	if !strings.Contains(s, "/repo/internal/foo.go") {
		t.Errorf("in_repo path redacted unexpectedly: %s", s)
	}
	if strings.Contains(s, "/etc/passwd") {
		t.Errorf("outside path NOT redacted: %s", s)
	}
	// json.Encoder HTML-escapes `<` and `>` to < / > by default.
	if !strings.Contains(s, "\\u003credacted\\u003e") {
		t.Errorf("no redaction marker: %s", s)
	}
	if !strings.Contains(s, ".cortex/db/x.json") {
		t.Errorf(".cortex/ path redacted unexpectedly: %s", s)
	}
	if !strings.Contains(s, "internal/foo.go") {
		t.Errorf("relative path redacted unexpectedly: %s", s)
	}
}

// TestEnvelopeRedactDoesntBreakStructure verifies redaction leaves the
// JSON parseable (we walk string literals, not the parsed tree).
func TestEnvelopeRedactDoesntBreakStructure(t *testing.T) {
	e := NewEmitter("/repo")
	var buf bytes.Buffer
	if err := e.Ok(&buf, map[string]any{
		"nested": map[string]string{"deep": "/var/log/something"},
		"list":   []string{"/etc/x", "/repo/y", "plain"},
	}); err != nil {
		t.Fatalf("Ok: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("envelope still parses after redaction: %v\nraw: %s", err, buf.String())
	}
	if !env.Ok {
		t.Errorf("Ok=false after redaction")
	}
}
