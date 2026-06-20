# Refactoring `cmd/loop/main.go` — Technical Specification

## 1. Problem Statement

`cmd/loop/main.go` is ~39,000 lines (~156 KB) and serves as the single entry point for the `loop` CLI. It bundles:

- **Model configuration & fleet discovery** — `ModelSpec`, `ModelInfo`, `Fleet`, `Config`, `Backend`, role-based model selection, HTTP metadata discovery
- **Tool execution** — `EditFile`, `RemovePath`, `ReadFile`, `WriteFile`, `RunShell`, `StudyFile`, `Search`, plus path confinement, tool call parsing, repetition detection
- **Session management** — `CortexSession` struct with 30+ methods: transcript lifecycle, retrieval, capture/distillation, compaction, metrics, session listing
- **Agent loop** — `Turn`, `Resolve`, `runToolCalls`, `runAnchoredTurn`, `streamPrinter`, `reasoningTail`
- **Shell output handling** — `spillShellOutput`, `studyShellOutput`
- **CLI subcommands** — `runTurnCLI`, `runStudyCLI`, `compactNow`, interactive REPL with `/remember`, `/compact`, `/sessions`, `/clear`
- **Output rendering** — `streamPrinter`, `markdownRenderer`, `splitBlocks`, ANSI trimming (already partially extracted to `render.go`)
- **Constants & utilities** — `maxRepeatedToolCalls`, `maxToolOutput`, `compactThreshold`, `rolePolicies`, `isDuplicateInsight`, `normalizeInsight`

The file violates the single-responsibility principle, impedes testability, and makes incremental changes risky.

## 2. Target Architecture

Extract into a `cmd/loop/` package with these files, each a `package main` unit:

```
cmd/loop/
├── main.go              # Entry point: flag parsing, subcommand dispatch, main()
├── config.go            # ModelSpec, ModelInfo, Fleet, Config, Backend, rolePolicies, model selection
├── session.go           # CortexSession struct + all methods (transcript, retrieval, capture, distillation, compaction, metrics)
├── tools.go             # Tool implementations: EditFile, RemovePath, ReadFile, WriteFile, RunShell, StudyFile, Search
├── tools_parse.go       # Tool call parsing: parseXMLToolCalls, stripToolMarkup, FunctionCall, stringArg, intArg
├── agent.go             # Agent loop: Turn, Resolve, runToolCalls, runAnchoredTurn, TurnResult
├── stream.go            # Output rendering: streamPrinter, reasoningTail, assembleStreamResponse
├── shell.go             # Shell output: spillShellOutput, studyShellOutput, bashStudyWindow
├── session_cmds.go      # Session CLI: printSessions, Clear, compactNow, runStudyCLI, runTurnCLI
├── repl.go              # Interactive REPL: main loop, /remember, /compact, /sessions, /clear, Prompt, ctxColor
├── change.go            # (already extracted)
├── discord.go           # (already extracted)
├── render.go            # (already extracted)
└── *_test.go            # Tests co-located with their source files
```

### Design Principles

1. **Each file has one responsibility** — types, methods, and helpers that belong together stay together.
2. **Interfaces define boundaries** — `CortexSession` methods that other files call are the public API; internal helpers stay unexported.
3. **No circular dependencies** — `main.go` imports nothing from `cmd/loop/`; all other files import only from `internal/`, `pkg/`, and stdlib.
4. **Tests follow source files** — each extracted file gets its own `_test.go` with tests co-located.
5. **Preserve existing behavior** — no behavioral changes; this is a pure refactor.

## 3. File-by-File Breakdown

### 3.1 `main.go` — Entry Point (~200 lines)

**Responsibility:** Parse CLI flags, determine mode (REPL, turn, study, discord, change), dispatch to the appropriate handler.

**Contents:**
- `main()` function
- Flag definitions (`--session-id`, `--input`, `--quiet`, `--output`, `--goal`, `--path`, etc.)
- Mode detection logic (anchored/capture/signal, headless turn, study CLI, discord, change)
- Dispatch to `runREPL()`, `runTurnCLI()`, `runStudyCLI()`, `runDiscordCLI()`, `runChangeCLI()`

