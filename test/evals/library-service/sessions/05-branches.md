# Session 5 — Branches

Four previous sessions set up Books, Authors, Loans, and Members. Read the existing code first.

## Your task in this session

Implement the **Branches** resource end-to-end:
- HTTP handlers for all 5 CRUD endpoints
- Storage layer
- Tests
- Schema additions

## Important

Match the conventions established by the existing resources. Your code should look structurally identical to them modulo the resource name.

Look at all four existing resources before writing yours. They should agree on patterns; if they don't, prefer the Books style.

Make sure `go build ./...` and `go test ./...` pass.

This is the final session. After your changes, all 25 endpoints across 5 resources should be implemented and the integration test should pass.
