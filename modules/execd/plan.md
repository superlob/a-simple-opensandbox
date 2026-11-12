# Execd 实施计划（Go + Gin）

依据：[execd-design.md](execd-design.md)、[runtime-design.md](../runtime/runtime-design.md)（Runtime 对 Execd 的部署、启动配置、健康检查和访问端点约定）。

## 0. 约定与待确认假设

设计文档未明确的点，本计划先按以下默认值实现，若与预期不符请在开发前指出：

| 事项 | 默认方案 | 说明 |
|---|---|---|
| 源码位置 | 源码位于仓库根目录下的独立目录 `execd/`（与 `modules/` 分离）；`go.mod` 位于 `execd/`，模块名待定（建议 `sandbox/execd`） | `modules/` 只存放设计文档与计划 |
| 监听地址与端口 | 由 Runtime 生成的配置文件指定，默认 `0.0.0.0:<port>`，端口不写死 | 端口值属于环境相关配置，需放入 `SANDBOX.env` 或由 Runtime 注入，不在代码中猜测 |
| 配置来源 | 配置文件（JSON）路径通过环境变量 `EXECD_CONFIG` 指定；其余字段可被同名环境变量覆盖 | Runtime 生成配置文件 |
| 认证 | 不实现认证（单用户沙箱，直接调用） | 不做 Token 校验，`/health` 与其他接口一致开放 |
| 工作区 | 默认 `/workspace`，可配置，启动时校验存在且可写 | 由 Runtime 创建 |
| 持久化数据目录 | 默认 `/var/lib/execd`（可配置）；后台任务记录与日志存放于此 | 容器停止/启动期间保留即可，不需要挂载卷；删除容器时记录一并删除 |
| 环境变量作用域 | 继承 Runtime 在容器创建时配置的默认环境变量（含 `PATH`）；不提供全局 `/env` 接口，仅支持单次请求携带 `env` 与 `cwd` | Python/uv 的 `/workspace/.venv` 由 Runtime 配置 `PATH` |
| 删除不存在的文件 | 返回 404，响应明确说明资源不存在 | 调用方据此判断文件不存在 |
| API 前缀 | `/v1` | 版本化 |
| 日志 | 标准库 `log/slog`，JSON 输出到 stdout | 便于 Docker 采集 |

## 1. 目录结构（规划）

```
execd/                            # 源码（仓库根目录，独立于 modules/）
├── go.mod
├── cmd/execd/main.go            # 入口：加载配置、启动服务、信号处理
├── internal/
│   ├── config/                  # 配置加载与校验
│   ├── security/                # 路径校验、argv/env 校验、输出限流 writer
│   ├── middleware/              # 请求体大小限制、请求日志
│   ├── api/                     # Gin 路由与 handler（health/info/files/command/tasks）
│   ├── fileops/                 # 文件与目录操作（仅依赖 security）
│   ├── runner/                  # 底层命令执行（前台/后台共用）
│   ├── task/                    # 任务管理器：状态机、并发控制、取消、超时、日志
│   ├── store/                   # 任务记录持久化（JSON 文件，原子写入）
│   └── lifecycle/               # 优雅退出、PID1 子进程回收
└── testdata/

modules/execd/                   # 设计与计划文档
├── execd-design.md
└── plan.md
```

依赖方向：`api → task/runner/fileops → security`；`task → runner, store`；`fileops` 与 `runner` 互不依赖（满足设计中"文件操作与命令执行相互独立"）。

## 2. 实施阶段

### 阶段 P0：项目骨架

任务：
1. 在仓库根目录创建 `execd/`，初始化 `go.mod`，引入 `github.com/gin-gonic/gin`。
2. 创建上述目录骨架与 `cmd/execd/main.go` 空启动流程。
3. 配置 `Makefile`（位于 `execd/` 下）：`build`、`test`、`test-race`、`vet`。
4. 配置 `execd/.gitignore`（忽略构建产物）。

验证：`go build ./...`、`go vet ./...` 通过。

### 阶段 P1：配置模块（config）