**Imports:** `os`, `flag`, `fmt`, `log`, `context`, `time`, `strings`, `github.com/dereksantos/cortex/cmd/loop` (internal types), `github.com/dereksantos/cortex/pkg/config`, `github.com/dereksantos/cortex/pkg/llm`

**Does NOT contain:** Any business logic, tool implementations, session methods, or rendering code.

### 3.2 `config.go` — Model Configuration & Fleet Discovery (~300 lines)

**Responsibility:** Model selection, fleet discovery, and configuration overlay.

**Contents:**
- Constants: `maxToolIterations`, `maxToolOutput`, `requestTimeout`, `maxSendAttempts`, `compactThreshold`, `bashStudyWindow`, `curationBudget`
- Types: `ModelSpec`, `ModelInfo`, `Fleet`, `Config`, `Backend`, `rolePolicy`, `ToolConfig`
- Functions: `selectModel()`, `discoverFleet()`, `applyFleet()`, `resolveBinding()`, `resolveBindingForRole()`, `sharedSwapGroup()`, `isOpenRouter()`, `deleteEnabled()`, `backendEndpoint()`, `windowSize()`
- Data: `rolePolicies` map, `modelRoles` constants

**Key method on Config:** `Resolve(role string, fleet Fleet) (ModelSpec, error)`

**Imports:** `context`, `encoding/json`, `fmt`, `io`, `net/http`, `os`, `strings`, `time`, `github.com/dereksantos/cortex/pkg/config`, `github.com/dereksantos/cortex/pkg/llm`

### 3.3 `session.go` — Session Management (~2,500 lines)

**Responsibility:** `CortexSession` struct and all its methods — the core state machine.

**Contents:**
- Type: `CortexSession` struct (session ID, transcript file, model bindings, terminal width, retrieval/capture state, metrics)
- Type: `sessionEntry` (message, retrieval, compaction kinds)
- Constructor: `NewCortexSession()`
- Transcript lifecycle: `StartTranscript()`, `ResumeTranscript()`, `latestSessionID()`, `loadTranscript()`, `writeEntry()`, `Close()`
- Message management: `Append()`, `SetModel()`, `PrintArgs()`
- Retrieval: `EnableRetrieval()`, `retrieve()`, `recordRetrieval()`, `formatRetrieved()`, `retrievalLimit`, `retrievedContentCap`
- Capture: `captureTurn()`, `remember()`
- Distillation: `noteTurn()`, `startDistill()`, `distillPending()`, `stopDistill()`, `reasoner()`, `isDuplicateInsight()`, `normalizeInsight()`
- Compaction: `Compact()`
- Metrics: `sessionSummary()`, `emitSessionMetrics()`, `humanCost()`, `humanK()`, `contextRatio()`
- Session listing: `printSessions()`, `Clear()`
- Utility: `firstLine()`, `relTime()`

**Imports:** `context`, `encoding/json`, `fmt`, `io/fs`, `log`, `os`, `path/filepath`, `sort`, `strings`, `sync`, `time`, `github.com/dereksantos/cortex/internal/capture`, `github.com/dereksantos/cortex/internal/journal`, `github.com/dereksantos/cortex/internal/study`, `github.com/dereksantos/cortex/pkg/llm`, `github.com/dereksantos/cortex/pkg/cognition/dag`, `github.com/dereksantos/cortex/pkg/cognition/dag/ops`

### 3.4 `tools.go` — Tool Implementations (~1,500 lines)

**Responsibility:** All tool execution functions that the agent calls.

**Contents:**
- Type: `ToolConfig` (with `EnableDelete`, `DeleteRoot`)
- Functions: `EditFile()`, `RemovePath()`, `ReadFile()`, `WriteFile()`, `RunShell()`, `StudyFile()`, `Search()`
- Helpers: `confinedPath()`, `allowDelete()`, `similarLines()` (for edit hints), `toolCallSignature()` (for repetition detection)
- Constants: `maxToolOutput`, `maxRepeatedToolCalls`

**Key safety:** `RemovePath` enforces path confinement, rejects `.git`/`.cortex`, guards against symlink escapes.

