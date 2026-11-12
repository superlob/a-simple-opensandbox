# runtime 设计框架（docker + Go）

## 1. 项目目标

实现一个基于 Go 的 Docker Runtime，负责 Sandbox 容器的创建、初始化、生命周期管理、状态查询及资源回收，并负责 Execd 在 Sandbox 容器内部的部署、启动配置及访问端点管理。

Runtime 通过 Docker Go SDK 与 Docker Daemon 交互，统一管理 Docker Sandbox 容器，并为上层 Manager 提供标准化的 Sandbox 管理能力。Sandbox 以 Docker 容器作为唯一运行载体，默认工作区域为 `/workspace`。

**核心原则：**

- **职责边界清晰**：Runtime 负责 Sandbox 容器及其运行环境的管理，不负责容器内部的常规文件操作、命令执行及后台任务管理。
- **容器统一管理**：Sandbox 以 Docker 容器作为唯一运行载体，Runtime 通过 Docker Go SDK 管理容器，不依赖 Docker CLI。
- **基础环境统一**：Runtime 负责 Sandbox 基础运行环境、工作目录及持久化存储的初始化，为 Execd 提供运行基础。
- **Execd 独立运行**：Runtime 负责 Execd 的部署、启动配置及访问端点管理；Execd 作为容器主进程，通过 HTTP API 独立提供容器内部操作能力，Runtime 不参与 Execd API 请求及执行结果的转发。
- **状态管理分层**：以 Docker 容器实际生命周期状态为主要依据，结合 Docker HEALTHCHECK 获取 Execd 服务健康状态。Runtime 负责容器级状态及基础信息管理，不维护 Execd 内部的命令执行任务状态。
- **可靠性保障**：生命周期操作需要考虑超时、异常处理、幂等性及资源清理；Runtime 重启后能够重新发现和管理已有 Sandbox，避免容器资源失去管理。

## 2. 核心功能

### 2.1 Sandbox 创建与初始化

Runtime 使用统一维护的固定 Docker 基础镜像创建 Sandbox 容器（创建请求不可指定镜像），并完成以下初始化工作：

- **基础运行环境**：基础镜像预置 Python 3.13 及 uv 0.13.7，满足常规 Python 程序执行和依赖安装需求。
- **工作目录**：创建 `/workspace` 作为默认工作目录。工作文件与依赖环境保存在容器可写层中，Sandbox 停止及重新启动后保留；删除 Sandbox 时随容器一并清理，不使用独立持久化卷。
- **Execd 服务**：基础镜像预置 Execd 程序，并配置固定启动入口，使其随容器启动自动运行，作为容器常驻服务，为后续环境配置、文件操作、命令执行及任务管理提供支持。
- **运行配置**：Runtime 生成 Execd 配置文件，设置服务监听地址、端口、工作目录及其他必要参数，并通过 Docker `Config.Env` 注入容器默认环境变量。Execd 启动时读取配置、继承容器环境变量，并支持命令级环境变量配置，通过 HTTP API 对外提供服务。

Runtime 负责创建容器、初始化工作目录、配置持久化存储及生成 Execd 运行配置，并通过 Docker Labels 记录 Sandbox ID 等管理信息。容器启动后，Runtime 根据 Docker HEALTHCHECK 获取 Execd 健康状态，确认服务正常就绪后，将 Sandbox 标记为可用。

### 2.2 Sandbox 生命周期管理

提供 Docker Sandbox 的统一生命周期管理能力，包括：

- **创建（Create）**：基于固定基础镜像创建 Docker 容器，完成工作目录初始化及 Execd 启动配置等必要设置。容器启动后，由 Execd 作为容器主进程（PID 1）运行并提供 HTTP 服务。Runtime 通过 Docker HEALTHCHECK 确认 Execd 服务正常后，将 Sandbox 标记为可用状态。
- **启动（Start）**：通过 Docker Daemon 启动已停止的 Sandbox 容器，由容器主进程 Execd 重新启动服务。保留原有容器配置及容器可写层中的数据，并等待 HEALTHCHECK 确认服务恢复正常。
- **停止（Stop）**：通过 Docker Daemon 停止正在运行的 Sandbox 容器。Docker Daemon 向 Execd（PID 1）发送 SIGTERM，Execd 执行优雅退出流程，包括停止接受新请求、终止后台任务及清理相关资源；超时后由 Docker Daemon 强制终止容器。
- **删除（Delete）**：通过 Docker Daemon 停止并删除 Sandbox 容器。容器可写层中的工作区与任务记录随容器一并清理，不需要额外的存储清理步骤。

### 2.3 状态与信息查询

提供 Sandbox 运行状态及基础信息的查询能力，包括容器实际运行状态、Sandbox 基本信息、资源使用情况、Execd 访问端点及必要的异常信息。

主要包括：
- 运行状态查询：通过 Docker Go SDK 获取 Sandbox 容器的实际运行状态及健康状态。
- 基本信息查询：提供 Sandbox ID、创建时间、基础镜像等基本信息的查询能力。
- 资源使用情况查询：获取 Sandbox 容器的 CPU、内存等资源使用情况。
- 访问端点查询：根据 Docker 网络配置获取并维护 Execd 的 HTTP 访问 Endpoint，记录 Sandbox ID 与 Endpoint 之间的映射关系，供外部调用方查询和访问。

### 3. 安全与资源管理

- 对 Sandbox 容器设置 CPU、内存及 PID 等基本资源限制。
- 默认采用非特权容器，限制不必要的 Linux Capabilities 和宿主机资源访问。
- 对容器网络及宿主机目录挂载进行必要限制，避免不必要的外部访问和宿主机文件访问。
- 当前版本不对 Execd 访问端点实施身份认证（单用户沙箱，直接访问）。访问范围通过部署网络和端口发布范围限制，避免未经授权的外部访问。
- 对 Sandbox 数量及资源使用设置必要限制，及时清理过期容器和无效资源。
- 不向 Sandbox 容器暴露 Docker Daemon Socket。

### 5. 验证要求

- **Sandbox 创建验证**：验证容器创建、基础环境配置、固定镜像使用及 `/workspace` 初始化是否正常，并验证停止后重新启动时工作文件保留。
- **Execd 部署验证**：验证 Execd 部署、启动及 HTTP 服务是否正常，确保其能够作为容器主进程运行。
- **生命周期验证**：验证 Sandbox 创建、启动、停止、重启及删除操作是否正常。
- **状态与信息查询验证**：验证容器运行状态、健康状态、资源使用情况及基础信息的查询能力。
- **访问端点验证**：验证 Execd Endpoint 的获取、查询及 Sandbox 停止、重启、删除后的更新情况。
- **进程管理验证**：验证 Execd 对终止信号的处理，以及退出时后台任务和相关资源的清理。
- **异常与安全验证**：验证 Docker Daemon 不可用、容器异常退出、Runtime 重启等场景，以及容器资源限制、权限隔离和访问认证是否正常生效。