任务：
1. 定义 `Config` 结构：`ListenAddr`、`Port`、`WorkspaceDir`（默认 `/workspace`）、`DataDir`（默认 `/var/lib/execd`）、`MaxConcurrentTasks`、`MaxUploadBytes`、`MaxOutputBytes`、`DefaultTimeout`、`MaxTimeout`、`TaskRetention`（已结束任务保留时长/数量）、`ShutdownGrace`。
2. 从 `EXECD_CONFIG` 指定的 JSON 文件读取，环境变量覆盖。
3. 校验：端口范围、目录存在性与可写性、数值上限为正数、`DefaultTimeout <= MaxTimeout`。

验证（单元测试）：
- 缺省值生效，未指定 `WorkspaceDir` 时为 `/workspace`。
- 非法端口、超时关系错误均返回明确错误。
- 环境变量覆盖文件配置。

### 阶段 P2：安全基础（security）

任务：
1. **路径校验**：`ResolvePath(workspace, userPath)`：拒绝绝对路径越界、`..` 越界；对已存在路径及其父目录执行 `filepath.EvalSymlinks`，确认结果仍位于工作区内，防止符号链接逃逸。
2. **argv 校验**：非空数组，首元素非空，拒绝包含 NUL 字节；不做 Shell 拼接。
3. **环境变量校验**：键名符合 `^[A-Za-z_][A-Za-z0-9_]*$`，值不含 NUL，数量与总长度有上限。
4. **工作目录校验**：复用路径校验，必须存在且为目录。
5. **输出限流 writer**：实现 `io.Writer`，超过 `MaxOutputBytes` 后丢弃并标记 `truncated`，不阻塞子进程。

验证（单元测试）：
- `../etc/passwd`、绝对路径越界、指向工作区外的符号链接均被拒绝。
- 合法相对路径通过。
- 非法 env 键名、含 NUL 的参数被拒绝。
- 输出限流：写入超限后返回长度正确、标记 truncated、不返回错误导致子进程失败。

### 阶段 P3：中间件与基础 API（health / info）

任务：
1. 请求体大小限制中间件（非上传接口使用较小上限）。
2. 请求日志中间件（slog，JSON）。
3. `GET /health`：返回进程存活与服务就绪状态（供 Docker HEALTHCHECK 调用，仅返回最小信息）。
4. `GET /v1/info`：返回版本、工作区路径、任务数量概览、运行时长。

验证（httptest）：
- `/health` 返回 200 且包含就绪状态。
- 请求体超限返回 413。

### 阶段 P4：请求级工作目录与环境变量

范围：不提供全局环境配置接口（`/v1/env` 已取消），工作目录与环境变量仅随单次请求生效。

任务：
1. `POST /v1/command` 的 `cwd` 缺省为 `/workspace`，须通过路径校验且为目录。
2. `env` 仅作用于本次执行，与 Execd 进程环境合并，同名键以请求为准。
3. 默认环境变量（含 `PATH`）直接继承 Execd 进程环境，由 Runtime 在容器创建时配置；Execd 不修改 `PATH` 等变量。

验证（集成到 P7 测试中）：
- 未指定 `cwd` 时 `pwd` 输出 `/workspace`。
- 指定 `cwd` 与 `env` 后命令输出符合预期。
- 请求级 `env` 覆盖 Execd 进程中的同名变量。

### 阶段 P5：文件操作（fileops）

任务：
1. 上传：`POST /v1/files`（multipart，指定目标路径）；大小受 `MaxUploadBytes` 限制；写入临时文件后原子 `rename`，保证传输完整。
2. 下载：`GET /v1/files?path=`，流式返回，设置 `Content-Length` 与 `Content-Disposition`。
3. 目录创建：`POST /v1/dirs`，等同 `mkdir -p`，重复调用幂等。
4. 查询：`GET /v1/files/stat?path=`（元数据）、`GET /v1/dirs?path=`（目录列表，支持分页或数量上限）。
5. 删除：`DELETE /v1/files?path=`；支持目录递归删除开关；删除工作区根目录禁止。
6. 所有路径经 P2 的 `ResolvePath`。