**Imports:** `context`, `encoding/json`, `fmt`, `io/fs`, `os`, `os/exec`, `path/filepath`, `strings`, `time`, `github.com/dereksantos/cortex/internal/study`

### 3.5 `tools_parse.go` — Tool Call Parsing (~150 lines)

**Responsibility:** Parse tool calls from model responses.

**Contents:**
- Functions: `parseXMLToolCalls()`, `stripToolMarkup()`
- Types: `FunctionCall`
- Helpers: `stringArg()`, `intArg()`

**Imports:** `encoding/json`, `fmt`, `regexp`, `strings`

### 3.6 `agent.go` — Agent Loop (~600 lines)

**Responsibility:** The core agentic turn loop.

**Contents:**
- Type: `TurnResult` (Reply, Interrupted)
- Functions: `Turn()`, `Resolve()`, `runToolCalls()`, `runAnchoredTurn()`
- Logic: tool call iteration with repetition detection, nudge injection, max iteration cap, context overflow handling

**Imports:** `context`, `fmt`, `log`, `strings`, `time`, `github.com/dereksantos/c/pkg/llm`

### 3.7 `stream.go` — Output Streaming (~400 lines)

**Responsibility:** Live output rendering and streaming.

**Contents:**
- Types: `streamPrinter`, `reasoningTail`
- Functions: `assembleStreamResponse()`, `streamPrinter.new()`, `streamPrinter.send()`, `streamPrinter.writeBlock()`, `streamPrinter.flush()`
- Helpers: `streamingEnabled()`, `terminalWidth()`, `newMarkdownRenderer()` (if not in render.go)

**Imports:** `fmt`, `os`, `strings`, `github.com/charmbracelet/glamour`, `golang.org/x/term`

### 3.8 `shell.go` — Shell Output Handling (~80 lines)

**Responsibility:** Spill and study large shell command outputs.

**Contents:**
- Constants: `bashStudyWindow`
- Functions: `spillShellOutput()`, `studyShellOutput()`

**Imports:** `crypto/sha256`, `encoding/json`, `fmt`, `os`, `path/filepath`, `strings`, `time`

### 3.9 `session_cmds.go` — Session CLI Commands (~400 lines)

**Responsibility:** Headless turn CLI, study CLI, compaction CLI.

**Contents:**
- Functions: `runTurnCLI()`, `runStudyCLI()`, `compactNow()`, `lastAssistantText()`

**Imports:** `context`, `encoding/json`, `fmt`, `log`, `os`, `strings`, `time`

### 3.10 `repl.go` — Interactive REPL (~500 lines)

**Responsibility:** The interactive REPL main loop and session commands.

**Contents:**
- Functions: `runREPL()` — the main interactive loop
- Session commands: `/remember`, `/compact`, `/sessions`, `/clear`
- Prompt rendering: `Prompt()`, `ctxColor()`
- Input handling: anchored vs capture vs signal modes

**Imports:** `context`, `fmt`, `log`, `os`, `os/signal`, `strings`, `syscall`, `time`, `github.com/dereksantos/cortex/internal/lineedit`

## 4. Dependency Graph

```
main.go
  ├── config.go
  ├── session.go
  ├── tools.go
  ├── tools_parse.go
  ├── agent.go
  ├── stream.go
  ├── shell.go
  ├── session_cmds.go
  ├── repl.go
  ├── change.go       (already extracted)
  ├── discord.go      (already extracted)
  └── render.go       (already extracted)

Internal dependencies:
  session.go → internal/capture, internal/journal, internal/study, pkg/llm, pkg/cognition/dag
  tools.go → internal/study
  agent.go → pkg/llm
  stream.go → glamour, term
  shell.go → (stdlib + internal/study)
  repl.go → internal/lineedit
```

No circular dependencies. `main.go` imports nothing from within `cmd/loop/` — it only imports from `internal/`, `pkg/`, and stdlib.

## 5. Migration Strategy

