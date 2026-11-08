import json
import time

import requests
from fastapi import APIRouter, Depends, Header, HTTPException, Request
from fastapi.responses import Response, StreamingResponse
from sqlalchemy import select
from sqlalchemy.orm import Session

from .config import SwitchboardConfig
from .db import utcnow
from .models import ApiKey, Organization, RequestLog
from .policy import allowlist_error, quota_error
from .proxy import extract_usage_json, extract_usage_sse, try_targets
from .routing import resolve_targets
from .security import hash_api_key

router = APIRouter()


def get_db(request: Request):
    db = request.app.state.session_factory()
    try:
        yield db
    finally:
        db.close()


def get_config(request: Request) -> SwitchboardConfig:
    return request.app.state.config


def get_api_key(
    request: Request,
    authorization: str | None = Header(default=None),
    db: Session = Depends(get_db),
) -> ApiKey:
    if not authorization or not authorization.lower().startswith("bearer "):
        raise HTTPException(status_code=401, detail="missing or malformed Authorization header")
    token = authorization.split(" ", 1)[1].strip()
    key = db.execute(
        select(ApiKey).where(ApiKey.key_hash == hash_api_key(token), ApiKey.is_active.is_(True))
    ).scalar_one_or_none()
    if key is None:
        raise HTTPException(status_code=401, detail="invalid api key")
    key.last_used_at = utcnow()
    db.commit()
    return key


def base_log(key: ApiKey, requested_model: str, request: Request, raw_body: bytes, stream: bool) -> RequestLog:
    return RequestLog(
        user_id=key.user_id,
        organization_id=key.organization_id,
        api_key_id=key.id,
        requested_model=requested_model or "",
        stream=stream,
        request_method=request.method,
        request_path=request.url.path,
        request_headers=dict(request.headers),
        request_body=raw_body.decode("utf-8", "replace"),
    )


@router.get("/models")
def list_models(config: SwitchboardConfig = Depends(get_config), key: ApiKey = Depends(get_api_key)):
    return {
        "object": "list",
        "data": [
            {"id": name, "object": "model", "created": 0, "owned_by": "switchboard"}
            for name in config.known_model_names()
        ],
    }


@router.post("/chat/completions")
async def chat_completions(
    request: Request,
    db: Session = Depends(get_db),
    config: SwitchboardConfig = Depends(get_config),
    key: ApiKey = Depends(get_api_key),
):
    raw_body = await request.body()
    try:
        payload = json.loads(raw_body) if raw_body else {}
    except ValueError:
        raise HTTPException(status_code=400, detail="request body is not valid JSON")
    if not isinstance(payload, dict):
        raise HTTPException(status_code=400, detail="request body must be a JSON object")

    requested_model = payload.get("model")
    targets = resolve_targets(config, requested_model if isinstance(requested_model, str) else None)
    if targets is None:
        raise HTTPException(status_code=400, detail=f"model '{requested_model}' is not available on this switchboard")

    org = db.get(Organization, key.organization_id)
    err = allowlist_error(org, requested_model)
    if err:
        raise HTTPException(status_code=403, detail=err)
    err = quota_error(db, org, request.app.state.settings.quota_window_hours)
    if err:
        raise HTTPException(status_code=429, detail=err)

    stream = bool(payload.get("stream"))
    log = base_log(key, requested_model, request, raw_body, stream)
    started = time.perf_counter()

    session = requests.Session()
    target, upstream, response, _sent_body, attempts = try_targets(session, config, targets, payload, stream)
    if response is None:
        session.close()
        log.latency_ms = int((time.perf_counter() - started) * 1000)
        log.error = json.dumps(attempts)
        db.add(log)
        db.commit()
        raise HTTPException(status_code=502, detail=f"all upstreams failed: {json.dumps(attempts)}")

    log.upstream = upstream.name
    log.upstream_model = target.model
    log.latency_ms = int((time.perf_counter() - started) * 1000)

    if stream:
        content_type = response.headers.get("content-type", "text/event-stream")

        def event_stream():
            chunks: list[bytes] = []
            try:
                for chunk in response.iter_content(chunk_size=8192):
                    if chunk:
                        chunks.append(chunk)
                        yield chunk
            finally:
                text = b"".join(chunks).decode("utf-8", "replace")
                response.close()
                session.close()
                usage = extract_usage_sse(text) or {}
                log.status_code = response.status_code
                log.response_headers = dict(response.headers)
                log.response_body = text
                log.prompt_tokens = usage.get("prompt_tokens")
                log.completion_tokens = usage.get("completion_tokens")
                log.total_tokens = usage.get("total_tokens")
                log.latency_ms = int((time.perf_counter() - started) * 1000)
                with request.app.state.session_factory() as db2:
                    db2.add(log)
                    db2.commit()

        return StreamingResponse(
            event_stream(),
            status_code=response.status_code,
            media_type=content_type.split(";")[0].strip() or "text/event-stream",
            headers={"content-type": content_type, "cache-control": "no-cache"},
        )

    content = response.content
    text = content.decode("utf-8", "replace")
    status_code = response.status_code
    response_headers = dict(response.headers)
    response.close()
    session.close()

    usage = extract_usage_json(text) or {}
    log.status_code = status_code
    log.response_headers = response_headers
    log.response_body = text
    log.prompt_tokens = usage.get("prompt_tokens")
    log.completion_tokens = usage.get("completion_tokens")
    log.total_tokens = usage.get("total_tokens")
    db.add(log)
    db.commit()

    return Response(
        content=content,
        status_code=status_code,
        headers={"content-type": response_headers.get("content-type", "application/json")},
    )
