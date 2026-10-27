# Agent Note: POPO 成员会话模式与上下文隔离

Status: implemented
Kind: architecture
Protocol: agent-notes/v1

## 问题

同一机器人服务多位成员时，机器人级模板不能决定所有人的输入模式。群聊共享的模型会话也不能承载成员独立选择的普通聊天。成员在切换模式后收到旧事件重放，可能把原普通聊天重新解释为创建需求。

## 决策

新账号绑定默认普通聊天。每个账号绑定的 `config.popo_conversations` 保存以聊天类型和真实 chat ID 区分的个人模式与当前需求或草稿指针。迁移前已配置需求模板的机器人账号通过 `popo_default_mode` 保留原入口语义，显式 scope 选择优先，避免升级前队列消息突然进入普通 Agent 执行。账号绑定自身限定工作区、机器人和成员；每次读取选定需求仍校验当前参与者和 Agent。模板仅提供创建草稿时的默认值。持久化选择不是 Requirement 状态副本。

模式、选择的写入与 `channel_inbound_write` 的 `popo_conversation` 幂等记录在同一事务提交。旧控制消息不能覆盖新选择。重复事件由 Router 标记为 command replay；需求入口只能恢复已存在草稿，不得根据新模式重新解释旧普通消息或 `/new`。若原草稿已存在而指针因崩溃缺失，重放只在指针为空时补回；已有后续选择时不覆盖。

POPO 普通群聊使用群 ID、发送人和解析后的 Multica 用户 UUID 生成稳定的模型会话 key；会话创建者为实际成员，真实群 ID 继续存于 binding config 作为回传目标。复用已有会话前必须核对 creator，POPO 账号换绑到其他 Multica 用户时轮换会话而非继承旧历史；同一用户重绑可恢复原路由。桥自动附加的群历史不进入个人上下文；用户明确选中的引用和转发仍可作为输入。旧共享群 Chat 保留历史，新的个人会话不继承它的模型上下文。其他 IM 的群聊行为不变。新来源预览冻结 session key，已发布来源记录保留其原会话证据。

`/mode`、`/chat` 和 `/requirement` 的命令由服务端解析。查询和错误子命令不创建草稿、不启动 Agent；`/issue`、`/reply`、`/status`、`/stop`、`/yixiezuo` 保留既有分发。群中的个人控制和需求查询私发给操作者并注明原群。需求的批准、验收、取消继续在 Multica 中确认。

设置页只展示和删除当前成员的 POPO 账号绑定，并提供私聊绑定指引；一次性 token 仍负责证明 POPO 账号控制权，不允许设置页直接声明外部身份。解绑清除该 binding 的模式选择但保留 Chat 和 Requirement 历史。

本记录细化[来源渠道与人工决策记录](2026-10-27-requirement-channel-and-human-gates.md)的入口模式段落，来源快照、唯一执行 Issue 和人工闸门保持适用。Multica 的会话标识和阶段提示由当前 Requirement 查询派生，不在客户端保存另一份模式或服务端状态。

## 替代方案

机器人共享一个 current Requirement 或群聊上下文：会把成员之间的意图、模式或历史混用，因此不采用。

单独新增会话偏好和命令表：现有账号绑定配置与入站动作账本已经表达这些职责，因此复用其原子更新和删除边界。

只修改提示词，要求模型判断模式与解析控制命令：无法保证错误命令无副作用，也无法可靠处理旧消息重放，因此控制面由服务端执行。

## 后果

同群成员可独立选择模式并保留各自上下文。群中公开发出的消息与回复仍然可见；模型会话隔离不改变群的展示范围。已批准 Issue 沿用工作区任务权限；需求 Chat 则按参与者角色授权。成员需要各自完成账号绑定，来源操作仍使用其本人的易协作 bridge。

## 验证与限制

2026-10-27，[多成员数据库测试](../../../server/internal/handler/popo_member_conversation_test.go)通过同群独立模式与草稿、并发、旧命令重放、参与者撤销、Multica 普通聊天记录和需求 Plan 输入的隔离验证；追加的群聊/私聊换绑、缺失指针重放恢复、保留后续选择及成员级绑定管理回归通过。[群聊 key、旧默认模式与命令解析测试](../../../server/internal/integrations/popo/member_conversation_test.go)、[自动群历史隔离测试](../../../server/internal/integrations/popo/inbound_test.go)、[私发控制回执测试](../../../server/internal/integrations/popo/replier_test.go)及[来源路由批准测试](../../../server/internal/handler/requirement_approve_test.go)通过。

`pnpm typecheck`、`pnpm lint`、Go vet、迁移包和 Agent Notes 检查通过；Core API 326 项及 POPO 设置/绑定页测试通过。全量前端 6422 项中 6421 项通过，未修改的 issue-limit Escape 嵌套弹窗用例失败，单跑 14 项通过。完整 Go 检查中 POPO、通用 channel engine、易协作和迁移包通过；未修改的 seat-capacity 和 source-context 测试单跑仍失败，squad 触发器全量失败但单跑通过，均未作为本次修复的通过证据。未配置 Redis 与跨仓库探针的测试按原有条件跳过。

数据库与合成桥回归不证明真实 POPO 双账号并发、真实 Agent CLI 执行或生产部署已经验收。群中主动公开的内容、成员明确选中的引用以及工作区执行任务仍遵循原有可见范围。
