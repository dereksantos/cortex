"""In-memory storage for python-todo.

Threading note: Flask's default dev server is single-threaded so the
plain dict here is fine for tests. In production this would back onto
a real DB; the storage interface is the seam to swap on.
"""

from .models import Todo

MAX_TODOS = 100


class TodoStorage:
    def __init__(self) -> None:
        self._items: dict[str, Todo] = {}

    def list(self) -> list[Todo]:
        return list(self._items.values())

    def get(self, todo_id: str) -> Todo | None:
        return self._items.get(todo_id)

    def add(self, todo: Todo) -> None:
        if len(self._items) >= MAX_TODOS:
            raise StorageFullError(f"max {MAX_TODOS} todos reached")
        self._items[todo.id] = todo

    def remove(self, todo_id: str) -> bool:
        return self._items.pop(todo_id, None) is not None

    def update(self, todo: Todo) -> None:
        if todo.id not in self._items:
            raise KeyError(todo.id)
        self._items[todo.id] = todo


class StorageFullError(RuntimeError):
    """Raised when MAX_TODOS is reached."""
