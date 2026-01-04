# Cortex CLI

To simplify the implementation and provide a better interface to LLMs we ought to consolidate all interactions with cortex to CLI commands. All mocks should be removed, and only a single default prompt should return from agent calls when ollama is not available. For commands with mechanical modes like `retrieve`, it can return pre-processed context.

The CLI should be structured as

`cortex [v] [[n]...] [c]`

Where `v` represents a cognitive verb,
`n` represents a noun,
and `c` represents context to be processed or a query for context:

```
capture pattern
retrieve
think
dream
eval

etc.
```

This way Claude Code can more easily string together useful CLI calls with controls to manage longer term context. If we can make it good enough and have Claude prefer it, it can even run in non injection mode, where it prefers a simple bash call to cortex for curated context. That said, I think its important to have a mechanical mode, if its as good as fully agentic mode, it can help manage costs.

I think this is largely implemented already, however the code needs to be tightened up and made to have a cleaner architectue.

We need to cleanup duplicate code. Interestingly, its a situation where more verbs could be introduced for budgeting and background tasks, with the goal of providing hints that over time would help the LLM tool improve the quality of the codebase over time.


Eight cognitive verbs:

  1. Sense - perceive incoming information
  2. Filter - separate signal from noise
  3. Match - recognize patterns
  4. Rank - prioritize by relevance
  5. Resolve - decide on action
  6. Learn - update from experience
  7. Synthesize - combine into insights
  8. Recall - retrieve when needed

These map loosely to Cortex's modes:
  - Reflex: sense, filter, match
  - Reflect: rank, resolve
  - Think: learn, synthesize
  - Dream: synthesize, learn
  - Retrieve: recall

> Yes, I think an overhaul of our verbiage is in sight. How many of these do you think we'll need 
to solve for something like code quality improving over time? Just list out the verbs, dont 
implement or anything, just the verb list. 

⏺ For code quality improving over time:

  1. Capture - record decisions, corrections, patterns
  2. Recall - retrieve relevant context at the right moment
  3. Learn - extract durable insights from corrections
  4. Apply - inject context to influence behavior

  That's the core loop. Four verbs.

  Optional additions for sophistication:
  - Forget - deprecate outdated context
  - Rank - prioritize conflicting guidance

  So: 4 essential, 6 max.

> Cool! How about an entire SDLC? 

SDLC verbs

  Core (always needed):
  1. Capture - record events, decisions, artifacts
  2. Recall - retrieve context when relevant
  3. Learn - extract patterns from experience
  4. Apply - inject guidance at decision points

  Planning & Design:
  5. Plan - break down into steps, identify dependencies
  6. Decide - choose between alternatives with rationale

  Execution:
  7. Verify - check against constraints/patterns
  8. Correct - update when mistakes found

  Maintenance & Evolution:
  9. Forget - deprecate stale context
  10. Evolve - update patterns as codebase changes

  ---
  10 verbs total. The first 4 are the engine; the rest are domain-specific applications of that engine


Important: All evals ought to work through cli calls and structured output. 