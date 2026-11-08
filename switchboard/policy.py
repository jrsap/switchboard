from datetime import timedelta

from sqlalchemy import func, select
from sqlalchemy.orm import Session

from .db import utcnow
from .models import Organization, RequestLog


def allowlist_error(org: Organization, requested_model: str) -> str | None:
    if org.allowed_models is not None and requested_model not in org.allowed_models:
        return f"model '{requested_model}' is not allowed for organization '{org.name}'"
    return None


def quota_error(db: Session, org: Organization, window_hours: int) -> str | None:
    since = utcnow() - timedelta(hours=window_hours)
    count, prompt_tokens, completion_tokens = db.execute(
        select(
            func.count(RequestLog.id),
            func.coalesce(func.sum(RequestLog.prompt_tokens), 0),
            func.coalesce(func.sum(RequestLog.completion_tokens), 0),
        ).where(
            RequestLog.organization_id == org.id,
            RequestLog.created_at >= since,
            RequestLog.status_code.isnot(None),
        )
    ).one()
    if org.max_requests_per_day is not None and count >= org.max_requests_per_day:
        return f"quota exceeded: {count}/{org.max_requests_per_day} requests in the last {window_hours}h"
    if org.max_prompt_tokens_per_day is not None and prompt_tokens >= org.max_prompt_tokens_per_day:
        return (
            f"quota exceeded: {prompt_tokens}/{org.max_prompt_tokens_per_day} "
            f"prompt tokens in the last {window_hours}h"
        )
    if org.max_completion_tokens_per_day is not None and completion_tokens >= org.max_completion_tokens_per_day:
        return (
            f"quota exceeded: {completion_tokens}/{org.max_completion_tokens_per_day} "
            f"completion tokens in the last {window_hours}h"
        )
    return None
