import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest
import yaml
from fastapi.testclient import TestClient

from switchboard.app import create_app
from switchboard.config import load_config
from switchboard.models import ApiKey, Membership, Organization, User
from switchboard.security import generate_api_key, hash_api_key, hash_password
from switchboard.settings import Settings


def make_stub_server(behavior: str):
    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            length = int(self.headers.get("Content-Length", "0"))
            raw = self.rfile.read(length)
            self.server.last_request = {
                "path": self.path,
                "headers": dict(self.headers),
                "body": raw.decode("utf-8", "replace"),
            }
            try:
                payload = json.loads(raw or b"{}")
            except ValueError:
                payload = {}
            if self.path != "/v1/chat/completions":
                self._respond(404, {"error": "not found"})
                return
            model = payload.get("model", "")
            if behavior == "flaky":
                self._respond(500, {"error": {"message": "upstream exploded"}})
            elif payload.get("stream"):
                body = (
                    'data: {"id":"chatcmpl-1","object":"chat.completion.chunk",'
                    '"choices":[{"index":0,"delta":{"content":"Hello"}}]}\n\n'
                    'data: {"id":"chatcmpl-1","object":"chat.completion.chunk",'
                    '"choices":[{"index":0,"delta":{"content":" world"}}],'
                    '"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}\n\n'
                    "data: [DONE]\n\n"
                )
                self._respond(200, body, content_type="text/event-stream")
            else:
                self._respond(
                    200,
                    {
                        "id": "chatcmpl-1",
                        "object": "chat.completion",
                        "model": model,
                        "choices": [
                            {
                                "index": 0,
                                "message": {"role": "assistant", "content": f"echo:{model}"},
                                "finish_reason": "stop",
                            }
                        ],
                        "usage": {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
                    },
                )

        def _respond(self, status: int, obj, content_type: str = "application/json"):
            data = obj if isinstance(obj, str) else json.dumps(obj)
            encoded = data.encode()
            self.send_response(status)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(encoded)))
            self.end_headers()
            self.wfile.write(encoded)

        def log_message(self, *args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    server.last_request = None
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    return server


@pytest.fixture(scope="session")
def stub_good():
    server = make_stub_server("good")
    yield server
    server.shutdown()


@pytest.fixture(scope="session")
def stub_flaky():
    server = make_stub_server("flaky")
    yield server
    server.shutdown()


@pytest.fixture()
def config_file(tmp_path, stub_good, stub_flaky):
    raw = {
        "upstreams": [
            {
                "name": "up-good",
                "base_url": f"http://127.0.0.1:{stub_good.server_port}/v1",
                "api_key": "good-secret",
            },
            {
                "name": "up-flaky",
                "base_url": f"http://127.0.0.1:{stub_flaky.server_port}/v1",
                "api_key": "flaky-secret",
            },
        ],
        "models": {
            "good-model": [{"upstream": "up-good", "model": "good-model"}],
            "flaky-model": [
                {"upstream": "up-flaky", "model": "flaky-model"},
                {"upstream": "up-good", "model": "good-model"},
            ],
            "broken-model": [{"upstream": "up-flaky", "model": "flaky-model"}],
        },
        "aliases": {
            "big": ["flaky-model", "good-model"],
            "fast": ["good-model"],
        },
    }
    path = tmp_path / "config.yaml"
    path.write_text(yaml.safe_dump(raw))
    return str(path)


@pytest.fixture()
def settings(tmp_path, config_file):
    return Settings(
        database_url=f"sqlite:///{tmp_path}/test.db",
        secret_key="test-secret-0123456789abcdef0123456789",
        config_path=config_file,
    )


@pytest.fixture()
def app(settings, config_file):
    return create_app(settings=settings, config=load_config(config_file))


@pytest.fixture()
def client(app):
    with TestClient(app) as test_client:
        yield test_client


@pytest.fixture()
def env(app):
    db = app.state.session_factory()
    org = Organization(name="acme")
    db.add(org)
    db.flush()
    user = User(username="alice", password_hash=hash_password("wonderland"))
    db.add(user)
    db.flush()
    db.add(Membership(user_id=user.id, organization_id=org.id, role="admin"))
    raw_key = generate_api_key()
    db.add(
        ApiKey(
            key_hash=hash_api_key(raw_key),
            name="default",
            user_id=user.id,
            organization_id=org.id,
        )
    )
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


def auth_headers(env):
    return {"Authorization": f"Bearer {env['key']}"}
