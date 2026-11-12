# Runtime 实施计划（Go + Docker Go SDK）

依据：[runtime-design.md](runtime-design.md)、[execd-design.md](../execd/execd-design.md)、[execd/plan.md](../execd/plan.md)（Execd 已完成，其配置格式、健康检查命令与容器约定以已实现代码为准）。

## 0. 约定与决策

### 0.1 已确认决策

| # | 事项 | 决定 |
|---|---|---|
| D1 | Endpoint 对外地址与端口 | 从 `SANDBOX.env` 读取，已提供 `RUNTIME_BIND_HOST`、`RUNTIME_PORT`、`RUNTIME_PUBLIC_HOST`。代码中不写死任何地址或端口。Execd 在容器内的监听端口尚未在 `SANDBOX.env` 中定义，需要确定（见 §4） |
| D2 | Endpoint 认证 | **不实现认证**。Execd 端点与 Runtime API 均无 token，`GET /v1/sandboxes/{id}/endpoint` 只返回地址。runtime-design §3 中"端点基本身份认证"一条与此相悖，需在 runtime-design 中同步修订（见 §4） |
| D3 | `/workspace` 持久化 | **直接存放在容器可写层**，不使用命名卷。停止/启动后保留，删除容器时随之清理。不提供 `purge_volume` 参数 |
| D4 | 创建请求中的镜像 | **不接受 `image` 参数**。镜像固定由 Runtime 配置（基础镜像名与标签固定），创建请求不可覆盖 |
| D5 | 基础镜像版本 | Python **3.13**，uv **0.13.7**，在 `images/sandbox-base/Dockerfile` 中固定版本 |

### 0.2 默认假设（若有异议请在开发前指出）

| 事项 | 默认方案 |
|---|---|
| 源码位置 | 仓库根目录 `runtime/`，独立 `go.mod`，模块名 `sandbox/runtime` |
| Sandbox ID | UUID v4，容器名 `sandbox-<id>` |
| 管理标签 | `sandbox.managed=true`、`sandbox.id=<id>`、`sandbox.created_at=<RFC3339>`、`sandbox.image=<固定镜像引用>`、`sandbox.runtime_version=<v>` |
| Execd 配置注入 | 创建容器后、启动前，通过 Docker `CopyToContainer` 写入 `/etc/execd/config.json`，环境变量 `EXECD_CONFIG=/etc/execd/config.json`；不挂载宿主机文件 |
| 容器默认环境 | `Config.Env` 设置 `PATH=/workspace/.venv/bin:...`（与 execd Dockerfile 一致） |
| 容器安全基线 | 非特权；`CapDrop: ALL`；`SecurityOpt: no-new-privileges`；`PidsLimit`；CPU/内存限制；不挂载 Docker Socket；不使用 `Privileged`；不挂载宿主机路径 |
| Execd 进程 | 镜像 `ENTRYPOINT` 为 execd（PID 1，exec 形式）；Runtime 不覆盖 entrypoint |
| 健康检查 | 使用镜像内置 `HEALTHCHECK` 调用 `execd healthcheck`；Runtime 读取 `State.Health.Status` |
| 停止超时 | `docker stop` 的 timeout 大于 Execd `shutdown_grace`（默认 15s，Execd 为 10s） |
| 资源默认值 | CPU 1 核、内存 1 GiB、PIDs 512；创建请求可覆盖，但不得超过 Runtime 配置的上限 |
| 资源统计 | Docker `ContainerStatsOneShot` |
| 元数据来源 | Runtime 不另建数据库；容器 Labels 为事实来源，启动时据此重新发现 Sandbox |
| 日志 | 标准库 `log/slog`，JSON 输出到 stdout |
| 幂等 | 创建接口支持可选客户端 ID（`Idempotency-Key` 头），重复提交返回已有记录；start/stop/delete 对已处于目标状态的 Sandbox 直接返回当前状态 |

