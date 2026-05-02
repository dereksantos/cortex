# Library Service (eval seed)

This is the empty starting point for the library-service multi-session eval.

The system specification is in `../../library-service/system-spec.md`.

The current session's task is in `../../library-service/sessions/<NN>-*.md`.

This README and `cmd/server/main.go` are intentionally bare. The first session designs the project layout. Subsequent sessions follow the conventions S1 established.

Add dependencies (e.g., `modernc.org/sqlite`) as you need them via `go get`. The eval measures cohesion, not dependency choice.
