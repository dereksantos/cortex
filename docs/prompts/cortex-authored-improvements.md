# cmd/loop — Code Quality, Design, Stability & Quality Improvements

A review of the AI coding harness in `cmd/loop` identifying improvements across code quality, code design, and general stability/quality.

---

## 1. Code Quality / Readability

### A. Extract the main loop into smaller functions

`main()` is roughly 200+ lines with command dispatch, the REPL loop, and turn processing all tangled together. Extract:

- `handleCommands(session, input)` — the `/quit`, `/clear`, `/compact`, `/remember`, `/model` dispatch
- `runTurn(session, input)` — the full turn: retrieval → resolve → capture → distill → compact

This would make `main()` a clean `parse → dispatch → loop` and each sub-function independently testable.

### B. The `resolveBinding` method is long and does too much

At ~50 lines it handles: role policy defaults, config overrides, endpoint/key inheritance, discovery selection, thinking policy, and fleet overlay. Consider splitting into:

- `resolveModel(role, config, fleet)` — model name selection
- `resolveEndpoint(role, config, fleet)` — endpoint resolution
- `applyRolePolicy(spec, role)` — thinking off, etc.

### C. Group magic numbers into typed blocks

Several constants are well-documented but could be grouped more logically:

- `maxToolOutput = 10000`, `maxToolIterations = 100`, `maxRepeatedToolCalls = 3` — "inner loop guards"
- `retrievalLimit = 5`, `retrievedContentCap = 240`, `captureExcerptCap = 280` — "retrieval/capture sizing"
- `distillRecentInsights = 20`, `maxInstructionBytes = 16384` — "context budgeting"

Group them in typed blocks with a shared comment header.

### D. `tokenizeCommand` is complex but isolated

This 80+ line function is the most algorithmically complex in the file. It's well-tested, but consider extracting the state machine into a separate `cmdTokenizer` type with `Next() string` to make the logic more declarative.

---

## 2. Code Design

### E. `CortexSession` is a god struct

It carries ~25 fields spanning: CLI args, HTTP request state, model specs, fleet metadata, transcript management, retrieval/capture state, distillation state, and session metrics. Consider:

- A `Transcript` type that owns `transcript`, `SessionID`, and all transcript methods
- A `Retrieval` type that owns `retriever`, `store`, `capturer`
- A `Distillation` type that owns `distillMu`, `pendingTurns`, `distillCancel`, `distillDone`
- `CortexSession` would then compose these, making the field count manageable and the responsibilities clear.

### F. Tool definitions are global vars

`readFile`, `writeFile`, `editFile`, `studyTool`, `bash`, and `tools` are all package-level `var`s. They're immutable after init, so `const`-like, but they're built with function calls. Consider a `func makeTools() []Tool` called once at init, or scoped to the request lifecycle.

### G. `AgentRequest` mixes transport fields with request body

`BaseURL`, `APIKey`, and `EphemeralSystem` are tagged `json:"-"` — they're transport, not payload. They live on the same struct as `Messages`, `Model`, `Temperature`. Consider:

```go
type AgentRequest struct {
    Model       string
    Messages    []Message
    Temperature float64
    Tools       []Tool
}

type HTTPRequest struct {
    *AgentRequest
    BaseURL           string
    APIKey            string
    EphemeralSystem   string
}
```

This makes the wire contract explicit vs. the transport layer.

### H. The `Spinner` type could be cleaner

The `sync.Mutex` for stdout serialization is the right call. However, `NewSpinner()` returns a bare `*Spinner` with no initialization — the fields are lazily set by `Start()`. Consider making `Start()` the constructor, or using a package-level `spinnerMu` since there's only ever one spinner running at a time.

---

## 3. Stability

### I. `httpClient` is a package-level singleton with a fixed timeout

```go
var httpClient = &http.Client{Timeout: requestTimeout}
```

This means:
- All requests share one connection pool (fine for local, problematic for multi-backend)
- The timeout is the same for discovery (`4s`), model calls (`10min`), and study (`10min`)
- No per-request timeout control

Consider making `httpClient` a method on `CortexSession` or passing it explicitly, so different operations can have different timeouts.

