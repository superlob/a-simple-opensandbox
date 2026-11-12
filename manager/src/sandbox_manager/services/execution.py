"""命令、文件与任务编排：经 Endpoint 直接调用 Execd。

Execd 连接失败时，失效 Endpoint 缓存并重新获取，然后仅对幂等的 GET 请求重试一次。
命令执行与写操作不自动重试。
"""

from __future__ import annotations

from collections.abc import AsyncIterator, Awaitable, Callable
from typing import Any, TypeVar

import httpx

from sandbox_manager.clients.execd import ExecdClient
from sandbox_manager.errors import ManagerError
from sandbox_manager.services.endpoint_cache import EndpointCache

T = TypeVar("T")


class ExecutionService:
    def __init__(self, http: httpx.AsyncClient, endpoints: EndpointCache) -> None:
        self._http = http
        self._endpoints = endpoints

    async def _call(
        self, sandbox_id: str, op: Callable[[ExecdClient], Awaitable[T]], *, retry: bool
    ) -> T:
        endpoint = await self._endpoints.get(sandbox_id)
        try:
            return await op(ExecdClient(self._http, endpoint))
        except ManagerError as exc:
            if not (retry and exc.code == "upstream_unavailable"):
                raise
            self._endpoints.invalidate(sandbox_id)
        endpoint = await self._endpoints.get(sandbox_id)
        return await op(ExecdClient(self._http, endpoint))

    async def info(self, sandbox_id: str) -> dict[str, Any]:
        return await self._call(sandbox_id, lambda c: c.info(), retry=True)

    async def command(self, sandbox_id: str, body: dict[str, Any]) -> dict[str, Any]:
        return await self._call(sandbox_id, lambda c: c.command(body), retry=False)

    async def tasks(self, sandbox_id: str, status: str | None) -> dict[str, Any]:
        return await self._call(sandbox_id, lambda c: c.tasks(status), retry=True)

    async def task(self, sandbox_id: str, task_id: str) -> dict[str, Any]:
        return await self._call(sandbox_id, lambda c: c.task(task_id), retry=True)

    async def task_logs(
        self, sandbox_id: str, task_id: str, stream: str, offset: int, limit: int | None
    ) -> dict[str, Any]:
        return await self._call(
            sandbox_id,
            lambda c: c.task_logs(task_id, stream, offset, limit),
            retry=True,
        )

    async def cancel_task(self, sandbox_id: str, task_id: str) -> dict[str, Any]:
        return await self._call(sandbox_id, lambda c: c.cancel_task(task_id), retry=False)

    async def upload(self, sandbox_id: str, path: str, filename: str, data: bytes) -> dict[str, Any]:
        return await self._call(
            sandbox_id, lambda c: c.upload(path, filename, data), retry=False
        )

    async def download(self, sandbox_id: str, path: str) -> AsyncIterator[bytes]:
        return await self._call(sandbox_id, lambda c: c.download(path), retry=True)

    async def stat(self, sandbox_id: str, path: str) -> dict[str, Any]:
        return await self._call(sandbox_id, lambda c: c.stat(path), retry=True)

    async def delete_file(self, sandbox_id: str, path: str, recursive: bool) -> None:
        await self._call(sandbox_id, lambda c: c.delete_file(path, recursive), retry=False)

    async def mkdir(self, sandbox_id: str, path: str) -> dict[str, Any]:
        return await self._call(sandbox_id, lambda c: c.mkdir(path), retry=False)

    async def list_dir(self, sandbox_id: str, path: str, limit: int | None) -> dict[str, Any]:
        return await self._call(sandbox_id, lambda c: c.list_dir(path, limit), retry=True)
