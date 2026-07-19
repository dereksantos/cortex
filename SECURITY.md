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

## `cortex serve`'s auth posture

`cortex serve` binds loopback-only (`127.0.0.1`/`localhost`/`[::1]`) and, as
of 2026-07-19, gates every `/api/...` request with a strict Host/Origin
allowlist (`hostOriginMiddleware`, `cmd/cortex/serve.go`) instead of a
bearer token. This is by design, not an oversight: on a loopback-only bind
there is no network attacker to keep out with a secret — the actual threats
are DNS rebinding (a malicious page gets a victim's browser to resolve some
other hostname to `127.0.0.1`) and cross-site request forgery (a malicious
page's browser issues a same-machine request on the victim's behalf). Both
are Host/Origin problems, not authentication problems, and a bearer token
carried in a URL query string is itself a leak surface (browser history,
referrer headers, shell scrollback) that the allowlist avoids entirely. A
bypass of the allowlist — a request whose Host or Origin should have been
rejected but was accepted — is in scope for this policy, at the same tier
as a shell-risk gate bypass below. If `cortex serve` ever binds to a
non-loopback address, that build will need a real auth mechanism again;
loopback-only is the precondition this posture depends on.

## Scope

In scope:
- The `cortex` CLI and its agentic turn loop
- `cortex serve`'s Host/Origin allowlist (see above) — a request that
  should be rejected by Host or Origin but is accepted anyway
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
  running `cortex`
