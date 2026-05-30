"""Bearer-token middleware.

Every request must carry `Authorization: Bearer <token>` matching the
app's configured TODO_API_TOKEN. Missing or mismatched tokens 401.
"""

import os

from flask import abort, current_app, request


def require_bearer() -> None:
    expected = current_app.config.get("TODO_API_TOKEN") or os.environ.get("TODO_API_TOKEN")
    if not expected:
        # No token configured = reject everything. Better than silently
        # opening the API.
        abort(401, description="server misconfigured: no TODO_API_TOKEN")

    header = request.headers.get("Authorization", "")
    if not header.startswith("Bearer "):
        abort(401, description="missing bearer token")
    presented = header[len("Bearer ") :]
    if presented != expected:
        abort(401, description="invalid bearer token")
