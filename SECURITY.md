# Security Policy

## Supported Versions

Cortex is in public alpha. Only the `main` branch receives security fixes.

## Reporting a Vulnerability

Please **do not** open a public GitHub issue for security vulnerabilities.

Instead, report privately via GitHub's [Private Vulnerability Reporting](https://github.com/dereksantos/cortex/security/advisories/new) (the "Report a vulnerability" button on the Security tab). Reports go directly to the maintainer.

Please include:
- A description of the issue and its impact
- Steps to reproduce
- Affected versions or commits
- Any suggested mitigations

You can expect an initial response within 7 days. We'll work with you on a fix and a coordinated disclosure timeline.

## Scope

In scope:
- The `loop` CLI and its agentic turn loop
- The shell-risk gate (`internal/shellrisk`) — i.e. a command that should
  be classified Risky/Blocked but runs as Safe, or any bypass of the
  approval prompt
- Workspace confinement for `remove_path` and `bash` — escapes outside the
  configured `delete_root` / workspace, or deletion of `.git` / `.cortex`
- Secret handling — API keys are referenced by `key_env` / `key_service`
  and must never be written to `~/.cortex/config.json`, the journal, or
  session transcripts
- The journal's local-only invariant (`journal.AssertLocalOnly`) — any path
  that sends `.cortex/` contents off the machine by default
- Anything that handles user data in `~/.cortex/` or a project's `.cortex/`

Out of scope:
- Third-party LLM providers and model backends (report to Anthropic,
  Ollama, OpenRouter, etc.)
- Prompt-injection causing the agent to run a command you would have
  approved anyway — the risk gate mitigates but cannot eliminate this;
  review what the agent proposes
- Issues that require local code execution as the same user already
  running `loop`
