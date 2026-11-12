"""端到端：Manager → 真实 Runtime → Docker → Execd。需要 SANDBOX.env 与 Docker，`make integration` 运行。"""

from __future__ import annotations

import hashlib
import os
import time
from collections.abc import Iterator

import pytest
from fastapi.testclient import TestClient

from sandbox_manager.api.app import create_app
from sandbox_manager.config import Settings

pytestmark = pytest.mark.integration


@pytest.fixture(scope="module")
def client() -> Iterator[TestClient]:
    settings = Settings()  # type: ignore[call-arg]  # 从环境变量读取（Makefile 加载 SANDBOX.env）
    with TestClient(create_app(settings)) as c:
        yield c


def test_full_lifecycle(client: TestClient) -> None:
    api = "/api/v1/sandboxes"
    created = client.post(api, json={"env": {"MGR_IT": "yes"}}, headers={"Idempotency-Key": "mgr-it-1"})
    assert created.status_code == 201, created.text
    sid = created.json()["id"]
    try:
        # 前台命令：默认 cwd 为 /workspace，请求环境变量生效
        r = client.post(f"{api}/{sid}/command",
                        json={"argv": ["sh", "-c", "pwd; echo $MGR_IT"]})
        assert r.status_code == 200, r.text
        assert r.json()["exit_code"] == 0
        assert r.json()["stdout"].split() == ["/workspace", "yes"]

        # 文件往返 sha256 一致
        payload = os.urandom(64 * 1024)
        up = client.post(f"{api}/{sid}/files", params={"path": "/workspace/it.bin"},
                         files={"file": ("it.bin", payload, "application/octet-stream")})
        assert up.status_code == 201, up.text
        down = client.get(f"{api}/{sid}/files", params={"path": "/workspace/it.bin"})
        assert hashlib.sha256(down.content).digest() == hashlib.sha256(payload).digest()

        # 路径越界由 Execd 拒绝，并透传 403
        bad = client.get(f"{api}/{sid}/files/stat", params={"path": "/etc/passwd"})
        assert bad.status_code == 403

        # 后台任务：提交、查询、日志
        bg = client.post(f"{api}/{sid}/command",
                         json={"argv": ["sh", "-c", "echo bg-ok"], "background": True})
        assert bg.status_code == 202, bg.text
        task_id = bg.json()["execution_id"]
        status = None
        for _ in range(50):
            status = client.get(f"{api}/{sid}/tasks/{task_id}").json()["status"]
            if status in ("succeeded", "failed", "canceled"):
                break
            time.sleep(0.2)
        assert status == "succeeded"
        log = client.get(f"{api}/{sid}/tasks/{task_id}/logs").json()
        assert "bg-ok" in log["content"]

        # 停止 → 端点与命令返回 409；启动后文件保留
        assert client.post(f"{api}/{sid}/stop").status_code == 200
        assert client.post(f"{api}/{sid}/command", json={"argv": ["true"]}).status_code == 409
        assert client.post(f"{api}/{sid}/start").status_code == 200
        kept = client.get(f"{api}/{sid}/files", params={"path": "/workspace/it.bin"})
        assert kept.status_code == 200 and len(kept.content) == len(payload)

        # 统计
        assert client.get(f"{api}/{sid}/stats").status_code == 200
    finally:
        assert client.delete(f"{api}/{sid}").status_code == 204
    assert client.get(f"{api}/{sid}").status_code == 404
