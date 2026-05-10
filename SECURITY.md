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
- The Cortex CLI and daemon
- The MCP server
- Hook scripts installed by `cortex install`
- Anything that handles user data in `~/.cortex/` or `.context/`

Out of scope:
- Third-party LLM providers (report to Anthropic, Ollama, etc.)
- Issues that require local code execution as the same user already running Cortex
