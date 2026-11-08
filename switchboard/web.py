import json
from pathlib import Path

from fastapi import APIRouter, Form, HTTPException, Request
from fastapi.responses import HTMLResponse, RedirectResponse
from fastapi.templating import Jinja2Templates
from sqlalchemy import func, select

from . import health
from .models import Membership, Organization, RequestLog, UpstreamStatus, User
from .routing import resolve_targets
from .security import create_session_token, verify_password, verify_session_token

router = APIRouter()
templates = Jinja2Templates(directory=str(Path(__file__).parent / "templates"))

PAGE_SIZE = 25


def pretty_json(value):
    if value is None:
        return ""
    if isinstance(value, (dict, list)):
        return json.dumps(value, indent=2)
    try:
        return json.dumps(json.loads(value), indent=2)
    except (ValueError, TypeError):
        return str(value)


templates.env.filters["pretty_json"] = pretty_json


def get_web_user(request: Request) -> User | None:
    token = request.cookies.get("session")
    if not token:
        return None
    user_id = verify_session_token(token, request.app.state.settings.secret_key)
    if user_id is None:
        return None
    db = request.app.state.session_factory()
    try:
        return db.get(User, user_id)
    finally:
        db.close()


def user_or_redirect(request: Request):
    user = get_web_user(request)
    if user is None:
        return RedirectResponse("/login")
    return user


@router.get("/", include_in_schema=False)
def index():
    return RedirectResponse("/activity")


@router.get("/login", response_class=HTMLResponse)
def login_page(request: Request):
    if get_web_user(request) is not None:
        return RedirectResponse("/activity")
    return templates.TemplateResponse(request, "login.html", {})


@router.post("/login")
def login(request: Request, username: str = Form(...), password: str = Form(...)):
    db = request.app.state.session_factory()
    try:
        user = db.execute(select(User).where(User.username == username)).scalar_one_or_none()
        if user is None or not verify_password(password, user.password_hash):
            return templates.TemplateResponse(
                request, "login.html", {"error": "Invalid username or password"}, status_code=401
            )
        token = create_session_token(user.id, request.app.state.settings.secret_key)
    finally:
        db.close()
    response = RedirectResponse("/activity")
    response.set_cookie("session", token, httponly=True, samesite="lax", max_age=12 * 3600)
    return response


@router.get("/logout")
def logout():
    response = RedirectResponse("/login")
    response.delete_cookie("session")
    return response


