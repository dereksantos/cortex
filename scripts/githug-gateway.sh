#!/usr/bin/env sh
# githug Gateway client for the cortex harness.
#
# Exchanges the agent key for a 1-hour, repo-scoped GitHub token minted from the
# "githug Gateway" GitHub App. cortex never holds a long-lived GitHub credential.
#
#   GITHUG_AGENT_KEY   the agent's bearer key (ghg_…), from the 600 secrets file
#   GITHUG_GATEWAY     gateway origin (default https://githug.ai)
#   GITHUG_REPO        owner/repo (default: the origin remote of the current checkout)
#   GITHUG_RUN_ID      optional id of the run minting the token (e.g. a loop tick); the
#                      gateway records it with the mint so every token traces to its run
#
# Modes:
#   scripts/githug-gateway.sh token            → prints a fresh token (for GH_TOKEN)
#   scripts/githug-gateway.sh env              → prints `export GH_TOKEN=… GIT_AUTHOR_NAME=… …` to eval
#   scripts/githug-gateway.sh credential get   → git credential-helper protocol (auto-refresh on every push)
#
# Wire git once per container:
#   git config --global credential.https://github.com.helper "!$PWD/scripts/githug-gateway.sh credential"
#   eval "$(scripts/githug-gateway.sh env)"     # GH_TOKEN for gh, author identity for commits
set -eu

GATEWAY="${GITHUG_GATEWAY:-https://githug.ai}"
KEY="${GITHUG_AGENT_KEY:-}"
[ -n "$KEY" ] || { echo "githug-gateway: GITHUG_AGENT_KEY is not set" >&2; exit 1; }

repo_from_origin() {
  git config --get remote.origin.url 2>/dev/null | sed -E 's#^(https://github\.com/|git@github\.com:)##; s#\.git$##'
}
REPO="${GITHUG_REPO:-$(repo_from_origin)}"
# Restricted to a JSON-safe charset so it can be spliced into the body unescaped.
RUN=$(printf '%s' "${GITHUG_RUN_ID:-}" | tr -cd 'A-Za-z0-9._:-' | cut -c1-120)

fetch() {
  curl -fsS -X POST "$GATEWAY/v1/token" \
    -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
    -d "{\"repo\":\"$REPO\",\"run\":\"$RUN\"}"
}
json_field() { # $1 = json, $2 = top-level string field
  printf '%s' "$1" | sed -n "s/.*\"$2\":\"\([^\"]*\)\".*/\1/p"
}

case "${1:-token}" in
  token)
    R=$(fetch); json_field "$R" token; echo ;;
  env)
    R=$(fetch)
    T=$(json_field "$R" token)
    NAME=$(printf '%s' "$R" | sed -n 's/.*"git_author":{"name":"\([^"]*\)".*/\1/p')
    EMAIL=$(printf '%s' "$R" | sed -n 's/.*"git_author":{"name":"[^"]*","email":"\([^"]*\)".*/\1/p')
    ACC=$(json_field "$R" accountable)
    echo "export GH_TOKEN='$T'"
    [ -n "$NAME" ] && echo "export GIT_AUTHOR_NAME='$NAME' GIT_COMMITTER_NAME='$NAME'"
    [ -n "$EMAIL" ] && echo "export GIT_AUTHOR_EMAIL='$EMAIL' GIT_COMMITTER_EMAIL='$EMAIL'"
    echo "export GITHUG_ACCOUNTABLE='$ACC'"
    ;;
  credential)
    # git calls: `<helper> get` with key=value lines on stdin; we answer for github.com only.
    [ "${2:-}" = "get" ] || exit 0
    HOST=""; while IFS= read -r line; do case "$line" in host=*) HOST="${line#host=}";; esac; [ -z "$line" ] && break; done
    [ "$HOST" = "github.com" ] || exit 0
    R=$(fetch)
    printf 'username=x-access-token\npassword=%s\n' "$(json_field "$R" token)"
    ;;
  *)
    echo "usage: $0 [token|env|credential get]" >&2; exit 2 ;;
esac
