// envelope.go — the uniform {ok, data, error, meta} shape every
// --json-emitting cortex command writes (axis 4, Result; see
// docs/tool-surface.md). Replaces the old per-command bare-payload
// shape: callers used to parse `{mode, results}` straight from
// stdout; now they parse `{ok, data:{mode, results}, ...}` and pull
// `data` out themselves.
//
// Why an envelope at all? Three things you can't bolt on after the
// fact: machine-readable error codes (axis 4), per-call trace_id so
// telemetry rows can be joined back to the invocation that produced
// them (axis 6), and a single canonical "did this succeed" flag so
// scripts don't have to parse stderr to know.

package cliout

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Envelope is the canonical contract for every --json output. Callers
// that previously parsed bare payloads now parse this and pull Data
// out — typed as json.RawMessage so each command keeps its own Data
// schema without forcing every consumer through a generic shape.
//
// On success Error is nil and Data carries the payload. On failure
// Data may be present (partial result) but Ok is false and Error
// carries the machine-readable code + message.
type Envelope struct {
	Ok    bool             `json:"ok"`
	Data  json.RawMessage  `json:"data,omitempty"`
	Error *EnvelopeError   `json:"error,omitempty"`
	Meta  EnvelopeMeta     `json:"meta"`
}

// EnvelopeError carries enough for callers to switch on Code without
// substring-matching Message. Code values stay stable; Message can
// evolve to reflect operator-friendly context.
type EnvelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// EnvelopeMeta carries axis-6 telemetry that's the same shape for
// every command. TraceID is unique per invocation; LatencyMs is wall
// time from when the command started measuring. Truncated signals
// that Data was capped by a size limit (callers should treat the
// result as a prefix, not a complete answer).
type EnvelopeMeta struct {
	TraceID   string `json:"trace_id"`
	LatencyMs int64  `json:"latency_ms"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Common error codes. Commands SHOULD reuse these where applicable so
// callers can write a finite switch. New codes need a docs/tool-surface.md
// update — adding one quietly is what creates a "two of every
// payload-shape" mess later.
const (
	ErrCodeInternal       = "internal_error"
	ErrCodeInvalidArgs    = "invalid_args"
	ErrCodeNotFound       = "not_found"
	ErrCodeBudgetExceeded = "budget_exceeded"
	ErrCodeUnavailable    = "unavailable" // upstream provider / index missing
	ErrCodeTimeout        = "timeout"
)

// NewTraceID returns a 16-byte random hex string. Long enough to be
// unique across a session's invocations without being so long it
// dominates short telemetry lines.
func NewTraceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Crypto failure means the runtime is so broken nothing
		// downstream matters. Fall back to a time-based id so we
		// still emit *something* parseable.
		return fmt.Sprintf("ts-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Emitter wraps the typical "command starts → runs → emits JSON"
// flow: construct with NewEmitter at the top of Execute, then call
// Ok, Fail, or OkTruncated when ready. LatencyMs is computed at
// emit time from the start timestamp.
//
// One Emitter per invocation. Don't reuse — TraceID would be wrong
// and LatencyMs would compound.
type Emitter struct {
	traceID    string
	start      time.Time
	projectDir string // for path redaction
	homeDir    string // for path redaction
}

// NewEmitter returns an emitter stamped with a fresh trace id. The
// projectDir argument scopes path redaction: any absolute path inside
// projectDir or the user's .cortex/ home dir survives unchanged; paths
// outside get redacted before encoding. Pass "" to disable scoping
// (only ~/.cortex/ survives).
func NewEmitter(projectDir string) *Emitter {
	home, _ := os.UserHomeDir()
	cortexHome := ""
	if home != "" {
		cortexHome = filepath.Join(home, ".cortex")
	}
	abs := projectDir
	if abs != "" {
		if resolved, err := filepath.Abs(abs); err == nil {
			abs = resolved
		}
	}
	return &Emitter{
		traceID:    NewTraceID(),
		start:      time.Now(),
		projectDir: abs,
		homeDir:    cortexHome,
	}
}

// TraceID returns the id stamped at construction. Telemetry callers
// use this to join the cell_results.jsonl row back to the envelope.
func (e *Emitter) TraceID() string { return e.traceID }

// Ok wraps data in a success envelope and writes it as a single JSON
// line to w. data is marshaled with the standard encoder; for raw
// payloads already serialized to JSON, pass json.RawMessage to skip
// the round-trip.
func (e *Emitter) Ok(w io.Writer, data any) error {
	return e.emit(w, true, data, nil, false)
}

// OkTruncated is Ok but sets meta.truncated to true. Use when the
// command capped a list/string at a size limit so callers know the
// payload is a prefix, not a complete answer.
func (e *Emitter) OkTruncated(w io.Writer, data any) error {
	return e.emit(w, true, data, nil, true)
}

// Fail wraps a structured error in a failure envelope. data is optional
// — pass nil when there's no partial result. Returned error is the
// write error (i.e. couldn't reach stdout), NOT the application
// error being reported.
func (e *Emitter) Fail(w io.Writer, code, message string, data any) error {
	return e.emit(w, false, data, &EnvelopeError{Code: code, Message: message}, false)
}

func (e *Emitter) emit(w io.Writer, ok bool, data any, errPayload *EnvelopeError, truncated bool) error {
	var raw json.RawMessage
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("marshal data: %w", err)
		}
		raw = e.redactPaths(b)
	}
	env := Envelope{
		Ok:    ok,
		Data:  raw,
		Error: errPayload,
		Meta: EnvelopeMeta{
			TraceID:   e.traceID,
			LatencyMs: time.Since(e.start).Milliseconds(),
			Truncated: truncated,
		},
	}
	enc := json.NewEncoder(w)
	return enc.Encode(env)
}

// redactPaths walks the marshaled JSON as text and rewrites absolute
// paths outside projectDir + ~/.cortex/ to a literal "<redacted>"
// token. Operates on the bytes (not the unmarshaled tree) so per-
// command Data shapes don't need to register a redaction list.
//
// Conservative: only rewrites paths that look like absolute /unix or
// C:\windows roots, never relative or short tokens. Reduces false
// positives when the payload contains user-supplied strings.
func (e *Emitter) redactPaths(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	s := string(b)
	if !strings.ContainsAny(s, `"/\`) {
		return b
	}

	// Walk string literals only; everything outside quotes is JSON
	// structural punctuation.
	var out strings.Builder
	out.Grow(len(s))
	inString := false
	escaped := false
	literalStart := -1
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !inString {
			if c == '"' {
				inString = true
				literalStart = i + 1
				out.WriteByte(c)
				continue
			}
			out.WriteByte(c)
			continue
		}
		// Inside a string literal.
		if escaped {
			escaped = false
			out.WriteByte(c)
			continue
		}
		if c == '\\' {
			escaped = true
			out.WriteByte(c)
			continue
		}
		if c == '"' {
			// Closing quote: redact the literal we just walked.
			literal := s[literalStart:i]
			out.WriteString(e.redactLiteral(literal))
			out.WriteByte('"')
			inString = false
			literalStart = -1
			continue
		}
	}
	if inString && literalStart >= 0 {
		// Unclosed string is malformed JSON; pass through unchanged.
		out.WriteString(s[literalStart:])
	}
	return []byte(out.String())
}

