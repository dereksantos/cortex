#!/bin/bash
# ABR test driver: 4 configs × 3 trials.
# Outputs go to /tmp/cortex-abr-test/<config>/trial-<n>.txt
#
# Scenario: project decision says "use sqlx, NOT pgx, NOT database/sql".
# Cold model is expected to default to pgx (modern Go-postgres standard).
# Context-injected model should produce sqlx code.

set -uo pipefail

CORTEX=/Users/dereksantos/eng/projects/cortex-dag-stage-2/bin/cortex
TASK='Print the following Go code in your response text (do not use any tools, do not output JSON, just print the Go code in plain text):

A complete Go file with: package main, imports block, and a function InsertUser(name, email string) error that connects to a postgres database and inserts a single user record into a users table. Include the package declaration, all imports needed, and the full function body.

Output only the Go source code in your text response. Stop after the code.'

CONTEXT='Project context — decisions already made for this codebase:
- Use github.com/jmoiron/sqlx (sqlx) for all postgres queries.
- REJECTED: github.com/jackc/pgx (pgx) — do not use.
- REJECTED: database/sql alone — do not use.
- Use sqlx.Connect() to open the connection; use db.NamedExec() or db.MustExec() for inserts.

Write code that follows these project decisions strictly.'

run_one() {
    local cfg="$1"; local trial="$2"; local model="$3"; local with_context="$4"
    local workdir="/tmp/cortex-abr-test/${cfg}"
    local trialdir="${workdir}/trial-${trial}"
    mkdir -p "$trialdir"
    local out="${trialdir}/output.txt"
    local cmd=("$CORTEX" code --workdir "$trialdir" --model "$model" --max-turns 4 --quiet --no-search)
    # qwen models route through local Ollama; haiku stays on OpenRouter (default).
    case "$model" in
        qwen*) cmd+=(--local) ;;
    esac
    if [ "$with_context" = "yes" ]; then
        cmd+=(--system-prompt "$CONTEXT")
    fi
    cmd+=("$TASK")
    echo "=== $cfg trial-$trial ==="
    "${cmd[@]}" > "$out" 2>&1
    echo "exit=$?"
    echo "--- output ---"
    cat "$out"
    echo "--- end ---"
    echo
}

for t in 1 2 3; do
    run_one qwen-cold     "$t" "qwen2.5-coder:1.5b"        "no"
    run_one qwen-context  "$t" "qwen2.5-coder:1.5b"        "yes"
    run_one haiku-cold    "$t" "anthropic/claude-haiku-4.5" "no"
    run_one haiku-context "$t" "anthropic/claude-haiku-4.5" "yes"
done
