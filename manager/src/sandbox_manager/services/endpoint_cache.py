"""Sandbox ID -> Execd Endpoint 的短期缓存。

同一 ID 的并发获取只向 Runtime 发起一次请求；失效后下次获取重新查询。
Manager 不持久化 Endpoint，事实来源为 Runtime。
"""

from __future__ import annotations

import asyncio
import time
from collections.abc import Awaitable, Callable


class EndpointCache:
    def __init__(
        self,
        fetch: Callable[[str], Awaitable[str]],
        ttl: float,
        clock: Callable[[], float] = time.monotonic,
    ) -> None:
        self._fetch = fetch
        self._ttl = ttl
        self._clock = clock
        self._values: dict[str, tuple[str, float]] = {}
        self._locks: dict[str, asyncio.Lock] = {}

    async def get(self, sandbox_id: str) -> str:
        cached = self._fresh(sandbox_id)
        if cached is not None:
            return cached
        lock = self._locks.setdefault(sandbox_id, asyncio.Lock())
        async with lock:
            cached = self._fresh(sandbox_id)
            if cached is not None:
                return cached
            value = await self._fetch(sandbox_id)
            self._values[sandbox_id] = (value, self._clock() + self._ttl)
            return value

    def invalidate(self, sandbox_id: str) -> None:
        self._values.pop(sandbox_id, None)

    def _fresh(self, sandbox_id: str) -> str | None:
        entry = self._values.get(sandbox_id)
        if entry is None:
            return None
        value, expires = entry
        if self._clock() >= expires:
            self._values.pop(sandbox_id, None)
            return None
        return value
