"""Manager 配置。

所有环境相关值从环境变量读取（Makefile 会加载仓库根目录的 SANDBOX.env）。
必填项缺失时启动失败，不提供猜测的默认值。
"""

from __future__ import annotations

from pydantic import Field, HttpUrl
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(extra="ignore", case_sensitive=False)

    manager_bind_host: str = Field(description="Manager 监听地址")
    manager_port: int = Field(ge=1, le=65535, description="Manager 监听端口")
    manager_runtime_url: HttpUrl = Field(description="Runtime 基础 URL")

    # 以下为可选项，有默认值。
    upstream_connect_timeout: float = Field(default=3.0, gt=0)
    upstream_read_timeout: float = Field(default=60.0, gt=0)
    endpoint_cache_ttl: float = Field(default=30.0, gt=0, description="Endpoint 缓存 TTL（秒）")
    upload_max_bytes: int = Field(default=64 * 1024 * 1024, gt=0, description="文件上传上限")