验证（单元 + httptest）：
- 上传后下载内容的 SHA-256 与原始一致（完整性）。
- 超过上传上限返回 413，且不留下残留文件。
- 路径越界、符号链接逃逸被拒绝（403 或 400，统一错误码）。
- `mkdir -p` 重复调用成功；删除不存在路径返回 404（错误信息明确为资源不存在）；禁止删除工作区根目录。

### 阶段 P6：底层命令执行（runner）

任务：
1. 基于 `exec.CommandContext` 实现 `Run(ctx, spec)`：`spec` 包含 argv、cwd、env、timeout、输出上限。
2. 设置 `SysProcAttr.Setpgid = true`，取消/超时时对整个进程组发送信号（`kill(-pgid)`），避免孙进程残留。
3. stdout/stderr 使用限流 writer；返回 `exit_code`、`stdout`、`stderr`、`truncated`、`timed_out`、`duration`。
4. 超时：先发送 SIGTERM，等待 `ShutdownGrace` 后 SIGKILL。
5. 启动失败（命令不存在等）返回明确错误，不伪装为退出码。

验证（单元测试）：
- 正常命令 exit 0 与 stdout 正确；非零退出码正确返回。
- 超时命令被终止，`timed_out=true`。
- 命令启动后产生的孙进程也被清理（以 `sleep` 子进程为例检查）。
- 输出超限时 `truncated=true`。

### 阶段 P7：前台命令执行 API

任务：
1. `POST /v1/command`，请求体：`argv`、`cwd`（可选）、`env`（可选）、`timeout`（可选）、`background`（布尔，默认 false）。
2. `background=false`：调用 runner 同步执行，返回 `stdout`、`stderr`、`exit_code` 等；不写入任务记录。
3. 并发控制：前台请求也占用并发信号量，超过上限返回 429。
4. 客户端断开时取消执行（使用请求 context）。

验证（httptest + 集成）：
- 前台执行返回正确结果、退出码和超时结果。
- 并发超限返回 429。
- 请求超时/客户端断开后子进程被终止。

### 阶段 P8：任务管理（task / store）

任务：
1. 任务状态机：`pending → running → succeeded | failed | timed_out | canceled`；`running` 在重启恢复时修正为 `interrupted`。
2. 任务 ID（`execution_id`）：生成 UUID，全局唯一。
3. `POST /v1/command` 且 `background=true`：立即返回 `execution_id`，后台 goroutine 执行 runner。
4. 并发控制：`MaxConcurrentTasks` 信号量，超出返回 429。
5. 日志收集：stdout/stderr 写入 `DataDir/tasks/<id>/` 下的日志文件，受 `MaxOutputBytes` 限制；查询接口支持 `offset`/`limit`（或 `tail`）。
6. 查询接口：
   - `GET /v1/tasks`：列表（支持状态过滤）。
   - `GET /v1/tasks/{id}`：状态、命令、时间、退出码、结果摘要。
   - `GET /v1/tasks/{id}/logs`：执行日志。
7. 取消接口：`POST /v1/tasks/{id}/cancel`，幂等；对已结束任务返回当前状态。
8. 持久化（store）：每个任务一个 JSON 元数据文件，采用临时文件 + `rename` 原子写入；状态变更时落盘。
9. 启动恢复：扫描 `DataDir/tasks/`，加载记录；状态为 `pending/running` 的任务修正为 `interrupted`，不自动重新执行。
10. 结束后清理：按 `TaskRetention`（时长或数量）删除已结束任务的记录与日志。

验证：
- 后台任务立即返回 `execution_id`，随后状态由 running 变为 succeeded，日志可查。
- 取消运行中的任务后状态变为 canceled，子进程被终止；重复取消幂等。
- 并发超限返回 429。
- 进程重启（重新初始化 manager）后已结束任务可查询；原为 running 的任务变为 interrupted。
- 过期任务按保留策略被清理。
- `go test -race` 下无数据竞争。

### 阶段 P9：生命周期、信号与进程回收（lifecycle）

