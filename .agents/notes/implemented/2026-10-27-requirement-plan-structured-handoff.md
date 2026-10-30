# Agent Note: 原生 Plan 通过结构化输出交接候选

Status: implemented
Kind: simplification
Protocol: agent-notes/v1

## 问题

Requirement 的 Plan 在用户批准前用于规划，但候选最终需要持久化到 Multica。让模型写候选文件再调用 CLI，需要同时维护文件路径白名单、Shell 参数解析和原生 Plan 权限例外；原生 Claude Plan 实测仍会拒绝这条写文件路径。

## 决策

内置 Claude Plan 保留原生 `plan` 权限，只提供 Read、Glob、Grep，关闭继承的 hooks 和 MCP。模型通过受控 JSON Schema 返回 `reply` 和 `candidate`；需要澄清时 candidate 为 null。StructuredOutput 仅在宿主提供 Schema 时放行，不授予文件、Shell 或网络工具权限。

内置 DSH 按自己的原生 Plan 定义接入：桥接在本轮开始前调用 `planMode.set(agent, true)`，普通执行时设为 false，恢复会话也显式应用本轮模式。DSH Plan 是规划引导，sandbox 和 approval 独立生效；不复制 Claude 的工具白名单，也不把 DSH Plan 称为强制只读。Requirement 共用的平台任务凭证、外部集成隔离和人工批准闸门继续适用。

DSH 通过受控 `multica_plan_output` 工具返回 Schema 校验过的结果；工具只交回数据，不批准候选或切换模式。daemon 在发送规划提示前检查桥接声明的 `dsh-native-plan-v1` 和结构化输出能力。旧桥接缺少能力时明确失败，不按普通执行启动。Claude 和 DSH 使用不同的 Plan 权限标识，避免混用。

daemon 在工作目录就绪、服务端确认任务进入 running 后，通过任务凭证读取 Requirement，校验工作区、需求、Agent、Chat 和未绑定 Issue 的关系，捕获 revision。任务启动未确认时不能提前读取。快照放在本轮提示中，不写入跨轮缓存的 runtime brief。模型返回后，由 daemon 通过同一任务凭证保存候选；CAS 使用捕获的 revision，重试复用基于 task 和请求内容的幂等键。结构化输出缺失、版本冲突或保存失败都不能报告候选已完成。

候选仍由服务端验证并计算 digest，用户在 Multica Chat 审批后才创建唯一 Issue。此交接没有增加自动批准入口，也不改变 Execute 的文件交付方式。

外部 OpenCode 可以通过原生 `requirement-requests` API 请求单 Agent 或 Squad 队长做上述 Plan，再读取候选、澄清和交付结果。OpenCode 是协调调用方，这不代表其 Multica provider 已获得原生 Plan 能力；Plan 仍按目标 provider 的能力闸门运行。原生来源不依赖易协作预览操作，后续批准与验收仍使用同一 Requirement 账本。

本地目录绑定和自定义运行时仍不支持 Plan。daemon 在 P4 绑定或执行环境准备前检查这一边界，使用独立的 `plan_mode_unavailable` 原因说明本轮不会进入本地目录；修复 P4 登录不会使这一环境获得规划能力。

Execute 的本轮上下文明确说明该 Issue 已由用户在 Multica Chat 批准，不能因 Issue 评论中没有批准文字而重复索要批准。任务凭证可读取自身 Requirement 的参与者和 Issue revision；读取仍校验 running 状态、Agent、Requirement、Issue 绑定及取消状态，不授予批准、验收或来源回写权限。

## 替代方案

允许 Plan 写特定 candidate.json 并执行受限 Bash：原生权限不稳定，还引入路径与 Shell 的第二套权限模型，因此删除。

改用 bypassPermissions 执行规划：会取消批准前只读的保证，因此不采用。

从普通回复中猜测或提取 JSON：可能把澄清文本误当成候选，因此只接受独立的结构化结果。

对 DSH 增加 Read、Glob、Grep 白名单：会改变 DSH 原生 Plan 的定义，因此按原生模式接入，仅补齐 Requirement 的数据交接。

## 后果

规划模型只提供候选数据，候选持久化由 daemon 负责。旧人工 CLI 命令仍可用于显式任务 API 操作，但运行时 brief 不再要求模型执行它。其他 provider 尚未接入原生规划能力，不能自动降级到 Execute。DSH 的原生 Plan 不提供 Claude 那样的工具级只读保证。

## 验证与限制

2026-10-27，[Agent 权限测试](../../../server/pkg/agent/claude_plan_test.go)、[结构化输出测试](../../../server/pkg/agent/claude_structured_output_test.go)、[daemon 交接测试](../../../server/internal/daemon/requirement_plan_test.go) 和 [brief 测试](../../../server/internal/daemon/execenv/runtime_config_requirement_test.go) 通过。启动确认前读取上下文的回归在旧实现上复现 403，调整时序后通过。任务读取回归拒绝错误 Issue、Agent、需求范围、已结束任务及未批准或已取消需求。

[隔离真实链路验收](../../../e2e/requirement-nine-live.spec.ts) 使用真实 POPO 入站、易协作新草稿 110156 和 Claude CLI，分阶段完成只读 Plan、Chat 界面批准、Execute 临时文件与 SHA256、界面验收和来源回写。账号绑定使用隔离测试夹具；这不是账号绑定或生产部署验收。

协议见 [RequirementPlanOutputSchema](../../../server/pkg/protocol/requirement_plan.go)，Claude CLI 的输出字段参见 [官方说明](https://code.claude.com/docs/en/headless)。

2026-10-28，DSH 原生模式选择、结构化输出 Schema、保留原生工具、被策略拒绝的结果不交接，以及普通回复不作候选，由桥接仓库测试覆盖。Multica 的 [协议回归](../../../server/pkg/agent/dsh_plan_test.go)、[daemon 权限回归](../../../server/internal/daemon/dsh_plan_test.go)、[数据库领取回归](../../../server/internal/handler/dsh_plan_test.go) 和 [brief 回归](../../../server/internal/daemon/execenv/runtime_config_requirement_test.go) 通过。DSH 的模式语义参见 [上游说明](https://github.com/deepseek-ai/deepseek-harness/blob/master/packages/plan/plan-mode/README.md)。

[真实 DSH 链路验收](../../../e2e/requirement-dsh-live.spec.ts) 使用本机原生 DSH 和配置中的 `deepseek/deepseek-flash` 完成 Plan、浏览器 Chat 批准及 Execute。DSH 日志记录 `plan/mode.active=true` 和 `multica_plan_output`；批准前无交付文件，批准后仅一条批准记录和一个执行任务，文件内容及宿主 SHA256 与模型报告一致。最终需求为待验收状态。来源预览采用夹具，没有真实易协作或 POPO 交互；未做生产部署。脚本必须显式开启真实模型验收，并传入当前模型目录中的 ID，不依赖历史模型名。
