"""Execd HTTP 客户端。每次调用传入 Endpoint 基础 URL，文件与命令请求直接发往 Execd。"""

from __future__ import annotations

from collections.abc import AsyncIterator
from typing import Any

import httpx

from sandbox_manager.errors import from_response, upstream_unavailable


class ExecdClient:
    def __init__(self, http: httpx.AsyncClient, endpoint: str) -> None:
        self._http = http
        self._base = endpoint.rstrip("/")

    async def _request(self, method: str, path: str, **kwargs: Any) -> httpx.Response:
        try:
            resp = await self._http.request(method, f"{self._base}{path}", **kwargs)
        except httpx.HTTPError as exc:
            raise upstream_unavailable(exc) from exc
        if resp.status_code >= 400:
            raise from_response(resp)
        return resp

    async def info(self) -> dict[str, Any]:
        return (await self._request("GET", "/v1/info")).json()

    async def command(self, body: dict[str, Any]) -> dict[str, Any]:
        resp = await self._request("POST", "/v1/command", json=body)
        return resp.json()

    async def upload(self, path: str, filename: str, data: bytes) -> dict[str, Any]:
        resp = await self._request(
            "POST",
            "/v1/files",
            data={"path": path},
            files={"file": (filename, data, "application/octet-stream")},
        )
        return resp.json()

    async def download(self, path: str) -> AsyncIterator[bytes]:
        """流式下载。上游错误在读取响应头阶段即抛出，调用方可据此返回正确状态码。"""
        try:
            req = self._http.build_request("GET", f"{self._base}/v1/files", params={"path": path})
            resp = await self._http.send(req, stream=True)
        except httpx.HTTPError as exc:
            raise upstream_unavailable(exc) from exc
        if resp.status_code >= 400:
            await resp.aread()
            await resp.aclose()
            raise from_response(resp)

        async def body() -> AsyncIterator[bytes]:
            try:
                async for chunk in resp.aiter_bytes():
                    yield chunk
            finally:
                await resp.aclose()

        return body()

    async def stat(self, path: str) -> dict[str, Any]:
        return (await self._request("GET", "/v1/files/stat", params={"path": path})).json()

    async def delete_file(self, path: str, recursive: bool) -> None:
        params = {"path": path, "recursive": "true" if recursive else "false"}
        await self._request("DELETE", "/v1/files", params=params)

    async def mkdir(self, path: str) -> dict[str, Any]:
        return (await self._request("POST", "/v1/dirs", json={"path": path})).json()

    async def list_dir(self, path: str, limit: int | None) -> dict[str, Any]:
        params: dict[str, str] = {"path": path}
        if limit is not None:
            params["limit"] = str(limit)
        return (await self._request("GET", "/v1/dirs", params=params)).json()

    async def tasks(self, status: str | None) -> dict[str, Any]:
        params = {"status": status} if status else None
        return (await self._request("GET", "/v1/tasks", params=params)).json()

    async def task(self, task_id: str) -> dict[str, Any]:
        return (await self._request("GET", f"/v1/tasks/{task_id}")).json()

    async def task_logs(
        self, task_id: str, stream: str, offset: int, limit: int | None
    ) -> dict[str, Any]:
        params: dict[str, str] = {"stream": stream, "offset": str(offset)}
        if limit is not None:
            params["limit"] = str(limit)
        return (await self._request("GET", f"/v1/tasks/{task_id}/logs", params=params)).json()

    async def cancel_task(self, task_id: str) -> dict[str, Any]:
        return (await self._request("POST", f"/v1/tasks/{task_id}/cancel")).json()