任务：
1. `main` 中使用 `signal.NotifyContext` 监听 SIGTERM、SIGINT。
2. 收到信号后：停止接受新请求（`http.Server.Shutdown`，超时为 `ShutdownGrace`）→ 拒绝新任务提交 → 对运行中任务发送取消 → 等待其结束或超时 → 落盘任务状态 → 退出码 0。
3. **PID 1 职责**：Execd 作为容器主进程，需回收被孤儿化的子进程，避免僵尸进程。方案：启动 reaper goroutine 处理 SIGCHLD，并以 `waitid(P_PID, ..., WNOWAIT)` 或仅对非 Execd 托管的 PID 调用 `wait4`，避免抢占 `exec.Cmd.Wait` 的回收。**此点需先做一次原型验证**（见风险）。
4. 异常退出后的状态修正由阶段 P8 的启动恢复完成。

验证（集成测试，编译二进制后在子进程中运行）：
- 发送 SIGTERM 后进程在 `ShutdownGrace` 内退出，退出码 0。
- 退出时运行中的后台任务被取消，任务记录状态为 canceled 并落盘。
- 孤儿进程被回收（无僵尸残留，检查 `/proc` 中状态 Z 的进程数为 0）。
- 强制 kill -9 后重启，运行中的任务被修正为 interrupted。

### 阶段 P10：容器集成验证

任务：
1. 编写 Dockerfile 示例（仅用于本模块验证，正式基础镜像由 Runtime 规划）：多阶段构建，产出静态二进制；`ENTRYPOINT` 直接为 execd（使其为 PID 1）。
2. 编写 `HEALTHCHECK`（调用 `/health`）示例，验证 starting → healthy 转换。
3. 验证 `docker stop` 时 Execd 收到 SIGTERM 并优雅退出（退出码与耗时记录）。

验证：
- 容器内 `docker inspect` 显示 health 状态为 healthy。
- `docker stop` 后容器在 `ShutdownGrace` 内退出。
- `docker start` 后任务记录与 `/workspace` 文件保留（依赖容器可写层，不使用挂载卷）。

> 注：若本机无 Docker 守护进程，阶段 P10 无法执行，将在 PROGRESS.md 中如实记录，并以阶段 P9 的二进制级集成测试替代其中可验证部分。

## 3. 测试策略总览

- 单元测试：`go test ./...`，覆盖 config、security、fileops、runner、store。
- HTTP 测试：`httptest` 驱动 Gin 路由，覆盖错误码、幂等性。
- 并发与竞争：`go test -race ./...`。
- 集成测试：使用 build tag `integration` 区分，编译二进制并通过子进程验证信号、重启恢复。
- 设计文档第 4 节各验证项须在上述测试中有对应用例，并在 PROGRESS.md 中逐项记录结果。

## 4. 风险与待决策项

1. **PID 1 子进程回收**：Go 运行时的 `exec.Cmd.Wait` 与全局 SIGCHLD 回收存在竞争，需先原型验证方案（阶段 P9 前完成 spike）。
2. **端口注入**：依赖 Runtime 生成的配置文件，格式与字段名需与 Runtime 设计对齐；如 Runtime 设计后续调整，本计划的 Config 结构需同步修改。
3. **设计文档已同步**：[execd-design.md](execd-design.md) 第 2.1 节已改为"读取 Runtime 提供的默认环境变量配置"，不再提供全局环境变量设置接口，仅保留请求级 `env`/`cwd`。
4. **默认 PATH**：由 Runtime 在容器创建时通过 `ENV` 统一配置（包括 `/workspace/.venv/bin`），Execd 直接继承进程环境，不修改 `PATH`。需在 Runtime 开发时确认该配置项。
5. **删除幂等语义**：已确认删除不存在的文件返回 404。
6. **持久化范围**：任务记录保存在容器可写层，容器停止/启动后保留；删除容器后随之删除，符合需求。
7. **SANDBOX.env**：端口等环境相关值不写入代码或计划；开发需要这些值时，将向用户询问。

## 5. 开发顺序与进度记录

按 P0 → P10 顺序推进。每完成一个阶段，更新 [PROGRESS.md](../../PROGRESS.md) 中 Execd 模块的状态、验证结果与待办事项。模块全部完成并验证后停止，不自动开始 Runtime 模块。
