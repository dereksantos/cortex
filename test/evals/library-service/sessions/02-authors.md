# Session 2 — Authors

A previous session set up the project and the Books resource. Read the existing code to understand the conventions in use.

## Your task in this session

Implement the **Authors** resource end-to-end, **matching the patterns established for Books**:
- HTTP handlers for all 5 CRUD endpoints (list, get, create, update, delete)
- Storage layer
- Tests for each handler
- Schema additions if needed

## Important

The codebase has conventions. Follow them. Specifically, your Authors handlers, storage code, and tests should be **structurally indistinguishable** from the Books versions modulo the resource name.

Look at the Books implementation before writing yours. Match:
- Function naming pattern
- Error-handling style
- Response shape
- Validation approach
- Test layout and assertions
- Logging idiom

Make sure `go build ./...` and `go test ./...` pass after your changes.

Out of scope: loans, members, branches.
