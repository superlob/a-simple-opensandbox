"""请求模型。未知字段一律拒绝（与 Runtime 的行为一致）。"""

from __future__ import annotations

from typing import Literal

from pydantic import BaseModel, ConfigDict, Field


class Resources(BaseModel):
    model_config = ConfigDict(extra="forbid")

    cpus: float | None = Field(default=None, gt=0)
    memory_mb: int | None = Field(default=None, gt=0)
    pids_limit: int | None = Field(default=None, gt=0)


class CreateSandboxRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")

    resources: Resources | None = None
    env: dict[str, str] | None = None
    timeout_seconds: int | None = Field(default=None, ge=0)


class CommandRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")

    argv: list[str] = Field(min_length=1)
    cwd: str | None = None
    env: dict[str, str] | None = None
    timeout: str | None = None
    background: bool = False


class MkdirRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")

    path: str = Field(min_length=1)


StreamName = Literal["stdout", "stderr"]
