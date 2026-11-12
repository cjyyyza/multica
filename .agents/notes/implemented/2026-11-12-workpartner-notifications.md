# Agent Note: 工作伙伴以部署配置发送通用通知

Status: implemented
Kind: architecture
Protocol: agent-notes/v1

## 问题

POPO「连接智能体」接入的 DSH 等工作伙伴具有发送身份。协调轮次结束后，Multica 仍可能进入待批准、阻塞提问、待验收或完成状态。仅依赖该轮对话返回无法持续通知。Multica、工作伙伴和通知进程会共同部署在服务器上，通知配置属于部署环境，不能把开发者的群号、账号或本机路径写入 Multica 仓库的 AGENTS.md。

## 决策

[正式 CLI](../../../server/cmd/multica/cmd_notify.go) 提供 `notify send`，接收任意事件名、标题、正文和链接；[通知包](../../../server/internal/notify/popo.go) 的 `Notification`、`Sender` 与任务状态解耦。调用 `use_robot_send_msg` 时始终使用配置中的完整工作伙伴名称，群内提醒同时带实际 UID 和查回的显示名，不回退个人发送接口。

通知配置按命名目标保存工作伙伴、接收群或账号、被 @ 的账号及可选网页地址。服务器通过 `--config` 或 `MULTICA_NOTIFY_CONFIG` 指向部署环境的独立 JSON；协调智能体也可在自己的 AGENTS.md 使用单一 `multica-notify` JSON 代码块，由 `--agents-file` 选择。两个文件参数互斥，显式参数优先于环境变量，选定文件失败不回退到仓库指令，不跨项目合并。仅未选择文件且没有配置环境变量时查找最近的 AGENTS.md。凭证不写入路由 JSON、Markdown 或投递记录，仓库根 AGENTS.md 不承载运行时通知配置。

`notify watch` 是该通用发送入口的一个调用方。它用显式成员凭证读取已有 Requirement 或普通 Issue API，拒绝随 Run 结束而失效的任务凭证。服务器使用默认前台模式交给进程管理器托管，[systemd 模板](../../../examples/workpartner-notifications/multica-notify@.service) 按 Requirement 实例保留状态目录并在异常退出时恢复。协调智能体直接启动时也可使用[现有进程脱离机制](../../../server/cmd/multica/cmd_notify_background.go)，取得监听锁后才返回启动回执；子进程使用已解析的绝对配置路径，不因工作目录或环境变量变化选错配置。通知进程不在 DSH 模型轮次里维持等待，不改服务端数据库或增加公共回调接口。

通知以业务状态为准：待批准、具体阻塞问题、待验收和完成；单个 Run 结束不代表 Requirement 已验收。Requirement 归属仍遵循[渠道与人工决策分离](2026-10-27-requirement-channel-and-human-gates.md)，拒绝同时用其 Issue 建立第二条通知流。POPO 仅通知；被 @ 不授予参与者权限，也不触发答复、批准或验收。

通知主机上的投递意图、回执和监听检查点持久保存并使用文件锁。相同键的已确认请求回放回执，变更载荷拒绝复用旧键。POPO 缺少服务端幂等契约，因此调用中断或回执不明时保持 unknown，不自动重发；操作者在目标会话核验实际消息 ID 后，可用 `confirm-delivery` 记录已有回执。它只修改本地投递记录。

## 替代方案

只在协调智能体当前轮次结束时发通知：无法覆盖对话结束后产生的问题或交付，所以使用独立监听进程。

沿用 DJ01Bot 桥接或让未配置工作伙伴身份的后端进程代发：前者属于另一种接入身份与通道，后者不会因为同处服务器就自动继承另一个账号或容器的 POPO 登录态。当前链路与工作伙伴共享同一登录环境，以正式 POPO CLI 为发送端。

为每一种完成、询问、失败事件实现专用接口：会把调用方生命周期绑进发送器，因此保留通用载荷，监听器只负责从现有读取接口生成通知。

超时后自动重复发送：没有 POPO 幂等键，可能在已送达但回执丢失时重复 @ 人，因此保留待核实状态和显式回执恢复。

## 后果

部署者可在外置 JSON 或协调智能体自己的 AGENTS.md 切换群和 @ 对象，主动询问与自动状态提醒共用实现。开发仓库保持环境无关。监听按当前观察状态工作；两次轮询之间短暂出现并消失的状态不保证通知。首次注册会通知当前需要处理的状态，普通 revision 或标题变化不会刷屏，已观察到的返工后送审可再次提醒。

状态目录属于通知主机，不提供多主机去重；换目录或删除记录可能重复投递。配置在启动时加载，修改路由前需停止旧实例。通知依赖服务器、POPO 登录和 Multica 凭证可用；独立后台进程自身不随开机恢复，服务器的进程管理器负责启动和恢复。使用说明见[工作伙伴通知](../../../apps/docs/content/docs/workpartner-notifications.zh.mdx)，[示例 JSON](../../../examples/workpartner-notifications/notify.example.json) 和[环境模板](../../../examples/workpartner-notifications/notify.env.example) 均不含实际账号凭证。

## 验证与限制

2026-11-12，[配置、POPO 适配、投递与监听测试](../../../server/internal/notify/watch_test.go) 和[CLI 回归](../../../server/cmd/multica/cmd_notify_test.go) 覆盖命名路由、真实 @ 参数、中文文件、严格凭证、普通任务与 Requirement 归属、不同问题、候选与返工状态、重启回放、未知回执及恢复。Windows 检查使用 agent CLI guard，默认测试不访问用户安装的 Agent 或 POPO CLI。

配置回归还覆盖独立 JSON 与工作伙伴 Markdown 共用校验、部署环境变量、显式文件优先级、冲突或缺失文件不回退、无需修改仓库 AGENTS.md 即可发送，以及后台子进程固定外置配置绝对路径。Windows CLI 构建与 Linux amd64 CLI 交叉编译通过；systemd 文件是部署模板，不代表已经安装到真实服务器或完成 Linux 运行验收。

本机开发 CLI 的实际 Windows 后台进程通过隔离测试 API 和显式假 POPO CLI 验证：启动命令退出后继续发送两次不同提问、待验收和完成通知；重启无重复，任意事件发送、dry-run、丢失回执停止重发和确认后回放均符合预期。该项验证不发送真实 POPO 消息、不调用模型，不等同于生产 Multica 到真实 POPO 的完整验收。

外置 JSON 模式还通过同一套模拟链路：仅从 `MULTICA_NOTIFY_CONFIG` 选择配置，工作目录中的 AGENTS.md 不含通知设置且始终保持原文。仓库根 AGENTS.md 已恢复为 HEAD 原版，没有纳入本次改动。

定向 Go 测试、`go vet`、Agent Notes 检查、MDX 索引生成、文档类型检查和文档 30 项测试通过。完整 CLI 回归中的 Windows daemon 停止测试返回 `exit status 1`；用 Go overlay 排除全部新通知命令、恢复 HEAD 的 main 后，完整基线也在同一组原有测试失败。该组通过 PowerShell/CIM 查询进程身份，本次未修改或绕过它。

本次未替换已安装 CLI、重启现有 daemon 或部署服务器，未运行无关的前端、数据库或真实模型验收。
