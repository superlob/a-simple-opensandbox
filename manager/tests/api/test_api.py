from __future__ import annotations

import pytest
from fastapi.testclient import TestClient

from sandbox_manager.api.app import create_app
from sandbox_manager.config import Settings
from tests.fakes import RUNTIME_URL, FakeWorld, transport_for


@pytest.fixture
def world() -> FakeWorld:
    return FakeWorld()


@pytest.fixture
def client(world: FakeWorld):  # type: ignore[no-untyped-def]
    settings = Settings(
        manager_bind_host="127.0.0.1",
        manager_port=18080,
        manager_runtime_url=RUNTIME_URL,  # type: ignore[arg-type]
        upload_max_bytes=16,
    )
    app = create_app(settings, transport=transport_for(world))
    with TestClient(app) as c:
        yield c


def test_health(client: TestClient) -> None:
    assert client.get("/health").json() == {"status": "ok"}


def test_create_forwards_body_and_idempotency_key(client: TestClient, world: FakeWorld) -> None:
    r = client.post(
        "/api/v1/sandboxes",
        json={"resources": {"cpus": 1}, "env": {"A": "1"}},
        headers={"Idempotency-Key": "k1"},
    )
    assert r.status_code == 201
    assert r.json()["id"] == "sbx1"
    assert world.last_create_body == {"resources": {"cpus": 1}, "env": {"A": "1"}}
    assert world.last_create_headers.get("idempotency-key") == "k1"


def test_unknown_field_rejected_as_400(client: TestClient) -> None:
    r = client.post("/api/v1/sandboxes", json={"image": "evil"})
    assert r.status_code == 400
    assert r.json()["error"]["code"] == "invalid_argument"


def test_runtime_error_code_passes_through(client: TestClient) -> None:
    r = client.get("/api/v1/sandboxes/missing")
    assert r.status_code == 404
    assert r.json() == {"error": {"code": "not_found", "message": "sandbox not found"}}


def test_endpoint_is_cached_across_commands(client: TestClient, world: FakeWorld) -> None:
    client.post("/api/v1/sandboxes", json={})
    r1 = client.post("/api/v1/sandboxes/sbx1/command", json={"argv": ["echo", "hi"]})
    r2 = client.post("/api/v1/sandboxes/sbx1/command", json={"argv": ["echo", "hi"]})
    assert r1.status_code == r2.status_code == 200
    assert r1.json()["stdout"] == "hi\n"
    assert world.endpoint_fetches == 1


def test_command_body_forwarded_without_nulls(client: TestClient, world: FakeWorld) -> None:
    client.post("/api/v1/sandboxes", json={})
    client.post("/api/v1/sandboxes/sbx1/command", json={"argv": ["ls"], "background": True})
    assert world.last_command == {"argv": ["ls"], "background": True}


def test_command_argv_required(client: TestClient) -> None:
    client.post("/api/v1/sandboxes", json={})
    r = client.post("/api/v1/sandboxes/sbx1/command", json={"argv": []})
    assert r.status_code == 400


def test_stopped_sandbox_endpoint_conflict(client: TestClient) -> None:
    client.post("/api/v1/sandboxes", json={})
    client.post("/api/v1/sandboxes/sbx1/stop")
    r = client.post("/api/v1/sandboxes/sbx1/command", json={"argv": ["ls"]})
    assert r.status_code == 409
    assert r.json()["error"]["code"] == "conflict"


def test_stop_then_start_refreshes_endpoint(client: TestClient, world: FakeWorld) -> None:
    client.post("/api/v1/sandboxes", json={})
    client.get("/api/v1/sandboxes/sbx1/endpoint")
    client.post("/api/v1/sandboxes/sbx1/stop")
    client.post("/api/v1/sandboxes/sbx1/start")
    r = client.get("/api/v1/sandboxes/sbx1/endpoint")
    assert r.status_code == 200
    # 首次获取；停止与启动均失效缓存，因此只有启动后的查询会再次访问 Runtime
    assert world.endpoint_fetches == 2


def test_get_retried_once_after_connection_error(client: TestClient, world: FakeWorld) -> None:
    client.post("/api/v1/sandboxes", json={})
    world.execd_fail_next_get = 1
    r = client.get("/api/v1/sandboxes/sbx1/tasks/t1")
    assert r.status_code == 200
    assert r.json()["status"] == "succeeded"


def test_post_not_affected_by_get_failure_injection(client: TestClient, world: FakeWorld) -> None:
    client.post("/api/v1/sandboxes", json={})
    world.execd_fail_next_get = 1  # 只作用于 GET；命令为 POST，此处应正常成功且不消耗该计数
    r = client.post("/api/v1/sandboxes/sbx1/command", json={"argv": ["ls"]})
    assert r.status_code == 200
    assert world.execd_fail_next_get == 1


def test_unreachable_execd_maps_to_503(client: TestClient, world: FakeWorld) -> None:
    client.post("/api/v1/sandboxes", json={})
    world.execd_fail_next_get = 2  # GET 首次失败后重试仍失败
    r = client.get("/api/v1/sandboxes/sbx1/tasks/t1")
    assert r.status_code == 503
    assert r.json()["error"]["code"] == "upstream_unavailable"


def test_execd_403_passes_through(client: TestClient) -> None:
    client.post("/api/v1/sandboxes", json={})
    r = client.get("/api/v1/sandboxes/sbx1/files/stat", params={"path": "/etc"})
    assert r.status_code == 403
    assert r.json()["error"]["code"] == "forbidden"


def test_upload_limit_returns_413(client: TestClient) -> None:
    client.post("/api/v1/sandboxes", json={})
    r = client.post(
        "/api/v1/sandboxes/sbx1/files",
        params={"path": "/workspace/big"},
        files={"file": ("big", b"x" * 17, "application/octet-stream")},
    )
    assert r.status_code == 413


def test_upload_and_download_roundtrip(client: TestClient) -> None:
    client.post("/api/v1/sandboxes", json={})
    payload = bytes(range(256))
    r = client.post(
        "/api/v1/sandboxes/sbx1/files",
        params={"path": "/workspace/a.bin"},
        files={"file": ("a.bin", payload[:16], "application/octet-stream")},
    )
    assert r.status_code == 201
    d = client.get("/api/v1/sandboxes/sbx1/files", params={"path": "/workspace/a.bin"})
    assert d.status_code == 200
    assert d.content == payload[:16]


def test_task_logs_validates_stream(client: TestClient) -> None:
    client.post("/api/v1/sandboxes", json={})
    r = client.get("/api/v1/sandboxes/sbx1/tasks/t1/logs", params={"stream": "bogus"})
    assert r.status_code == 400


def test_cancel_task_returns_202(client: TestClient) -> None:
    client.post("/api/v1/sandboxes", json={})
    r = client.post("/api/v1/sandboxes/sbx1/tasks/t1/cancel")
    assert r.status_code == 202


def test_delete_returns_204_and_clears_endpoint(client: TestClient, world: FakeWorld) -> None:
    client.post("/api/v1/sandboxes", json={})
    client.get("/api/v1/sandboxes/sbx1/endpoint")
    assert client.delete("/api/v1/sandboxes/sbx1").status_code == 204
    assert client.get("/api/v1/sandboxes/sbx1").status_code == 404
