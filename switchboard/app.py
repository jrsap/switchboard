from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import JSONResponse

from .api import router as api_router
from .config import SwitchboardConfig, load_config
from .db import Base, make_engine, make_session_factory
from .settings import Settings
from .web import router as web_router


def create_app(settings: Settings | None = None, config: SwitchboardConfig | None = None) -> FastAPI:
    settings = settings or Settings()
    engine = make_engine(settings.database_url)
    Base.metadata.create_all(engine)
    session_factory = make_session_factory(engine)
    cfg = config if config is not None else load_config(settings.config_path)

    app = FastAPI(title="Switchboard")
    app.state.settings = settings
    app.state.engine = engine
    app.state.session_factory = session_factory
    app.state.config = cfg

    @app.exception_handler(HTTPException)
    async def handle_http_exception(request: Request, exc: HTTPException):
        return JSONResponse(
            status_code=exc.status_code,
            content={
                "error": {
                    "message": exc.detail,
                    "type": "invalid_request_error",
                    "code": str(exc.status_code),
                }
            },
        )

    @app.get("/healthz")
    def healthz():
        return {"status": "ok"}

    app.include_router(api_router, prefix="/v1")
    app.include_router(web_router)
    return app
