# Library Service (eval seed)

This is the empty starting point for the library-service multi-session eval.

The system specification is in `system-spec.md` (copied into the workdir by the eval runner).

The current session's task is provided directly to the model — you don't need to look it up.

This README and `cmd/server/main.go` are intentionally bare. The first session designs the project layout. Subsequent sessions follow the conventions S1 established.

Add dependencies (e.g., `modernc.org/sqlite`) as you need them via `go get`. The eval measures cohesion, not dependency choice.
