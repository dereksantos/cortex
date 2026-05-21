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
  # Exclude vendor/, .cortex/ (session snapshots — frozen historical
  # copies that may pre-date gofmt rule changes), and test/evals/
  # (eval-target source trees not part of the cortex module).
  local unformatted
  unformatted="$(gofmt -l . | grep -Ev '^(vendor|\.cortex|test/evals)/' || true)"
  if [[ -n "$unformatted" ]]; then
    echo "✖ gofmt: the following files are not formatted:" >&2
    echo "$unformatted" >&2
    echo "" >&2
    echo "Fix with: gofmt -w \$(<filename>)" >&2
    return 1
  fi
}

run_vet() {
  # ./... would include test/evals/{fixtures,projects} which are
  # eval-target source trees not part of the cortex module. List the
  # real package roots explicitly. (./integrations/... existed pre-
  # audit-D1 and is gone now; do not re-add unless the directory is
  # restored.)
  if ! go vet ./cmd/... ./internal/... ./pkg/... 2>&1; then
    echo "" >&2
    echo "✖ go vet: errors above" >&2
    return 1
  fi
}

run_lint() {
  # Resolution order: PATH → $GOBIN → $(go env GOPATH)/bin. Catches
  # the common `go install golangci-lint@latest` install location even
  # when ~/go/bin isn't on PATH (the GitHub Action and ad-hoc devs
  # both rely on that).
  local bin
  bin="$(command -v golangci-lint 2>/dev/null || true)"
  if [[ -z "$bin" ]]; then
    if [[ -n "${GOBIN:-}" && -x "${GOBIN}/golangci-lint" ]]; then
      bin="${GOBIN}/golangci-lint"
    else
      local gopath
      gopath="$(go env GOPATH 2>/dev/null || echo "$HOME/go")"
      if [[ -x "${gopath}/bin/golangci-lint" ]]; then
        bin="${gopath}/bin/golangci-lint"
      fi
    fi
  fi
  if [[ -z "$bin" ]]; then
    echo "✖ golangci-lint not installed." >&2
    echo "  Install:  brew install golangci-lint" >&2
    echo "  Or:       go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest" >&2
    return 1
  fi
  if ! "$bin" run --timeout=5m; then
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