@router.get("/activity", response_class=HTMLResponse)
def activity(request: Request, page: int = 1):
    user = user_or_redirect(request)
    if isinstance(user, RedirectResponse):
        return user
    page = max(page, 1)
    db = request.app.state.session_factory()
    try:
        total = db.execute(
            select(func.count(RequestLog.id)).where(RequestLog.user_id == user.id)
        ).scalar_one()
        logs = db.execute(
            select(RequestLog)
            .where(RequestLog.user_id == user.id)
            .order_by(RequestLog.created_at.desc(), RequestLog.id.desc())
            .limit(PAGE_SIZE)
            .offset((page - 1) * PAGE_SIZE)
        ).scalars().all()
    finally:
        db.close()
    pages = max((total + PAGE_SIZE - 1) // PAGE_SIZE, 1)
    return templates.TemplateResponse(
        request,
        "activity.html",
        {"user": user, "logs": logs, "page": page, "pages": pages, "total": total},
    )


@router.get("/activity/{log_id}", response_class=HTMLResponse)
def activity_detail(request: Request, log_id: int):
    user = user_or_redirect(request)
    if isinstance(user, RedirectResponse):
        return user
    db = request.app.state.session_factory()
    try:
        log = db.get(RequestLog, log_id)
        if log is None or log.user_id != user.id:
            raise HTTPException(status_code=404, detail="request not found")
    finally:
        db.close()
    return templates.TemplateResponse(request, "activity_detail.html", {"user": user, "log": log})


@router.get("/status", response_class=HTMLResponse)
def status_page(request: Request, refreshed: int = 0):
    user = user_or_redirect(request)
    if isinstance(user, RedirectResponse):
        return user
    config = request.app.state.config
    db = request.app.state.session_factory()
    try:
        rows = db.execute(select(UpstreamStatus).order_by(UpstreamStatus.upstream)).scalars().all()
    finally:
        db.close()
    statuses = {row.upstream: row for row in rows}

    upstream_views = []
    for name, upstream in sorted(config.upstreams.items()):
        row = statuses.get(name)
        upstream_views.append(
            {
                "name": name,
                "base_url": upstream.base_url,
                "status": row,
                "model_count": len(row.models) if row and row.ok and row.models else 0,
            }
        )

    model_views = []
    for name in config.known_model_names():
        targets = resolve_targets(config, name) or []
        target_views = []
        for target in targets:
            row = statuses.get(target.upstream)
            listed = None
            caps: dict = {}
            if row is not None and row.ok and row.models:
                match = next((m for m in row.models if m.get("id") == target.model), None)
                listed = match is not None
                if match is not None:
                    caps = match
            target_views.append(
                {
                    "upstream": target.upstream,
                    "upstream_model": target.model,
                    "status": row,
                    "listed": listed,
                    "caps": caps,
                }
            )
        model_views.append(
            {"name": name, "is_alias": name in config.aliases, "targets": target_views}
        )

    return templates.TemplateResponse(
        request,
        "status.html",
        {
            "user": user,
            "upstreams": upstream_views,
            "models": model_views,
            "refreshed": refreshed,
        },
    )


@router.post("/status/refresh")
def refresh_status(request: Request):
    user = user_or_redirect(request)
    if isinstance(user, RedirectResponse):
        return user
    health.poll_all(
        request.app.state.session_factory,
        request.app.state.config,
        request.app.state.settings.health_poll_timeout_seconds,
    )
    return RedirectResponse("/status?refreshed=1")


@router.get("/orgs", response_class=HTMLResponse)
def orgs(request: Request):
    user = user_or_redirect(request)
    if isinstance(user, RedirectResponse):
        return user
    db = request.app.state.session_factory()
    try:
        memberships = db.execute(
            select(Membership, Organization)
            .join(Organization, Membership.organization_id == Organization.id)
            .where(Membership.user_id == user.id)
            .order_by(Membership.id)
        ).all()
    finally:
        db.close()
    return templates.TemplateResponse(request, "orgs.html", {"user": user, "memberships": memberships})


@router.get("/orgs/{org_id}", response_class=HTMLResponse)
def org_detail(request: Request, org_id: int, saved: int = 0):
    user = user_or_redirect(request)
    if isinstance(user, RedirectResponse):
        return user
    db = request.app.state.session_factory()
    try:
        membership = db.execute(
            select(Membership).where(Membership.user_id == user.id, Membership.organization_id == org_id)
        ).scalar_one_or_none()
        if membership is None:
            raise HTTPException(status_code=404, detail="organization not found")
        org = db.get(Organization, org_id)
        members = db.execute(
            select(Membership, User)
            .join(User, Membership.user_id == User.id)
            .where(Membership.organization_id == org_id)
            .order_by(Membership.id)
        ).all()
    finally:
        db.close()
    is_admin = membership.role == "admin"
    allowed = ", ".join(org.allowed_models) if org.allowed_models else ""
    return templates.TemplateResponse(
        request,
        "org_detail.html",
        {
            "user": user,
            "org": org,
            "members": members,
            "is_admin": is_admin,
            "saved": saved,
            "allowed": allowed,
        },
    )


@router.post("/orgs/{org_id}/settings")
def update_org_settings(
    request: Request,
    org_id: int,
    max_requests_per_day: str = Form(""),
    max_prompt_tokens_per_day: str = Form(""),
    max_completion_tokens_per_day: str = Form(""),
    allowed_models: str = Form(""),
):
    user = user_or_redirect(request)
    if isinstance(user, RedirectResponse):
        return user
    db = request.app.state.session_factory()
    try:
        membership = db.execute(
            select(Membership).where(Membership.user_id == user.id, Membership.organization_id == org_id)
        ).scalar_one_or_none()
        if membership is None:
            raise HTTPException(status_code=404, detail="organization not found")
        if membership.role != "admin":
            raise HTTPException(status_code=403, detail="only organization admins can change settings")
        org = db.get(Organization, org_id)

        def parse_int(value: str) -> int | None:
            value = value.strip()
            if not value:
                return None
            try:
                return int(value)
            except ValueError:
                raise HTTPException(status_code=400, detail=f"'{value}' is not a valid integer")

        org.max_requests_per_day = parse_int(max_requests_per_day)
        org.max_prompt_tokens_per_day = parse_int(max_prompt_tokens_per_day)
        org.max_completion_tokens_per_day = parse_int(max_completion_tokens_per_day)
        allowed = [m.strip() for m in allowed_models.split(",") if m.strip()]
        org.allowed_models = allowed or None
        db.commit()
    finally:
        db.close()
    return RedirectResponse(f"/orgs/{org_id}?saved=1")
