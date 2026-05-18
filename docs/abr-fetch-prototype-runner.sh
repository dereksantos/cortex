#!/bin/bash
# Third-arm ABR: qwen + context + worked sqlx example.
# Tests the "remember.fetch_external" hypothesis: would injecting a
# sqlx code snippet (what such an op would fetch on demand) flip
# qwen-1.5b from 0/3 to N/3 on the InsertUser task?

set -uo pipefail

CORTEX=/Users/dereksantos/eng/projects/cortex-dag-stage-2/bin/cortex
TASK='Print the following Go code in your response text (do not use any tools, do not output JSON, just print the Go code in plain text):

A complete Go file with: package main, imports block, and a function InsertUser(name, email string) error that connects to a postgres database and inserts a single user record into a users table. Include the package declaration, all imports needed, and the full function body.

Output only the Go source code in your text response. Stop after the code.'

# This is what a `remember.fetch_external` op would inject when it
# detected qwen lacked sqlx depth: the decision text PLUS a small
# worked example demonstrating the API surface.
CONTEXT_WITH_EXAMPLE='Project context — decisions already made for this codebase:
- Use github.com/jmoiron/sqlx (sqlx) for all postgres queries.
- REJECTED: github.com/jackc/pgx (pgx) — do not use.
- REJECTED: database/sql alone — do not use.

Reference example of how this project uses sqlx (follow this pattern):

```go
package main

import (
    "github.com/jmoiron/sqlx"
    _ "github.com/lib/pq"
)

func GetUserByID(id int) (string, error) {
    db, err := sqlx.Connect("postgres", "user=postgres dbname=mydb sslmode=disable")
    if err != nil {
        return "", err
    }
    defer db.Close()
    var name string
    err = db.Get(&name, "SELECT name FROM users WHERE id=$1", id)
    return name, err
}
```

Write code that follows these project decisions strictly. Use sqlx.Connect and the sqlx-style API like the example.'

mkdir -p /tmp/cortex-abr-test/qwen-context-example

for t in 1 2 3; do
    trialdir="/tmp/cortex-abr-test/qwen-context-example/trial-${t}"
    mkdir -p "$trialdir"
    out="${trialdir}/output.txt"
    echo "=== qwen-context-example trial-$t ==="
    "$CORTEX" code --workdir "$trialdir" --model "qwen2.5-coder:1.5b" --max-turns 4 --quiet --no-search --local --system-prompt "$CONTEXT_WITH_EXAMPLE" "$TASK" > "$out" 2>&1
    echo "exit=$?"
    cat "$out"
    echo
done