### Phase 1: Extract types and configuration (`config.go`)
1. Move `ModelSpec`, `ModelInfo`, `Fleet`, `Config`, `Backend`, `rolePolicy`, `ToolConfig` types and all associated functions to `config.go`
2. Move constants (`maxToolIterations`, `maxToolOutput`, `requestTimeout`, `maxSendAttempts`, `compactThreshold`, `bashStudyWindow`, `curationBudget`) to `config.go`
3. Move `rolePolicies` map and `modelRoles` constants to `config.go`
4. Verify compilation: `go build ./cmd/loop/`

### Phase 2: Extract tool implementations (`tools.go`, `tools_parse.go`)
1. Move `EditFile`, `RemovePath`, `ReadFile`, `WriteFile`, `RunShell`, `StudyFile`, `Search` to `tools.go`
2. Move `parseXMLToolCalls`, `stripToolMarkup`, `FunctionCall`, `stringArg`, `intArg` to `tools_parse.go`
3. Move `confinedPath`, `allowDelete`, `similarLines`, `toolCallSignature` to `tools.go`
4. Verify compilation and run existing tests

### Phase 3: Extract session management (`session.go`)
1. Move `CortexSession` struct definition and `sessionEntry` to `session.go`
2. Move all `CortexSession` methods in order:
   - Constructor: `NewCortexSession()`
   - Transcript: `StartTranscript()`, `ResumeTranscript()`, `latestSessionID()`, `loadTranscript()`, `writeEntry()`, `Close()`
   - Messages: `Append()`, `SetModel()`, `PrintArgs()`
   - Retrieval: `EnableRetrieval()`, `retrieve()`, `recordRetrieval()`, `formatRetrieved()`
   - Capture: `captureTurn()`, `remember()`
   - Distillation: `noteTurn()`, `startDistill()`, `distillPending()`, `stopDistill()`, `reasoner()`, `isDuplicateInsight()`, `normalizeInsight()`
   - Compaction: `Compact()`
   - Metrics: `sessionSummary()`, `emitSessionMetrics()`, `humanCost()`, `humanK()`, `contextRatio()`
   - Session listing: `printSessions()`, `Clear()`
   - Utility: `firstLine()`, `relTime()`
3. Verify compilation and run existing tests

### Phase 4: Extract agent loop (`agent.go`)
1. Move `TurnResult` type to `agent.go`
2. Move `Turn()`, `Resolve()`, `runToolCalls()`, `runAnchoredTurn()` to `agent.go`
3. Verify compilation and run existing tests

### Phase 5: Extract streaming and output (`stream.go`, `shell.go`)
1. Move `streamPrinter`, `reasoningTail`, `assembleStreamResponse()` to `stream.go`
2. Move `spillShellOutput()`, `studyShellOutput()` to `shell.go`
3. Move `streamingEnabled()`, `terminalWidth()`, `newMarkdownRenderer()` to `stream.go` (or keep in `render.go` if already there)
4. Verify compilation and run existing tests

### Phase 6: Extract CLI commands (`session_cmds.go`, `repl.go`)
1. Move `runTurnCLI()`, `runStudyCLI()`, `compactNow()`, `lastAssistantText()` to `session_cmds.go`
2. Move `runREPL()` and all REPL-related functions to `repl.go`
3. Move `Prompt()`, `ctxColor()` to `repl.go`
4. Verify compilation and run existing tests

### Phase 7: Clean up `main.go`
1. Remove all extracted code from `main.go`
2. Keep only: `main()` function, flag parsing, mode detection, dispatch logic
3. Final compilation and test pass

## 6. Test Strategy

### 6.1 Unit Tests (per file)

