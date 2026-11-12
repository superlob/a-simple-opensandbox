"""FastAPI 应用工厂。"""

from __future__ import annotations

import logging
import time
import uuid
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Any

import httpx
from fastapi import FastAPI, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse
from fastapi.staticfiles import StaticFiles
from mcp.server.transport_security import TransportSecuritySettings

from sandbox_manager.api import routes
from sandbox_manager.config import Settings
from sandbox_manager.deps import build_services
from sandbox_manager.errors import ManagerError
from sandbox_manager.mcp.server import build_mcp

log = logging.getLogger("sandbox_manager.access")
WEB_DIR = Path(__file__).resolve().parent.parent / "web"


def create_app(
    settings: Settings | None = None,
    transport: httpx.AsyncBaseTransport | None = None,
) -> FastAPI:
    """创建应用。测试时可注入 transport 替换上游（Runtime/Execd）。"""
    cfg = settings or Settings()  # type: ignore[call-arg]

    mcp = build_mcp(lambda: app.state.services)

    @asynccontextmanager
    async def lifespan(app: FastAPI) -> AsyncIterator[None]:
        timeout = httpx.Timeout(
            cfg.upstream_read_timeout, connect=cfg.upstream_connect_timeout
        )
        async with httpx.AsyncClient(timeout=timeout, transport=transport) as http:
            app.state.services = build_services(cfg, http)
            # 挂载的 MCP 子应用不会自动运行其 lifespan，需显式启动会话管理器。
            async with mcp.session_manager.run():
                yield

    app = FastAPI(title="Sandbox Manager", version="0.1.0", lifespan=lifespan)
    app.include_router(routes.router, prefix="/api/v1")

    @app.exception_handler(ManagerError)
    async def _manager_error(_: Request, exc: ManagerError) -> JSONResponse:
        return JSONResponse(status_code=exc.status, content=exc.to_body())

    @app.exception_handler(RequestValidationError)
    async def _validation(_: Request, exc: RequestValidationError) -> JSONResponse:
        # 请求校验失败统一为 400 invalid_argument（与 Runtime 一致），不返回输入值。
        first = exc.errors()[0] if exc.errors() else {}
        loc = ".".join(str(p) for p in first.get("loc", ()))
        msg = f"invalid request field: {loc}" if loc else "invalid request"
        return JSONResponse(
            status_code=400,
            content={"error": {"code": "invalid_argument", "message": msg}},
        )

    @app.middleware("http")
    async def _access_log(request: Request, call_next: Any) -> Any:
        request_id = uuid.uuid4().hex[:12]
        started = time.perf_counter()
        status = 500
        try:
            response = await call_next(request)
            status = response.status_code
            return response
        finally:
            # 只记录方法、路径、状态与耗时；不记录请求体与查询参数中的 env 值。
            log.info(
                "request",
                extra={
                    "request_id": request_id,
                    "method": request.method,
                    "path": request.url.path,
                    "status": status,
                    "duration_ms": round((time.perf_counter() - started) * 1000, 1),
                },
            )

    @app.get("/health")
    async def health() -> dict[str, str]:
        return {"status": "ok"}

    @app.exception_handler(Exception)
    async def _unhandled(_: Request, exc: Exception) -> JSONResponse:
        log.exception("unhandled error: %s", type(exc).__name__)
        return JSONResponse(
            status_code=500,
            content={"error": {"code": "internal", "message": "internal error"}},
        )

    # DNS 重绑定防护：只接受 Manager 绑定地址与本机回环地址的 Host。
    allowed = {f"{cfg.manager_bind_host}:{cfg.manager_port}", f"127.0.0.1:{cfg.manager_port}",
               f"localhost:{cfg.manager_port}"}
    app.mount(
        "/mcp",
        mcp.streamable_http_app(
            streamable_http_path="/",
            stateless_http=True,
            json_response=True,
            transport_security=TransportSecuritySettings(allowed_hosts=sorted(allowed)),
        ),
    )

    if WEB_DIR.is_dir() and (WEB_DIR / "index.html").exists():
        app.mount("/", StaticFiles(directory=WEB_DIR, html=True), name="web")

    return app
