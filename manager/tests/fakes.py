"""内存版 Runtime 与 Execd，通过 httpx.MockTransport 替换上游。"""

from __future__ import annotations

import json
import re
from dataclasses import dataclass, field
from typing import Any

import httpx

RUNTIME_URL = "http://runtime.test:8081"
EXECD_URL = "http://execd.test:9000"


def err(status: int, code: str, message: str) -> httpx.Response:
    return httpx.Response(status, json={"error": {"code": code, "message": message}})


@dataclass
class FakeWorld:
    sandboxes: dict[str, dict[str, Any]] = field(default_factory=dict)
    requests: list[httpx.Request] = field(default_factory=list)
    endpoint_fetches: int = 0
    execd_fail_next_get: int = 0  # 让接下来 N 次 Execd GET 模拟连接失败
    execd_files: dict[str, bytes] = field(default_factory=dict)
    last_command: dict[str, Any] | None = None
    last_create_headers: dict[str, str] = field(default_factory=dict)
    last_create_body: dict[str, Any] | None = None

    def handler(self, request: httpx.Request) -> httpx.Response:
        self.requests.append(request)
        url = str(request.url)
        if url.startswith(RUNTIME_URL):
            return self._runtime(request)
        if url.startswith(EXECD_URL):
            return self._execd(request)
        return err(404, "not_found", "unknown host")

    # ---- Runtime ----
    def _runtime(self, request: httpx.Request) -> httpx.Response:
        path = request.url.path
        method = request.method
        if method == "POST" and path == "/v1/sandboxes":
            self.last_create_headers = dict(request.headers)
            self.last_create_body = json.loads(request.content or b"{}")
            sid = "sbx1"
            sb = {"id": sid, "image": "sandbox-base:py3.13-uv0.13.7", "status": "ready",
                  "created_at": "2026-11-11T00:00:00Z", "resources": {}, "endpoint": EXECD_URL}
            self.sandboxes[sid] = sb
            return httpx.Response(201, json=sb)
        if method == "GET" and path == "/v1/sandboxes":
            return httpx.Response(200, json={"items": list(self.sandboxes.values())})
        parts = path.strip("/").split("/")
        if len(parts) >= 3 and parts[:2] == ["v1", "sandboxes"]:
            sid = parts[2]
            sb = self.sandboxes.get(sid)
            if sb is None:
                return err(404, "not_found", "sandbox not found")
            action = parts[3] if len(parts) > 3 else ""
            if method == "GET" and action == "":
                return httpx.Response(200, json=sb)
            if method == "GET" and action == "endpoint":
                self.endpoint_fetches += 1
                if sb["status"] != "ready":
                    return err(409, "conflict", "sandbox has no reachable endpoint")
                return httpx.Response(200, json={"endpoint": sb["endpoint"]})
            if method == "POST" and action == "stop":
                sb["status"] = "stopped"
                return httpx.Response(200, json=sb)
            if method == "POST" and action == "start":
                sb["status"] = "ready"
                return httpx.Response(200, json=sb)
            if method == "DELETE" and action == "":
                del self.sandboxes[sid]
                return httpx.Response(204)
        return err(404, "not_found", "no route")

    # ---- Execd ----
    def _execd(self, request: httpx.Request) -> httpx.Response:
        path = request.url.path
        method = request.method
        if method == "GET" and self.execd_fail_next_get > 0:
            self.execd_fail_next_get -= 1
            raise httpx.ConnectError("refused", request=request)
        if path == "/v1/command" and method == "POST":
            self.last_command = json.loads(request.content)
            return httpx.Response(200, json={"exit_code": 0, "stdout": "hi\n", "stderr": ""})
        if path == "/v1/files" and method == "POST":
            body = request.read()
            m = re.search(rb'name="path"\r\n\r\n([^\r]*)\r\n', body)
            target = m.group(1).decode() if m else ""
            fm = re.search(rb'name="file".*?\r\n\r\n(.*?)\r\n--', body, re.DOTALL)
            data = fm.group(1) if fm else b""
            self.execd_files[target] = data
            return httpx.Response(201, json={"path": "/workspace/x", "size": len(data)})
        if path == "/v1/files" and method == "GET":
            data = self.execd_files.get(request.url.params.get("path", ""))
            if data is None:
                return err(404, "not_found", "file not found")
            return httpx.Response(200, content=data)
        if path == "/v1/tasks/t1" and method == "GET":
            return httpx.Response(200, json={"id": "t1", "status": "succeeded"})
        if path == "/v1/tasks/t1/cancel" and method == "POST":
            return httpx.Response(202, json={"id": "t1", "status": "canceled"})
        if path == "/v1/files/stat" and method == "GET":
            return err(403, "forbidden", "path outside workspace")
        return err(404, "not_found", "no route")


def transport_for(world: FakeWorld) -> httpx.MockTransport:
    return httpx.MockTransport(world.handler)
