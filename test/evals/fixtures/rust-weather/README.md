# rust-weather

A tiny axum-style weather service used as a fixture for Cortex's
codebase-reading eval suite. Hand-authored, self-contained, MIT.

## Layout

```
src/
  main.rs       # binary entrypoint, builds the Router
  router.rs     # GET /forecast, GET /current, /health
  handlers.rs   # request handlers + JSON shape
  models.rs     # Forecast / Reading structs
  storage.rs    # in-memory station cache
  auth.rs       # API-key middleware
tests/
  integration_test.rs
Cargo.toml
.gitignore
```

## Endpoints

- `GET /current?station=<id>`   — most recent reading
- `GET /forecast?station=<id>`  — next-12h synthetic forecast
- `GET /health`                 — `{"ok":true}` (unauthed)

## Auth

Non-`/health` endpoints require `X-API-Key: <key>` matching
`WEATHER_API_KEY`. See `src/auth.rs`.

## Default config

`MAX_STATIONS = 64` — the in-memory cache caps at this number.
