# Requirement 状态与恢复边界

- 状态：当前实现（2026-10-26）
- 适用范围：Requirement Issue 的状态、task 生命周期、阻塞恢复和失败恢复。
- 迁移：9081 初版，9083 显式 action，9087 增加 `task_reset`。历史文件不改写。

## 1. 原则

Requirement Issue 不是普通看板 Issue：

- 标题、描述、assignee、project 等非状态字段仍可编辑。
- 相同状态的 no-op 是允许的。
- 通用 `UpdateIssue` / `UpdateIssueStatus` 不得改变 Requirement Issue 状态。
- task token 携带 `source_task_id` 不会获得隐式授权；普通 `multica issue status` 同样受此限制。
- PR、P4、workflow 自动化继续通过各自 SQL 中的 `NOT EXISTS requirement` 跳过 Requirement Issue。

SQL 策略统一为：

```text
requirement_issue_status_allowed(workspace_id, issue_id, current_status, next_status, action)
```

四参数旧签名保留为空 action wrapper。运行时不再使用 `multica.requirement_status_action` GUC，也不再维护 SQL 文本扫描器。

## 2. 动作矩阵

| action | 当前状态 | 目标状态 | 真实调用方 |
| --- | --- | --- | --- |
| 空 | 任意 | 不允许变化 | 普通 Issue 编辑 |
| `task_start` | executing（不含 blocked/review/terminal） | `in_progress` | daemon 的真实 task start |
| `task_complete` | executing（不含 blocked/review/terminal） | `in_review` | daemon 的真实 task complete |
| `task_reset` | executing（不含 blocked/review/terminal） | `todo` | stuck-task sweeper，已确认无活动任务且无新 retry |
| `accept` | `in_review` | `done` | Requirement accept |
| `reject` | `in_review` | `in_progress` | Requirement reject |
| `block` | executing/blocked | `blocked` | Requirement block |
| `wakeup_resume` | `blocked` | `in_progress` | 有效 wakeup recovery task start |
| `cancel` | 非终态 | `cancelled` | Requirement cancel |

未知 action、action 与目标不匹配、terminal 后转换均拒绝。相同状态判断先于 action，因此 no-op 不受上述限制。

`task_start` / `task_complete` 的兼容别名 `start` / `complete_task`，以及 `wakeup_resume` 的别名 `resume` 暂留于 SQL；当前 Go 代码只使用主名称。

## 3. 真实 task 生命周期

`server/internal/service/task_requirement_lifecycle.go` 是 task 与 Requirement 状态之间的唯一适配层。

### Start

`StartTask` 与 `StartTaskForClaim` 在非 replay 的同一事务中：

1. 验证 task 是 execute mode，且 Requirement/Issue 绑定一致、未取消。
2. 普通 executing Issue 使用 `task_start`。
3. blocked Issue 仅允许 `wakeup_resume`，并验证：
   - task context 中的 wakeup ID 和 generation；
   - 当前 `issue_wakeup` 的 Requirement、Issue、revision、parent comment；
   - `last_triggered_comment_id` 与 task trigger comment 一致；
   - `last_task_id` 与当前 task 一致。
4. 普通 task 没有这些回答证据时不能从 blocked 启动。

### Complete

`CompleteTaskWithTransition` 在同一事务中：

- executing Requirement Issue 使用 `task_complete` 进入 `in_review`；
- blocked Issue 保持 blocked，因为 agent 可能在调用 block 后正常结束；
- terminal Issue 不被复活；
- 已经是 `in_review` 的旧流程保持幂等。

### Reset

`HandleFailedTasks` 只有在无活动任务且没有刚创建 retry 时才传 `task_reset`。该 action 不开放给普通 Issue API。

所有 Issue 变化仍在 task 事务内完成，post-commit 才广播 Issue 和 Requirement 通知。

## 4. Wakeup 与删除边界

- 一个问题使用一条稳定的 `issue_wakeup`；`requirement_id + parent_comment_id` 标识问题和归属。
- rearm 就地推进 `revision`，不再维护逻辑行、物理 successor 或 `current_wakeup_id`。
- blocked no-op 状态写不禁用 wakeup、不丢 receipt、不取消 task。
- 离开 blocked、取消、accept、删除等真实 fence 路径才禁用 Requirement Wakeup。
- 未取消的 Requirement Issue 不能删除；取消后删除保留 requirement→issue tombstone。

9084 down 只允许在 folded rows 尚未收到新写入的窗口执行。发现 migration 后的新 rearm/trigger 时必须失败，不能回滚到旧的逻辑账本。

## 5. POPO 与状态通知

- `popo_bridge_command` 是 Requirement 通知的唯一业务意图和传输状态账本。
- 进入 `in_review` 仍由 `syncRequirementOutboxForIssueTransition` 生成 requirement event 和通知。
- blocked/review 的新状态会使尚未发送的旧通知 superseded；已 dispatched 或 unknown 的通知不被重写。
- 普通 POPO v1 的聊天、评论、媒体和 bridge 生命周期保持独立。

9088 将 9085 错误迁成 pending 的旧 accepted 改为 unknown。9089 按 lease/receipt 证据接管存量 v2；不确定结果不进入自动重试。

## 6. 关键验证

- `server/internal/service/requirement_status_gate_test.go`：动作矩阵、普通 task 写拒绝、no-op、普通 Issue 对照。
- `server/internal/service/task_requirement_lifecycle_test.go`：真实 start/complete、blocked complete、无回答恢复拒绝、合法 wakeup resume。
- `server/internal/handler/requirement_status_gate_test.go`：task token 通用状态写拒绝、blocked no-op、删除门禁。
- `server/internal/migrations/popo_command_takeover_test.go`：accepted 防重发和 v2 证据接管。
- `server/internal/migrations/requirement_rollback_guards_test.go`：新写入后的 down 必须失败并保留数据。

DB 测试必须确认实际连接并执行；testutil 或显式环境检查产生的 skip 不算验证通过。
