#!/usr/bin/env bash
# scripts/check.sh — formatting + lint + vet gate.
#
# Runs the checks the pre-commit hook + CI run. Exits non-zero on
# the first failure so callers can chain it. Intended to be cheap
# enough to run on every commit:
#
#   gofmt   ~100ms on cortex's tree
#   go vet  ~1-2s
#   golangci-lint (if installed)  ~5-10s
#
# Usage:
#   ./scripts/check.sh            # all checks
#   ./scripts/check.sh fmt        # gofmt only (what the hook runs first)
#   ./scripts/check.sh vet        # go vet only
#   ./scripts/check.sh lint       # golangci-lint only (skipped if absent)
#
# To install as the pre-commit hook:
#   git config core.hooksPath .githooks

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$repo_root"

run_fmt() {
  local unformatted
  unformatted="$(gofmt -l . | grep -v '^vendor/' || true)"
  if [[ -n "$unformatted" ]]; then
    echo "✖ gofmt: the following files are not formatted:" >&2
    echo "$unformatted" >&2
    echo "" >&2
    echo "Fix with: gofmt -w ." >&2
    return 1
  fi
}

run_vet() {
  if ! go vet ./... 2>&1; then
    echo "" >&2
    echo "✖ go vet: errors above" >&2
    return 1
  fi
}

run_lint() {
  if ! command -v golangci-lint >/dev/null 2>&1; then
    # Lint is optional locally; CI is the enforcement layer.
    echo "(golangci-lint not installed — skipping; CI will enforce)"
    return 0
  fi
  if ! golangci-lint run ./...; then
    echo "" >&2
    echo "✖ golangci-lint: violations above" >&2
    return 1
  fi
}

case "${1:-all}" in
  fmt)  run_fmt ;;
  vet)  run_vet ;;
  lint) run_lint ;;
  all)  run_fmt && run_vet && run_lint ;;
  *)    echo "Usage: $0 [fmt|vet|lint|all]" >&2; exit 2 ;;
esac

echo "✓ checks passed"
