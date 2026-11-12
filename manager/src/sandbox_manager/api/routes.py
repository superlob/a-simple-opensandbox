"""HTTP 路由（前缀 /api/v1）。只做参数转换，业务逻辑在 services 层。"""

from __future__ import annotations

from typing import Any

from fastapi import APIRouter, Header, Query, Request, UploadFile
from fastapi.responses import JSONResponse, Response, StreamingResponse

from sandbox_manager.api.schemas import CommandRequest, CreateSandboxRequest, MkdirRequest
from sandbox_manager.deps import Services
from sandbox_manager.errors import ManagerError

router = APIRouter()


def _svc(request: Request) -> Services:
    return request.app.state.services  # type: ignore[no-any-return]


def _dump(model: Any) -> dict[str, Any]:
    return model.model_dump(exclude_none=True)


# ---- Sandbox 管理（封装 Runtime） ----

@router.post("/sandboxes", status_code=201)
async def create_sandbox(
    request: Request,
    body: CreateSandboxRequest,
    idempotency_key: str | None = Header(default=None, alias="Idempotency-Key"),
) -> dict[str, Any]:
    return await _svc(request).sandboxes.create(_dump(body), idempotency_key)


@router.get("/sandboxes")
async def list_sandboxes(
    request: Request, status: str | None = Query(default=None)
) -> dict[str, Any]:
    return {"items": await _svc(request).sandboxes.list(status)}


@router.get("/sandboxes/{sandbox_id}")
async def get_sandbox(request: Request, sandbox_id: str) -> dict[str, Any]:
    return await _svc(request).sandboxes.get(sandbox_id)


@router.post("/sandboxes/{sandbox_id}/start")
async def start_sandbox(request: Request, sandbox_id: str) -> dict[str, Any]:
    return await _svc(request).sandboxes.start(sandbox_id)


@router.post("/sandboxes/{sandbox_id}/stop")
async def stop_sandbox(request: Request, sandbox_id: str) -> dict[str, Any]:
    return await _svc(request).sandboxes.stop(sandbox_id)


@router.post("/sandboxes/{sandbox_id}/restart")
async def restart_sandbox(request: Request, sandbox_id: str) -> dict[str, Any]:
    return await _svc(request).sandboxes.restart(sandbox_id)


@router.delete("/sandboxes/{sandbox_id}", status_code=204)
async def delete_sandbox(request: Request, sandbox_id: str) -> Response:
    await _svc(request).sandboxes.delete(sandbox_id)
    return Response(status_code=204)


@router.get("/sandboxes/{sandbox_id}/stats")
async def sandbox_stats(request: Request, sandbox_id: str) -> dict[str, Any]:
    return await _svc(request).sandboxes.stats(sandbox_id)


@router.get("/sandboxes/{sandbox_id}/endpoint")
async def sandbox_endpoint(request: Request, sandbox_id: str) -> dict[str, Any]:
    return {"endpoint": await _svc(request).sandboxes.endpoint(sandbox_id)}


# ---- 命令与任务（封装 Execd） ----

@router.get("/sandboxes/{sandbox_id}/info")
async def execd_info(request: Request, sandbox_id: str) -> dict[str, Any]:
    return await _svc(request).execution.info(sandbox_id)


@router.post("/sandboxes/{sandbox_id}/command")
async def run_command(request: Request, sandbox_id: str, body: CommandRequest) -> JSONResponse:
    result = await _svc(request).execution.command(sandbox_id, _dump(body))
    # 与 Execd 一致：后台任务返回 202，前台执行返回 200。
    status = 202 if body.background else 200
    return JSONResponse(status_code=status, content=result)


@router.get("/sandboxes/{sandbox_id}/tasks")
async def list_tasks(
    request: Request, sandbox_id: str, status: str | None = Query(default=None)
) -> dict[str, Any]:
    return await _svc(request).execution.tasks(sandbox_id, status)


@router.get("/sandboxes/{sandbox_id}/tasks/{task_id}")
async def get_task(request: Request, sandbox_id: str, task_id: str) -> dict[str, Any]:
    return await _svc(request).execution.task(sandbox_id, task_id)


@router.get("/sandboxes/{sandbox_id}/tasks/{task_id}/logs")
async def task_logs(
    request: Request,
    sandbox_id: str,
    task_id: str,
    stream: str = Query(default="stdout", pattern="^(stdout|stderr)$"),
    offset: int = Query(default=0, ge=0),
    limit: int | None = Query(default=None, ge=1),
) -> dict[str, Any]:
    return await _svc(request).execution.task_logs(sandbox_id, task_id, stream, offset, limit)


@router.post("/sandboxes/{sandbox_id}/tasks/{task_id}/cancel", status_code=202)
async def cancel_task(request: Request, sandbox_id: str, task_id: str) -> dict[str, Any]:
    return await _svc(request).execution.cancel_task(sandbox_id, task_id)


# ---- 文件与目录（封装 Execd） ----

@router.post("/sandboxes/{sandbox_id}/files", status_code=201)
async def upload_file(
    request: Request,
    sandbox_id: str,
    path: str = Query(min_length=1),
    file: UploadFile | None = None,
) -> dict[str, Any]:
    svc = _svc(request)
    if file is None:
        raise ManagerError("invalid_argument", "multipart field 'file' is required")
    limit = svc.settings.upload_max_bytes
    data = await file.read(limit + 1)
    if len(data) > limit:
        raise ManagerError("payload_too_large", f"file exceeds {limit} bytes")
    return await svc.execution.upload(sandbox_id, path, file.filename or "upload", data)


@router.get("/sandboxes/{sandbox_id}/files")
async def download_file(request: Request, sandbox_id: str, path: str = Query(min_length=1)) -> Response:
    chunks = await _svc(request).execution.download(sandbox_id, path)
    return StreamingResponse(chunks, media_type="application/octet-stream")


@router.get("/sandboxes/{sandbox_id}/files/stat")
async def stat_file(request: Request, sandbox_id: str, path: str = Query(min_length=1)) -> dict[str, Any]:
    return await _svc(request).execution.stat(sandbox_id, path)


@router.delete("/sandboxes/{sandbox_id}/files", status_code=204)
async def delete_file(
    request: Request,
    sandbox_id: str,
    path: str = Query(min_length=1),
    recursive: bool = Query(default=False),
) -> Response:
    await _svc(request).execution.delete_file(sandbox_id, path, recursive)
    return Response(status_code=204)


@router.post("/sandboxes/{sandbox_id}/dirs")
async def make_dir(request: Request, sandbox_id: str, body: MkdirRequest) -> dict[str, Any]:
    return await _svc(request).execution.mkdir(sandbox_id, body.path)


@router.get("/sandboxes/{sandbox_id}/dirs")
async def list_dir(
    request: Request,
    sandbox_id: str,
    path: str = Query(min_length=1),
    limit: int | None = Query(default=None, ge=1),
) -> dict[str, Any]:
    return await _svc(request).execution.list_dir(sandbox_id, path, limit)
