from __future__ import annotations

from sandbox_manager.mcp.server import build_mcp


async def test_mcp_tools_registered() -> None:
    mcp = build_mcp(lambda: None)  # type: ignore[arg-type,return-value]
    names = {t.name for t in await mcp.list_tools()}
    expected = {
        "sandbox_create", "sandbox_list", "sandbox_get", "sandbox_start", "sandbox_stop",
        "sandbox_restart", "sandbox_delete", "sandbox_stats", "sandbox_exec",
        "sandbox_exec_background", "task_get", "task_logs", "task_cancel",
        "sandbox_file_upload", "sandbox_file_download", "sandbox_file_delete",
        "sandbox_dir_list", "sandbox_dir_create",
    }
    assert expected <= names
