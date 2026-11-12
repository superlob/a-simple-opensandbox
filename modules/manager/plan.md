# Manager 实施计划（Python + FastAPI + React/Vite）

依据：[manager-design.md](manager-design.md)、[runtime-design.md](../runtime/runtime-design.md)、[runtime/plan.md](../runtime/plan.md)（Runtime 已完成，其 HTTP 接口以 `runtime/internal/api/router.go` 与 `handlers.go` 为准）、[execd-design.md](../execd/execd-design.md)（Execd 接口以 `execd/internal/api/api.go` 为准）。

## 0. 约定与决策

### 0.1 默认假设（若有异议请在开发前指出）

| 事项 | 默认方案 |
|---|---|
| 源码位置 | 仓库根目录 `manager/`，Python 包名 `sandbox_manager`，依赖与虚拟环境由 `uv` 管理（`pyproject.toml` + `uv.lock`） |
| Python 版本 | 3.13（与 Sandbox 基础镜像一致） |
| Web 界面 | 仓库根目录 `manager/web/`，React + Vite + TypeScript；构建产物由 FastAPI 以静态文件方式托管，同源访问，避免 CORS |
| 状态来源 | Manager **不建数据库**。Sandbox 事实来源为 Runtime（Docker Labels）；Manager 只做请求编排与短期缓存 |
| Endpoint 缓存 | 按 Sandbox ID 缓存 Runtime 返回的 Execd Endpoint；Execd 请求失败（连接错误）或 Sandbox 启停后主动失效，并重新向 Runtime 获取 |
| 与 Runtime 通信 | `httpx.AsyncClient`，设置连接/读取超时；Runtime 地址由配置提供 |
| 与 Execd 通信 | Manager 直接调用 Endpoint，文件与命令请求不经 Runtime 转发（与 CLAUDE.md 架构约定一致）；代理层仅做参数转换与错误映射 |
| 认证 | **不实现认证**（用户已确认，2026-11-11）。与 Runtime、Execd 保持一致，访问范围通过部署网络和端口发布范围限制 |
| 错误格式 | 统一 `{"error":{"code","message"}}`，与 Runtime、Execd 一致；Runtime/Execd 的错误码原样透传并映射为 HTTP 状态码 |
| MCP | 使用官方 Python MCP SDK，以 Streamable HTTP 挂载在 `/mcp`；MCP Tool 与 HTTP 路由调用同一套 service 层，不重复实现业务逻辑 |
| 日志 | 标准库 `logging`，JSON 输出到 stdout；不记录请求体中的 env 值与文件内容 |
| 配置 | `pydantic-settings` 从环境变量与 `SANDBOX.env` 读取；缺失必填项启动失败，不设猜测默认值 |

### 0.2 已确认的环境约束

`SANDBOX.env` 中当前已有：`RUNTIME_BIND_HOST`、`RUNTIME_PORT`、`RUNTIME_PUBLIC_HOST`、`EXECD_PORT`。

用户已在 `SANDBOX.env` 中补充 Manager 配置（2026-11-11），变量名如下，取值不在文档中记录：

| 变量 | 用途 |
|---|---|
| `MANAGER_BIND_HOST` | Manager 监听地址 |
| `MANAGER_PORT` | Manager 监听端口 |
| `MANAGER_RUNTIME_URL` | Manager 访问 Runtime 的基础 URL |

## 1. 目录结构（规划）

```
manager/
├── pyproject.toml                  # 依赖、ruff/pytest 配置（uv 管理）
├── uv.lock
├── Makefile                        # make dev / test / lint / build-web / integration
├── .gitignore
├── src/sandbox_manager/
│   ├── __init__.py
│   ├── main.py                     # 入口：加载配置、创建 app、uvicorn 启动
│   ├── config.py                   # Settings（pydantic-settings）
│   ├── errors.py                   # 统一错误类型与 HTTP 映射
│   ├── clients/
│   │   ├── runtime.py              # Runtime HTTP 客户端（Sandbox 管理）
│   │   └── execd.py                # Execd HTTP 客户端（命令、文件、任务）
│   ├── services/
│   │   ├── sandbox.py              # Sandbox 管理编排（创建/启停/删除/查询/统计）
│   │   ├── execution.py            # 命令执行与任务管理编排（经 Endpoint 调 Execd）
│   │   └── endpoint_cache.py       # Endpoint 缓存与失效
│   ├── api/
│   │   ├── router.py               # /api/v1 路由注册
│   │   ├── sandboxes.py            # Sandbox 管理接口
│   │   ├── execution.py            # 命令/文件/任务接口
│   │   └── schemas.py              # 请求/响应模型（Pydantic）
│   ├── mcp/
│   │   └── server.py               # MCP Tools 定义，调用 services 层
│   └── web/                        # 构建后的前端静态文件（由 build-web 生成，不入库）
├── web/                            # React + Vite 源码
│   ├── package.json
│   ├── vite.config.ts
│   └── src/
│       ├── api/                    # 调用 Manager HTTP API 的薄封装
│       ├── pages/                  # Sandbox 列表、详情、执行面板
│       └── components/
└── tests/
    ├── unit/                       # 使用 httpx MockTransport / fake client，不需要真实服务
    ├── api/                        # FastAPI TestClient，覆盖路由与错误映射
    ├── mcp/                        # MCP Tools 调用测试
    ├── web/                        # 前端单元测试（Vitest + Testing Library）
    └── integration/                # 标记 integration，需要真实 Runtime 与 Docker
```

