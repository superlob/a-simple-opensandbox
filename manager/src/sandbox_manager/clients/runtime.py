"""Runtime HTTP 客户端：Sandbox 生命周期与状态查询。"""

from __future__ import annotations

from typing import Any

import httpx

from sandbox_manager.errors import ManagerError, from_response, upstream_unavailable


class RuntimeClient:
    def __init__(self, http: httpx.AsyncClient, base_url: str) -> None:
        self._http = http
        self._base = base_url.rstrip("/")

    async def _request(
        self,
        method: str,
        path: str,
        *,
        json: Any = None,
        params: dict[str, str] | None = None,
        headers: dict[str, str] | None = None,
    ) -> httpx.Response:
        try:
            resp = await self._http.request(
                method, f"{self._base}{path}", json=json, params=params, headers=headers
            )
        except httpx.HTTPError as exc:
            raise upstream_unavailable(exc) from exc
        if resp.status_code >= 400:
            raise from_response(resp)
        return resp

    async def create(self, body: dict[str, Any], idempotency_key: str | None) -> dict[str, Any]:
        headers = {"Idempotency-Key": idempotency_key} if idempotency_key else None
        resp = await self._request("POST", "/v1/sandboxes", json=body, headers=headers)
        return resp.json()

    async def list(self, status: str | None) -> list[dict[str, Any]]:
        params = {"status": status} if status else None
        resp = await self._request("GET", "/v1/sandboxes", params=params)
        return resp.json()["items"]

    async def get(self, sandbox_id: str) -> dict[str, Any]:
        return (await self._request("GET", f"/v1/sandboxes/{sandbox_id}")).json()

    async def start(self, sandbox_id: str) -> dict[str, Any]:
        return (await self._request("POST", f"/v1/sandboxes/{sandbox_id}/start")).json()

    async def stop(self, sandbox_id: str) -> dict[str, Any]:
        return (await self._request("POST", f"/v1/sandboxes/{sandbox_id}/stop")).json()

    async def delete(self, sandbox_id: str) -> None:
        await self._request("DELETE", f"/v1/sandboxes/{sandbox_id}")

    async def stats(self, sandbox_id: str) -> dict[str, Any]:
        return (await self._request("GET", f"/v1/sandboxes/{sandbox_id}/stats")).json()

    async def endpoint(self, sandbox_id: str) -> str:
        resp = await self._request("GET", f"/v1/sandboxes/{sandbox_id}/endpoint")
        value = resp.json().get("endpoint")
        if not value:
            raise ManagerError("conflict", "sandbox has no reachable endpoint")
        return str(value)
