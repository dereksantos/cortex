# Library Service — System Specification

This spec is shared verbatim across all sessions. It defines *what* the system does. Implementation patterns (error handling, naming, tests, etc.) are deliberately left to the model.

## Overview

A small HTTP API for a public library. Five resources, each with standard CRUD.

## Resources

### Books
- `id` (uuid, server-assigned)
- `title` (string, required)
- `isbn` (string, required, unique)
- `author_id` (uuid, references authors.id)
- `published_year` (int, optional)

### Authors
- `id` (uuid, server-assigned)
- `name` (string, required)
- `birth_year` (int, optional)

### Loans
- `id` (uuid, server-assigned)
- `book_id` (uuid, references books.id)
- `member_id` (uuid, references members.id)
- `loaned_at` (timestamp)
- `returned_at` (timestamp, nullable)

### Members
- `id` (uuid, server-assigned)
- `name` (string, required)
- `email` (string, required, unique)

### Branches
- `id` (uuid, server-assigned)
- `name` (string, required)
- `address` (string, required)

## Endpoints (per resource)

For a resource `X`:
- `GET /X` — list (with optional `limit` and `offset` query params)
- `GET /X/{id}` — get one
- `POST /X` — create (body: JSON without `id`; returns the created resource with `id`)
- `PUT /X/{id}` — update (body: JSON; full replacement of mutable fields)
- `DELETE /X/{id}` — delete (returns 204)

## Constraints

- Storage: SQLite via `database/sql` + `modernc.org/sqlite` (no CGO).
- Standard library `net/http` for the server. No web framework.
- Server listens on `:8080` by default; override with `PORT` env var.
- Schema is created at startup if not present.
- All responses are JSON. Errors return JSON with at least an error message.
- Validation: required fields must be present; uniqueness must be enforced.

## Out of scope

- Authentication / authorization
- Pagination beyond simple `limit`/`offset`
- Soft deletes
- Migrations beyond initial schema
- Logging framework choice (use whatever you prefer, just be consistent)
