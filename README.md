# Docker Sandbox 平台

基于 Docker 的 Sandbox 管理与执行平台，为 Agent 和外部调用方提供隔离、可管理的命令执行环境。

## 架构

| 模块 | 目录 | 技术 | 职责 |
|---|---|---|---|
| Manager | `manager/` | Python 3.13、FastAPI、React + Vite、MCP | 统一入口：HTTP API、MCP Tools、Web 界面 |
| Runtime | `runtime/` | Go、Docker Go SDK、Gin | 管理 Sandbox 容器的创建、启停、删除、状态与资源统计 |
| Execd | `execd/` | Go、Gin | 运行于容器内（PID 1），提供文件、命令与后台任务接口 |

调用关系：外部调用方通过 Manager 获取 Execd Endpoint，之后直接访问 Execd；文件与命令请求不经过 Manager 或 Runtime 转发。

## 环境准备

- Docker（本机已验证 Server 27.3.1）
- Go（Runtime、Execd）
- Python 3.13 与 [uv](https://docs.astral.sh/uv/)（Manager）
- Node.js 与 npm（仅构建 Web 界面时需要）

配置统一放在仓库根目录的 `SANDBOX.env`（不入库，不要提交）。需要的变量：

| 变量 | 所属模块 | 说明 |
|---|---|---|
| `RUNTIME_BIND_HOST` / `RUNTIME_PORT` | Runtime | 监听地址与端口 |
| `RUNTIME_PUBLIC_HOST` | Runtime | 返回给调用方的 Endpoint 主机名 |
| `EXECD_PORT` | Runtime / Execd | Execd 在容器内的监听端口 |
| `MANAGER_BIND_HOST` / `MANAGER_PORT` | Manager | Manager 监听地址与端口 |
| `MANAGER_RUNTIME_URL` | Manager | Runtime 的基础 URL |

## 构建与启动

### 1. 基础镜像（Sandbox 容器镜像）

```bash
# 在仓库根目录构建（需要 execd/ 在构建上下文中）
docker build --build-arg GOPROXY=https://goproxy.io,direct \
  -f runtime/images/sandbox-base/Dockerfile -t sandbox-base:py3.13-uv0.13.7 .
```

镜像内置 Python 3.13、uv 0.13.7 与 Execd。镜像名由 Runtime 配置固定，创建请求不能指定镜像。

### 2. Runtime

```bash
cd runtime
make build
set -a; . ../SANDBOX.env; set +a
./bin/runtime
```

### 3. Manager

```bash
cd manager
uv sync
make build-web      # 可选：构建 Web 界面到 src/sandbox_manager/web/
make dev            # 读取 ../SANDBOX.env 后启动
```

Manager 启动后：Web 界面位于 `/`，HTTP API 位于 `/api/v1`，MCP 端点位于 `/mcp/`。

## 使用说明

### HTTP API（Manager）

```bash
# 创建（可选 Idempotency-Key 实现幂等；未知字段返回 400）
curl -X POST http://$MANAGER/api/v1/sandboxes \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: job-42' \
  -d '{"resources":{"cpus":1,"memory_mb":512,"pids_limit":256},"env":{"FOO":"bar"}}'

# 前台命令（默认工作目录 /workspace）
curl -X POST http://$MANAGER/api/v1/sandboxes/$ID/command \
  -H 'Content-Type: application/json' -d '{"argv":["python","-c","print(1)"]}'

# 后台任务（返回 202 与 execution_id）
curl -X POST http://$MANAGER/api/v1/sandboxes/$ID/command \
  -H 'Content-Type: application/json' -d '{"argv":["sleep","60"],"background":true}'

# 文件上传 / 下载
curl -X POST "http://$MANAGER/api/v1/sandboxes/$ID/files?path=/workspace/a.txt" -F file=@a.txt
curl "http://$MANAGER/api/v1/sandboxes/$ID/files?path=/workspace/a.txt" -o a.txt
```

主要接口：

| 方法 | 路径 | 说明 |
|---|---|---|
| POST / GET | `/api/v1/sandboxes` | 创建、列表（`?status=` 过滤） |
| GET / DELETE | `/api/v1/sandboxes/{id}` | 详情、删除 |
| POST | `.../{id}/start`、`.../stop`、`.../restart` | 生命周期 |
| GET | `.../{id}/stats`、`.../{id}/endpoint` | 资源统计、Execd Endpoint |
| POST | `.../{id}/command` | 前台（200）或后台（202）执行 |
| GET | `.../{id}/tasks`、`.../tasks/{tid}`、`.../tasks/{tid}/logs` | 任务查询与日志（`stream`、`offset`、`limit`） |
| POST | `.../tasks/{tid}/cancel` | 取消任务 |
| POST / GET / DELETE | `.../{id}/files`、`.../files/stat`、`.../dirs` | 文件与目录 |

错误统一为 `{"error":{"code","message"}}`，错误码与 Runtime、Execd 一致（如 `not_found`、`conflict`、`resource_exhausted`、`upstream_unavailable`）。

### MCP

Manager 在 `/mcp/` 提供 MCP（Streamable HTTP）服务，共 18 个 Tools，包括 `sandbox_create`、`sandbox_exec`、`sandbox_exec_background`、`task_logs`、`sandbox_file_upload`、`sandbox_file_download` 等。MCP 只接受绑定地址与本机回环地址的 Host 头。

### 直接访问 Execd

Manager 返回的 Endpoint 形如 `http://<RUNTIME_PUBLIC_HOST>:<动态端口>`，调用方可直接访问 Execd 的 `/v1/command`、`/v1/files`、`/v1/tasks` 等接口。Endpoint 在 Sandbox 停止后不可用，启动后端口可能变化，需重新获取。

## 安全说明

- **不做认证**：Runtime、Execd、Manager 均无认证，访问控制依赖部署网络与端口发布范围。Manager 绑定到非本机地址时，删除与命令执行能力会对网络暴露，部署时需控制 `MANAGER_BIND_HOST`。
- Sandbox 容器：非特权，`CapDrop=ALL`，`no-new-privileges`，无宿主机挂载，不挂载 Docker Socket，端口仅绑定到 `RUNTIME_BIND_HOST`。
- 容器内进程以 root 运行（未实现非 root）。
- Execd 限制文件操作在工作区内（越界返回 403）。

## 性能参数

以下为代码中的默认值与上限，来自各模块配置，可通过环境变量或配置文件修改。

**Runtime（Sandbox 资源）**

| 参数 | 默认值 | 上限 / 说明 |
|---|---|---|
| CPU | 1 核 | 最大 4 核 |
| 内存 | 1024 MiB | 最大 8192 MiB |
| PIDs | 512 | 最大 4096 |
| 就绪超时（ReadyTimeout） | 60 s | 创建或启动后等待 healthy |
| 停止超时（StopTimeout） | 15 s | 须大于 Execd 的 shutdown_grace（10 s） |
| 最大 Sandbox 数（MaxSandboxes） | 50 | 同时管理的容器数 |
| Sandbox TTL | 0（关闭） | 超过后自动回收 |

**Execd（容器内）**

| 参数 | 默认值 | 说明 |
|---|---|---|
| 最大并发执行数 | 8 | 前台与后台共用，超出返回 429 |
| 上传大小上限 | 100 MiB | 单个文件 |
| 非上传请求体上限 | 1 MiB | |
| 单流输出上限 | 1 MiB | stdout、stderr 各自计算 |
| 默认命令超时 | 60 s | 请求未指定时 |

**Manager**

| 参数 | 默认值 | 说明 |
|---|---|---|
| 上游连接超时 | 3 s | 访问 Runtime 与 Execd |
| 上游读取超时 | 60 s | |
| Endpoint 缓存 TTL | 30 s | 启停、删除后主动失效 |
| 上传大小上限 | 64 MiB | 在内存中读取，未做流式 |

**实测性能**：本阶段没有做压测或基准测试，上面的数字是配置值，不是测量结果。集成测试中一次完整流程（创建到删除）约 14 秒，其中包含镜像启动与健康检查等待。

## 测试与验证

### Runtime

- 单元测试与 `-race`：配置、Docker 适配层（httptest 模拟守护进程）、Sandbox 生命周期、恢复与回收、Endpoint 映射、HTTP API。全部通过。
- 集成测试（`make integration`，真实 Docker）：创建与初始化、Execd 契约（配置格式与容器内进程）、生命周期（停止→启动文件保留→删除）、安全基线（能力集为空、NoNewPrivs、挂载被拒、PIDs 与内存限制、容器崩溃后恢复）、守护进程不可用时的错误分类、重启后恢复。全部通过，测试无残留容器。
- 端到端脚本：启动真实 Runtime 二进制，完成 20 项检查（幂等创建、经端点访问 Execd、64 KiB 文件 sha256 往返、前台与后台命令、停止与启动端口刷新、Runtime 重启后沙箱仍可用、删除）。全部通过。

### Execd

- 单元测试与 `-race`：配置、安全（路径与符号链接）、文件操作、命令执行（超时杀进程组、输出限流、信号退出码）、任务存储与分页、任务生命周期、HTTP 路由与错误码。全部通过。
- 进程级集成测试：`kill -9` 后重启，运行中任务恢复为 `interrupted`；SIGTERM 时任务落盘为 `canceled`。
- 容器验证：健康检查为 healthy；文件上传下载；越界返回 403；`docker stop` 时后台任务在 0.2 秒内取消；PID 1 回收孤儿进程（zombie 数为 0）；并发上限返回 429；`docker kill` 后重启任务为 `interrupted`。

### Manager

- 单元、API、MCP 测试：24 项通过（覆盖上游错误码透传、Endpoint 缓存的并发与 TTL、重试策略、413/400/409 映射、MCP 工具注册与 HTTP 初始化、Host 白名单）。
- `ruff`、`mypy` 通过。
- 集成测试：真实 Runtime、Docker、Execd，覆盖创建、前台与后台命令、文件往返、越界 403、停止后 409、启动后文件保留、统计与删除。通过，无残留容器。
- Web 界面：`npm run build`（含 TypeScript 检查）通过。

## 已知缺口

- Manager Web 界面尚无文件面板、任务列表与日志页面，也没有 Vitest 测试。
- Execd 上传到已存在文件的子路径时返回 500（未映射 ENOTDIR）。
- Runtime 对同一守护进程上非本系统创建的托管容器会一并纳入恢复与回收范围，默认 TTL 关闭，遗留容器默认只报告不删除。
- 无认证，无非 root 运行，无性能基准数据。

## 开发文档

- 模块设计：`modules/<模块>/<模块>-design.md`
- 实施计划：`modules/<模块>/plan.md`
- 进度记录：`PROGRESS.md`
