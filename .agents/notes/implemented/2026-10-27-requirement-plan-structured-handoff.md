# Agent Note: 只读 Plan 通过结构化输出交接候选

Status: implemented
Kind: simplification
Protocol: agent-notes/v1

## 问题

Plan 必须在用户批准前保持只读，但候选最终需要持久化到 Multica。让模型写候选文件再调用 CLI，需要同时维护文件路径白名单、Shell 参数解析和原生 Plan 权限例外；原生 Claude Plan 实测仍会拒绝这条写文件路径。

## 决策

内置 Claude Plan 保留原生 `plan` 权限，只提供 Read、Glob、Grep，关闭继承的 hooks 和 MCP。模型通过受控 JSON Schema 返回 `reply` 和 `candidate`；需要澄清时 candidate 为 null。StructuredOutput 仅在宿主提供 Schema 时放行，不授予文件、Shell 或网络工具权限。

daemon 在工作目录就绪、服务端确认任务进入 running 后，通过任务凭证读取 Requirement，校验工作区、需求、Agent、Chat 和未绑定 Issue 的关系，捕获 revision。任务启动未确认时不能提前读取。快照放在本轮提示中，不写入跨轮缓存的 runtime brief。模型返回后，由 daemon 通过同一任务凭证保存候选；CAS 使用捕获的 revision，重试复用基于 task 和请求内容的幂等键。结构化输出缺失、版本冲突或保存失败都不能报告候选已完成。

候选仍由服务端验证并计算 digest，用户在 Multica Chat 审批后才创建唯一 Issue。此交接没有增加自动批准入口，也不改变 Execute 的文件交付方式。

Execute 的本轮上下文明确说明该 Issue 已由用户在 Multica Chat 批准，不能因 Issue 评论中没有批准文字而重复索要批准。任务凭证可读取自身 Requirement 的参与者和 Issue revision；读取仍校验 running 状态、Agent、Requirement、Issue 绑定及取消状态，不授予批准、验收或来源回写权限。

## 替代方案

允许 Plan 写特定 candidate.json 并执行受限 Bash：原生权限不稳定，还引入路径与 Shell 的第二套权限模型，因此删除。

改用 bypassPermissions 执行规划：会取消批准前只读的保证，因此不采用。

从普通回复中猜测或提取 JSON：可能把澄清文本误当成候选，因此只接受独立的结构化结果。

## 后果

规划模型不接触提交动作，只提供候选数据。旧人工 CLI 命令仍可用于显式任务 API 操作，但运行时 brief 不再要求模型执行它。其他 provider 仍需先证明原生只读能力，不能自动降级到 Execute。

## 验证与限制

2026-10-27，[Agent 权限测试](../../../server/pkg/agent/claude_plan_test.go)、[结构化输出测试](../../../server/pkg/agent/claude_structured_output_test.go)、[daemon 交接测试](../../../server/internal/daemon/requirement_plan_test.go) 和 [brief 测试](../../../server/internal/daemon/execenv/runtime_config_requirement_test.go) 通过。启动确认前读取上下文的回归在旧实现上复现 403，调整时序后通过。任务读取回归拒绝错误 Issue、Agent、需求范围、已结束任务及未批准或已取消需求。

[隔离真实链路验收](../../../e2e/requirement-nine-live.spec.ts) 使用真实 POPO 入站、易协作新草稿 110156 和 Claude CLI，分阶段完成只读 Plan、Chat 界面批准、Execute 临时文件与 SHA256、界面验收和来源回写。账号绑定使用隔离测试夹具；这不是账号绑定或生产部署验收。

协议见 [RequirementPlanOutputSchema](../../../server/pkg/protocol/requirement_plan.go)，Claude CLI 的输出字段参见 [官方说明](https://code.claude.com/docs/en/headless)。
