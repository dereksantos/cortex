"""python-todo: Flask app factory.

The factory builds a single Flask instance with the auth middleware,
the in-memory storage layer, and the route handlers wired up. Tests
import create_app() directly so they can patch storage without standing
up a server.
"""

from flask import Flask

from .auth import require_bearer
from .routes import register_routes
from .storage import TodoStorage

API_VERSION = "1.0.0"


def create_app(token: str | None = None) -> Flask:
    """Return a configured Flask app.

    Args:
        token: bearer token the auth middleware requires. None falls
            back to the TODO_API_TOKEN env var; if neither is set the
            middleware refuses every request.
    """
    app = Flask(__name__)
    app.config["TODO_API_TOKEN"] = token
    app.storage = TodoStorage()
    app.before_request(require_bearer)
    register_routes(app)
    return app
