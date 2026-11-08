from .conftest import auth_headers


def test_models_requires_auth(client):
    response = client.get("/v1/models")
    assert response.status_code == 401
    assert "error" in response.json()


def test_models_rejects_bad_key(client):
    response = client.get("/v1/models", headers={"Authorization": "Bearer sk-wrong"})
    assert response.status_code == 401


def test_models_lists_models_and_aliases(client, env):
    response = client.get("/v1/models", headers=auth_headers(env))
    assert response.status_code == 200
    body = response.json()
    assert body["object"] == "list"
    ids = {m["id"] for m in body["data"]}
    assert {"good-model", "flaky-model", "broken-model", "big", "fast"} <= ids
    assert all(m["object"] == "model" for m in body["data"])