## 1. 目录结构与接口草案

### 1.1 目录结构（规划）

```
runtime/
├── go.mod
├── cmd/runtime/main.go            # 入口：加载配置、初始化 Docker 客户端、启动 HTTP 服务、优雅退出
├── internal/
│   ├── config/                    # Runtime 配置（监听地址、固定镜像、默认资源上限、过期策略）
│   ├── docker/                    # Docker Go SDK 封装（唯一直接调用 SDK 的包，便于测试替换）
│   ├── sandbox/                   # Sandbox 领域逻辑：创建、启动、停止、删除、状态聚合、幂等
│   ├── execdcfg/                  # 生成 Execd 配置 JSON（与 execd/internal/config 字段对齐）
│   ├── endpoint/                  # Sandbox ID → Execd URL 映射
│   ├── discovery/                 # 基于 Labels 的启动恢复
│   ├── reaper/                    # 过期/超量 Sandbox 清理策略
│   ├── api/                       # HTTP API（与 execd 保持一致优先 Gin）
│   └── errors/                    # 统一错误码与 HTTP 映射
├── images/sandbox-base/Dockerfile # 固定基础镜像（Python 3.13 + uv 0.13.7 + execd + HEALTHCHECK）
├── Makefile
└── test/integration/              # build tag `integration`，需要真实 Docker Daemon
```

依赖方向：`api → sandbox → docker, execdcfg, endpoint`；`discovery`、`reaper` 仅依赖 `sandbox`；`docker` 不依赖上层包。

### 1.2 Sandbox 状态模型

Runtime 只维护容器级状态，不维护 Execd 内部任务状态。对外状态由 Docker 状态与健康状态聚合得出：

| 对外状态 | 判定 |
|---|---|
| `creating` | 容器已创建但尚未完成首次启动或健康检查 |
| `starting` | Docker `running`，健康状态 `starting` |
| `ready` | Docker `running`，健康状态 `healthy` |
| `unhealthy` | Docker `running`，健康状态 `unhealthy` |
| `stopped` | Docker `exited` / `created`（已停止） |
| `deleting` | 删除进行中（容器标签中记录删除意图，重启后可继续） |
| `missing` | 不实现（见 R7）：列表以 Docker 容器为准，不存在该状态 |

### 1.3 Runtime HTTP API 草案（待 Manager 设计确认）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/health` | Runtime 自身健康 |
| POST | `/v1/sandboxes` | 创建并启动 Sandbox；请求含 `resources`（可选，不超过上限）、`env`（容器级默认环境，可选）、`timeout_seconds`（等待 ready 超时）。镜像由 Runtime 固定，不接受 `image` |
| GET | `/v1/sandboxes` | 列表，支持 `status` 过滤 |
| GET | `/v1/sandboxes/{id}` | 基本信息、状态、镜像、创建时间、资源限制 |
| POST | `/v1/sandboxes/{id}/start` | 启动已停止的 Sandbox，等待 ready |
| POST | `/v1/sandboxes/{id}/stop` | 停止（Docker SIGTERM → 超时强杀） |
| DELETE | `/v1/sandboxes/{id}` | 停止并删除容器（工作区随容器清理） |
| GET | `/v1/sandboxes/{id}/stats` | CPU、内存、PIDs 使用情况 |
| GET | `/v1/sandboxes/{id}/endpoint` | Execd Endpoint URL（无认证凭据） |

错误响应统一为 `{"error": {"code": "...", "message": "..."}}`，错误码包括 `not_found`、`conflict`、`invalid_argument`、`docker_unavailable`、`timeout`、`resource_exhausted`。

## 2. 实施阶段

### 阶段 R0：项目骨架

任务：
1. 创建 `runtime/`，初始化 `go.mod`，引入 `github.com/docker/docker`（Docker Go SDK）与 HTTP 框架依赖。
2. 创建目录骨架与 `cmd/runtime/main.go` 空启动流程。
3. `Makefile`：`build`、`test`、`test-race`、`vet`、`integration`。
4. `.gitignore` 忽略构建产物。

