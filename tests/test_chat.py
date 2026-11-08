import json

from sqlalchemy import select

from .conftest import auth_headers
from switchboard.models import Organization, RequestLog


def chat_payload(model, **extra):
    payload = {
        "model": model,
        "messages": [{"role": "user", "content": "hello"}],
    }
    payload.update(extra)
    return payload


def get_logs(app):
    db = app.state.session_factory()
    try:
        return db.execute(select(RequestLog).order_by(RequestLog.id)).scalars().all()
    finally:
        db.close()


def test_chat_completion_success(client, app, env, stub_good):
    response = client.post(
        "/v1/chat/completions", headers=auth_headers(env), json=chat_payload("good-model")
    )
    assert response.status_code == 200
    body = response.json()
    assert body["choices"][0]["message"]["content"] == "echo:good-model"
    assert body["usage"]["total_tokens"] == 18

    upstream_request = stub_good.last_request
    assert upstream_request["headers"].get("Authorization") == "Bearer good-secret"
    sent = json.loads(upstream_request["body"])
    assert sent["model"] == "good-model"
    assert sent["messages"][0]["content"] == "hello"

    logs = get_logs(app)
    assert len(logs) == 1
    log = logs[0]
    assert log.status_code == 200
    assert log.upstream == "up-good"
    assert log.upstream_model == "good-model"
    assert log.requested_model == "good-model"
    assert log.user_id == env["user_id"]
    assert log.organization_id == env["org_id"]
    assert "hello" in log.request_body
    assert log.request_headers.get("authorization", "").startswith("Bearer sk-")
    assert "echo:good-model" in log.response_body
    assert log.prompt_tokens == 11
    assert log.completion_tokens == 7
    assert log.total_tokens == 18
    assert log.latency_ms is not None
    assert log.stream is False


def test_chat_completion_failover(client, app, env, stub_flaky, stub_good):
    response = client.post("/v1/chat/completions", headers=auth_headers(env), json=chat_payload("big"))
    assert response.status_code == 200
    assert response.json()["choices"][0]["message"]["content"] == "echo:good-model"

    flaky_request = stub_flaky.last_request
    assert flaky_request is not None
    assert json.loads(flaky_request["body"])["model"] == "flaky-model"
    assert flaky_request["headers"].get("Authorization") == "Bearer flaky-secret"

    good_request = stub_good.last_request
    assert json.loads(good_request["body"])["model"] == "good-model"

    logs = get_logs(app)
    assert len(logs) == 1
    assert logs[0].upstream == "up-good"
    assert logs[0].upstream_model == "good-model"
    assert logs[0].status_code == 200


def test_chat_completion_all_upstreams_fail(client, app, env):
    response = client.post(
        "/v1/chat/completions", headers=auth_headers(env), json=chat_payload("broken-model")
    )
    assert response.status_code == 502
    assert "all upstreams failed" in response.json()["error"]["message"]

    logs = get_logs(app)
    assert len(logs) == 1
    assert logs[0].status_code is None
    assert logs[0].upstream is None
    assert "upstream exploded" in logs[0].error


def test_chat_completion_unknown_model(client, env):
    response = client.post(
        "/v1/chat/completions", headers=auth_headers(env), json=chat_payload("does-not-exist")
    )
    assert response.status_code == 400
    assert "does-not-exist" in response.json()["error"]["message"]


def test_chat_completion_requires_auth(client):
    response = client.post("/v1/chat/completions", json=chat_payload("good-model"))
    assert response.status_code == 401


def test_chat_completion_invalid_json(client, env):
    response = client.post(
        "/v1/chat/completions",
        headers={**auth_headers(env), "Content-Type": "application/json"},
        content=b"not json",
    )
    assert response.status_code == 400


def test_allowlist_blocks_model(client, app, env):
    db = app.state.session_factory()
    org = db.get(Organization, env["org_id"])
    org.allowed_models = ["good-model"]
    db.commit()
    db.close()

    response = client.post("/v1/chat/completions", headers=auth_headers(env), json=chat_payload("big"))
    assert response.status_code == 403
    assert "not allowed" in response.json()["error"]["message"]

    response = client.post(
        "/v1/chat/completions", headers=auth_headers(env), json=chat_payload("good-model")
    )
    assert response.status_code == 200


def test_request_quota(client, app, env):
    db = app.state.session_factory()
    org = db.get(Organization, env["org_id"])
    org.max_requests_per_day = 1
    db.commit()
    db.close()

    first = client.post("/v1/chat/completions", headers=auth_headers(env), json=chat_payload("good-model"))
    assert first.status_code == 200
    second = client.post(
        "/v1/chat/completions", headers=auth_headers(env), json=chat_payload("good-model")
    )
    assert second.status_code == 429
    assert "quota exceeded" in second.json()["error"]["message"]


def test_token_quota(client, app, env):
    db = app.state.session_factory()
    org = db.get(Organization, env["org_id"])
    org.max_prompt_tokens_per_day = 11
    db.commit()
    db.close()

    first = client.post("/v1/chat/completions", headers=auth_headers(env), json=chat_payload("good-model"))
    assert first.status_code == 200
    second = client.post(
        "/v1/chat/completions", headers=auth_headers(env), json=chat_payload("good-model")
    )
    assert second.status_code == 429
    assert "prompt tokens" in second.json()["error"]["message"]


def test_chat_completion_streaming(client, app, env, stub_good):
    response = client.post(
        "/v1/chat/completions",
        headers=auth_headers(env),
        json=chat_payload("good-model", stream=True),
    )
    assert response.status_code == 200
    assert response.headers["content-type"].startswith("text/event-stream")
    assert "data:" in response.text
    assert "[DONE]" in response.text
    assert "Hello" in response.text

    logs = get_logs(app)
    assert len(logs) == 1
    log = logs[0]
    assert log.stream is True
    assert log.status_code == 200
    assert log.upstream == "up-good"
    assert "Hello" in log.response_body
    assert log.total_tokens == 5
    assert log.prompt_tokens == 3