依赖方向：`api, mcp → services → clients`；`clients` 不依赖上层；`web` 仅通过 HTTP API 访问 Manager。

## 2. 接口设计草案

### 2.1 Manager 对外 HTTP API（前缀 `/api/v1`）

**Sandbox 管理（封装 Runtime）**

| 方法 | 路径 | 说明 | 对应 Runtime |
|---|---|---|---|
| POST | `/sandboxes` | 创建（支持 `Idempotency-Key` 透传） | `POST /v1/sandboxes` |
| GET | `/sandboxes` | 列表，支持 `status` 过滤 | `GET /v1/sandboxes` |
| GET | `/sandboxes/{id}` | 详情（基本信息、状态、健康、资源限制） | `GET /v1/sandboxes/{id}` |
| POST | `/sandboxes/{id}/start` | 启动 | `POST .../start` |
| POST | `/sandboxes/{id}/stop` | 停止 | `POST .../stop` |
| POST | `/sandboxes/{id}/restart` | 重启（组合 stop + start，见 §2.3） | 组合调用 |
| DELETE | `/sandboxes/{id}` | 删除 | `DELETE /v1/sandboxes/{id}` |
| GET | `/sandboxes/{id}/stats` | 资源统计（CPU、内存、PIDs） | `GET .../stats` |
| GET | `/sandboxes/{id}/endpoint` | 返回 Execd Endpoint | `GET .../endpoint`（结果写入缓存） |

**命令执行与任务（封装 Execd，经 Endpoint 直接访问）**

| 方法 | 路径 | 说明 | 对应 Execd |
|---|---|---|---|
| POST | `/sandboxes/{id}/command` | 前台执行或后台任务（`background` 字段） | `POST /v1/command` |
| GET | `/sandboxes/{id}/tasks` | 任务列表 | `GET /v1/tasks` |
| GET | `/sandboxes/{id}/tasks/{task_id}` | 任务状态 | `GET /v1/tasks/:id` |
| GET | `/sandboxes/{id}/tasks/{task_id}/logs` | 任务日志（透传分页参数） | `GET /v1/tasks/:id/logs` |
| POST | `/sandboxes/{id}/tasks/{task_id}/cancel` | 取消任务 | `POST /v1/tasks/:id/cancel` |
| GET | `/sandboxes/{id}/info` | Execd 运行信息 | `GET /v1/info` |

**文件操作（封装 Execd）**

| 方法 | 路径 | 说明 | 对应 Execd |
|---|---|---|---|
| POST | `/sandboxes/{id}/files` | 上传 | `POST /v1/files` |
| GET | `/sandboxes/{id}/files` | 下载 | `GET /v1/files` |
| GET | `/sandboxes/{id}/files/stat` | 文件信息 | `GET /v1/files/stat` |
| DELETE | `/sandboxes/{id}/files` | 删除 | `DELETE /v1/files` |
| POST | `/sandboxes/{id}/dirs` | 创建目录 | `POST /v1/dirs` |
| GET | `/sandboxes/{id}/dirs` | 列目录 | `GET /v1/dirs` |