| File | Test File | What to Test |
|------|-----------|-------------|
| `config.go` | `config_test.go` | `selectModel()` with various role/tag combos, `discoverFleet()` mock HTTP, `applyFleet()` overlay logic, `resolveBinding()` with fleet+config |
| `tools.go` | `tools_test.go` | `EditFile()` exact/whitespace matching, `RemovePath()` path confinement, `confinedPath()` escape detection, `toolCallSignature()` repetition |
| `tools_parse.go` | `tools_parse_test.go` | `parseXMLToolCalls()` with various XML formats, `stripToolMarkup()`, `stringArg()`/`intArg()` extraction |
| `session.go` | `session_test.go` | `StartTranscript()`/`ResumeTranscript()` file I/O, `captureTurn()` artifact recording, `isDuplicateInsight()` normalization, `Compact()` study invocation |
| `agent.go` | `agent_test.go` | `Turn()` with mock LLM, `Resolve()` tool call loop, `runToolCalls()` interruption handling, `TurnResult` capture |
| `stream.go` | `stream_test.go` | `streamPrinter` callbacks, `reasoningTail` bounded output, `assembleStreamResponse()` construction |
| `shell.go` | `shell_test.go` | `spillShellOutput()` content-addressed file creation, `studyShellOutput()` study invocation |
| `session_cmds.go` | `session_cmds_test.go` | `runTurnCLI()` JSON output, `compactNow()` compaction trigger |
| `repl.go` | `repl_test.go` | `runREPL()` input mode detection, session command parsing |

### 6.2 Integration Tests

- **End-to-end REPL test:** `main_test.go` (existing) — verify the full loop with a mock LLM
- **Session lifecycle test:** Create → Turn → Capture → Distill → Compact → Close
- **Tool execution test:** EditFile → ReadFile → Verify content
- **Fleet discovery test:** Mock HTTP server returning model metadata, verify `applyFleet()` overlay

### 6.3 Verification Steps

1. **Compilation:** `go build ./cmd/loop/` succeeds with zero errors
2. **Existing tests pass:** `go test ./cmd/loop/...` — all existing tests pass without modification
3. **Behavior parity:** Run the same sequence of inputs through the old and new code; verify identical output
4. **Import audit:** `go mod tidy` — no unexpected new dependencies
5. **Static analysis:** `go vet ./cmd/loop/...` — no warnings
6. **Race detection:** `go test -race ./cmd/loop/...` — no data races

## 7. Risk Assessment

| Risk | Mitigation |
|------|-----------|
| Breaking existing tests | Each phase compiles and passes tests before proceeding |
| Circular dependencies | Dependency graph reviewed before extraction; `main.go` imports nothing from `cmd/loop/` |
| Behavioral regression | Git diff reviewed per phase; integration tests verify end-to-end behavior |
| Large session.go file | `session.go` will be ~2,500 lines — acceptable for a single-responsibility file; further splitting only if methods exceed 200 lines each |

## 8. Post-Refactoring Considerations

### 8.1 Future Improvements (out of scope for this refactor)

1. **Interface extraction:** Define `TranscriptStore`, `RetrievalEngine`, `DistillationService` interfaces so `CortexSession` depends on abstractions rather than concrete implementations
2. **Config loading:** Extract `Config` loading from `NewCortexSession()` into a separate `configloader` package
3. **Tool registry:** Replace the switch-based tool dispatch with a registry pattern for easier tool addition
4. **Stream abstraction:** Define a `Writer` interface for `streamPrinter` to support non-terminal outputs (files, network)

### 8.2 File Size Targets

| File | Current (in main.go) | Target |
|------|---------------------|--------|
| `main.go` | ~39,000 lines | ~200 lines |
| `config.go` | ~300 lines | ~300 lines |
| `session.go` | ~2,500 lines | ~2,500 lines |
| `tools.go` | ~1,500 lines | ~1,500 lines |
| `tools_parse.go` | ~150 lines | ~150 lines |
| `agent.go` | ~600 lines | ~600 lines |
| `stream.go` | ~400 lines | ~400 lines |
| `shell.go` | ~80 lines | ~80 lines |
| `session_cmds.go` | ~400 lines | ~400 lines |
| `repl.go` | ~500 lines | ~500 lines |

Total: ~11 files, each with a clear single responsibility, vs. 1 monolithic file.

## 9. Acceptance Criteria

- [ ] `cmd/loop/main.go` is under 500 lines
- [ ] All extracted files compile as `package main`
- [ ] `go build ./cmd/loop/` succeeds
- [ ] `go test ./cmd/loop/...` passes all existing tests
- [ ] `go vet ./cmd/loop/...` reports no warnings
- [ ] No behavioral changes (verified by integration test)
- [ ] Each file has a clear, documented single responsibility
- [ ] No circular dependencies between files
