# Requirement 流程简化 Plan

## 当前实施记录（2026-10-27）

已按本计划完成核心收敛，并追加以下正确性修复：

- 状态 action 不再从 `source_task_id + 目标状态` 推断。真实 daemon start/complete 显式推进 Requirement Issue；普通 task-token 状态写被拒绝。9087 增加仅 sweeper 使用的 `task_reset`。
- blocked no-op 不再禁用 Wakeup；resume 必须匹配当前 wakeup、generation 和实际回答评论。
- `requirement_id` hydration/realtime 查询失败改为字段缺失，不能作为权威空绑定覆盖 UI 或缓存。
- 9088 将旧 accepted 的不确定投递改为 unknown，防止 9085 的 pending 映射造成重发。9089 按证据接管存量 v2，不恢复双 worker。
- 9084/9085 down 增加新写入保护；产生新写入后只能前向修复，不能静默回滚。
- POPO 入口复用 `yixiezuo_operation` 创建正文标注“待澄清”的来源草稿；9090 只扩展既有操作类型。未知创建回执保持 unknown，提供只读核对，不自动再建单。
- Plan 在任务启动确认后读取上下文并结构化交接候选；Execute 接收已在 Multica Chat 批准的事实，并可读取自身需求的参与者与 Issue revision。
- Requirement 查询随任务生命周期失效重读，流式消息和进度不触发这次刷新。隔离 Docker、真实 POPO/来源回写及 Web/Desktop 的验证边界见两份实现记录。

具体动作和恢复矩阵见 `docs/requirement-status-gate-inventory.md`。

## 目标架构

保留一条清晰主链：

> POPO 想法 → 易协作待澄清草稿 → 新 Multica Chat 只读澄清 → Candidate → 在 Multica Chat 审批 → 唯一 Issue → Agent 执行 → 验收/拒绝/阻塞问答 → 审阅后回写易协作

手动单据预览与确认发布仍是独立入口；PM 自动化继续只读。

只保留一套对应能力：

- Requirement 核心模型，不折成草稿 Issue。
- Issue 承载审批后的执行状态。
- `issue_wakeup` 承载所有阻塞问答和恢复。
- `popo_bridge_command` 承载通知 outbox、投递状态和回执。
- daemon 工具隔离 + 服务端 capability 共同约束 Plan Mode。
- 易协作用现有 `yixiezuo_operation`，继续人工确认发布。

## 0. 先确认可以走哪条路线

盘点所有环境，区分“迁移执行过”和“功能发布过”：

- 是否存在真实 Requirement 通知。
- 是否存在 `popo_v2_*` 生产数据。
- 是否有 bridge 选择过 v2。
- 是否存在未终态的 v1/v2 delivery。
- 9081 等迁移在哪些环境执行过。

### 路线 A：完整 v2 未发布、没有真实流量

按本 Plan 删除未发布的 v2 实现，不推广到其他环境。

已执行过的迁移文件保持不动；不再使用的表可以暂时保留，之后通过新的前向迁移清理。

### 路线 B：已经发布或存在真实 v2 delivery

先保留 v2 receipt/recovery，直到存量全部终态。禁止直接关闭 flag、降级到 command 或删除恢复代码。

存量排空后，再进入下面的 command 收敛工作。路线 B 的兼容迁移单独评审，不和首轮简化捆绑。

---

## 1. 收紧 Requirement Issue 状态模型

### 目标行为

通用 Issue 编辑不再改变 Requirement Issue 的状态，只有明确动作可以转换：

| 动作 | 允许的状态变化 |
|---|---|
| 执行任务完成 | executing → in_review |
| accept | in_review → done |
| reject | in_review → executing |
| block | executing/blocked → blocked |
| 问答恢复 | blocked → executing |
| cancel | 非终态 → cancelled |
| PR/P4/workflow | 继续跳过 Requirement Issue |

标题、描述等非状态字段仍可正常编辑；相同状态写入仍可作为 no-op。

