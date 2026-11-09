"""Mock OpenAI-compatible upstream used by PR preview environments.

Serves GET /v1/models and POST /v1/chat/completions (JSON and SSE) with canned
responses. Set MOCK_UPSTREAM_FAIL_STATUS to an HTTP status code to make every
chat completion fail with that status (useful for exercising failover/502 paths).

Usage: python3 scripts/mock_upstream.py <port>
"""

import json
import os
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

MODEL_ID = "mock-model"


def completion_payload(model: str) -> dict:
    return {
        "id": "chatcmpl-mock",
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
    }


def sse_payload(model: str) -> str:
    def chunk(delta: dict, usage: dict | None = None) -> str:
        obj = {
            "id": "chatcmpl-mock",
            "object": "chat.completion.chunk",
            "model": model,
            "choices": [{"index": 0, "delta": delta}],
        }
        if usage:
            obj["usage"] = usage
        return f"data: {json.dumps(obj)}\n\n"

    return (
        chunk({"role": "assistant", "content": ""})
        + chunk({"content": f"echo:{model}"})
        + chunk({}, {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18})
        + "data: [DONE]\n\n"
    )


class Handler(BaseHTTPRequestHandler):
    def _respond(self, status: int, body: str, content_type: str = "application/json"):
        encoded = body.encode()
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def do_GET(self):
        if self.path == "/v1/models":
            self._respond(
                200,
                json.dumps({"object": "list", "data": [{"id": MODEL_ID, "object": "model"}]}),
            )
        elif self.path == "/healthz":
            self._respond(200, json.dumps({"status": "ok"}))
        else:
            self._respond(404, json.dumps({"error": {"message": "not found"}}))

    def do_POST(self):
        if self.path != "/v1/chat/completions":
            self._respond(404, json.dumps({"error": {"message": "not found"}}))
            return
        length = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(length)
        try:
            payload = json.loads(raw or b"{}")
        except ValueError:
            payload = {}
        fail = os.environ.get("MOCK_UPSTREAM_FAIL_STATUS")
        if fail:
            self._respond(
                int(fail), json.dumps({"error": {"message": "mock upstream forced failure"}})
            )
            return
        model = payload.get("model", MODEL_ID)
        if payload.get("stream"):
            self._respond(200, sse_payload(model), content_type="text/event-stream")
        else:
            self._respond(200, json.dumps(completion_payload(model)))

    def log_message(self, *args):
        pass


def make_server(port: int, host: str = "127.0.0.1") -> ThreadingHTTPServer:
    return ThreadingHTTPServer((host, port), Handler)


def main():
    port = int(sys.argv[1])
    server = make_server(port)
    print(f"mock upstream listening on http://{server.server_address[0]}:{server.server_port}/v1", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