验证：`go build ./...`、`go vet ./...` 通过。

### 阶段 R1：Runtime 配置与基础镜像

任务：
1. `config`：监听地址、端口、Docker Host（默认本地 socket）、固定镜像引用、默认资源与上限、等待 ready 超时、过期策略（`max_sandboxes`、`sandbox_ttl`）。从文件与环境变量加载；`RUNTIME_BIND_HOST`、`RUNTIME_PORT`、`RUNTIME_PUBLIC_HOST` 仅从 `SANDBOX.env` 读取，代码中不设默认值。
2. `images/sandbox-base/Dockerfile`：
   - 基于 Python 3.13 基础镜像，安装 uv 0.13.7（版本固定）。
   - 复制 execd 二进制到 `/usr/local/bin/execd`，`ENTRYPOINT ["/usr/local/bin/execd"]`（exec 形式）。
   - 创建 `/workspace`、`/var/lib/execd`、`/etc/execd`；`WORKDIR /workspace`。
   - `ENV PATH=/workspace/.venv/bin:...`。
   - `HEALTHCHECK ... CMD ["/usr/local/bin/execd", "healthcheck"]`。

验证：
- 配置缺省值、非法端口、缺失镜像引用时报明确错误。
- `docker build` 成功，`docker run` 后 `HEALTHCHECK` 进入 `healthy`，`python --version` 为 3.13.x，`uv --version` 为 0.13.7。

### 阶段 R2：Docker 适配层（docker）

任务：
1. 封装 Docker Go SDK 客户端：`Ping`、`ImageInspect`、`ContainerCreate`、`ContainerStart`、`ContainerStop`、`ContainerRemove`、`ContainerInspect`、`ContainerList`（按 label）、`CopyToContainer`、`ContainerStatsOneShot`。
2. 统一超时（每个调用带 context）与错误分类（Daemon 不可用 / 不存在 / 冲突）。
3. 接口化（`DockerAPI` interface），便于单元测试使用 fake。

验证：
- 单元测试：使用 fake 验证错误分类与超时传递。
- Daemon 不可用时 `Ping` 返回 `docker_unavailable`，不 panic。

### 阶段 R3：Execd 配置生成与端点（execdcfg / endpoint）

任务：
1. `execdcfg`：生成 JSON，字段名与 `execd/internal/config` 的 `json` tag 完全一致（`listen_addr`、`port`、`workspace_dir`、`data_dir`、`max_concurrent_tasks`、`max_upload_bytes`、`max_output_bytes`、`default_timeout`、`max_timeout`、`shutdown_grace`、`task_retention`、`max_finished_tasks` 等）。时长字段使用 Go duration 字符串。
2. 容器内 Execd 端口取自 `SANDBOX.env` 的 `EXECD_PORT`，写入 `execdcfg` 的 `port` 字段。镜像不写 `EXPOSE`，由 Runtime 在创建容器时设置 `ExposedPorts`，避免构建参数与运行时配置不一致。Runtime 通过端口映射将其发布到宿主机动态端口。
3. `endpoint`：从容器 `NetworkSettings.Ports` 解析宿主机端口，结合 `SANDBOX.env` 中的对外 Host 生成 URL；容器停止时标记为不可用，启动后刷新（动态端口可能变化）。

验证（单元测试）：
- 生成配置的字段名与 execd 配置结构体一致（可通过让真实 Execd 读取配置做契约验证，放在集成测试中）。
- 端口映射解析：有/无映射、停止后端点不可用、重启后刷新。

### 阶段 R4：Sandbox 创建与初始化（sandbox.create）

