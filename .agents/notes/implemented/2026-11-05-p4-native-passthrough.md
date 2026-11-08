# Agent Note: P4 原生命令透传

Status: implemented
Kind: simplification
Protocol: agent-notes/v1

## 问题

Multica 曾用 Cobra 子命令和 daemon 策略分别解释 P4 参数，再把它们重建为原生 argv。不同 P4 版本和命令的参数位置、取值及新增 flag 因此需要两套持续维护的适配；任务 CL 自动创建和收尾又依赖这些解析结果。

## 决策

P4 命令语法完全归本机安装的 `p4` 所有。任务绑定仍负责解析并校验现有工作区的 Owner/Host/Root，注入真实 `P4PORT`、`P4USER`、`P4CHARSET`、`P4CLIENT`，并提供任务认证、工作目录、超时和输出边界。

原生 `p4` 在任务中直接可用。薄代理条款已由 [P4 仅原生执行](2026-11-08-p4-native-direct-only.md) 部分取代：当前不再提供 `multica p4 <args>`、`POST /p4/native` 或 `perforce-native-v1`。本记录其余关于工作区绑定、任务生命周期、Swarm 和配置清理的决策继续适用。

Multica 不再创建、复用、revert、shelve、submit 或自动评审任务 CL，也不再用单号、depot 路径、独占锁或动作开关约束原生命令。`allow_submit`、`allow_sync`、`allow_unshelve`、`allow_exclusive`、`no_auto_review` 从配置界面和类型中移除。工作区 depot 管理移到 `multica p4 registry ...`，避免占用原生 `p4 add`。

Swarm 的显式 create/link、issue 上手动关联 CL、既有 CL/Swarm 轮询与状态推进继续保留。绑定工作区可为 Swarm 补全省略的 port/depot/stream 和连接默认值，但不补任务 CL；`swarm create` 要求显式 CL 且不再隐式 shelve。旧 daemon 已持久化的 CL 回执仍可解码并排空，但新运行不产生这种回执。

## 替代方案

继续扩充逐命令白名单：需要持续跟踪每个 P4 版本的参数语义，正是本次要消除的维护成本。

只放打开少数写命令并继续注入任务 CL：仍需理解参数位置，也会保留部分隐式行为；用户明确选择不做这种折中。

完全删除 daemon 代理，只允许直接 `p4`：可以满足原生执行，但会失去统一工作目录、任务上下文和可选的本地调用入口，因此保留一个不解释命令的薄代理。

## 后果

这是可信 agent 的共享工作区模式，不是防止任意命令误用或恶意绕过的安全边界。P4 原生写操作可以直接影响人的 client 和 depot。操作规范、人工验收与按需关联/轮询取代命令级强制约束。

已有的本地目录成员隔离、私有运行说明/skill 目录和 Swarm 凭证目的地限制不受本次决策改变。

## 验证与限制

Go 全量 `go build ./...` 通过。聚焦测试覆盖任意原生 argv/stdin、stdout/stderr/退出码、真实 client 环境、绑定不创建任务 CL、Swarm create 不 shelve、绑定 depot 默认值、原生协议版本偏斜提示、内部超时 HTTP 504、depot 配置清理和既有轮询。`internal/daemon/p4cache`、`internal/daemon`、`internal/daemon/execenv`、`internal/handler`、`internal/p4depot`、`internal/service` 全量检查及 `cmd/multica` 的 P4 定向检查通过。

这些检查使用测试创建的 fake P4 和 agent，可证明参数边界与新流程，不证明真实 P4 服务器、UE、Swarm 或生产客户端已经验收。根级 `pnpm lint` / `pnpm test` 与 `cmd/multica` 全量测试还存在本次未修改的 icon、zh-Hant parity、Windows symlink 权限和 daemon 强制停止用例失败；本次修改文件的 ESLint、前端相关测试及 P4 定向 Go 测试分别通过。
