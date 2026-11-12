"""Sandbox 生命周期编排：封装 Runtime 接口，并维护 Endpoint 缓存。"""

from __future__ import annotations

from typing import Any

from sandbox_manager.clients.runtime import RuntimeClient
from sandbox_manager.services.endpoint_cache import EndpointCache


class SandboxService:
    def __init__(self, runtime: RuntimeClient, endpoints: EndpointCache) -> None:
        self.runtime = runtime
        self.endpoints = endpoints

    async def create(self, body: dict[str, Any], idempotency_key: str | None) -> dict[str, Any]:
        sb = await self.runtime.create(body, idempotency_key)
        self.endpoints.invalidate(sb["id"])
        return sb

    async def list(self, status: str | None) -> list[dict[str, Any]]:
        return await self.runtime.list(status)

    async def get(self, sandbox_id: str) -> dict[str, Any]:
        return await self.runtime.get(sandbox_id)

    async def start(self, sandbox_id: str) -> dict[str, Any]:
        sb = await self.runtime.start(sandbox_id)
        # 启动后端口可能变化，必须重新获取。
        self.endpoints.invalidate(sandbox_id)
        return sb

    async def stop(self, sandbox_id: str) -> dict[str, Any]:
        sb = await self.runtime.stop(sandbox_id)
        self.endpoints.invalidate(sandbox_id)
        return sb

    async def restart(self, sandbox_id: str) -> dict[str, Any]:
        """组合 stop + start。任一步失败即返回错误，不做回滚（见 plan.md R1）。"""
        await self.stop(sandbox_id)
        return await self.start(sandbox_id)

    async def delete(self, sandbox_id: str) -> None:
        await self.runtime.delete(sandbox_id)
        self.endpoints.invalidate(sandbox_id)

    async def stats(self, sandbox_id: str) -> dict[str, Any]:
        return await self.runtime.stats(sandbox_id)

    async def endpoint(self, sandbox_id: str) -> str:
        return await self.endpoints.get(sandbox_id)
