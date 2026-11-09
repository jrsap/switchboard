"""Smoke test for a running switchboard preview instance.

Usage: python3 scripts/smoke_test.py <base_url> <api_key>

Exits 0 if /healthz, GET /v1/models and POST /v1/chat/completions (plain and
streaming) all answer correctly; exits 1 with a message otherwise.
"""

import json
import sys

import requests


def check(label: str, fn):
    try:
        fn()
    except Exception as exc:
        print(f"FAIL {label}: {exc}")
        sys.exit(1)
    print(f"PASS {label}")


def main():
    base = sys.argv[1].rstrip("/")
    key = sys.argv[2]
    headers = {"Authorization": f"Bearer {key}"}

    def healthz():
        resp = requests.get(f"{base}/healthz", timeout=10)
        assert resp.status_code == 200, f"status {resp.status_code}"
        assert resp.json()["status"] == "ok", resp.text

    def models():
        resp = requests.get(f"{base}/v1/models", headers=headers, timeout=10)
        assert resp.status_code == 200, f"status {resp.status_code}: {resp.text[:200]}"
        ids = [m["id"] for m in resp.json()["data"]]
        assert "mock-model" in ids, f"mock-model missing from {ids}"

    def chat():
        payload = {"model": "fast", "messages": [{"role": "user", "content": "hello"}]}
        resp = requests.post(f"{base}/v1/chat/completions", headers=headers, json=payload, timeout=30)
        assert resp.status_code == 200, f"status {resp.status_code}: {resp.text[:300]}"
        body = resp.json()
        assert body["choices"][0]["message"]["content"], "empty completion"

    def chat_stream():
        payload = {
            "model": "fast",
            "messages": [{"role": "user", "content": "hello"}],
            "stream": True,
        }
        with requests.post(
            f"{base}/v1/chat/completions", headers=headers, json=payload, stream=True, timeout=30
        ) as resp:
            assert resp.status_code == 200, f"status {resp.status_code}"
            text = resp.text
        assert "data:" in text and "[DONE]" in text, text[:300]

    check("GET /healthz", healthz)
    check("GET /v1/models", models)
    check("POST /v1/chat/completions", chat)
    check("POST /v1/chat/completions (stream)", chat_stream)
    print("smoke test OK")


if __name__ == "__main__":
    main()
