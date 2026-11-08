from sqlalchemy import select

from .conftest import auth_headers
from switchboard.models import ApiKey, Membership, Organization, RequestLog, User
from switchboard.security import generate_api_key, hash_api_key, hash_password


def add_user(app, username, password, org_id, role="member"):
    db = app.state.session_factory()
    user = User(username=username, password_hash=hash_password(password))
    db.add(user)
    db.flush()
    db.add(Membership(user_id=user.id, organization_id=org_id, role=role))
    raw_key = generate_api_key()
    db.add(
        ApiKey(key_hash=hash_api_key(raw_key), name="default", user_id=user.id, organization_id=org_id)
    )
    db.commit()
    result = {"user_id": user.id, "key": raw_key}
    db.close()
    return result


def login(client, username, password):
    return client.post(
        "/login", data={"username": username, "password": password}, follow_redirects=False
    )


def make_request(client, env, model="good-model"):
    return client.post(
        "/v1/chat/completions",
        headers=auth_headers(env),
        json={"model": model, "messages": [{"role": "user", "content": "hello"}]},
    )


def get_logs(app, user_id):
    db = app.state.session_factory()
    try:
        return (
            db.execute(select(RequestLog).where(RequestLog.user_id == user_id).order_by(RequestLog.id))
            .scalars()
            .all()
        )
    finally:
        db.close()


def test_login_logout_flow(client, env):
    response = login(client, "alice", "wrong-password")
    assert response.status_code == 401

    response = login(client, env["username"], env["password"])
    assert response.status_code == 307
    assert response.headers["location"] == "/activity"

    response = client.get("/activity", follow_redirects=False)
    assert response.status_code == 200
    assert "Your activity" in response.text

    response = client.get("/logout", follow_redirects=False)
    assert response.status_code == 307
    response = client.get("/activity", follow_redirects=False)
    assert response.status_code == 307
    assert response.headers["location"] == "/login"


def test_activity_requires_login(client):
    response = client.get("/activity", follow_redirects=False)
    assert response.status_code == 307
    assert response.headers["location"] == "/login"


def test_activity_lists_own_requests(client, app, env):
    make_request(client, env, "good-model")
    make_request(client, env, "fast")

    login(client, env["username"], env["password"])
    response = client.get("/activity")
    assert response.status_code == 200
    assert "good-model" in response.text
    assert "fast" in response.text
    assert "up-good" in response.text

    logs = get_logs(app, env["user_id"])
    assert len(logs) == 2
    detail = client.get(f"/activity/{logs[0].id}")
    assert detail.status_code == 200
    assert "hello" in detail.text
    assert "echo:good-model" in detail.text
    assert "authorization" in detail.text


def test_activity_detail_hidden_from_other_users(client, app, env):
    make_request(client, env, "good-model")
    bob = add_user(app, "bob", "builder", env["org_id"], role="member")
    make_request(client, bob, "fast")

    bob_log = get_logs(app, bob["user_id"])[0]

    login(client, env["username"], env["password"])
    response = client.get(f"/activity/{bob_log.id}")
    assert response.status_code == 404


def test_org_pages_and_admin_settings(client, app, env):
    login(client, env["username"], env["password"])
    response = client.get("/orgs")
    assert response.status_code == 200
    assert "acme" in response.text
    assert "admin" in response.text

    response = client.get(f"/orgs/{env['org_id']}")
    assert response.status_code == 200
    assert "alice" in response.text
    assert "Max requests per day" in response.text

    response = client.post(
        f"/orgs/{env['org_id']}/settings",
        data={
            "max_requests_per_day": "5",
            "max_prompt_tokens_per_day": "",
            "max_completion_tokens_per_day": "1000",
            "allowed_models": "big, fast",
        },
        follow_redirects=False,
    )
    assert response.status_code == 307

    db = app.state.session_factory()
    org = db.get(Organization, env["org_id"])
    assert org.max_requests_per_day == 5
    assert org.max_prompt_tokens_per_day is None
    assert org.max_completion_tokens_per_day == 1000
    assert org.allowed_models == ["big", "fast"]
    db.close()

    response = client.post(
        "/v1/chat/completions",
        headers=auth_headers(env),
        json={"model": "good-model", "messages": [{"role": "user", "content": "hi"}]},
    )
    assert response.status_code == 403

    response = client.post(
        "/v1/chat/completions",
        headers=auth_headers(env),
        json={"model": "fast", "messages": [{"role": "user", "content": "hi"}]},
    )
    assert response.status_code == 200


def test_org_settings_forbidden_for_members(client, app, env):
    bob = add_user(app, "bob", "builder", env["org_id"], role="member")
    login(client, "bob", "builder")
    response = client.post(
        f"/orgs/{env['org_id']}/settings",
        data={"max_requests_per_day": "1", "allowed_models": ""},
        follow_redirects=False,
    )
    assert response.status_code == 403

    response = client.get(f"/orgs/{env['org_id']}")
    assert response.status_code == 200
    assert "Only organization admins can change these settings." in response.text
