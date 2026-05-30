"""Todo dataclass + id helpers."""

from dataclasses import dataclass, field
from uuid import uuid4


@dataclass
class Todo:
    """One todo item.

    Attributes:
        id:   uuid4 hex string, generated at creation.
        text: free-form text (max 280 chars enforced upstream).
        done: completion flag.
    """

    id: str = field(default_factory=lambda: uuid4().hex)
    text: str = ""
    done: bool = False


def to_dict(todo: Todo) -> dict:
    return {"id": todo.id, "text": todo.text, "done": todo.done}


MAX_TEXT_LEN = 280
