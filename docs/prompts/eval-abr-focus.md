Eval System Redesign: Unify Around ABR

  Context

  The internal/eval/ package has grown fragmented through iteration. There are separate code paths for:
  - E2E journey evals (testing full task completion with/without Cortex)
  - Cognition mode evals (testing Reflex, Reflect, Think, Dream individually)
  - Various scenario types, reporters, and persistence mechanisms

  Current issues:
  1. Two eval types that don't share structure - E2E and cognition evals have different runners, results, and persistence
  2. Cognition results aren't persisted - Only E2E results go to SQLite
  3. ABR (Agentic Benefit Ratio) is the key metric but isn't first-class in the design
  4. Too many files - persist.go, persist_jsonl.go, persist_sqlite.go, e2e_eval.go, e2e_journey.go, scenario.go, cognition.go, etc.

  The Core Metric: ABR

  From CLAUDE.md, ABR measures how well Think makes Fast mode perform like Full mode:

  ABR = quality(Fast + Think) / quality(Full)
  Goal: ABR → 1.0 as session progresses

  This should be the central organizing principle for evals.

  Task

  Redesign internal/eval/ with these goals:

  1. Single unified Eval type that can represent both E2E and cognition scenarios
  2. ABR as first-class metric - every eval should measure/track ABR where applicable
  3. Single persistence path - one way to save results to SQLite, supporting all eval types
  4. Simpler file structure - consolidate into fewer, clearer files
  5. Keep what works - the YAML scenario format, CLI interface, and A/B comparison approach are good

  Suggested Approach

  1. Read the existing eval code to understand current structure
  2. Read CLAUDE.md cognitive architecture section for metric definitions
  3. Design a unified Eval struct and Result struct
  4. Plan the new file structure (maybe: eval.go, runner.go, persist.go, scenario.go)
  5. Present the design for approval before implementing

  Key Files to Review

  - internal/eval/*.go - current implementation
  - test/evals/scenarios/ and test/evals/journeys/ - scenario formats
  - CLAUDE.md - ABR definition and cognitive mode descriptions
  - .cortex/db/evals.db - current schema (only has E2E data)

  Constraints

  - Keep standard library testing only (no testify)
  - Maintain CLI compatibility: ./cortex eval -t e2e and ./cortex eval --cognition
  - Results must persist to SQLite for Grafana visualization