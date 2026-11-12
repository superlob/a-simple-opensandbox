from __future__ import annotations

import asyncio

from sandbox_manager.services.endpoint_cache import EndpointCache


async def test_concurrent_gets_fetch_once() -> None:
    calls: list[str] = []

    async def fetch(sid: str) -> str:
        calls.append(sid)
        await asyncio.sleep(0.01)
        return "http://e"

    cache = EndpointCache(fetch, ttl=60)
    results = await asyncio.gather(*(cache.get("a") for _ in range(20)))
    assert results == ["http://e"] * 20
    assert calls == ["a"]


async def test_ttl_expiry_refetches() -> None:
    now = [0.0]
    calls = 0

    async def fetch(_: str) -> str:
        nonlocal calls
        calls += 1
        return f"http://e{calls}"

    cache = EndpointCache(fetch, ttl=10, clock=lambda: now[0])
    assert await cache.get("a") == "http://e1"
    now[0] = 9.0
    assert await cache.get("a") == "http://e1"
    now[0] = 10.0
    assert await cache.get("a") == "http://e2"


async def test_invalidate_forces_refetch() -> None:
    calls = 0

    async def fetch(_: str) -> str:
        nonlocal calls
        calls += 1
        return "x"

    cache = EndpointCache(fetch, ttl=60)
    await cache.get("a")
    cache.invalidate("a")
    await cache.get("a")
    assert calls == 2