### J. `discoverFleet` uses the shared `httpClient`

The fleet discovery has its own `fleetDiscoveryTimeout` context, but it uses the shared `httpClient` which has a 10-minute timeout. If the backend is slow to respond (not unreachable, just slow), the discovery could hang for a long time before the context deadline fires. The context timeout should also apply to the HTTP client's dial timeout.

### K. `os.ReadDir` error handling in `latestSessionID`

If the sessions directory doesn't exist, `os.ReadDir` returns an error that gets wrapped as "no sessions at ...". The error message is misleading — it's not that there are no sessions, it's that the directory doesn't exist. A more specific check would help.

### L. `loadTranscript` uses `strings.Split` instead of a line scanner

```go
for i, line := range strings.Split(string(data), "\n") {
```

For large transcripts (thousands of turns), this loads the entire file into memory as a string, then splits it. A `bufio.Scanner` would be more memory-efficient and idiomatic.

### M. `distillPending` has an infinite loop with no backoff

```go
func (cs *CortexSession) distillPending(ctx context.Context) {
    for {
        // ... process one turn ...
    }
}
```

If the reasoner is consistently failing (not transient, but always returning errors), this loop will spin rapidly through pending turns. Consider a small sleep or at least a check that we're not burning CPU on a consistently broken distillation path.

---

## 4. Testing Quality

### N. No integration test for the full Resolve loop

The `TestResolveAccumulatesTokens` test uses a mock server but only tests a single-turn, no-tool-call path. The full inner loop (send → tool calls → run tools → re-send) is tested indirectly via `TestResolveStopsRepeatedToolCalls`, but a happy-path integration test that exercises the full tool-call cycle would be valuable.

### O. `TestSendHonorsContextCancel` is fragile

It relies on timing (`50ms` timeout) and a blocking server. The test could pass or fail based on system load. Consider using `context.WithCancel` and canceling from a goroutine after a short delay, which is more deterministic.

### P. Missing test for `parseXMLToolCalls` with malformed XML

The regex-based parser handles well-formed XML tool calls but doesn't have tests for edge cases like nested `<function>` tags, empty parameter values, or XML with attributes.

---

## 5. General Quality

### Q. Typos in `SystemPrompt`

```go
const SystemPrompt = `Your are cortex, a coding agent focused on a continous quality improvement...`
```

"Your" → "You're" (or "You are"), "continous" → "continuous". These are in the system prompt that gets sent to the model, so they affect behavior.

### R. `compactNow` creates a new context for each compaction

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
```

This is called from three places (manual `/compact`, red-gauge auto-trigger, overflow recovery). Each creates a fresh signal context. This is correct but could be a method on `CortexSession` to make the intent clearer.

### S. `sessionSummary()` uses `time.Since` at exit

The summary is printed after `stopDistill()` waits for distillation to complete. If distillation takes a long time, the session duration will be inflated. Consider recording `sessionEnd` at the last user turn, not at exit.

### T. The `bashAllowlist` could be a typed set

This is minor, but a typed `CommandSet` with an `Add()` method and a `Contains()` method would make the intent clearer and allow for future validation (e.g., rejecting dangerous flags).

---

## Summary — Prioritized

| Priority | Item | Impact |
|----------|------|--------|
| **High** | A: Extract main loop | Readability, testability |
| **High** | E: Split CortexSession | Cohesion, maintainability |
| **High** | Q: Fix SystemPrompt typos | Model behavior |
| **Medium** | B: Split resolveBinding | Readability |
| **Medium** | L: Scanner for loadTranscript | Memory for large sessions |
| **Medium** | M: Backoff in distillPending | CPU stability |
| **Medium** | I: Per-operation HTTP client | Reliability |
| **Low** | C: Group constants | Readability |
| **Low** | F: Scope tool definitions | Cleanliness |
| **Low** | G: Separate transport from request | API clarity |

The highest-impact changes would be extracting the main loop (A) and splitting the `CortexSession` struct (E), as these would unlock better testability and make the remaining improvements easier to implement. The typo fix (Q) is trivial but important since it affects the model's behavior directly.
