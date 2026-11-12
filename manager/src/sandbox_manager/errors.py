"""统一错误模型。错误格式与 Runtime、Execd 一致：{"error": {"code", "message"}}。"""

from __future__ import annotations

from typing import Any

import httpx

# 错误码 -> HTTP 状态码。Runtime/Execd 的错误码按原样透传。
STATUS_BY_CODE: dict[str, int] = {
    "invalid_argument": 400,
    "forbidden": 403,
    "not_found": 404,
    "conflict": 409,
    "payload_too_large": 413,
    "resource_exhausted": 429,
    "internal": 500,
    "start_failed": 500,
    "image_unavailable": 503,
    "docker_unavailable": 503,
    "upstream_unavailable": 503,
    "timeout": 504,
}


class ManagerError(Exception):
    def __init__(self, code: str, message: str, status: int | None = None) -> None:
        super().__init__(message)
        self.code = code
        self.message = message
        self.status = status if status is not None else STATUS_BY_CODE.get(code, 500)

    def to_body(self) -> dict[str, Any]:
        return {"error": {"code": self.code, "message": self.message}}


def from_response(resp: httpx.Response) -> ManagerError:
    """把上游（Runtime/Execd）的错误响应转换为 ManagerError，保留上游错误码。"""
    code = "internal"
    message = f"upstream returned HTTP {resp.status_code}"
    try:
        body = resp.json()
        err = body.get("error", {})
        if isinstance(err, dict):
            code = str(err.get("code") or code)
            message = str(err.get("message") or message)
    except ValueError:
        pass
    if resp.status_code >= 500 and code == "internal":
        # 上游 500 不暴露细节。
        message = "upstream internal error"
    return ManagerError(code, message, status=resp.status_code if code not in STATUS_BY_CODE else None)


def upstream_unavailable(exc: Exception) -> ManagerError:
    return ManagerError("upstream_unavailable", f"upstream unreachable: {type(exc).__name__}")
