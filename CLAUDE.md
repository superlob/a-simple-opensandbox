# Docker Sandbox 平台设计框架

## 1. 项目目标

实现一个基于 Docker 的 Sandbox 管理与执行平台，为 Agent 及外部调用方提供隔离、可管理的命令执行环境。

平台采用 Manager、Runtime 和 Execd 三层架构，分别负责统一管理入口、Docker 容器管理及容器内部执行服务，实现 Sandbox 的创建、生命周期管理、文件操作、命令执行及后台任务管理。

## 2. 整体架构

平台由三个独立模块组成：

- **Manager**：统一管理入口，提供 HTTP API 和 Web 管理界面，负责 Sandbox 管理及 Execd Endpoint 管理。
- **Runtime**：容器管理层，基于 Docker Go SDK 负责 Sandbox 容器的创建、初始化、生命周期管理及资源回收。
- **Execd**：容器执行层，运行于 Sandbox 容器内部，通过 HTTP API 提供文件操作、命令执行及后台任务管理能力。

### 模块交互关系

- Manager 通过 HTTP API 调用 Runtime，完成 Sandbox 管理。
- Runtime 通过 Docker Go SDK 与 Docker Daemon 交互，管理 Sandbox 容器。
- Execd 运行于 Sandbox 容器内部，独立提供容器内部操作接口。
- 外部调用方通过 Manager 获取 Execd Endpoint 后，直接访问 Execd，文件操作和命令执行请求不经过 Manager 或 Runtime 转发。

## 3. 技术选型

| 模块 | 技术选型 |
|---|---|
| Manager | Python + FastAPI |
| 管理界面 | React + Vite |
| Runtime | Go + Docker Go SDK |
| Execd | Go + Gin |
| 运行环境 | Docker |
| 持久化存储 | Docker Volume / Bind Mount |

整体后端统一采用 Go 实现，各模块通过 HTTP API 进行通信。

## 4. 开发规则

### 模块设计与计划

- 每个模块都有独立的设计文档，存放在 `/modules/模块名/模块名-design.md`。
- 开发某个模块前，必须先阅读对应的设计文档，明确模块职责、技术方案和接口约定。
- 阅读完成后，制定详细的实施计划，并保存为对应模块目录下的 `plan.md`。
- Plan 应包含具体实施任务及相应的验证测试。

### 开发进度管理

- 考虑到一个模块可能无法在一轮对话中完成，必须使用 `PROGRESS.md` 持续记录开发进度。
- 每次开始开发前，先阅读 `PROGRESS.md`，了解当前实现状态。
- 每完成一个阶段或一轮开发，及时更新 `PROGRESS.md`，记录已完成内容、验证结果和待完成任务。
- 模块全部开发并验证完成后，停止当前工作，不自动开始下一个模块。

### 环境配置与敏感信息

- API Key、Host、IP 地址等敏感信息及环境相关配置统一保存在项目根目录的 `SANDBOX.env` 中。
- 需要使用这些配置但文件中缺少相应信息时，向用户询问。
- 严禁猜测、编造或硬编码敏感信息。


