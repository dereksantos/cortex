# Model self-healing — discovery, fallback, and diagnosis

**Status: live direction (2026-07-20).** Pre-release requirement: when a
configured model is unavailable or failing, Cortex must fall back onto the
curated suite of free OpenRouter models, recover the session onto a working
model, and tell the user exactly what happened and what to change.

## What exists today (and why it isn't enough)

- `preflight.go` checks the live OpenRouter catalog **once, at session
  construction, for curated bindings only**. A pinned model that has been
  retired, a model that is listed but 429/500-ing per request, or any failure
  that starts mid-session is invisible to it.
- `transport.go`'s `Send`/`SendStream` retry 429/5xx up to `max_send_attempts`
  — **always on the same model**. 401/403/404 fail on the first attempt.
- Every transport error crosses the `Sender` seam as a flat wrapped string
  (`"model call failed after 3 attempts: agent returned 404: {...}"`). The
  REPL and headless drivers print it raw and end the turn. The only
  error-class that feeds back into model state is context overflow
  (`learnedWindows`, `parseCtxSize`).

## Design

Four pieces, layered so each is independently testable.

### 1. Typed classification at the transport boundary (`modelerr.go`)

`modelCallError{Status, Class, Model, Detail}` — the `ContextOverflowError`
pattern applied to the rest of the failure space. `sendOnce` returns it for
every non-200; `Send`'s `%w` wrapping preserves it through `errors.As`.
Classes:

| Class | Trigger | Healable by model swap? |
|---|---|---|
| `model-missing` | 404; 400/404 bodies naming an unknown/invalid model or "no endpoints" | **yes** |
| `rate-limited` | 429 (after transport retries exhausted) | **yes** |
| `server` | 5xx (after transport retries exhausted) | **yes** |
| `auth` | 401 / 403 | no — the key is broken, not the model |
| `timeout` | request deadline exceeded | no — same endpoint would time out again |
| `unreachable` | connection failure before any HTTP status | no — endpoint/network problem |

`classifyModelError(err, model)` resolves an error to a class: `errors.As`
first, then bounded string sniffing for the streaming path (`stream status
N`), `context.DeadlineExceeded`, and transport-level failures. String
sniffing is confined to this one function.

### 2. A healing `Sender` decorator (`heal.go`)

`cs.healingSender(role, inner)` wraps `coderSender()` and `blockingSender()`
— the seam every model round-trip already crosses, so `runLoop` and the
subagents stay untouched and a mid-turn recovery resumes the *same* request
on the new model instead of abandoning the turn.

On a send error, when the backend is OpenRouter and `network.self_heal` is
on (default **true**):

1. Classify. Non-healable classes pass straight through (diagnosis handles
   them; swapping models cannot fix a revoked key or a dead endpoint).
2. Mark the failing model dead **for this session** (`cs.deadModels`) so
   later turns and other roles skip it.
3. Fetch the live catalog once (bounded by the existing preflight timeout).
   If the catalog itself is unreachable, do not thrash — return the original
   error; the endpoint is the problem.
4. Walk the existing ladder — `nextCuratedPick` → `discoverFreeModel` —
   skipping session-dead models, and re-issue the pending request on the
   candidate. The real request is the smoke test: a candidate that fails is
   marked dead and the walk continues, capped at `healMaxCandidates` (3).
5. On success: mutate the live binding the way `SetModel` does —
   `req.Model`, and the role's session state (`cs.Window` for code,
   `cs.Study.Model/Window` for study) so every later `requestFor` builds on
   the healed model — print one stderr line (`old → new, why`), and journal
   a `model.substitution` event with the failure class in the reason.
6. On exhaustion: journal one `model.failure` event and return the original
   error, classified, to the caller.

The config file is **never rewritten** — same posture as the startup
preflight: heal the process, tell the user, let them decide whether to
change the pin.

Non-OpenRouter backends never ladder (there is no free suite behind an
Ollama endpoint); they still get classification and diagnosis.

### 3. Startup preflight covers pinned models (`preflight.go`)

Today the preflight refuses to second-guess a user's pinned model. Under
`network.self_heal` (default on) it now also substitutes a **pinned**
OpenRouter model that is absent from the live catalog — same ladder, same
one-line notice, same journal event, config untouched. Set
`network.self_heal: false` to restore the old "never touch my pin" posture.

### 4. Diagnosis (`main.go`, `cli.go`, `model.go`, `internal/journal`)

- Turn errors print a classified one-liner instead of raw wrapping: what
  failed (model + class), what Cortex did (healed to X / gave up after N
  candidates / did not attempt), and the one thing to change (`key_env`,
  the pin in `.cortex/config.json`, the endpoint, or "run `cortex model`").
- New journal event `model.failure` (v1: role, model, class, status,
  detail) beside the existing `model.substitution`.
- `cortex model` gains a trailing "recent model events" section — the last
  few substitution/failure events from `.cortex/journal/model/` — so a user
  can see what churned and why after the fact.

## Bounds

- At most one catalog fetch per heal attempt, bounded by
  `network.preflight_timeout_sec` (existing default 4s).
- At most `healMaxCandidates` (3) replacement sends per failing call.
- `cs.deadModels` is session-local; nothing is persisted except journal
  events. A model that was rate-limited today is retried fresh tomorrow.
- Healing never fires for `context.Canceled` (user interrupt) and never
  during an already-streaming response (`started` guard stays as-is —
  a partially-streamed turn returns its error; the *next* send heals).

## Tests

- `modelerr_test.go`: table-driven classification — statuses, OpenRouter
  body shapes, stream-status strings, deadline/connection errors.
- `heal_test.go`: fake inner sender + fake catalog — 404 heals to next
  curated pick and mutates session state; dead models excluded on the next
  failure; auth/timeout/unreachable never swap; catalog-down returns the
  original error; candidate exhaustion journals `model.failure`; the
  `network.self_heal:false` gate disables both mid-session healing and
  pinned-preflight substitution.
- `preflight_test.go`: pinned-model substitution added beside the existing
  curated cases.
