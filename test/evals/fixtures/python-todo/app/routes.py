"""HTTP route handlers."""

from flask import Flask, jsonify, request

from .models import MAX_TEXT_LEN, Todo, to_dict
from .storage import StorageFullError


def register_routes(app: Flask) -> None:
    @app.get("/todos")
    def list_todos():
        return jsonify([to_dict(t) for t in app.storage.list()])

    @app.post("/todos")
    def create_todo():
        body = request.get_json(silent=True) or {}
        text = (body.get("text") or "").strip()
        if not text:
            return jsonify(error="text required"), 400
        if len(text) > MAX_TEXT_LEN:
            return jsonify(error=f"text too long (>{MAX_TEXT_LEN})"), 400
        todo = Todo(text=text)
        try:
            app.storage.add(todo)
        except StorageFullError as e:
            return jsonify(error=str(e)), 507
        return jsonify(to_dict(todo)), 201

    @app.patch("/todos/<string:todo_id>")
    def patch_todo(todo_id: str):
        todo = app.storage.get(todo_id)
        if not todo:
            return jsonify(error="not found"), 404
        body = request.get_json(silent=True) or {}
        if "text" in body:
            todo.text = body["text"]
        if "done" in body:
            todo.done = bool(body["done"])
        app.storage.update(todo)
        return jsonify(to_dict(todo))

    @app.delete("/todos/<string:todo_id>")
    def delete_todo(todo_id: str):
        if not app.storage.remove(todo_id):
            return jsonify(error="not found"), 404
        return ("", 204)