### 实现方案

- 删除 `multica.requirement_status_action` GUC 授权。
- 将 SQL 策略改为**显式 action 参数**：
  - 通用 `UpdateIssue/UpdateIssueStatus` 传空 action，禁止状态变化。
  - 任务完成和 Requirement 动作使用专用 query，传入明确 action。
  - SQL 同时校验 action 与目标状态，关闭当前 `ELSE TRUE` 缝隙。
- 用新的前向迁移替换 9081 的函数签名；旧迁移不改名、不重写。
- 删除 1500 行 SQL lexer/conformance 扫描，不再靠文本扫描发现所有写路径。
- 为每条专用写路径保留真实数据库行为测试。

暂不再引入触发器或另一套通用授权框架；显式 action + 专用 query 已足够表达当前产品规则。

---

## 2. 用现有 POPO command 替代完整 v2 和影子 outbox

### 目标模型

`popo_bridge_command` 同时承担：

- 业务投递意图；
- 收件人/backlog 路由状态；
- lease/attempt；
- delivered/failed/unknown；
- not-sent 重试证据。

删除 `requirement_outbox` 这条独立状态链，不再进行：

> outbox 状态 → command/delivery 状态 → receipt → 回写 outbox

### command 需要补充的能力

- Requirement source/occurrence/recipient 身份。
- 可延迟解析的 bridge、binding，支持无 binding 的 backlog。
- 不可变 payload/snapshot 和稳定的 delivery identity。
- attempt/lease fencing。
- expiry、`evidence_finalized`、`not_sent`。
- 必要的 payload hash，防止重试时内容漂移。

### 安全语义

- pending 且可以证明未 dispatch：允许继续投递。
- leased 已交给 bridge，过期且无回执：进入 `unknown`，禁止自动重发。
- 只有明确、最终的 `failed + not_sent + retryable` 才允许下一 attempt。
- delivered、partial、存在 remote message ID 或证据不完整：禁止盲目重发。
- late/duplicate receipt 按 command、attempt、lease 做幂等和 fencing。

### 明确不做

- 不保留分量回执。
- 不实现通用多段消息协议。
- 不保留完整 v2 的六表模型。
- 不让普通聊天、评论、媒体等存量 POPO 功能跟随本次重写。
- 不在 command 与另一张 outbox 表之间同步状态。

Requirement API 直接读取 command 的投递投影。真实通知仍然只是“每个收件人一条文本”。

---

## 3. 把 Requirement Wakeup 折回 Issue Wakeup

### 目标模型

一条 Requirement 问题对应一条稳定的 `issue_wakeup`：

- `requirement_id` 标识归属。
- `parent_comment_id` 标识问题。
- `revision` 或新增 `generation` 做 CAS。
- `enabled/disabled` 表示当前是否武装。
- `last_triggered_comment_id` 记录最近覆盖的评论。
- 现有 receipt、task、coalescing 机制继续复用。

### 调整方式

- rearm 就地推进同一条 wakeup，不再创建物理 successor。
- 删除 `current_wakeup_id` 指针和 `requirement_wakeup` 逻辑账本。
- 禁用、取消、终态只需 fence 一处。
- 保留 Requirement 特有的遗漏评论扫描和恢复策略。
- task context 只保存 wakeup ID、generation 和必要的恢复信息，不再同时保存逻辑/物理两套 ID。
- 通过前向迁移搬迁存量；旧迁移保持不变。

---

## 4. 简化 Candidate digest，但保留人工确认

### 提交 Candidate

- CLI 只提交 snapshot 和 `expectedRevision`。
- 服务端 canonicalize 并计算 digest。
- 删除“客户端计算一次、服务端再计算并比较”的重复流程。
- 保留服务端 digest，作为候选内容的稳定身份。

### 审批

保留人工确认 digest 的产品语义：

- UI 展示服务端返回的 digest。
- 审批仍携带 `candidateId + candidateDigest + expectedRevision`。
- 服务端确认它是当前最新 candidate，防止用户看过 A、实际审批 B。

