from app import create_app


def _client():
    app = create_app(token="test-token")
    return app.test_client()


def _auth():
    return {"Authorization": "Bearer test-token"}


def test_list_empty():
    c = _client()
    r = c.get("/todos", headers=_auth())
    assert r.status_code == 200
    assert r.get_json() == []


def test_create_and_list():
    c = _client()
    r = c.post("/todos", json={"text": "buy milk"}, headers=_auth())
    assert r.status_code == 201
    todo = r.get_json()
    assert todo["text"] == "buy milk"
    assert todo["done"] is False

    r = c.get("/todos", headers=_auth())
    assert len(r.get_json()) == 1


def test_unauthorized():
    c = _client()
    r = c.get("/todos")
    assert r.status_code == 401
