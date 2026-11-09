import sys
import threading
from pathlib import Path

import pytest
import requests

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "scripts"))

import mock_upstream


@pytest.fixture()
def server():
    srv = mock_upstream.make_server(0)
    thread = threading.Thread(target=srv.serve_forever, daemon=True)
    thread.start()
    yield srv
    srv.shutdown()
    srv.server_close()


def base(server):
    return f"http://127.0.0.1:{server.server_port}"


def test_models(server):
    resp = requests.get(f"{base(server)}/v1/models", timeout=5)
    assert resp.status_code == 200
    assert [m["id"] for m in resp.json()["data"]] == ["mock-model"]


def test_chat_completion(server):
    resp = requests.post(
        f"{base(server)}/v1/chat/completions",
        json={"model": "fast", "messages": [{"role": "user", "content": "hi"}]},
        timeout=5,
    )
    assert resp.status_code == 200
    body = resp.json()
    assert body["choices"][0]["message"]["content"] == "echo:fast"
    assert body["usage"]["total_tokens"] == 18


def test_chat_completion_stream(server):
    resp = requests.post(
        f"{base(server)}/v1/chat/completions",
        json={"model": "big", "messages": [], "stream": True},
        timeout=5,
    )
    assert resp.status_code == 200
    assert resp.headers["content-type"].startswith("text/event-stream")
    assert "data:" in resp.text and "[DONE]" in resp.text
    assert "echo:big" in resp.text


def test_forced_failure(server, monkeypatch):
    monkeypatch.setenv("MOCK_UPSTREAM_FAIL_STATUS", "500")
    resp = requests.post(
        f"{base(server)}/v1/chat/completions", json={"model": "fast", "messages": []}, timeout=5
    )
    assert resp.status_code == 500
