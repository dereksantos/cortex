package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dereksantos/cortex/pkg/llm"
)

// ToolHandler is implemented by each tool the loop exposes to the
// model. The args parameter is the model's raw JSON argument string;
// implementations are responsible for parsing it (and for returning a
// soft error string the loop can feed back to the model when the
// JSON is malformed, rather than aborting the run).
//
// Spec returns the OpenAI-format tool declaration that GenerateWithTools
// passes in the `tools` array.
type ToolHandler interface {
	Name() string
	Spec() llm.ToolSpec
	Call(ctx context.Context, args string) (output string, err error)
}

// ToolRegistry holds the set of tools available to a Loop. The
// dispatcher is lenient: a malformed args JSON, an unknown tool name,
// or any handler error becomes a structured tool-result message rather
// than a panic — the model gets to see the error and try again.
type ToolRegistry struct {
	tools map[string]ToolHandler
	// observedInjectedTokens accumulates bytes the cortex_search tool
	// returned. Divided by 4 to estimate tokens (matches the rough
	// proxy used elsewhere in this codebase).
	injectedContextBytes int
	// shellNonZeroExits counts run_shell calls that returned a non-zero
	// exit code. The harness reports this as CorrectionTurns on the
	// CellResult.
	shellNonZeroExits int
	// filesWritten accumulates write_file targets so the harness can
	// populate HarnessResult.FilesChanged.
	filesWritten map[string]bool
}

// NewToolRegistry constructs an empty registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools:        make(map[string]ToolHandler),
		filesWritten: make(map[string]bool),
	}
}

// Register adds a handler. Replaces any prior handler with the same
// name (the loop never relies on registration order, and replacement
// is useful for tests that swap real tools for fakes).
func (r *ToolRegistry) Register(h ToolHandler) {
	r.tools[h.Name()] = h
}

// Specs returns the tool declarations in registration-order-stable
// form (sorted by name) so two runs of the same scenario send the
// same `tools` array to OpenRouter — useful for caching and for
// deterministic transcripts.
func (r *ToolRegistry) Specs() []llm.ToolSpec {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	// Inline sort; this is called once per turn.
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	specs := make([]llm.ToolSpec, 0, len(names))
	for _, n := range names {
		specs = append(specs, r.tools[n].Spec())
	}
	return specs
}

// Dispatch resolves call.Function.Name and invokes the handler. The
// returned string is the content that should be sent back to the
// model in a role=tool message; the returned error is non-nil only
// when the call failed in a way the model should know about (the
// caller should still send the message — error messages are valid
// tool output).
//
// Name normalization: some open-weight models (notably
// openai/gpt-oss-20b:free on OpenRouter) leak chat-template tokens
// like "<|channel|>commentary" into the tool-name field. We strip
// any "<|...|>..." suffix and retry the lookup so these calls
// dispatch correctly instead of bouncing through "unknown tool" and
// burning turns.
func (r *ToolRegistry) Dispatch(ctx context.Context, call llm.ToolCall) (string, error) {
	name := normalizeToolName(call.Function.Name)
	h, ok := r.tools[name]
	if !ok {
		return fmt.Sprintf(`{"error":"unknown tool: %s"}`, name), errors.New("unknown tool")
	}

	// Lenient: pass raw args string through. Each handler parses on
	// its own and returns a structured error message if the JSON is
	// malformed.
	out, err := h.Call(ctx, call.Function.Arguments)

	// Track injected-context bytes for the cortex_search tool. We
	// match on Name() rather than type-asserting to keep the registry
	// decoupled from the specific tool implementations.
	if name == cortexSearchToolName {
		r.injectedContextBytes += len(out)
	}
	return out, err
}

// InjectedContextTokens returns a rough estimate of how many tokens
// the cortex_search tool surfaced to the model across the loop.
// Length/4 is the standard proxy used elsewhere in this codebase;
// over-counting whitespace is fine since the eval-grid contract
// requires reporting an upper bound, not a precise count.
func (r *ToolRegistry) InjectedContextTokens() int {
	return r.injectedContextBytes / 4
}

// ShellNonZeroExits returns the number of run_shell calls that ended
// with a non-zero exit. The runner reports this as CorrectionTurns.
func (r *ToolRegistry) ShellNonZeroExits() int {
	return r.shellNonZeroExits
}

// FilesWritten returns the deduplicated set of write_file targets the
// model touched, sorted for determinism. Paths are workdir-relative.
func (r *ToolRegistry) FilesWritten() []string {
	out := make([]string, 0, len(r.filesWritten))
	for k := range r.filesWritten {
		out = append(out, k)
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// noteFileWritten is called by the write_file tool after a successful
// write. Recording happens through the registry (not directly on the
// loop) so any future per-write_file accounting (size, etc) lives in
// one place.
func (r *ToolRegistry) noteFileWritten(relPath string) {
	r.filesWritten[relPath] = true
}

// noteShellExit is called by run_shell after the subprocess returns,
// with the exit code. Non-zero exits increment the correction-turn
// counter.
func (r *ToolRegistry) noteShellExit(exitCode int) {
	if exitCode != 0 {
		r.shellNonZeroExits++
	}
}

// parseJSONArgs is the helper each tool uses to decode its arguments
// blob. Returns a structured tool-error message ready to be sent back
// to the model when the JSON is malformed; the caller is expected to
// `return ..., nil` in that case so the loop keeps going.
func parseJSONArgs(args string, dst any) (errMsg string, ok bool) {
	if args == "" {
		args = "{}"
	}
	if err := json.Unmarshal([]byte(args), dst); err != nil {
		return fmt.Sprintf(`{"error":"invalid JSON arguments: %s"}`, escapeForJSONErrorMsg(err.Error())), false
	}
	return "", true
}

// normalizeToolName strips chat-template artifacts from tool names.
// Models like gpt-oss-20b emit `<|channel|>commentary` or similar
// after the real tool name; we drop everything from the first "<|"
// onward and trim whitespace. Idempotent; safe on clean names.
func normalizeToolName(s string) string {
	if i := strings.Index(s, "<|"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// escapeForJSONErrorMsg trims and escapes a Go error string for safe
// inclusion in a JSON tool-result. Avoids re-marshaling for a single
// field; the model treats the result as text anyway.
func escapeForJSONErrorMsg(s string) string {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch r {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\n', '\r', '\t':
			out = append(out, ' ')
		default:
			out = append(out, string(r)...)
		}
	}
	return string(out)
}
