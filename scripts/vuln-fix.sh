#!/usr/bin/env bash
# vuln-fix.sh — upgrade every dependency govulncheck reports as REACHABLE.
#
#   ./scripts/vuln-fix.sh            # apply the upgrades
#   ./scripts/vuln-fix.sh --dry-run  # print what would change, touch nothing
#
# Driven by .github/workflows/vuln-fix.yml, which runs it on demand and opens
# a PR with the result. Kept as a script rather than inline YAML so it can be
# read, run, and debugged locally.
#
# "Reachable" is the whole point. govulncheck reports three tiers: modules you
# require, packages you import, and code you actually call. Only the third can
# hurt you, and only that tier is upgraded here — so this never churns go.mod
# for a CVE in a code path the binary never enters. A finding counts as
# reachable when it carries a fixed version AND its trace bottoms out in a
# named function.
#
# Note it upgrades to the exact version govulncheck names as the fix, not to
# @latest. That keeps the diff to the security floor and leaves ordinary
# version churn to Renovate, which owns dependency updates here.
set -euo pipefail

cd "$(dirname "$0")/.."

DRY_RUN=0
[[ "${1:-}" == "--dry-run" ]] && DRY_RUN=1

command -v jq >/dev/null || { echo "vuln-fix: jq is required" >&2; exit 1; }

if ! command -v govulncheck >/dev/null; then
  echo "vuln-fix: govulncheck not found — install it with:" >&2
  echo "  go install golang.org/x/vuln/cmd/govulncheck@v1.6.0" >&2
  exit 1
fi

echo "vuln-fix: scanning..."
# govulncheck exits non-zero when it finds anything, which is the normal case
# here — don't let set -e treat that as a failure.
scan=$(govulncheck -format json ./... 2>/dev/null || true)

# govulncheck emits concatenated JSON objects, not one array; jq -s slurps them.
# Collected with a read loop rather than mapfile: mapfile is bash 4+, and macOS
# still ships bash 3.2, which would make this script CI-only.
upgrades=()
while IFS= read -r line; do
  [[ -n "$line" ]] && upgrades+=("$line")
done < <(
  printf '%s' "$scan" | jq -s -r '
    [ .[]
      | select(.finding.fixed_version and (.finding.trace[0].function // empty))
      | { module: .finding.trace[0].module, fixed: .finding.fixed_version }
    ]
    | unique
    | .[]
    | "\(.module)@\(.fixed)"
  '
)

if [[ ${#upgrades[@]} -eq 0 ]]; then
  echo "vuln-fix: no reachable vulnerabilities — nothing to do."
  exit 0
fi

echo "vuln-fix: ${#upgrades[@]} reachable module(s) to upgrade:"
printf '  %s\n' "${upgrades[@]}"

if [[ $DRY_RUN -eq 1 ]]; then
  echo "vuln-fix: --dry-run, stopping before go get."
  exit 0
fi

go get "${upgrades[@]}"
go mod tidy

echo "vuln-fix: verifying..."
go build ./...
go test ./...

echo "vuln-fix: re-scanning..."
govulncheck ./... || {
  echo "vuln-fix: findings REMAIN after the upgrade — the fixed version may not"
  echo "          cover every finding, or a new one surfaced. Review before merging."
  exit 0
}
echo "vuln-fix: clean."
