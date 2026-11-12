# Progress

 Update this file as you complete modules - Claude Code reads this to understand where you are in the project.

## Convention
- `[ ]` = Not started
- `[-]` = In progress
- `[x]` = Completed

## Execd 模块
- [-] 实施计划已制定：modules/execd/plan.md
- [-] 源码目录：execd/（Go，模块名 sandbox/execd）
  - [x] P0 项目骨架：go.mod、Makefile、.gitignore；`go vet ./...`、`go build ./cmd/execd` 通过。
  - [x] P1–P9 代码已编写，编译通过（未经单元测试覆盖）。
  - [x] P4 请求级 cwd/env：并入 command 处理，cwd 限定在工作区内。
  - [x] 单元测试与 HTTP 测试（2026-11-10）：`go test ./...` 全部通过，覆盖 config、security、fileops、runner（含超时杀进程组、输出限流、信号退出码）、store（分页读取）、task（后台执行、取消、超时、并发上限 429、前台不落盘、cwd/env 校验、重启恢复 interrupted、按数量清理、shutdown）、api（httptest 覆盖路由、错误码、body 限制、文件上传下载）、lifecycle（孤儿进程回收）。
  - [x] `CGO_ENABLED=1 go test -race -count=1 ./...` 通过（gcc 安装后）。
  - [x] 进程级集成测试（`go test -tags integration ./test/integration/`，2026-11-10）：kill -9 后重启，运行中任务恢复为 interrupted；SIGTERM 退出码 0，运行中任务落盘为 canceled 且重启后可查询。
  - [x] P10 容器集成验证（2026-11-10）：镜像 execd:dev 构建成功（Dockerfile 增加 GOPROXY build-arg）；HEALTHCHECK 达到 healthy；前台命令、文件上传/下载、路径越界返回 403、不存在文件返回 404 均验证通过；后台任务在 `docker stop` 时被取消（exit_code 143，状态 canceled，0.2s 内退出）；`docker start` 后任务记录保留。
  - [x] 集成测试中未覆盖的两项已在容器内补验：PID 1 孤儿进程回收、并发上限 429（见下方 P10 补充验证）。
  - [x] P10 补充验证（2026-11-10，容器内）：
    - 并发上限：max_concurrent_tasks=1 时，第二个后台任务和前台请求均返回 429；取消后可释放（cancel 返回 202）。
    - PID 1 孤儿进程回收：后台任务 `sh -c "sleep 300 & echo spawned"` 结束后，孤儿 sleep 被进程组清理，容器内 zombie 进程数为 0。
    - kill -9 恢复：`docker kill -s KILL` 后容器退出（exit 137），`docker start` 后运行中任务为 interrupted，error 为 "execd restarted before the task finished"，无残留进程。
  - [x] P10 全部验证项完成。

### 待办
- 无阻塞项。P0–P10 已全部完成并验证。
- 已知缺口（非阻塞，待决定是否修复）：
  1. 上传到已存在文件的子路径、或符号链接指向不存在目标时，fileops 返回 500 而非 4xx（ENOTDIR 等错误未映射）。
  2. middleware 包没有独立测试（行为已由 api 测试间接覆盖）。
  3. execd/ 目录尚未提交到 git。
- 模块完成后停止，不自动开始 Runtime 模块。

## Runtime 模块
- [-] 实施计划已制定：modules/runtime/plan.md（2026-11-10，R0–R1 已开始编码）
- [-] 源码目录：runtime/（Go，模块名 sandbox/runtime）
  - [x] R0 项目骨架（2026-11-10）：目录结构、cmd/runtime 入口占位、Makefile、.gitignore；依赖 `github.com/moby/moby/client` v0.6.2（Docker 官方 SDK 现路径，`github.com/docker/docker/client` 已不可用）与 `github.com/gin-gonic/gin` v1.12.0，经 tools.go 固定；`make vet`、`make build` 通过。
  - 注意：moby client 与本机 Docker Server 27.3.1 的 API 协商在 R2 的 Ping 中验证。
  - [x] R1 配置与基础镜像（2026-11-10）：
    - `internal/config`：RUNTIME_BIND_HOST / RUNTIME_PORT / RUNTIME_PUBLIC_HOST / EXECD_PORT 必填无默认值；可选 RUNTIME_CONFIG 文件，环境变量覆盖；单元测试通过。
    - `images/sandbox-base/Dockerfile`：python:3.13-slim-bookworm + uv==0.13.7 + execd；构建命令需 `--build-arg GOPROXY=https://goproxy.io,direct`（沿用 execd 的做法）。镜像 `sandbox-base:py3.13-uv0.13.7` 已构建。
    - 验证：容器 healthy；Python 3.13.18；uv 0.13.7；PATH 生效；工作目录 /workspace；`execd healthcheck` 退出 0。
    - 镜像不写 EXPOSE，端口由 Runtime 在创建时设置。
    - 注意：容器以 root 运行（计划未要求非 root），后续安全验证（R9）需一并确认。
    - 未清理：本机已存在名为 rt-r1-test 的容器（非本次创建），未删除，需用户确认。