**其他**

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/health` | Manager 自身健康检查（不检查下游，下游状态单独展示） |
| GET | `/` 及静态资源 | Web 管理界面 |
| — | `/mcp` | MCP Streamable HTTP 端点 |

### 2.2 错误映射

- Runtime 错误码（`invalid_argument`、`not_found`、`conflict`、`resource_exhausted`、`timeout`、`image_unavailable`、`docker_unavailable`、`start_failed`、`internal`）按 Runtime 文档透传为相同 `code`，HTTP 状态码保持一致。
- Execd 错误码按其返回透传。
- Runtime 或 Execd 不可达：返回 `503 upstream_unavailable`，并在日志中记录上游地址与错误类型。
- Endpoint 不可用（Sandbox 已停止）：返回 `409 conflict`，message 说明需先启动。
- Manager 内部异常：`500 internal`，不返回堆栈与内部细节。

### 2.3 重启语义

`restart` 为 Manager 编排：先 `stop`，再 `start`。任一步失败即返回错误并保留当前状态，不做自动回滚；失效 Endpoint 缓存。若 Runtime 后续提供原生重启接口，再替换实现。

### 2.4 MCP Tools（与 HTTP 能力对齐）

- `sandbox_create`、`sandbox_list`、`sandbox_get`、`sandbox_start`、`sandbox_stop`、`sandbox_restart`、`sandbox_delete`、`sandbox_stats`
- `sandbox_exec`（前台）、`sandbox_exec_background`、`task_get`、`task_logs`、`task_cancel`
- `sandbox_file_upload`、`sandbox_file_download`、`sandbox_file_delete`、`sandbox_dir_list`、`sandbox_dir_create`

每个 Tool 的输入输出 schema 与 HTTP 请求/响应模型共用同一 Pydantic 定义。

## 3. 实施阶段

### 阶段 M0：项目骨架与配置

- `uv init`、`pyproject.toml`（FastAPI、uvicorn、httpx、pydantic-settings、mcp；开发依赖：pytest、pytest-asyncio、ruff、mypy）。
- `Makefile`：`dev`、`test`、`lint`、`build-web`、`integration`。
- `config.py`：读取 `MANAGER_BIND_HOST`、`MANAGER_PORT`、`MANAGER_RUNTIME_URL`，缺失即失败；超时与缓存 TTL 可选，有默认值并在文档中说明。
- `SANDBOX.env` 已包含三个 Manager 变量（§0.2），集成验证可直接使用；单元测试不依赖这些值。
- 验证：`uv run pytest`、`ruff`、`mypy` 通过；缺少必填配置时启动失败且错误信息指明变量名。

### 阶段 M1：错误模型与上游客户端

- `errors.py`：`ManagerError`（code、http_status、message）与统一异常处理器。
- `clients/runtime.py`：封装 Runtime 全部接口，解析统一错误格式；`Idempotency-Key` 透传。
- `clients/execd.py`：封装 Execd 接口；文件上传使用 multipart，下载返回流式响应，避免整文件载入内存。
- 验证（`httpx.MockTransport`）：正常响应解析；Runtime 各错误码透传；连接错误与超时映射为 `upstream_unavailable`；Execd 下载大文件时不整体缓冲。

### 阶段 M2：Endpoint 缓存与 Sandbox 服务

- `endpoint_cache.py`：按 ID 缓存 Endpoint，支持失效、TTL、并发去重（同一 ID 同时取值只请求一次）。
- `services/sandbox.py`：创建、列表、详情、启停、重启、删除、统计。启动、停止、删除后主动失效缓存。
- 验证（fake Runtime）：缓存命中与失效；restart 失败不回滚并返回原始错误；删除后缓存已清空；并发取 Endpoint 只发起一次请求。

### 阶段 M3：命令、文件与任务服务

- `services/execution.py`：通过 Endpoint 调用 Execd；Endpoint 不可用时返回 `409`；Execd 连接失败时失效缓存并重试一次（仅对幂等的 GET 请求，命令执行不自动重试）。
- 参数校验：`argv` 非空；`timeout` 格式由 Execd 校验，Manager 不重复解析；文件路径不在 Manager 做路径规范化（由 Execd 负责边界检查）。
- 验证：前台/后台命令参数正确透传；命令不自动重试；任务取消与日志分页透传；文件上传下载二进制内容往返一致（使用 fake Execd）。

### 阶段 M4：HTTP API 层

- `api/schemas.py`：请求/响应模型；拒绝未知字段（与 Runtime 行为一致）。
- `api/sandboxes.py`、`api/execution.py`：路由实现，仅做参数转换，业务逻辑在 services 层。
- 请求体大小限制（文件上传除外，文件上传另设上限，可配置）。
- 请求日志中间件：method、path、status、耗时、sandbox_id；不记录请求体。
- 验证（FastAPI TestClient + fake 上游）：全部路由的成功路径；错误码映射表逐项覆盖；未知字段返回 400；日志不包含 env 值与文件内容。

### 阶段 M5：MCP 接口

- `mcp/server.py`：按 §2.4 注册 Tools，调用 services 层；工具描述写明参数含义与副作用（尤其是删除、取消）。
- 挂载到 FastAPI 的 `/mcp`，生命周期与应用一致。
- 验证：通过 MCP 客户端会话列出 Tools；调用创建、执行、删除的完整流程（fake 上游）；错误以 MCP 工具错误返回而非协议崩溃。

### 阶段 M6：Web 管理界面（React + Vite）

- 页面：
  1. Sandbox 列表：ID、状态、健康、创建时间、Endpoint 是否可用；按状态过滤；自动刷新（可关闭）。
  2. Sandbox 详情：基本信息、资源限制、实时统计（CPU、内存、PIDs），运行中才请求统计。
  3. 生命周期操作：创建（表单：CPU、内存、PIDs、环境变量）、启动、停止、重启、删除；删除需二次确认。
  4. 执行面板：前台命令输出；后台任务列表、日志查看、取消。
  5. 文件面板：目录浏览、上传、下载、删除。
- 状态展示：加载、空、错误、Sandbox 停止等状态均有明确提示；上游不可用时显示原因而非空白。
- `make build-web` 产物输出到 `src/sandbox_manager/web/`，由 FastAPI `StaticFiles` 托管。
- 验证：Vitest 单元测试覆盖列表过滤、操作按钮的启用条件（如停止状态不可执行命令）、错误展示；手动在浏览器中完成一次创建→执行→删除流程并记录结果。

### 阶段 M7：集成验证

在真实 Runtime 与 Docker 上验证（`make integration`，需 `SANDBOX.env` 配置完整）：

1. Manager 启动，`/health` 返回 200。
2. 经 Manager 创建 Sandbox（带环境变量），幂等重复提交返回同一 ID。
3. 前台命令：默认工作目录 `/workspace`，请求 env 生效，退出码正确。
4. 后台任务：提交、查询状态、读取日志、取消，状态与 Execd 一致。
5. 文件：上传 64 KiB 随机文件，下载后 sha256 一致；路径越界返回 Execd 的 403。
6. 停止后 `endpoint` 与命令接口返回 `409`；启动后 Endpoint 端口刷新，文件保留。
7. 重启 Manager 后，Sandbox 仍可经 Manager 列出与访问（Manager 无状态）。
8. Runtime 不可达时，Manager 返回 `503 upstream_unavailable`，恢复后自动可用。
9. 删除后详情返回 404，容器已移除，无残留容器。
10. MCP：通过 MCP 客户端完成 2–9 中的核心流程。
11. Web：浏览器完成列表、创建、执行、删除。

测试结束后清理所有本次创建的 Sandbox，并确认无残留容器。

## 4. 风险与待决策项

| # | 事项 | 说明 | 建议 |
|---|---|---|---|
| Q1 | Manager 配置 | **已解决**：`SANDBOX.env` 已包含 `MANAGER_BIND_HOST`、`MANAGER_PORT`、`MANAGER_RUNTIME_URL` | — |
| Q2 | Manager 认证 | **已解决**：用户确认不认证（2026-11-11）。风险仍然存在：Manager 绑定到非本机地址时会暴露删除与命令执行能力，部署时需确保 `MANAGER_BIND_HOST` 与网络范围受控 | 文档与部署说明中明确该限制 |
| Q3 | Web 界面是否需要用户体系 | 设计未提及 | 默认无用户体系 |
| R1 | 重启不是原子操作 | stop 成功而 start 失败时 Sandbox 停留在已停止状态 | 返回明确错误，由用户重试；不做自动回滚 |
| R2 | Endpoint 缓存陈旧 | Runtime 重启或端口变化时缓存可能失效 | 连接失败即失效并重取；TTL 兜底 |
| R3 | 大文件与流式传输 | 上传下载经 Manager 中转会增加内存与延迟 | 全部流式处理；如影响明显，再评估前端直连 Endpoint（需与设计协调） |
| R4 | 后台任务日志量 | 日志接口可能返回大量内容 | 透传分页参数，前端默认只取最近一页 |
| R5 | 设计文档未定义 Manager 数据持久化 | 当前不存元数据，Manager 的 UI 偏好或审计记录无处保存 | 本阶段不做；需要时再增加设计 |

## 5. 测试策略总览

| 层 | 工具 | 是否需要真实服务 |
|---|---|---|
| 单元（clients、services、缓存） | pytest、httpx.MockTransport | 否 |
| API 层 | FastAPI TestClient + fake 上游 | 否 |
| MCP | MCP 客户端会话 + fake 上游 | 否 |
| 前端 | Vitest + Testing Library | 否 |
| 集成 | pytest（`-m integration`）+ 浏览器验证 | 是（Runtime、Docker、Execd 镜像） |

每个阶段结束时运行该阶段的测试，并在 `PROGRESS.md` 中记录结果。

## 6. 开发顺序与进度记录

- 开发顺序：M0 → M1 → M2 → M3 → M4 → M5 → M6 → M7。M5 与 M6 可在 M4 完成后并行。
- 每完成一个阶段，更新 `/PROGRESS.md` 中 Manager 小节：已完成内容、验证结果、待办。
- 模块全部验证完成后停止，不自动开始下一个模块。