任务：
1. 校验请求参数：资源不超过 Runtime 配置上限（超过返回 `invalid_argument`）、环境变量键名与数量。请求中出现 `image` 字段时拒绝（镜像固定）。
2. 创建顺序：
   1. 检查 `max_sandboxes`，超限返回 `resource_exhausted`。
   2. `ContainerCreate`：设置 Labels、`Config.Env`、`HostConfig`（资源限制、`CapDrop: ALL`、`SecurityOpt`、`PidsLimit`、`PortBindings`，`HostPort` 为空）、`StopTimeout`。`/workspace` 为容器可写层，不挂载卷。
   3. `CopyToContainer` 写入 `/etc/execd/config.json`。
   4. `ContainerStart`。
   5. 轮询 `State.Health` 直至 `healthy`，超时则删除容器并返回 `timeout`。
3. 失败回滚：任一步骤失败时删除已创建的容器，保证不留孤儿资源。
4. 幂等：若调用方提供客户端 ID 且已存在，返回已有记录（状态不变）。

验证：
- 创建后 `/workspace` 可写且位于容器可写层、`PATH` 生效、Execd 为 PID 1、`HEALTHCHECK` 为 healthy。
- 容器安全属性检查：`CapDrop` 为 ALL、`Privileged=false`、无 Docker Socket 挂载、无宿主机挂载、资源限制已设置。
- 健康检查超时后容器被回收，无残留资源。
- 重复提交同一客户端 ID 只创建一次。

### 阶段 R5：生命周期管理（sandbox.lifecycle）

任务：
1. **Start**：`ContainerStart` → 等待 `healthy` → 刷新 Endpoint 映射。对已运行的容器幂等返回。
2. **Stop**：`ContainerStop` 使用 `StopTimeout`；确认 Execd 以 SIGTERM 优雅退出（退出码为 0）。对已停止容器幂等返回。Stop 后 Endpoint 标记不可用。
3. **Delete**：若运行中则先 Stop → `ContainerRemove`（工作区随容器删除）。Docker 不允许修改已有容器的标签，因此不使用 `sandbox.deleting` 标签记录删除意图。删除中途失败时容器仍可见，调用方重试 DELETE 即可完成；删除不存在的 Sandbox 视为成功，保证重试安全。
4. **Restart 语义**：调用方需重启时按 stop → start 组合，不单独实现。

验证（集成测试，真实 Docker）：
- 创建 → stop → start → delete 全流程；每步状态与 Docker 实际状态一致。
- `/workspace` 中写入的文件在 stop/start 后保留；删除后容器不存在。
- 重复 stop/start/delete 幂等。
- Stop 后 Execd 退出码为 0，耗时不超过 `StopTimeout`。

### 阶段 R6：Runtime HTTP API（api）

任务：
1. 按 §1.3 实现路由、请求校验、统一错误格式。
2. 创建/启动等待 ready 使用请求级超时，可被客户端取消。
3. 请求体大小限制。
4. 日志中间件（slog），记录 Sandbox ID。

验证（httptest，Docker 使用 fake）：
- 各接口正常路径与错误码（404、409、400、429、503）。
- 携带 `image` 字段的创建请求被拒绝（400）。
- 幂等接口重复调用返回一致结果。

### 阶段 R7：状态查询与资源统计（sandbox.query）

任务：
1. 列表与详情：合并 Docker 容器状态、健康状态、Labels 中的元数据。
2. Stats：`ContainerStatsOneShot`，解析 CPU 百分比、内存用量/限制、PIDs；容器未运行时返回 `conflict`。
3. `missing` 状态不实现：列表直接来自 Docker 容器，没有独立的记录存储，因此不会出现“标签存在但容器缺失”的情况。容器被外部删除后，查询返回 not_found，列表中也不再出现。

验证：
- 状态映射（§1.2）单元测试覆盖。
- 运行中与停止状态下统计接口行为正确。

### 阶段 R8：启动恢复与过期回收（discovery / reaper）