- 已确认决策（见 plan.md §0.1）：
  1. D1 Endpoint 地址与端口从 `SANDBOX.env` 读取（已提供 `RUNTIME_BIND_HOST`、`RUNTIME_PORT`、`RUNTIME_PUBLIC_HOST`）。Execd 容器内端口从 `SANDBOX.env` 的 `EXECD_PORT` 读取。
  2. D2 不实现 Endpoint 认证。runtime-design 已同步修订。
  3. D3 `/workspace` 存放在容器可写层，不使用卷；删除容器即清理。runtime-design 已同步修订。
  4. D4 创建请求不接受 `image`，镜像由 Runtime 固定。
  5. D5 基础镜像 Python 3.13、uv 0.13.7。
  - [x] R2 Docker 适配层（2026-11-11）：`internal/docker` 封装 moby client（Ping、ImageExists、ContainerCreate/Start/Stop/Remove/Inspect、ContainersByLabel、CopyFileToContainer、ContainerStatsOnce）。
    - 每次调用带超时（默认 30s）；错误映射为 ErrUnavailable / ErrNotFound / ErrConflict / ErrTimeout / ErrInvalid，并保留原始错误。
    - 创建容器强制 CapDrop=ALL、no-new-privileges、非特权、CPU/内存/PIDs 限制、端口仅绑定到 BindHost 且 HostPort 动态分配、无宿主机挂载。
    - 单元测试使用 httptest 模拟 Docker Engine API（经 SDK 真实请求路径），覆盖上述行为与错误分类；`-race` 通过。
    - 集成：`make integration` 中对本机守护进程（27.3.1）做只读检查（Ping 与 API 版本协商、镜像存在、容器不存在、标签列表），通过。
    - 偏差：计划要求 `DockerAPI` 接口供 fake 替换；本阶段改为 httptest 模拟守护进程，未定义接口。R4 需要在 sandbox 包中按需引入接口。
  - [x] R3 Execd 配置生成与端点（2026-11-11）：
    - `internal/execdcfg`：生成 Execd 配置 JSON（listen_addr=0.0.0.0、port=EXECD_PORT、workspace_dir=/workspace、data_dir=/var/lib/execd，shutdown_grace 可选）。
    - 契约测试：解析 `execd/internal/config/config.go` 的 json tag，确认生成的键全部存在于 Execd 配置；通过。
    - 集成测试（`make integration`，从 SANDBOX.env 读取 EXECD_PORT，未设置则失败、不猜测）：将生成的配置挂载进基础镜像，真实 Execd 读取后健康检查为 healthy；通过。
    - `internal/endpoint`：Resolve（由 inspect 结果生成 http://RUNTIME_PUBLIC_HOST:宿主机端口，支持 IPv6；停止或未发布端口返回 ErrNotAvailable）、Registry（Set/Get/Invalidate/Refresh，并发安全；重启后刷新端口）。单元测试与 -race 通过。
    - 偏差：端点映射不持久化，以 Docker inspect 为准，registry 仅缓存最近一次观测值。
  - [x] R4 Sandbox 创建与初始化（2026-11-11）：`internal/sandbox`。
    - 请求校验：资源上限（超出返回 invalid_argument）、环境变量名/值/数量/大小、禁止覆盖 PATH 与 EXECD_CONFIG。CreateRequest 无 image 字段；API 层（R6）需拒绝未知字段。
    - 创建流程：镜像存在性检查 → ContainerCreate（标签、Env、资源限制、CapDrop=ALL、no-new-privileges、端口绑定）→ CopyToContainer 写入 Execd 配置 → Start → 轮询至 healthy（exited 立即失败；超时返回 ErrTimeout）→ 刷新端点。
    - 失败回滚：任一步失败删除容器；回滚失败时错误中点名残留容器（不静默丢弃）。回滚使用独立上下文，调用方取消也能清理。
    - 容量：列表、判断上限、占位在同一把锁内完成；pending 占位在 ContainerCreate 返回后释放（避免与列表重复计数）。并发测试通过。
    - 幂等：ClientID 通过标签 sandbox.client_id 查找已有 Sandbox；同一 ClientID 并发请求返回 ErrConflict。
    - 状态映射 StatusOf 按 runtime-design §1.2 实现。
    - 单元测试（内存 fake Docker）覆盖：成功路径、配置投递、输入校验、镜像缺失、上限与并发、健康超时回滚、提前退出快速失败、各步骤失败回滚、回滚失败报告、调用方取消后仍回滚、幂等、并发冲突。`-race` 通过。
    - 集成测试（真实 Docker，`make integration`）：PID 1 为 execd；/workspace 可写；无挂载；PATH；请求环境变量生效；Python 3.13；Privileged=false、CapDrop=ALL、no-new-privileges、内存与 PIDs 限制；端点 /health 返回 200；幂等；镜像缺失不创建容器。全部通过，无残留容器。
    - 未在真实 Docker 中验证的项：健康超时回滚与回滚失败（由单元测试覆盖）。
    - 偏差：无。
  - [x] R5 生命周期（2026-11-11）：`internal/sandbox/lifecycle.go`。
    - Get / Start / Stop / Delete；所有操作先校验 ID 格式，并检查容器带有管理标签且 sandbox.id 匹配，非 Runtime 容器不会被操作。
    - Start：已运行则幂等返回；启动后等待 healthy，并刷新端点（端口可能变化）。若未 healthy，则重新停止容器并保留容器和工作区，返回错误。
    - Stop：SIGTERM，超时 StopTimeout；已停止则幂等返回；端点失效。
    - Delete：必要时先停止，再删除容器（工作区随容器清理）；删除不存在的 Sandbox 视为成功，失败可安全重试。
    - 同一 ID 的生命周期操作由按 ID 的锁串行化。
    - 偏差（已同步到 plan.md）：Docker 不允许修改已有容器的标签，因此不使用 sandbox.deleting 标签记录删除中断。删除中断由调用方重试完成，R8 不再自动续做删除。
    - 单元测试（fake）与 -race 通过：覆盖停止/启动幂等、端点失效与刷新、启动未 healthy 的处理、删除与重试、非托管容器不被操作、畸形 ID、同 ID 并发串行。
    - 集成测试（真实 Docker）：创建 → 写入文件 → 停止（退出码 0，耗时不超过 stop_timeout）→ 启动（healthy，文件保留，/health 200）→ 删除（容器消失，重复删除成功）。通过，无残留容器。
  - [x] R6 Runtime HTTP API（2026-11-11）：`internal/api`（Gin），`cmd/runtime` 接入配置、Docker 客户端、优雅退出。
    - 路由：GET /health；POST/GET /v1/sandboxes（支持 ?status= 过滤，非法值返回 400）；GET/POST start|stop、DELETE、GET stats、GET endpoint。
    - 创建请求严格解码：拒绝未知字段；携带 image 返回 400 并说明镜像由 Runtime 固定；拒绝多余数据；请求体上限 64 KiB（超出返回 413）。Idempotency-Key 头映射为 ClientID（最长 128 字符，不含空白）。
    - 统一错误格式 {"error":{"code","message"}}，映射：invalid_argument 400、not_found 404、conflict 409、resource_exhausted 429、timeout 504、image_unavailable 503、docker_unavailable 503、start_failed 500、internal 500。500 不返回内部细节。
    - 请求日志：method、path、status、耗时、sandbox_id；不记录请求体，env 值不进入日志（有测试覆盖）。panic 转为 500。
    - service 新增 List、Stats；限制以标签（sandbox.cpus / memory_mb / pids_limit）保存，使 GET 能返回资源配置。
    - 创建时间截断到秒，保证创建响应与之后查询的 created_at 一致。
    - 测试：internal/api 14 项（内存版 Docker），覆盖上述行为；`-race` 通过。
    - 端到端冒烟（真实 Docker，二进制从 SANDBOX.env 加载配置）：health、幂等创建、image 被拒、stop→endpoint 409→start→endpoint 端口刷新、stats、Execd 经端点 /health 返回 200、list 过滤、delete→404。全部通过，无残留容器。
    - 偏差：计划中 GET stats 为 CPU、内存、PIDs；CPU 为单次采样，首次采样可能为 0。
  - [x] R7 状态查询与资源统计（2026-11-11）：
    - 详情与列表由 Docker 容器状态与健康状态、标签共同得出（R5、R6 已实现）。
    - 统计：运行中返回 CPU、内存、PIDs；停止时返回 409（api 测试覆盖）。
    - 新增真实 Docker 验证：容器内忙循环时 CPU 百分比不低于 20%，PIDs 增加；停止后统计返回 ErrConflict。通过，无残留容器。
    - `missing` 状态取消：列表以 Docker 为准，无独立记录，该状态无法出现（plan.md 已同步）。
    - 未验证：多个 Sandbox 同时列出时的性能（N+1 次 inspect）；当前规模可接受，若数量增大再优化。
  - [ ] R8 启动恢复与过期回收（2026-11-11，部分完成）：
    - 已实现（位于 `internal/sandbox`，偏差：未拆成独立 discovery / reaper 包，以便复用 Service 的 fake 测试）：
      - `Recover`：启动时把 ready Sandbox 的端点恢复到 registry；删除 created 状态的遗留容器（创建在启动前中断）；running 但未 healthy 的容器只报告不删除；停止的 Sandbox 保留。
      - `ReapExpired` / `RunReaper`：按 sandbox_ttl 删除超时 Sandbox（任意状态）；TTL=0 时关闭；单个失败不影响其他，错误收集后下一轮重试。
      - `cmd/runtime`：启动时执行恢复并记录报告；TTL>0 时启动回收循环，随进程退出停止。
    - 单元测试（fake）与 -race 通过：重启后端点恢复、遗留容器删除、运行中未 healthy 的容器保留、停止与外部容器不受影响、恢复幂等、TTL 边界、TTL=0 关闭、错误收集后重试、回收循环可停止。
    - 策略变更（R10 期间）：遗留的 created 容器默认只报告，不删除；需配置 RUNTIME_RECOVER_LEFTOVERS=true 才删除（config.recover_leftovers，默认 false）。原因：删除范围是守护进程上所有托管容器，这一范围过大，不应默认开启。单元测试覆盖默认报告与显式开启两种情况。
    - 真实 Docker 恢复测试 TestRealRecoverAfterRestart：已通过（需设置 RUNTIME_RECOVERY_INTEGRATION=1）。为缩小删除范围，测试通过 leftoverScope 只允许删除自己创建的遗留容器；生产路径中 leftoverScope 为空，不做额外限制。验证：端点被恢复到注册表、遗留容器被删除、运行中的沙箱 /health 200、无残留容器。
    - 修复（R8 期间）：并发创建的 pending 占位与 ContainerCreate 放入同一临界区，消除列表与占位的重复计数（之前的间歇性失败）。并发相关测试重复 30 次通过。测试阻塞点改为锁外的 ImageExists。
    - 真实 Docker 回归：除恢复测试外，其余 sandbox 集成测试在修复后全部通过，无残留容器。
    - 需要决定：Recover 与 ReapExpired 的范围是否应限定。当前设计假定 Runtime 独占管理 sbx-* 托管容器；若同一守护进程上还有其他 Runtime 实例或人工创建的托管容器，它们会被影响。
  - [x] R9 安全与资源验证（2026-11-11）：`internal/sandbox/security_integration_test.go`（integration tag，真实 Docker，只操作本测试创建的容器）。
    - 权限基线：有效能力集为空（CapEff 全 0）；NoNewPrivs=1；挂载 tmpfs 被拒绝（先确认 mount 命令存在）；Docker socket 不可见；挂载数为 0；Privileged=false。
    - PIDs 限制：PidsLimit=32 时尝试启动 300 个后台进程，Stats 中 pids 不超过 34；之后 /health 仍为 200。
    - 内存限制：MemoryMB=64 时分配 400 MiB 的进程退出码为 137（OOM SIGKILL），沙箱仍 /health 200。测试要求精确的 137，避免 127 等假阳性。
    - 容器崩溃：对 PID 1 发送 KILL 后状态为 stopped、端点为空、统计返回冲突；Start 后恢复 ready 且 /health 为 200。
    - 守护进程不可用：指向不存在的 socket 时，Create 与 Get 均返回 ErrDockerUnavailable，不会误报为 not found。
    - 以上 5 项全部通过，无残留容器。
    - 未覆盖：Runtime 重启场景由 R8 的恢复测试承担（尚未运行，需授权）。容器内仍以 root 运行（无能力集、非特权），非 root 运行未实现，计划未要求。
  - [x] R10 端到端集成验证（2026-11-11）：脚本 `/tmp/tmp.zppByJnzDL/e2e.sh` 启动真实 runtime 二进制，全部 20 项通过，无残留容器。
    - 流程：runtime 启动并 health → 幂等创建（带 env）→ 经端点直连 Execd /health → 上传 64 KiB 随机文件（201）并校验 sha256 往返一致 → 前台命令（默认 cwd=/workspace、请求 env 生效、能看到上传文件、exit 0）→ 后台任务 succeeded → 停止后端点 409 → 启动后端点端口刷新、文件保留 → SIGTERM 停止 runtime 后沙箱仍可访问 → 重启 runtime 后沙箱仍为 ready 且在列表中 → 删除 204、之后 404、容器已移除。
    - 运行方式注意：脚本显式设置 RUNTIME_SANDBOX_TTL=0s。SANDBOX.env 中没有 TTL，默认即关闭，此设置只是明确表示本次运行不回收。
    - 本次同时修正了脚本中的一处错误：Execd 上传返回 201，脚本原先只接受 200。
    - 未覆盖：对已有其他托管沙箱（非本次创建）的场景；其遗留容器在默认配置下只报告，不删除。
