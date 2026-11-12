"""应用级依赖容器，由 create_app 构造，路由与 MCP 共用同一套服务。"""

from __future__ import annotations

from dataclasses import dataclass

import httpx

from sandbox_manager.clients.runtime import RuntimeClient
from sandbox_manager.config import Settings
from sandbox_manager.services.endpoint_cache import EndpointCache
from sandbox_manager.services.execution import ExecutionService
from sandbox_manager.services.sandbox import SandboxService


@dataclass
class Services:
    settings: Settings
    http: httpx.AsyncClient
    sandboxes: SandboxService
    execution: ExecutionService


def build_services(settings: Settings, http: httpx.AsyncClient) -> Services:
    runtime = RuntimeClient(http, str(settings.manager_runtime_url))

    async def fetch(sandbox_id: str) -> str:
        return await runtime.endpoint(sandbox_id)

    endpoints = EndpointCache(fetch, ttl=settings.endpoint_cache_ttl)
    return Services(
        settings=settings,
        http=http,
        sandboxes=SandboxService(runtime, endpoints),
        execution=ExecutionService(http, endpoints),
    )
