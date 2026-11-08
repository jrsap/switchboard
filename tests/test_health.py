import time

import requests
from fastapi.testclient import TestClient
from sqlalchemy import select

from .conftest import auth_headers
from switchboard import health
from switchboard.app import create_app
from switchboard.config import Upstream, load_config
from switchboard.models import ApiKey, Membership, Organization, UpstreamStatus, User
from switchboard.security import generate_api_key, hash_api_key, hash_password
from switchboard.settings import Settings


def login(client, username, password):
    return client.post(
        "/login", data={"username": username, "password": password}, follow_redirects=False
    )


def get_status(app, name):
    db = app.state.session_factory()
    try:
        return db.execute(select(UpstreamStatus).where(UpstreamStatus.upstream == name)).scalar_one_or_none()
    finally:
        db.close()


def wait_for_status(app, name, predicate=lambda row: True, timeout=5.0):
    deadline = time.time() + timeout
    row = None
    while time.time() < deadline:
        row = get_status(app, name)
        if row is not None and predicate(row):
            return row
        time.sleep(0.05)
    return row


def make_app(tmp_path, config_file, **overrides):
    base = {
        "database_url": f"sqlite:///{tmp_path}/test.db",
        "secret_key": "test-secret-0123456789abcdef0123456789",
        "config_path": config_file,
        "health_poll_interval_seconds": 0,
    }
    base.update(overrides)
    settings = Settings(**base)
    return create_app(settings=settings, config=load_config(config_file))


def seed_user(app):
    db = app.state.session_factory()
    org = Organization(name="acme")
    db.add(org)
    db.flush()
    user = User(username="alice", password_hash=hash_password("wonderland"))
    db.add(user)
    db.flush()
    db.add(Membership(user_id=user.id, organization_id=org.id, role="admin"))
    raw_key = generate_api_key()
    db.add(ApiKey(key_hash=hash_api_key(raw_key), name="default", user_id=user.id, organization_id=org.id))
    db.commit()
    result = {
        "org_id": org.id,
        "user_id": user.id,
        "key": raw_key,
        "username": "alice",
        "password": "wonderland",
    }
    db.close()
    return result


def test_poll_upstream_parses_models_and_capabilities(stub_good):
    upstream = Upstream(
        name="up-good", base_url=f"http://127.0.0.1:{stub_good.server_port}/v1", api_key="good-secret"
    )
    with requests.Session() as session:
        result = health.poll_upstream(session, upstream, timeout=5)
    assert result["ok"] is True
    assert result["error"] is None
    assert result["latency_ms"] is not None
    ids = {m["id"] for m in result["models"]}
    assert {"good-model", "other-model"} <= ids
    good = next(m for m in result["models"] if m["id"] == "good-model")
    assert good["max_context_length"] == 128000
    assert good["modalities"] == ["text", "image"]
    assert good["supported_parameters"] == ["temperature", "tools"]
    other = next(m for m in result["models"] if m["id"] == "other-model")
    assert other["max_context_length"] == 32768
    assert other["modalities"] == ["text"]


def test_poll_upstream_sends_api_key(stub_good):
    upstream = Upstream(
        name="up-good", base_url=f"http://127.0.0.1:{stub_good.server_port}/v1", api_key="good-secret"
    )
    seen = {}

    class RecordingSession(requests.Session):
        def get(self, url, **kwargs):
            seen.update(kwargs.get("headers") or {})
            return super().get(url, **kwargs)

    with RecordingSession() as session:
        result = health.poll_upstream(session, upstream, timeout=5)
    assert result["ok"] is True
    assert seen.get("Authorization") == "Bearer good-secret"


def test_poll_upstream_reports_failure(stub_flaky):
    upstream = Upstream(
        name="up-flaky", base_url=f"http://127.0.0.1:{stub_flaky.server_port}/v1", api_key="flaky-secret"
    )
    with requests.Session() as session:
        result = health.poll_upstream(session, upstream, timeout=5)
    assert result["ok"] is False
    assert "500" in result["error"]
    assert result["models"] == []


def test_poll_and_store_persists_status(app, config_file):
    config = load_config(config_file)
    result = health.poll_and_store(app.state.session_factory, config, "up-good", timeout=5)
    assert result["ok"] is True
    row = get_status(app, "up-good")
    assert row is not None
    assert row.ok is True
    assert row.error is None
    assert row.checked_at is not None
    assert row.base_url.endswith("/v1")
    assert any(m["id"] == "good-model" for m in row.models)


def test_status_dashboard_requires_login(client):
    response = client.get("/status", follow_redirects=False)
    assert response.status_code == 307
    assert response.headers["location"] == "/login"


def test_status_dashboard_shows_servers_and_model_capabilities(client, env):
    login(client, env["username"], env["password"])
    response = client.post("/status/refresh", follow_redirects=False)
    assert response.status_code == 307
    assert response.headers["location"] == "/status?refreshed=1"

    response = client.get("/status")
    assert response.status_code == 200
    assert "up-good" in response.text
    assert "up-flaky" in response.text
    assert "good-model" in response.text
    assert "128000" in response.text
    assert "text, image" in response.text
    assert "up" in response.text
    assert "down" in response.text

    row = get_status(client.app, "up-good")
    assert row.ok is True
    row_flaky = get_status(client.app, "up-flaky")
    assert row_flaky.ok is False


def test_chat_request_triggers_stale_upstream_poll(tmp_path, config_file):
    app = make_app(tmp_path, config_file, health_stale_after_seconds=0)
    env = seed_user(app)
    with TestClient(app) as client:
        assert get_status(app, "up-good") is None
        response = client.post(
            "/v1/chat/completions",
            headers=auth_headers(env),
            json={"model": "good-model", "messages": [{"role": "user", "content": "hi"}]},
        )
        assert response.status_code == 200
        row = wait_for_status(app, "up-good", lambda r: r.ok, timeout=5)
        assert row is not None
        assert row.ok is True
        assert any(m["id"] == "good-model" and m["max_context_length"] == 128000 for m in row.models)


def test_periodic_poller_refreshes_status(tmp_path, config_file):
    app = make_app(tmp_path, config_file, health_poll_interval_seconds=0.2)
    with TestClient(app):
        row = wait_for_status(app, "up-good", lambda r: r.ok, timeout=5)
        assert row is not None and row.ok is True
        first_checked = row.checked_at
        row = wait_for_status(
            app, "up-good", lambda r: r.checked_at is not None and r.checked_at > first_checked, timeout=5
        )
        assert row is not None and row.checked_at > first_checked
        flaky = wait_for_status(app, "up-flaky", timeout=5)
        assert flaky is not None
        assert flaky.ok is False
        assert flaky.error