`requirement_approval.candidate_digest` 可暂留作不可变审批证据，不为了少一列单独做迁移。

---

## 5. 收紧 Plan Mode 路由，不重做 token 体系

- 默认拒绝无法确定策略的路由。
- 删除 task token 实际不可达的 daemon allowlist 项。
- 按现有真实认证边界逐项核对 register/claim/heartbeat/progress/complete 等路由，不批量替换凭证。
- 在路由注册处声明 capability，逐步移除 `taskpolicy` 中的路径字符串副本。
- Plan 仅开放：
  - 必要读取；
  - Requirement candidate；
  - 明确允许的聊天和任务操作。
- execute 对 Requirement 的写操作仅开放 block。
- daemon 侧继续剥离 checkout、MCP、插件、自定义 runtime 参数和危险 Claude 参数。

Plan 模型只返回结构化 reply/candidate；daemon 以任务凭证和规划前 revision 保存候选，不再要求模型写 candidate.json 或执行 Bash。批准仅在 Multica Chat 中发生。实现理由与验证边界见 [只读 Plan 交接记录](../.agents/notes/implemented/2026-10-27-requirement-plan-structured-handoff.md)。

POPO 的来源渠道与批准权限分离；通知仅保留需要用户参与的节点，验收前审阅回写内容。见 [渠道与人工决策记录](../.agents/notes/implemented/2026-10-27-requirement-channel-and-human-gates.md)。

token 体系的整体重构不纳入本轮。

---

## 6. 清理前端和桌面补偿逻辑

### Chat

服务端会话列表已经包含 Requirement 参与者，因此：

- 正常路径删除 participant probe、缓存补偿和 dangling-session 特判。
- 旧后端兼容集中到一个 adapter，不扩散到 controller、ChatPage、ChatWindow。
- 普通聊天继续依赖 additive `ChatSession.requirement_id`，无关联时零 Requirement 请求。

### Issue

- Issue 响应 additive 增加 `requirement_id`。
- IssueDetail 直接查询 Requirement，不再绕 `origin_id → ChatSession → Requirement`。

### Desktop

- 将 Requirement ID 纳入 chat 的 `resourceKey`，不同 Requirement 不复用同一个 Tab。
- 删除 `hasChatRequirementIntent` 和 switch/open 中的业务特判。
- 保留通用 `withSessionNavigation`。

---

## 明确保留，不在本轮合并

- `requirement`
- `requirement_participant`
- `requirement_candidate`
- `requirement_approval`
- `requirement_event`
- `requirement_action`
- `yixiezuo_operation`
- Plan Mode daemon 工具隔离
- Idempotency-Key、revision CAS、持久化响应重放
- 已执行迁移的文件名和内容
- 单索引独立迁移规则

## 交付拆分与验收

按独立 PR 推进：

1. 状态规则收紧。
2. command 安全语义与 outbox 单账本。
3. Wakeup 合并。
4. Candidate digest 简化。
5. Plan 路由 capability。
6. 前端和桌面清理。

每组分别验证：

- **状态**：完整动作矩阵、普通 Issue 不受影响、PR/P4/workflow 仍跳过。
- **POPO**：dispatch 前后崩溃、过期无回执、confirmed not-sent、late/duplicate receipt、unknown 不重发。
- **Wakeup**：回答后恢复、重复评论、任务失败后 rearm、取消和终态 fence。
- **Plan**：未知路由 fail-closed、候选可提交、普通写请求和外部工具被拒绝。
- **前端**：普通聊天、参与者深链、Requirement Issue、不同 Requirement 的桌面导航。
- **迁移**：空库、已发布基线升级、现有本地库前向升级；不重写已执行迁移。

首轮完成标准不是减少多少文件，而是：**没有平行 wakeup、没有影子 outbox、没有 GUC 状态授权、没有完整 v2 双轨，同时保留审批闸门和可靠投递语义。**
