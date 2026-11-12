"""MCP Tools：与 HTTP 接口共用 services 层，不重复实现业务逻辑。"""

from __future__ import annotations

import base64
from collections.abc import Callable
from typing import Any

from mcp.server.mcpserver import MCPServer

from sandbox_manager.deps import Services


def build_mcp(get_services: Callable[[], Services]) -> MCPServer:
    mcp = MCPServer(
        "sandbox-manager",
        instructions=(
            "管理 Sandbox（创建、启停、删除）并在其中执行命令、读写文件。"
            "删除 Sandbox 与取消任务不可撤销。"
        ),
    )

    def svc() -> Services:
        return get_services()

    # ---- Sandbox 管理 ----

    @mcp.tool(description="创建 Sandbox。resources 可选：cpus、memory_mb、pids_limit；env 为环境变量。")
    async def sandbox_create(
        resources: dict[str, Any] | None = None,
        env: dict[str, str] | None = None,
        timeout_seconds: int | None = None,
    ) -> dict[str, Any]:
        body = {k: v for k, v in {"resources": resources, "env": env,
                                   "timeout_seconds": timeout_seconds}.items() if v is not None}
        return await svc().sandboxes.create(body, None)

    @mcp.tool(description="列出 Sandbox，可按 status 过滤（ready、stopped 等）。")
    async def sandbox_list(status: str | None = None) -> dict[str, Any]:
        return {"items": await svc().sandboxes.list(status)}

    @mcp.tool(description="获取 Sandbox 详情。")
    async def sandbox_get(sandbox_id: str) -> dict[str, Any]:
        return await svc().sandboxes.get(sandbox_id)

    @mcp.tool(description="启动已停止的 Sandbox。")
    async def sandbox_start(sandbox_id: str) -> dict[str, Any]:
        return await svc().sandboxes.start(sandbox_id)

    @mcp.tool(description="停止 Sandbox（工作区保留）。")
    async def sandbox_stop(sandbox_id: str) -> dict[str, Any]:
        return await svc().sandboxes.stop(sandbox_id)

    @mcp.tool(description="重启 Sandbox（先停止再启动）。")
    async def sandbox_restart(sandbox_id: str) -> dict[str, Any]:
        return await svc().sandboxes.restart(sandbox_id)

    @mcp.tool(description="删除 Sandbox 及其全部文件，不可撤销。")
    async def sandbox_delete(sandbox_id: str) -> dict[str, str]:
        await svc().sandboxes.delete(sandbox_id)
        return {"status": "deleted", "sandbox_id": sandbox_id}

    @mcp.tool(description="获取 Sandbox 的 CPU、内存与 PIDs 统计（需运行中）。")
    async def sandbox_stats(sandbox_id: str) -> dict[str, Any]:
        return await svc().sandboxes.stats(sandbox_id)

    # ---- 命令与任务 ----

    @mcp.tool(description="在 Sandbox 中前台执行命令（argv 形式），返回退出码与输出。")
    async def sandbox_exec(
        sandbox_id: str,
        argv: list[str],
        cwd: str | None = None,
        env: dict[str, str] | None = None,
        timeout: str | None = None,
    ) -> dict[str, Any]:
        body = {k: v for k, v in {"argv": argv, "cwd": cwd, "env": env,
                                   "timeout": timeout}.items() if v is not None}
        return await svc().execution.command(sandbox_id, body)

    @mcp.tool(description="在 Sandbox 中后台执行命令，返回 execution_id，可用 task_get 查询。")
    async def sandbox_exec_background(
        sandbox_id: str,
        argv: list[str],
        cwd: str | None = None,
        env: dict[str, str] | None = None,
        timeout: str | None = None,
    ) -> dict[str, Any]:
        body = {k: v for k, v in {"argv": argv, "cwd": cwd, "env": env, "timeout": timeout,
                                   "background": True}.items() if v is not None}
        return await svc().execution.command(sandbox_id, body)

    @mcp.tool(description="查询后台任务状态。")
    async def task_get(sandbox_id: str, task_id: str) -> dict[str, Any]:
        return await svc().execution.task(sandbox_id, task_id)

    @mcp.tool(description="读取任务日志。stream 为 stdout 或 stderr；offset 与 limit 用于分页。")
    async def task_logs(
        sandbox_id: str,
        task_id: str,
        stream: str = "stdout",
        offset: int = 0,
        limit: int | None = None,
    ) -> dict[str, Any]:
        return await svc().execution.task_logs(sandbox_id, task_id, stream, offset, limit)

    @mcp.tool(description="取消后台任务，不可撤销。")
    async def task_cancel(sandbox_id: str, task_id: str) -> dict[str, Any]:
        return await svc().execution.cancel_task(sandbox_id, task_id)

    # ---- 文件 ----

    @mcp.tool(description="上传文件。content_base64 为文件内容的 base64 编码；path 为 Sandbox 内目标路径。")
    async def sandbox_file_upload(
        sandbox_id: str, path: str, content_base64: str
    ) -> dict[str, Any]:
        data = base64.b64decode(content_base64, validate=True)
        limit = svc().settings.upload_max_bytes
        if len(data) > limit:
            from sandbox_manager.errors import ManagerError

            raise ManagerError("payload_too_large", f"file exceeds {limit} bytes")
        name = path.rsplit("/", 1)[-1] or "upload"
        return await svc().execution.upload(sandbox_id, path, name, data)

    @mcp.tool(description="下载文件，返回 base64 内容（仅适合小文件）。")
    async def sandbox_file_download(sandbox_id: str, path: str) -> dict[str, Any]:
        chunks = await svc().execution.download(sandbox_id, path)
        buf = bytearray()
        async for chunk in chunks:
            buf.extend(chunk)
        return {"path": path, "size": len(buf),
                "content_base64": base64.b64encode(bytes(buf)).decode("ascii")}

    @mcp.tool(description="删除文件或目录。recursive=true 时递归删除目录，不可撤销。")
    async def sandbox_file_delete(
        sandbox_id: str, path: str, recursive: bool = False
    ) -> dict[str, str]:
        await svc().execution.delete_file(sandbox_id, path, recursive)
        return {"status": "deleted", "path": path}

    @mcp.tool(description="列出目录内容。")
    async def sandbox_dir_list(
        sandbox_id: str, path: str, limit: int | None = None
    ) -> dict[str, Any]:
        return await svc().execution.list_dir(sandbox_id, path, limit)

    @mcp.tool(description="创建目录（含父目录）。")
    async def sandbox_dir_create(sandbox_id: str, path: str) -> dict[str, Any]:
        return await svc().execution.mkdir(sandbox_id, path)

    return mcp
