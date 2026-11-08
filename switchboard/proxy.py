import json

import requests

from .config import SwitchboardConfig, Target, Upstream


def chat_completion_url(upstream: Upstream) -> str:
    return f"{upstream.base_url}/chat/completions"


def try_targets(
    session: requests.Session,
    config: SwitchboardConfig,
    targets: list[Target],
    payload: dict,
    stream: bool,
):
    attempts: list[dict] = []
    for target in targets:
        upstream = config.upstreams.get(target.upstream)
        if upstream is None:
            attempts.append({"upstream": target.upstream, "error": "unknown upstream"})
            continue
        sent_body = dict(payload)
        sent_body["model"] = target.model
        headers = {
            "Content-Type": "application/json",
            "Accept": "text/event-stream" if stream else "application/json",
        }
        if upstream.api_key:
            headers["Authorization"] = f"Bearer {upstream.api_key}"
        try:
            response = session.post(
                chat_completion_url(upstream),
                json=sent_body,
                headers=headers,
                timeout=(10, 300),
                stream=stream,
            )
        except requests.RequestException as exc:
            attempts.append({"upstream": upstream.name, "error": str(exc)})
            continue
        if response.status_code == 429 or response.status_code >= 500:
            attempts.append(
                {
                    "upstream": upstream.name,
                    "status_code": response.status_code,
                    "error": response.text[:2000],
                }
            )
            response.close()
            continue
        return target, upstream, response, sent_body, attempts
    return None, None, None, None, attempts


def extract_usage_json(body: str) -> dict | None:
    try:
        parsed = json.loads(body)
    except ValueError:
        return None
    usage = parsed.get("usage") if isinstance(parsed, dict) else None
    return usage if isinstance(usage, dict) else None


def extract_usage_sse(text: str) -> dict | None:
    usage = None
    for line in text.splitlines():
        line = line.strip()
        if not line.startswith("data:"):
            continue
        data = line[len("data:"):].strip()
        if data == "[DONE]":
            continue
        try:
            parsed = json.loads(data)
        except ValueError:
            continue
        if isinstance(parsed, dict) and isinstance(parsed.get("usage"), dict):
            usage = parsed["usage"]
    return usage
