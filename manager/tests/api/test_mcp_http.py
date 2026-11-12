from __future__ import annotations

from fastapi.testclient import TestClient

from sandbox_manager.api.app import create_app
from sandbox_manager.config import Settings
from tests.fakes import RUNTIME_URL

HEADERS = {"Accept": "application/json, text/event-stream", "Content-Type": "application/json"}


def _settings() -> Settings:
    return Settings(
        manager_bind_host="127.0.0.1",
        manager_port=18080,
        manager_runtime_url=RUNTIME_URL,  # type: ignore[arg-type]
    )


def test_mcp_initialize_and_list_tools_over_http() -> None:
    with TestClient(create_app(_settings()), base_url="http://127.0.0.1:18080") as c:
        init = c.post(
            "/mcp/",
            headers=HEADERS,
            json={
                "jsonrpc": "2.0",
                "id": 1,
                "method": "initialize",
                "params": {
                    "protocolVersion": "2025-06-18",
                    "capabilities": {},
                    "clientInfo": {"name": "test", "version": "1"},
                },
            },
        )
        assert init.status_code == 200
        tools = c.post(
            "/mcp/",
            headers=HEADERS,
            json={"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}},
        )
        assert tools.status_code == 200
        assert '"name":"sandbox_create"' in tools.text.replace(" ", "")


def test_mcp_rejects_foreign_host() -> None:
    with TestClient(create_app(_settings()), base_url="http://evil.example:18080") as c:
        r = c.post(
            "/mcp/",
            headers=HEADERS,
            json={"jsonrpc": "2.0", "id": 1, "method": "ping"},
        )
        assert r.status_code == 421
