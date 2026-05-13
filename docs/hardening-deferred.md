# Hardening — deferred items needing user decision

Companion to the hardening passes on 2026-05-12. Round 1 closed the
filesystem-perm / repo-hygiene / capture-path / web-bind / source-side
class of items. Round 2 closed most of the architectural-but-still-
tractable items (Reflect ID validation, imperative-prefix detection,
SLSA + reproducible builds, HF SHA pinning, dashboard token auth,
egress allowlist) plus the hook RCE audit. What remains below either
needs a destructive git operation, depends on architectural design
input, or is a larger build-out worth its own scoped effort.

What's still in flight that the session couldn't take unilaterally:

## Destructive — need explicit user go-ahead

- **Remove committed probe binaries.** `cortex-or-probe`,
  `cortex-opencode-probe`, `cortex-pidev-probe` (3–8 MB each) live at
  repo root. Anyone with push could swap them silently. Action: `git rm`
  them, build in CI instead. Decision: confirm none of these are
  referenced by an external workflow first.

- **Pin GH Action versions to SHA.** Done automatically by the Renovate
  config added in round 1 (`helpers:pinGitHubActionDigests`). The
  manual step is to merge the first Renovate-opened "pin all actions"
  PR — that's the one-shot pin. After that, Renovate keeps the SHAs
  current via ongoing PRs.

## Architectural — need design review first

- **Insight quarantine.** Dream extracts "durable insights" from
  sampled content. Insights from low-trust sources (Project files,
  Git, Claude History) should land in a quarantine table requiring
  manual promotion before they enter the retrieval pool. Schema
  change + UX for promotion. (Partial mitigation already shipped:
  insights whose content trips IsLikelyPromptInjection are auto-
  neutered — Importance forced to 0.01, tagged.)

- **Hash-chained event log.** Each JSONL row includes hash of prior
  row. Detects post-hoc rewrite of the audit trail. Cheap to add,
  needs a migration path for existing stores.

- **Per-retrieval audit log.** Persist `(query, retrieved_ids,
  injected_text_hash, model_response_hash)` for every runtime inject —
  not just evals. Enables post-incident "what context made the model
  do X" forensics.

- **Cross-project retrieval isolation.** Verified-or-tested? Add an
  explicit test that from project B, a search cannot return project
  A's events. Document the failure mode in user-facing docs.

## Distribution surface

- **Homebrew formula SHA pinning + Cask attestation verification.**
  Releases are now signed via `actions/attest-build-provenance`. The
  formula should verify the signature during install with
  `gh attestation verify` or the equivalent Sigstore path.

- **`cortex install --upgrade` diff + lockfile.** Print a diff of any
  hook script changes before overwriting `~/.claude/`. Refuse if the
  installed hook has local modifications, unless `--force`.

## Model supply chain — next step

- **Prefer vendored local embedder for production.** HF Hub fetch
  acceptable in dev; production should use a checked-in / signed
  model artifact. (SHA verification at load is done — next step is
  vendor + remove the network fetch path in release builds.)

## Sandboxing

- **Eval execution sandbox.** Run evals with a separate UID, no write
  access to `.cortex/`, no network unless the scenario opts in. As
  eval inputs grow AI-generated, the runner can't trust them.
  `runVerifier` already carries an explicit TRUST CONTRACT comment
  marking the assumption that `bash -c <cmd>` only sees developer-
  authored YAML — a sandbox is what would let that assumption relax.

## Operational follow-ups

- **`step-security/harden-runner`: flip from audit to block.** Currently
  in audit-only mode across both workflows. After a couple of runs to
  observe the legitimate egress set, change `egress-policy: audit` →
  `egress-policy: block` with the observed allowlist.

- **Dependabot security updates: decide on duplicate-PR cleanup.**
  Renovate is now active alongside Dependabot alerts. Dependabot
  security updates were intentionally left on for redundancy (real
  malware/CVE alerts have proven valuable). After Renovate's run-rate
  is known, decide whether duplicate PRs justify turning Dependabot
  security updates off. Alerts stay on regardless.

## Already done in this hardening sweep (for reference)

Round 1 (filesystem / repo / capture / web bind / source-side):
- AI-agent deny list in `.claude/settings.local.json`
- `**/node_modules/` in `.gitignore`
- Web dashboard binds `127.0.0.1` only
- Capture queue / log / JSONL streams / storage data dir at 0700/0600
- Secret-value regex extended (AWS, GitHub PAT, Slack, PEM)
- `resolveCortexBinary` returns absolute path or throws (no PATH fallback)
- Retrieved context wrapped in `<retrieved_context trust="untrusted">`
- `govulncheck` + OSV-Scanner in CI
- ProjectSource hard-excludes credential dirs (`.aws/`, `.kube/`, etc.)

Round 2 (cognitive defenses / supply chain / transport / docs):
- `IsLikelyPromptInjection` predicate; wired into Analysis + Dream insight extraction
- Reflect drops fabricated IDs from ranking AND contradictions
- Capture sanitizes `event.ID` against path traversal (real bug found)
- Adversarial capture-path test suite (null bytes, oversized, UTF-8, etc.)
- Web dashboard token auth (`Authorization: Bearer` or `?token=`)
- HF embedding model SHA pinning (`verifyModelSHA`, config field)
- Egress allowlist on Anthropic / Ollama / OpenRouter clients
- `step-security/harden-runner` on every workflow job
- Reproducible build flags + SLSA build-provenance attestations in `release.yml`
- Renovate config with action-digest pinning
- Hook RCE-on-capture audit: clean (one `bash -c` callsite locked to
  developer-trusted scenario YAML by an explicit TRUST CONTRACT comment)
- `os.Environ()` audit: clean (only used to build subprocess env, never
  serialized into the store)
- `SECURITY.md` expanded with trust model, AI-supply-chain, and
  indirect-prompt-injection sections