// redactLiteral inspects one string literal's contents. Returns the
// possibly-redacted form. Keep cheap: this runs on every Ok call.
func (e *Emitter) redactLiteral(s string) string {
	if len(s) < 2 {
		return s
	}
	if !looksLikeAbsolutePath(s) {
		return s
	}
	if e.allowed(s) {
		return s
	}
	return "<redacted>"
}

func looksLikeAbsolutePath(s string) bool {
	if len(s) == 0 {
		return false
	}
	if s[0] == '/' && len(s) > 1 && (s[1] != ' ' && s[1] != '/') {
		return true
	}
	// Windows-style C:\path
	if len(s) >= 3 && s[1] == ':' && (s[2] == '\\' || s[2] == '/') {
		first := s[0]
		if (first >= 'A' && first <= 'Z') || (first >= 'a' && first <= 'z') {
			return true
		}
	}
	return false
}

func (e *Emitter) allowed(path string) bool {
	if e.projectDir != "" && hasPrefix(path, e.projectDir) {
		return true
	}
	if e.homeDir != "" && hasPrefix(path, e.homeDir) {
		return true
	}
	// Also allow ".cortex/" anywhere in the path so per-workdir state
	// (used by benchmarks operating in tempdirs) doesn't get redacted.
	if strings.Contains(path, "/.cortex/") || strings.HasSuffix(path, "/.cortex") {
		return true
	}
	return false
}

// DecodeEnvelope is the consumer-side counterpart to Emitter.Ok/Fail.
// Reads one JSON envelope from the input, then unmarshals envelope.Data
// into out when out is non-nil. Returns an *EnvelopeError if the envelope
// reports failure (Ok=false) — callers can errors.As / type-switch to
// pull the structured Code out.
//
// Use this in benchmark runners + integrations instead of redefining
// the envelope shape locally. Keeps consumers in sync with the producer
// when fields are added.
func DecodeEnvelope(raw []byte, out any) (*Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	if !env.Ok {
		if env.Error != nil {
			return &env, env.Error
		}
		return &env, fmt.Errorf("envelope reports ok=false with no error payload")
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return &env, fmt.Errorf("decode data: %w", err)
		}
	}
	return &env, nil
}

// Error implements the error interface so EnvelopeError can be
// returned by consumers without an extra wrapper.
func (e *EnvelopeError) Error() string {
	if e == nil {
		return "<nil envelope error>"
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func hasPrefix(s, prefix string) bool {
	if len(prefix) == 0 {
		return false
	}
	if !strings.HasPrefix(s, prefix) {
		return false
	}
	if len(s) == len(prefix) {
		return true
	}
	return s[len(prefix)] == '/' || s[len(prefix)] == '\\'
}
