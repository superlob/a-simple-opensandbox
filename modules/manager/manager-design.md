# Manager 设计框架（Python）

## 1. 项目目标

实现一个基于 Python 的 Sandbox 管理服务 Manager，作为系统的统一管理入口，负责 Sandbox 的可视化管理、生命周期管理、状态查询及命令执行，并通过 HTTP API 和 MCP 两种方式向外部提供服务。

**核心原则：**
- Manager 负责统一管理和调度 Sandbox，不直接操作 Docker，由 Runtime 负责容器生命周期管理。
- Manager 通过 Runtime HTTP API 实现 Sandbox 的创建、启动、停止、重启、删除及状态查询。
- Manager 根据 Runtime 返回的 Execd Endpoint，直接调用 Execd HTTP API，实现容器内部的命令执行、文件操作及后台任务管理。
- Runtime 负责容器级状态和资源信息管理，Execd 负责容器内部运行信息及执行任务管理。
- 提供 Web 可视化界面，方便用户查看和管理 Sandbox。
- 同时提供 HTTP API 和 MCP 接口，支持 Web 界面及外部 Agent 调用。

## 2. 核心功能

### 2.1 Sandbox 可视化管理

提供 Web 管理界面，实现 Sandbox 的统一展示和交互管理，支持 Sandbox 列表、基本信息、运行状态及资源使用情况的查看，以及相关生命周期操作。

### 2.2 生命周期管理

封装 Runtime 提供的 HTTP API，实现 Sandbox 的创建、启动、停止、重启和删除等操作，并维护 Sandbox 的基本管理信息。

### 2.3 状态与信息查询

提供 Sandbox 运行状态及相关信息的查询能力：

- **容器级信息**：通过 Runtime 获取 Sandbox 容器运行状态、健康状态及资源使用情况。
- **容器内部信息**：通过 Execd 获取运行环境信息、工作目录信息及后台任务状态。
- **访问端点**：获取 Runtime 返回的 Execd Endpoint，供后续命令执行和文件操作使用。

### 2.4 命令执行与任务管理

封装 Execd 提供的 HTTP API，实现 Sandbox 内部的命令执行及后台任务管理，支持前台执行、后台执行、任务状态查询、执行日志获取和任务取消，并向 Web 界面及外部调用方提供统一的执行入口。

### 2.5 MCP 接口

基于 MCP 协议封装 Sandbox 管理及执行能力，将相关功能提供为 MCP Tools，供外部 Agent 调用。

MCP 与 HTTP API 共享 Manager 的业务能力，避免重复实现管理逻辑。

## 3. 验证要求

- **可视化管理验证**：验证 Sandbox 列表、详情、状态及资源信息展示是否正常。
- **生命周期管理验证**：验证 Sandbox 创建、启动、停止、重启和删除操作是否正常。
- **状态查询验证**：验证 Runtime 和 Execd 信息获取及展示是否正常。
- **命令执行验证**：验证前台执行、后台执行、任务查询、日志获取和任务取消是否正常。
- **HTTP API 验证**：验证 Manager 对外提供的 HTTP 接口及其与 Runtime、Execd 的通信是否正常。
- **MCP 验证**：验证 MCP Tools 能否正常调用 Sandbox 管理和命令执行能力，支持外部 Agent 访问。