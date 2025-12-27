# Cortex ↔ Claude Code Integration

## Overview

Cortex integrates with Claude Code via hooks, status line, and slash commands to provide persistent context across sessions.

```
┌─────────────────────────────────────────────────────────────┐
│                   Claude Code Session                       │
├─────────────────────────────────────────────────────────────┤
│  SessionStart ──▶ UserPromptSubmit ──▶ PostToolUse         │
│       │                  │                  │               │
└───────┼──────────────────┼──────────────────┼───────────────┘
        │                  │                  │
        ▼                  ▼                  ▼
   session start      inject context      capture event
        │                  │                  │
        └──────────────────┴──────────────────┘
                           │
                    ┌──────▼──────┐
                    │   Cortex    │
                    │   Daemon    │
                    └─────────────┘
```

---

## Implementation Sessions

### Session 1: Install & Status Line

**Goal**: `cortex install` auto-configures Claude Code, animated status line works.

#### 1.1 `cortex install` Command

**File**: `cmd/cortex/main.go` (add `handleInstall`)

**Behavior**:
```bash
cortex install
# Output:
# ✓ Detected Claude Code at ~/.claude/
# ✓ Created .claude/settings.local.json with hooks
# ✓ Created .claude/commands/cortex.md
#
# Checking LLM availability...
# ✓ Ollama installed at /usr/local/bin/ollama
# ✓ Model qwen2.5:3b available (recommended for Cortex)
#
# Run `claude` to start a session with Cortex enabled.
```

**If no model available**:
```bash
# Checking LLM availability...
# ✓ Ollama installed
# ⚠ No suitable model found
#
# Cortex works best with a local model for background processing.
# Install one with:
#   ollama pull qwen2.5:3b    (3GB, recommended)
#   ollama pull qwen2.5:0.5b  (500MB, lightweight)
#
# Or set ANTHROPIC_API_KEY for Claude API usage.
# Without an LLM, Cortex will run in mechanical-only mode (Reflex).
```

**If Ollama not installed**:
```bash
# Checking LLM availability...
# ⚠ No local LLM found
#
# For full functionality, install Ollama:
#   brew install ollama && ollama pull qwen2.5:3b
#
# Or set ANTHROPIC_API_KEY for Claude API usage.
# Without an LLM, Cortex will run in mechanical-only mode (Reflex).
```

**Detection logic**:
1. Check `~/.claude/` exists → Claude Code installed
2. Check `.claude/` in project → project already configured
3. Check if daemon is running → `cortex status` returns
4. Check LLM availability (see below)

**Actions**:
1. Create/merge `.claude/settings.local.json`:
   ```json
   {
     "hooks": {
       "SessionStart": [{
         "hooks": [{"type": "command", "command": "./cortex session-start"}]
       }],
       "UserPromptSubmit": [{
         "hooks": [{"type": "command", "command": "./cortex inject-context"}]
       }],
       "PostToolUse": [{
         "matcher": "Write|Edit|Bash",
         "hooks": [{"type": "command", "command": "./cortex capture"}]
       }]
     },
     "statusLine": {
       "type": "command",
       "command": "./cortex status --format=claude"
     }
   }
   ```
2. Copy slash command to `.claude/commands/cortex.md`
3. Ensure `.context/` directory exists
4. Print success message with next steps

