# UX Harness Components

1. Intent ingress — getting what the human means into the system: text, pasted errors, files, images, voice, slash commands, references to prior turns.
2. Grounding (world model) — what the harness knows without being told: repo state, open files, git, env, tool inventory, project conventions.
3. Memory & continuity — what persists between turns and sessions: user preferences, prior decisions, learned-the-hard-way corrections, handoff context.
4. Planning & alignment — before committing to work: turning intent into a concrete plan, surfacing assumptions, asking only when the cost of being wrong is high.
5. Execution — doing the work: tool-calling, file ops, shell, network, parallelism, background/long-running jobs, sandboxing, model/cost routing.
6. In-flight observability — what's happening right now: streaming text, tool-call summaries, status line, progress signals, off-screen notifications.
7. Steering & interrupt — mid-flight control: cancel, redirect, "actually do X instead," scope correction, escape hatches without losing context.
8. Reversibility & safety — bounding blast radius: worktrees, checkpoints, dry-run, permission gates, explicit confirmation on destructive ops, attribution.
9. Result presentation & verification — end-of-turn output: diffs, summaries, tests/typecheck/lint passing, "what changed, why, what's next," proof of work.
10. Extensibility & composition — adding capability without forking the harness: skills, hooks, MCP servers, slash commands, sub-agents, IDE/CI integration.