# python-todo

A tiny Flask-style Todo API used as a fixture for Cortex's
codebase-reading eval suite. Hand-authored, self-contained, MIT.

## Layout

```
app/
  __init__.py     # create_app() factory
  models.py       # Todo dataclass + ID helpers
  routes.py       # GET/POST/PATCH/DELETE handlers
  storage.py      # in-memory dict-backed store
  auth.py         # bearer-token middleware
tests/
  test_routes.py  # endpoint smoke tests
  test_storage.py # storage invariants
requirements.txt
setup.py
.gitignore
```

## Endpoints

- `GET    /todos`         — list all todos
- `POST   /todos`         — create one (json body)
- `PATCH  /todos/<id>`    — toggle `done` or rename
- `DELETE /todos/<id>`    — remove

## Auth

Every endpoint expects `Authorization: Bearer <TODO_API_TOKEN>`. The
middleware lives in `app/auth.py` and short-circuits with HTTP 401.

## Default config

`MAX_TODOS = 100` — the in-memory store rejects creates past the cap.