**Edge cases**:
- Merge with existing settings (don't overwrite user config)
- Handle missing cortex binary (suggest `go build`)
- Idempotent (safe to run multiple times)

#### 1.1.1 LLM Detection

**File**: `internal/llm/detect.go` (new file)

**Data structures**:
```go
type LLMStatus struct {
    Available        bool
    Provider         string   // "ollama" | "anthropic" | ""
    Model            string   // e.g., "qwen2.5:3b"
    OllamaInstalled  bool
    OllamaModels     []string // all installed models
    AnthropicKeySet  bool
    RecommendedModel string   // suggestion if no good model
}

// Recommended models in priority order
var RecommendedModels = []string{
    "qwen2.5:3b",      // Best balance
    "qwen2.5:7b",      // Higher quality
    "qwen2.5:0.5b",    // Lightweight
    "llama3.2:3b",     // Alternative
    "gemma2:2b",       // Alternative
}
```

**Detection function**:
```go
func DetectLLM() LLMStatus {
    status := LLMStatus{}

    // 1. Check Anthropic API key
    if os.Getenv("ANTHROPIC_API_KEY") != "" {
        status.AnthropicKeySet = true
        status.Available = true
        status.Provider = "anthropic"
        // Don't return yet - still check Ollama for local option
    }

    // 2. Check Ollama installed
    ollamaPath, err := exec.LookPath("ollama")
    if err != nil {
        if !status.Available {
            status.RecommendedModel = "qwen2.5:3b"
        }
        return status
    }
    status.OllamaInstalled = true

    // 3. List Ollama models
    cmd := exec.Command(ollamaPath, "list")
    output, err := cmd.Output()
    if err != nil {
        return status
    }
    status.OllamaModels = parseOllamaList(output)

    // 4. Check for recommended model
    for _, rec := range RecommendedModels {
        for _, installed := range status.OllamaModels {
            if strings.HasPrefix(installed, rec) {
                status.Available = true
                status.Provider = "ollama"
                status.Model = installed
                return status
            }
        }
    }

    // 5. Use any available model
    if len(status.OllamaModels) > 0 {
        status.Available = true
        status.Provider = "ollama"
        status.Model = status.OllamaModels[0]
        return status
    }

    // 6. Ollama installed but no models
    status.RecommendedModel = "qwen2.5:3b"
    return status
}

func parseOllamaList(output []byte) []string {
    // Parse "ollama list" output:
    // NAME            ID           SIZE    MODIFIED
    // qwen2.5:3b      abc123...    3.2 GB  2 days ago
    var models []string
    lines := strings.Split(string(output), "\n")
    for _, line := range lines[1:] { // skip header
        fields := strings.Fields(line)
        if len(fields) >= 1 {
            models = append(models, fields[0])
        }
    }
    return models
}
```

**Install command integration**:
```go
func handleInstall() {
    // ... Claude Code detection ...

    // Check LLM
    fmt.Println("\nChecking LLM availability...")
    llm := DetectLLM()

    if llm.Available {
        if llm.Provider == "ollama" {
            fmt.Printf("✓ Ollama model %s available\n", llm.Model)
        } else {
            fmt.Println("✓ Anthropic API key configured")
        }
    } else if llm.OllamaInstalled {
        fmt.Println("⚠ Ollama installed but no models found")
        fmt.Println("\nInstall a model with:")
        fmt.Println("  ollama pull qwen2.5:3b    (3GB, recommended)")
        fmt.Println("  ollama pull qwen2.5:0.5b  (500MB, lightweight)")
    } else {
        fmt.Println("⚠ No local LLM found")
        fmt.Println("\nFor full functionality, install Ollama:")
        fmt.Println("  brew install ollama && ollama pull qwen2.5:3b")
    }

    if !llm.Available {
        fmt.Println("\nWithout an LLM, Cortex runs in mechanical-only mode (Reflex).")
        fmt.Println("Set ANTHROPIC_API_KEY for Claude API usage.")
    }
}
```

---

#### 1.2 Status Line with Animation

**File**: `cmd/cortex/main.go` (update `handleStatus`)

**New flag**: `--format=claude`

**Input** (JSON from stdin):
```json
{
  "session_id": "abc123",
  "hook_event_name": "Status",
  "context_window": {
    "current_usage": {"input_tokens": 8500}
  }
}
```

**Output format**:
```
◐ Reflex: 12 insights loaded
◑ Think: learning patterns
◒ Dream: exploring history
● Ready: 847 events, 23 insights
```

**Animation states** (rotate on activity):
- `◐` `◓` `◑` `◒` - spinner during processing
- `●` - steady state (idle)
- `◌` - no data yet (cold start)

**Mode detection**:
- Check daemon state file for current mode
- Fall back to "Ready" if no active mode

**Implementation**:
```go
func handleStatus(format string) {
    if format == "claude" {
        // Read stdin for Claude Code context
        // Get daemon state
        // Format: "{spinner} {mode}: {description}"
        // Use ANSI colors for mode name
    }
    // ... existing status logic
}
```

#### 1.3 Testing

- [ ] Run `cortex install` in fresh project
- [ ] Verify hooks fire correctly
- [ ] Verify status line updates
- [ ] Test capture → inject round-trip

---

### Session 2: Slash Commands & Dream Source

**Goal**: Rich slash commands, Claude history as Dream source.

#### 2.1 Additional Slash Commands

**Location**: `.claude/commands/`

**`cortex-recall.md`** - Query stored context:
```markdown
---
description: Recall what Cortex knows about a topic
argument-hint: "<topic>"
allowed-tools: Bash(./cortex:*)
---

Search Cortex for context related to: $ARGUMENTS

Run: ./cortex search "$ARGUMENTS"

Summarize the relevant insights, decisions, and patterns found.
```

**`cortex-decide.md`** - Record architectural decision:
```markdown
---
description: Record an architectural decision
argument-hint: "<decision>"
allowed-tools: Bash(./cortex:*)
---

Record this architectural decision in Cortex:

Decision: $ARGUMENTS

Run: ./cortex capture --type=decision --content="$ARGUMENTS"

Confirm the decision was recorded.
```

**`cortex-correct.md`** - Record a correction:
```markdown
---
description: Record a correction (e.g., "we use X not Y")
argument-hint: "<correction>"
allowed-tools: Bash(./cortex:*)
---

Record this correction in Cortex:

Correction: $ARGUMENTS

This will be surfaced in future sessions when relevant.

Run: ./cortex capture --type=correction --content="$ARGUMENTS"
```

**`cortex-forget.md`** - Remove outdated context:
```markdown
---
description: Mark context as outdated
argument-hint: "<insight-id or description>"
allowed-tools: Bash(./cortex:*)
---

Mark this context as outdated/deprecated:

$ARGUMENTS

Run: ./cortex forget "$ARGUMENTS"
```

#### 2.2 CLI Support for Commands

**New flags for `cortex capture`**:
```bash
cortex capture --type=decision --content="Use Zustand for state"
cortex capture --type=correction --content="No Redux, use Zustand"
cortex capture --type=pattern --content="All API calls go through services/"
```

**New command `cortex forget`**:
```bash
cortex forget "insight-123"           # by ID
cortex forget "redux"                  # by keyword (marks as deprecated)
```

#### 2.3 Claude History Dream Source

**File**: `internal/cognition/sources/claude_history.go`

**Implements**: `cognition.DreamSource`

**Behavior**:
```go
type ClaudeHistorySource struct {
    transcriptDir string  // ~/.claude/sessions/ or project/.claude/
}

func (s *ClaudeHistorySource) Name() string {
    return "claude-history"
}

func (s *ClaudeHistorySource) Sample() ([]cognition.DreamItem, error) {
    // Find recent session transcripts (*.jsonl)
    // Parse JSONL format
    // Extract:
    //   - Tool uses (Write, Edit, Bash commands)
    //   - User corrections ("no, use X instead")
    //   - Decisions made
    //   - Errors encountered and fixes
    // Return as DreamItems for processing
}
```

**Transcript format** (Claude Code JSONL):
```json
{"type": "user", "content": "..."}
{"type": "assistant", "content": "...", "tool_calls": [...]}
{"type": "tool_result", "tool_name": "Write", "result": {...}}
```

**Registration**:
```go
// In main.go or cognition setup
dream.RegisterSource(sources.NewClaudeHistorySource(
    filepath.Join(os.Getenv("HOME"), ".claude", "sessions"),
))
```

#### 2.4 Testing

- [ ] Test each slash command
- [ ] Verify `--type` flag captures correctly
- [ ] Test Dream samples from Claude history
- [ ] Verify insights extracted from transcripts

---

### Session 3: Polish & Persistence (Optional)

**Goal**: Session persistence, robustness, documentation.

#### 3.1 Session Persistence

**Current**: SessionContext lost on daemon restart

**Solution**: Persist to `.context/session.json`

```go
type PersistedSession struct {
    SessionID    string
    TopicWeights map[string]float64
    WarmCache    map[string][]RetrievalResult
    LastUpdated  time.Time
}

func (s *SessionContext) Save(path string) error
func LoadSession(path string) (*SessionContext, error)
```

**Triggers**:
- Save: After Think updates, every 30 seconds, on graceful shutdown
- Load: On daemon start, on session-start hook

#### 3.2 Plugin Structure

Create distributable plugin:

```
.claude-plugin/
├── plugin.json
│   {
│     "name": "cortex",
│     "description": "Persistent context for AI coding",
│     "version": "0.1.0"
│   }
├── commands/
│   ├── cortex.md
│   ├── cortex-recall.md
│   ├── cortex-decide.md
│   ├── cortex-correct.md
│   └── cortex-forget.md
├── hooks/
│   └── hooks.json
└── README.md
```

Update `cortex install` to:
1. Build plugin structure
2. Register with Claude Code

#### 3.3 Robustness

- [ ] Graceful handling when daemon not running
- [ ] Timeout handling for slow LLM responses
- [ ] Fallback when storage corrupted
- [ ] Clear error messages for users

#### 3.4 Documentation

- [ ] Update CLAUDE.md with integration instructions
- [ ] Add troubleshooting section
- [ ] Document all slash commands

---

## Hook Reference

| Hook | Command | Purpose |
|------|---------|---------|
| `SessionStart` | `./cortex session-start` | Initialize, warm caches |
| `UserPromptSubmit` | `./cortex inject-context` | Inject relevant context |
| `PostToolUse` | `./cortex capture` | Capture events |
| Status Line | `./cortex status --format=claude` | Show state |

## File Locations

| File | Purpose | Git? |
|------|---------|------|
| `.claude/settings.local.json` | Hooks config | No |
| `.claude/commands/*.md` | Slash commands | Yes |
| `.context/db/events.db` | Event storage | No |
| `.context/queue/` | Capture queue | No |
| `.context/session.json` | Session state | No |

## Testing Checklist

### Session 1
- [ ] `cortex install` detects Claude Code
- [ ] `cortex install` creates hooks config
- [ ] `cortex install` is idempotent
- [ ] `cortex install` detects Ollama + models
- [ ] `cortex install` shows helpful message when no LLM
- [ ] `cortex install` detects ANTHROPIC_API_KEY
- [ ] Status line shows with animation
- [ ] Status line updates on mode change
- [ ] PostToolUse capture works
- [ ] UserPromptSubmit injection works

### Session 2
- [ ] `/cortex-recall` searches context
- [ ] `/cortex-decide` records decisions
- [ ] `/cortex-correct` records corrections
- [ ] `/cortex-forget` deprecates context
- [ ] Claude history Dream source samples transcripts
- [ ] Dream extracts insights from history

### Session 3
- [ ] Session persists across daemon restarts
- [ ] Plugin structure valid
- [ ] Error handling graceful
- [ ] Documentation complete