- 阶段进度：R0–R10 全部阶段已完成。R8 真实 Docker 恢复测试已在范围限制下通过。

## Manager 模块
- [x] 实施计划：modules/manager/plan.md（不做用户体系；不做认证）
- [x] 源码目录：manager/（Python 3.13，uv 管理；包名 sandbox_manager）
  - [x] M0 项目骨架与配置（2026-11-11）：uv 安装 Python 3.13.18；`uv sync` 完成；config 从环境变量读取 MANAGER_BIND_HOST/MANAGER_PORT/MANAGER_RUNTIME_URL。
  - [x] M1 错误模型与上游客户端：errors.py（统一错误码与状态映射，上游错误码透传）；clients/runtime.py、clients/execd.py（Execd 下载为流式）。
  - [x] M2 Endpoint 缓存与 Sandbox 服务：同 ID 并发获取单次请求；启停/删除后失效；restart 为 stop+start，不回滚。
  - [x] M3 命令、文件与任务服务：Execd 连接失败时仅 GET 失效重取并重试一次；命令、写操作不重试。
  - [x] M4 HTTP API：/api/v1 下 Sandbox、命令/任务、文件/目录路由；未知字段 400；上传上限 413；后台命令返回 202（与 Execd 一致）。
  - [x] M5 MCP：18 个 Tools，挂载于 /mcp；Host 白名单为绑定地址与本机回环。
  - [x] M6 Web 界面：web/（React 19 + Vite 8 + TS）列表、创建、启停、删除、执行命令；`npm run build` 通过，产物输出到 src/sandbox_manager/web/。文件面板与任务日志面板未实现。
  - [x] 单元/API/MCP 测试：`uv run pytest`，24 项通过；ruff、mypy 通过。
  - [x] M7 集成验证（2026-11-11）：真实 Runtime + Docker + Execd，创建（幂等 Key、env）→ 前台命令（cwd=/workspace、env）→ 64 KiB 文件 sha256 往返 → 越界 403 → 后台任务成功并读取日志 → 停止后 409 → 启动后文件保留 → 统计 → 删除 404；测试结束无残留 sandbox.managed 容器。
- 偏差（相对 plan.md）：路由合并为 api/routes.py；上传为有上限的内存读取，未做流式；新增 restart 与 info 路由；MCP 使用 mcp 2.x（MCPServer API）。
- 未完成：文件面板、任务列表与日志页面；Web 端无自动化测试（plan 要求 Vitest，尚未添加）。
- 环境注意：本会话启动的 Runtime 进程（runtime/bin/runtime，后台）未停止，因 pkill 被拒绝，需用户自行停止。