任务：
1. Runtime 启动时按 `sandbox.managed=true` 标签扫描容器，重建内存索引；不依赖本地数据库。
2. 处理未完成操作：不自动续做删除（删除通过调用方重试完成，见 R5）。创建中途崩溃留下的 `created` 容器默认只报告；由配置 `RUNTIME_RECOVER_LEFTOVERS=true` 显式开启后才删除，因为删除范围是守护进程上所有托管容器。running 但未 healthy 的容器只报告。
3. 过期回收：超过 `sandbox_ttl` 的 Sandbox 删除；超过 `max_sandboxes` 时拒绝新建（不自动删除运行中的 Sandbox）。

验证：
- Runtime 进程重启后能重新发现已有 Sandbox 并正确报告状态。
- 模拟删除中断后重启可完成删除。
- 过期回收只作用于超时的 Sandbox，未过期的不受影响。

### 阶段 R9：安全与资源验证

任务：
1. 容器权限检查：非特权、Capabilities 已删除、`no-new-privileges`、PIDs 与内存限制生效（容器内 fork 压力在 PIDs 限制下被阻止，不影响宿主机）。
2. 确认容器无法访问 Docker Socket 与宿主机路径。
3. Runtime 对 Docker Daemon 不可用、容器异常退出、Runtime 重启等场景的行为验证。

验证：以集成测试实现，结果记录于 `PROGRESS.md`。

### 阶段 R10：端到端集成验证

任务：
1. 启动 Runtime 二进制，通过 HTTP 完成：创建 → 等待 ready → 通过 Endpoint 调用 Execd（上传文件、执行前台命令）→ stop → start → 再次调用 → delete。
2. 验证 Runtime 重启后 Sandbox 仍可管理。
3. 验证 Runtime 停止不影响已运行的 Sandbox（由 Docker 独立管理）。

验证：全流程通过；失败时在 `PROGRESS.md` 如实记录。

## 3. 测试策略总览

- 单元测试：`go test ./...`，覆盖 config、execdcfg、endpoint、状态映射、错误分类、幂等逻辑（Docker 使用 fake）。
- HTTP 测试：`httptest` 覆盖路由与错误码。
- 并发与竞争：`go test -race ./...`。
- 集成测试：build tag `integration`，需要真实 Docker Daemon，覆盖 R4、R5、R8、R9、R10。
- 设计文档 §5 的每项验证要求须在测试中有对应用例，并在 `PROGRESS.md` 中逐项记录结果。

## 4. 风险与待办

1. **Execd 容器内端口**：已在 `SANDBOX.env` 中提供 `EXECD_PORT`。R1 的镜像约定与 R3 的 `execdcfg` 生成值必须使用该变量，不得另行写死。
2. **设计文档已同步**（2026-11-10）：runtime-design.md 已按 D2（不认证）、D3（可写层持久化）、D4（固定镜像）、D5（Python 3.13 / uv 0.13.7）修订。
3. **Execd 配置契约**：Runtime 生成的 JSON 必须与 `execd/internal/config` 对齐。任一方修改字段时需同步；由 R3 的契约测试守护。
4. **动态端口变化**：容器重启后宿主机端口可能变化，Endpoint 必须在 start 后刷新，不可缓存旧值。
5. **容器可写层持久化的边界**：`/workspace` 与 `/var/lib/execd` 均随容器删除而清理；停止/启动保留。这依赖 `docker stop` 不删除容器，Runtime 不得在 stop 中调用 `docker rm`。
6. **容器端口约定**：Execd 容器内端口由 `SANDBOX.env` 的 `EXECD_PORT` 提供，`execdcfg` 与 Runtime 的 `ExposedPorts`/`PortBindings` 均须引用该值。

## 5. 开发顺序与进度记录

按 R0 → R10 顺序推进。每完成一个阶段，更新 [PROGRESS.md](../../PROGRESS.md) 中 Runtime 模块的状态、验证结果与待办事项。模块全部完成并验证后停止，不自动开始下一个模块。
