#!/usr/bin/env bash
# scripts/ci/commit-identity.sh BASE HEAD — fail if any non-merge commit in
# BASE..HEAD was made by an agent without a githug identity.
#
# Agent work in this repo goes through githug (https://githug.ai): a
# short-lived, repo-scoped token and the agent's own commit identity. A commit
# passes when its author is:
#   - a githug agent (…@agents.githug.ai, or the githug bot accounts below),
#   - a dependency bot (Renovate, Dependabot), or
#   - the maintainer — unless the commit carries an AI co-author trailer
#     (Co-Authored-By: Claude / Cortex) without githug's Accountable: trailer,
#     which means an agent committed with the maintainer's own identity.
# Any other author fails.
set -euo pipefail

base="${1:?usage: commit-identity.sh BASE HEAD}"
head="${2:?usage: commit-identity.sh BASE HEAD}"

agent_email='(@agents\.githug\.ai|^[0-9]+\+(githug-gateway|cortex-githug)\[bot\]@users\.noreply\.github\.com)$'
dep_bot_email='^[0-9]+\+(renovate|dependabot)\[bot\]@users\.noreply\.github\.com$'
maintainer_email='^([0-9]+\+)?dereksantos@users\.noreply\.github\.com$|^santos\.derek@gmail\.com$'
ai_trailer='^co-authored-by:[[:space:]]*(claude|cortex)\b'

fail=0
while IFS= read -r sha; do
  email=$(git log -1 --format='%ae' "$sha")
  name=$(git log -1 --format='%an' "$sha")
  subject=$(git log -1 --format='%s' "$sha")
  if [[ "$email" =~ $agent_email || "$email" =~ $dep_bot_email ]]; then
    continue
  fi
  if [[ "$email" =~ $maintainer_email ]]; then
    body=$(git log -1 --format='%B' "$sha")
    if printf '%s\n' "$body" | grep -qiE "$ai_trailer" &&
      ! printf '%s\n' "$body" | grep -qiE '^accountable:'; then
      echo "✖ ${sha:0:9} \"$subject\" — authored as $name with an AI co-author trailer: agent work must be committed through githug (hug setup --agent <name>), not the maintainer's identity."
      fail=1
    fi
    continue
  fi
  echo "✖ ${sha:0:9} \"$subject\" — author $name <$email> is not a githug agent, a dependency bot, or the maintainer."
  fail=1
done < <(git rev-list --no-merges "$base..$head")

if (( fail )); then
  echo
  echo "Commit identity check failed. Agent commits in this repo must use githug: run 'hug setup --agent <name>' in the clone and open PRs with 'hug pr create --as agent'."
  exit 1
fi
echo "✓ every commit in ${base:0:9}..${head:0:9} has an allowed identity"
