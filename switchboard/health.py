import threading
import time

import requests
from sqlalchemy import select

from .config import SwitchboardConfig, Upstream
from .db import utcnow
from .models import UpstreamStatus

_CONTEXT_LENGTH_KEYS = ("max_context_length", "max_model_len", "context_length", "max_position_embeddings")
_MODALITY_KEYS = ("supported_modalities", "modalities", "input_modalities")


def models_url(upstream: Upstream) -> str:
    return f"{upstream.base_url}/models"


def _first_int(entry: dict, keys) -> int | None:
    for key in keys:
        value = entry.get(key)
        if isinstance(value, bool):
            continue
        if isinstance(value, (int, float)):
            return int(value)
    return None


def extract_model_info(entry: dict) -> dict:
    info: dict = {"id": entry.get("id")}
    for key, value in entry.items():
        if key not in ("id",) and isinstance(value, (str, int, float, bool, list)):
            info.setdefault(key, value)
    context_length = _first_int(entry, _CONTEXT_LENGTH_KEYS)
    if context_length is None:
        meta = entry.get("meta")
        if isinstance(meta, dict):
            context_length = _first_int(meta, _CONTEXT_LENGTH_KEYS)
    if context_length is not None:
        info["max_context_length"] = context_length
    modalities = None
    for key in _MODALITY_KEYS:
        value = entry.get(key)
        if isinstance(value, list) and value:
            modalities = [str(m) for m in value]
            break
    if modalities is None:
        input_mods = entry.get("input_modalities")
        output_mods = entry.get("output_modalities")
        if isinstance(input_mods, list) and isinstance(output_mods, list):
            modalities = sorted({str(m) for m in input_mods} | {str(m) for m in output_mods})
    if modalities:
        info["modalities"] = modalities
    return info


def poll_upstream(session: requests.Session, upstream: Upstream, timeout: float) -> dict:
    headers = {"Accept": "application/json"}
    if upstream.api_key:
        headers["Authorization"] = f"Bearer {upstream.api_key}"
    started = time.perf_counter()
    try:
        response = session.get(models_url(upstream), headers=headers, timeout=timeout)
    except requests.RequestException as exc:
        return {
            "ok": False,
            "error": str(exc),
            "latency_ms": int((time.perf_counter() - started) * 1000),
            "models": [],
        }
    latency_ms = int((time.perf_counter() - started) * 1000)
    if response.status_code >= 400:
        return {
            "ok": False,
            "error": f"HTTP {response.status_code}: {response.text[:500]}",
            "latency_ms": latency_ms,
            "models": [],
        }
    try:
        payload = response.json()
    except ValueError:
        return {"ok": False, "error": "response is not valid JSON", "latency_ms": latency_ms, "models": []}
    data = payload.get("data") if isinstance(payload, dict) else None
    if not isinstance(data, list):
        return {"ok": False, "error": "response 'data' is not a list", "latency_ms": latency_ms, "models": []}
    models = [extract_model_info(e) for e in data if isinstance(e, dict) and e.get("id")]
    return {"ok": True, "error": None, "latency_ms": latency_ms, "models": models}


def store_result(session_factory, upstream: Upstream, result: dict) -> UpstreamStatus:
    db = session_factory()
    try:
        row = db.execute(select(UpstreamStatus).where(UpstreamStatus.upstream == upstream.name)).scalar_one_or_none()
        if row is None:
            row = UpstreamStatus(upstream=upstream.name)
            db.add(row)
        row.base_url = upstream.base_url
        row.ok = bool(result["ok"])
        row.error = result.get("error")
        row.latency_ms = result.get("latency_ms")
        row.checked_at = utcnow()
        row.models = result.get("models")
        db.commit()
        db.refresh(row)
        return row
    finally:
        db.close()


def poll_and_store(session_factory, config: SwitchboardConfig, name: str, timeout: float) -> dict | None:
    upstream = config.upstreams.get(name)
    if upstream is None:
        return None
    with requests.Session() as session:
        result = poll_upstream(session, upstream, timeout)
    store_result(session_factory, upstream, result)
    return result


def poll_all(session_factory, config: SwitchboardConfig, timeout: float) -> dict[str, dict]:
    results: dict[str, dict] = {}
    for name in config.upstreams:
        result = poll_and_store(session_factory, config, name, timeout)
        if result is not None:
            results[name] = result
    return results


_refresh_lock = threading.Lock()
_inflight: set[str] = set()


def maybe_refresh_stale(app, config: SwitchboardConfig, targets) -> None:
    settings = app.state.settings
    stale_after = settings.health_stale_after_seconds
    timeout = settings.health_poll_timeout_seconds
    for name in sorted({t.upstream for t in targets}):
        upstream = config.upstreams.get(name)
        if upstream is None:
            continue
        db = app.state.session_factory()
        try:
            row = db.execute(select(UpstreamStatus).where(UpstreamStatus.upstream == name)).scalar_one_or_none()
        finally:
            db.close()
        if row is not None and row.checked_at is not None and stale_after > 0:
            if (utcnow() - row.checked_at).total_seconds() < stale_after:
                continue

        with _refresh_lock:
            if name in _inflight:
                continue
            _inflight.add(name)

        def run(upstream_name: str = name, up: Upstream = upstream):
            try:
                with requests.Session() as session:
                    result = poll_upstream(session, up, timeout)
                store_result(app.state.session_factory, up, result)
            except Exception:
                pass
            finally:
                with _refresh_lock:
                    _inflight.discard(upstream_name)

        threading.Thread(target=run, daemon=True, name=f"switchboard-health-{name}").start()


def start_periodic_poller(app) -> tuple[threading.Event, threading.Thread | None]:
    stop = threading.Event()
    interval = app.state.settings.health_poll_interval_seconds
    if not interval or interval <= 0:
        return stop, None

    def loop():
        while not stop.is_set():
            try:
                poll_all(app.state.session_factory, app.state.config, app.state.settings.health_poll_timeout_seconds)
            except Exception:
                pass
            stop.wait(interval)

    thread = threading.Thread(target=loop, daemon=True, name="switchboard-health-poller")
    thread.start()
    return stop, thread
