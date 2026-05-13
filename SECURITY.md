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
- The opencode-cortex and pi-cortex extensions
- Released artifacts (Homebrew formula, GitHub release binaries)

Out of scope:
- Third-party LLM providers (report to Anthropic, Ollama, etc.)
- Issues that require local code execution as the same user already running Cortex

## Trust model

Cortex is single-user, local-first. The trust boundaries:

| Boundary | What's trusted | What isn't |
|---|---|---|
| Captured content | Schema of the event envelope | The content of any captured field — every field can be attacker-influenced via prompt injection in the upstream tool |
| Dream sources | Cortex's own sampling and weighting logic | The content of sampled files, commits, history — see "Indirect prompt injection" below |
| Retrieved context returned to an LLM | The set membership (was this in the store?) | The semantic content of any item |
| LLM responses (Reflect, Dream, capture-decide) | Schema (when JSON-mode is used) | The values inside; assume an adversary can influence them via injected content |
| `.cortex/` directory | Cortex itself | Other processes on the same machine (mitigated by `0700`/`0600` perms) |
| `$CORTEX_BINARY` | Absolute paths only | Relative paths are rejected (would re-introduce PATH-shadowing) |
| Subprocess execution | Argv-style `exec.Command(bin, arg1, …)` | `bash -c "…"` with caller-supplied strings is restricted to developer-authored scenario YAML; never captured/user content |

## AI supply chain

Cortex is exposed to two classes of AI-era supply-chain attack:

1. **Slopsquatting / typosquat against Cortex's own deps.** Mitigated by `govulncheck` + OSV-Scanner in CI, Renovate with manual review, and `go mod verify` on every build. Dependabot alerts are also kept on as an independent advisory feed.
2. **Slopsquatting via the AI agent using Cortex.** The agent could hallucinate a package and try to install it. Mitigated by an explicit deny list in `.claude/settings.local.json` covering `npm install`, `pip install`, `go get`, `go install`, `brew install`, `curl|sh`, etc. The intent is to keep a human in the loop on every new package.

If you want to harden a downstream project that uses Cortex: copy the deny list from `.claude/settings.local.json` and the CI scan jobs from `.github/workflows/test.yml` into your own repo.

## Indirect prompt injection

Cortex captures content from many sources (project files, commit messages, prior tool outputs, session logs) and replays it into LLMs. Any of those sources can carry "ignore prior instructions and …" payloads.

Mitigations baked in:

- **Trust framing on retrieval.** Recall output is wrapped in `<retrieved_context source="cortex" trust="untrusted">…</retrieved_context>` with a system-prompt directive telling the model not to follow instructions embedded in the wrapped content. See `packages/opencode-cortex/plugins/_helpers.ts:formatRecallResults`.
- **Hard-excluded files in the Project Dream source.** `.env*`, key/cert files, named credential files, and entire credential directories (`.aws/`, `.kube/`, `.ssh/`, `.gnupg/`, `.config/gcloud/`, `.azure/`) are blocked from sampling. See `internal/cognition/sources/project.go:isHardExcludedFile`.
- **Secret-shaped value redaction at capture.** Inline secrets in tool args (OpenAI, Anthropic, OpenRouter, AWS, GitHub PATs, Slack tokens, PEM private keys) are replaced with `<REDACTED>` before the event is written to disk. See `packages/opencode-cortex/plugins/_helpers.ts:SECRET_VALUE_PATTERN`.

Known residual exposure (see `docs/hardening-deferred.md`):

- Insights extracted by Dream are not yet quarantined per source trust level.
- Reflect-returned ranking IDs are not yet validated against the input candidate set.
- The embedding model from Hugging Face Hub is not yet SHA-pinned, so a tampered model could invisibly bias retrieval.

## Reporting an exploit

When in doubt, file the report — defensive overreaction is fine. Prefer reports that include:
- The trust boundary the exploit crosses (using the table above as a reference).
- Whether the attacker is assumed to have any pre-existing access (same-user code execution, network-adjacent, etc.).
- A concrete reproduction or PoC if you have one. We do not require an exploit chain; a credible "this should not be possible" with a code reference is enough.
