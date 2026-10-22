# Agent Note: 小队调度归属独立于队长角色

Status: implemented
Kind: bug-fix
Protocol: agent-notes/v1

## 问题

运行的 `squad_id` 同时用于队长 briefing 和角色判断，普通 worker 不应为了参与小队并发计数而被标记为队长运行。自动化和快速创建的归属还分别保存在 `autopilot_run.squad_id` 与 quick-create context 中；客串小队的归属可能与任务负责人不同。

## 决策

通过迁移 [9012](../../../server/migrations/9012_task_effective_squad.up.sql) 的 `task_effective_squad_id` 读取调度归属，不改写 worker 的 `squad_id`。优先读取本运行及委派祖先的明确小队归属，最后才回退到任务负责人；最近的明确归属优先。递归记录已访问的运行 ID，遇到循环停止。

领取 gate、事务内容量复核和跨 runtime 唤醒统一使用这个函数，避免同一运行在不同阶段被计入不同小队。

容量复核和上限更新共享小队 advisory 事务锁，包括当前上限为 NULL 的小队。领取在持锁后重新读取上限；若解析的小队变化则回滚重试，避免嵌套取得不同小队的锁。需要 agent 行锁的路径统一采用 agent → 容量锁的顺序，更新队长与上限的组合请求也遵守该顺序。领取只读取小队行并使用独立的 advisory 锁，以保留小队配置路径既有的小队行 → agent 行顺序。

issue 改派写入之后再唤醒：先按旧小队查找排队运行，再按受影响的 issue 查找。任务可能只通过 `issue.assignee_type = 'squad'` 解析归属，写入后旧小队检索已找不到它；按 issue 补一次检索才能唤醒改派 issue 自身的排队任务。

## 替代方案

将来源小队直接写入所有 worker 的 `squad_id`：会改变已有队长 briefing 与角色语义，因此保留该字段的原有用途。

在各条查询中分别展开归属表达式：原实现已出现入口遗漏、任务负责人覆盖客串来源、只支持两层委派的问题，因此统一为数据库读取函数。

## 后果

无任务链接的自动化与快速创建也受小队上限约束。客串委派保留发起小队的额度，后续转交不受固定两层深度限制。部署新查询前必须应用 9012 迁移。

## 验证与限制

2026-10-21 在环境脚本生成的隔离数据库完成回归验证：[squad_concurrency_test.go](../../../server/internal/service/squad_concurrency_test.go) 覆盖来源入口、客串隔离、深层转交、容量释放、peer 唤醒、循环委派与无效 context。领取相关 service 测试及小队 handler 定向测试通过；两个既有 Redis 测试因未设置 `REDIS_TEST_URL` 跳过。这不代表生产迁移或真实 agent / 客户端端到端验收。

同文件与 [squad_max_concurrent_tasks_test.go](../../../server/internal/handler/squad_max_concurrent_tasks_test.go) 还覆盖有限额/无限额变更时的等待后重读、上限更新与领取的互斥，以及队长与上限组合编辑的锁顺序。

改派唤醒的回归测试覆盖两条路径，且任务的 `squad_id` 与 `delegated_from_task_id` 均为 NULL，只通过 `issue.assignee_id` 解析旧小队：改派 issue 自身的排队任务时唤醒其 runtime 并可领取；旧小队仍有其他 issue 占位时，改派占位 issue 会唤醒旧小队中的 peer runtime 并可领取。移除按 issue 的检索会使第一条路径失败。
