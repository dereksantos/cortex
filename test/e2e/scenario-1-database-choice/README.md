# Scenario 1: Architecture Decision Recall

## The Problem

**Day 1:** You have a detailed discussion with AI about choosing a database:
- Consider PostgreSQL (powerful, feature-rich)
- Consider SQLite (simple, zero-config)
- **Decision:** Choose SQLite for deployment simplicity and single-binary goal

**Day 30:** Fresh AI session, no memory:
- AI: "Let's use PostgreSQL for this project!"
- You: "Wait, didn't we already discuss this?" 🤦
- **Problem:** 30 minutes wasted re-explaining the decision

## The Solution

With Cortex, the decision is captured and instantly retrievable:

```bash
# Find the decision in 5 seconds
cortex search "database decision"
# → Returns: "Chose SQLite for single-binary deployment (30 days ago)"

cortex insights decision
# → Shows: [decision] Selected SQLite for zero-config deployment ⭐⭐⭐⭐⭐
```

## Mock Conversation (Day 1)

```
User: "Should we use PostgreSQL or SQLite for this project?"

AI: "Great question! Let me help you evaluate both:

PostgreSQL pros:
- More powerful features
- Better for high concurrency
- Advanced query capabilities

SQLite pros:
- Zero configuration
- Single file database
- Perfect for embedded applications
- No separate server process

What are your deployment requirements?"

User: "We want a single binary with no external dependencies."

AI: "In that case, SQLite is the clear winner. It embeds directly
into your application, requires no setup, and gives you a true
single-binary deployment. Let's use SQLite."

[Cortex captures this decision automatically]
```

## What Cortex Captures

Event captured from the AI interaction:

```json
{
  "id": "claude-1696800000000",
  "source": "claude",
  "tool_name": "Task",
  "tool_input": {
    "prompt": "implement database layer with SQLite"
  },
  "tool_result": "Created storage/db.go with SQLite implementation",
  "timestamp": "2025-10-01T10:00:00Z"
}
```

LLM Analysis (by Ollama):

```json
{
  "summary": "Chose SQLite over PostgreSQL for single-binary deployment",
  "category": "decision",
  "importance": 9,
  "tags": ["database", "sqlite", "architecture", "deployment", "single-binary"],
  "reasoning": "SQLite selected for zero-config, embedded database that supports single-binary distribution goal"
}
```

## Day 30: Fresh AI Session

Without Cortex:
```
AI: "For this project, I'd recommend PostgreSQL for better performance..."

User: "No wait, we already decided on SQLite! Let me explain why..."
[30 minutes of re-discussion]
```

With Cortex:
```
AI: "For this project, I'd recommend PostgreSQL..."

User: [Runs: cortex search "database decision"]

cortex search "database decision"
Found 1 result:

1. [claude] Task - 2025-10-01 10:00
   Chose SQLite over PostgreSQL for single-binary deployment
   Tags: database, sqlite, architecture, deployment
   Reasoning: Zero-config embedded database for single-binary goal

User: "Actually, we chose SQLite 30 days ago for single-binary deployment.
Here's the reasoning: [paste from Cortex]"

AI: "You're absolutely right! I'll use SQLite as decided."
[5 seconds to resolution]
```

## Knowledge Graph

Cortex builds relationships:

```
Decision: "Database Choice"
  ├─ implements → SQLite
  ├─ rejects → PostgreSQL
  ├─ reason → "single-binary deployment"
  └─ affects → "Storage Layer"

Pattern: "Single Binary"
  ├─ requires → SQLite (not PostgreSQL)
  ├─ requires → Embedded storage
  └─ affects → Deployment strategy
```

Query the graph:

```bash
cortex graph decision "Database Choice"
# Output:
🌐 Knowledge Graph for: Database Choice (decision)

Relationships (4):
1. Database Choice -[implements]-> SQLite
2. Database Choice -[rejects]-> PostgreSQL
3. Database Choice -[reason]-> single-binary deployment
4. Storage Layer -[uses]-> SQLite
```

## Running This Scenario

```bash
# Setup (simulates Day 1)
./setup.sh

# Test (simulates Day 30)
./test.sh

# Expected: Successfully retrieves decision in < 5 seconds
# Cleanup
./cleanup.sh
```

## Impact Metrics

- **Time to recall decision:** 5 seconds (vs 30 minutes)
- **Accuracy:** 100% (vs potentially wrong choice)
- **Context preservation:** Complete reasoning saved
- **Team alignment:** Everyone sees the same decision

## Key Takeaway

**Without Cortex:** "Why did we choose X again?"
**With Cortex:** `cortex search "X decision"` → Instant answer with full context
