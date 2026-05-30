import pytest

from app.models import Todo
from app.storage import MAX_TODOS, StorageFullError, TodoStorage


def test_add_and_get():
    s = TodoStorage()
    t = Todo(text="x")
    s.add(t)
    assert s.get(t.id) is t


def test_full():
    s = TodoStorage()
    for i in range(MAX_TODOS):
        s.add(Todo(text=f"t{i}"))
    with pytest.raises(StorageFullError):
        s.add(Todo(text="one too many"))


def test_remove_missing():
    s = TodoStorage()
    assert s.remove("nope") is False
