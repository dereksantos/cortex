# Session 1 — Scaffold + Books

You are starting a new Go HTTP service. The full system specification is in `system-spec.md` (also reproduced in the project README).

## Your task in this session

1. Set up the project structure (your choice of layout — `cmd/`, `internal/`, etc.)
2. Set up SQLite schema initialization at startup
3. Implement the **Books** resource end-to-end:
   - HTTP handlers for all 5 CRUD endpoints
   - Storage layer (SQL queries against the books table)
   - Tests for each handler
4. Wire it into `cmd/server/main.go` so the server runs and serves `/books*`
5. Make sure `go build ./...` and `go test ./...` pass

## Important

You are establishing the conventions that future sessions will follow:
- Error handling style
- Response envelope shape
- Validation approach
- Logging choice
- Test layout and assertion style
- Naming conventions

Choose deliberately. Future sessions will be told to match your conventions, but they will not have your reasoning, only your code.

Out of scope for this session: authors, loans, members, branches. Just books.
