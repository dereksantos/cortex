# Session 3 — Loans

Two previous sessions set up Books and Authors. Read the existing code first.

## Your task in this session

Implement the **Loans** resource end-to-end. Loans is special: it references both Books and Members, but Members doesn't exist yet — so reference it by `member_id` (uuid) without enforcing the foreign key in code yet (the schema can declare it; just don't validate at the handler level until Members exists).

- HTTP handlers for all 5 CRUD endpoints
- Storage layer
- Tests
- Schema additions

## Important

Match the conventions established by Books and Authors. Your code should look structurally identical to those modulo the resource name and the additional fields.

Look at both Books and Authors before writing yours. They should agree on patterns; if they don't, prefer the Books style.

Make sure `go build ./...` and `go test ./...` pass.

Out of scope: members, branches.
