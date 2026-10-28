# Agent Note: 本地目录绑定按用户隔离

Status: implemented
Kind: architecture
Protocol: agent-notes/v1

## 问题

项目代码资源由工作区成员共享，但 `local_directory` 记录包含成员电脑上的真实路径，标签也可能包含完整路径。仅按 daemon 区分电脑或在前端隐藏行，仍会通过列表、修改响应、工作区事件和任务上下文泄露其他成员的本地信息。

## 决策

本地目录绑定使用已有 `created_by` 作为归属，列表和修改/删除接口只允许本人访问；无法确认归属的旧记录不推断所有者。同一用户在其他电脑绑定的目录继续可见。此规则适用于全部 `local_directory`，包含 P4 client 和 Git 目录；服务端不为判断目录是否属于 P4 而访问成员机器。共享 depot 和仓库资源继续沿用工作区权限。

工作区的本地目录变更事件只广播 project/resource ID，不广播 ref 或标签。前端收到事件后重新读取已授权列表，展示和目录选择前也检查当前用户归属，以处理旧服务端数据和账号切换期间的缓存。

Issue 的目录横幅也在渲染前检查创建者。P4 任务的项目资源 JSON 与运行时说明、技能一起写入任务私有目录，brief 指向该私有文件，不把包含本地路径的资源副本写入 Helix 工作目录。

Issue、Chat、Autopilot 和 quick-create 任务领取上下文按执行 runtime 的 `owner_id` 过滤本地绑定。共享 Agent 和任务发起人不扩大该 runtime 的路径访问范围；没有所有者的 runtime 以及仅解析 depot 的服务端查询不接收任何本地目录。

## 替代方案

只隐藏 UI 或删除 `local_path` 字段：接口、标签和事件仍可能暴露路径或允许修改他人绑定。按 daemon ID 当作用户归属：无法处理同一用户的多台电脑，也不能证明绑定属于当前用户。新增归属表或迁移：现有 creator 和 runtime owner 已能表达所需边界，因此复用这些字段。仅忽略无主记录的冲突检查仍会被相同 ref 的唯一约束阻挡；显式重绑因此使用事务替换，不把旧记录重新归属给当前成员。

## 后果

用户只管理自己的本地目录，工作区共享的 P4 配置保持可用。无主绑定不自动转给任何成员。用户重新提交绑定时，服务端以新提供的路径和元数据创建本人记录，在同一事务内替换该项目、该 daemon 的无主旧记录，不继承旧路径、标签或记录 ID。项目行锁串行化并发重绑；创建失败时回滚旧记录删除。有主记录仍按每个项目/daemon 一个目录的约束返回冲突。此流程不批量推断历史归属，不调整 daemon 身份或运行时可见性。

## 验证与限制

2026-10-28，[数据库隔离测试](../../../server/internal/handler/project_resource_privacy_test.go)覆盖两个成员的列表、总数、双向修改/删除拦截、无主记录、共享 depot、runtime 所有者及创建/更新/项目批量创建事件。[重绑回归](../../../server/internal/handler/project_resource_rebind_test.go)在临时 PostgreSQL 上覆盖新路径、相同 ref、不替换有主绑定和并发重绑单一胜者，先在修改前代码复现失败。[目录展示测试](../../../packages/views/projects/components/project-resources-local-directory.test.tsx)覆盖同机不同用户、账号切换、本人其他电脑和归属缺失；[P4 目录选择测试](../../../packages/views/projects/components/project-resources-p4-directory.test.tsx)确保不复用他人目录；[实时刷新测试](../../../packages/core/realtime/use-realtime-sync-ws-instance.test.tsx)覆盖仅带 ID 的事件。列表与事件回归先在修改前代码复现失败。验证使用隔离 PostgreSQL 与合成 UI/daemon 上下文，不证明生产服务已部署或真实双账号客户端已验收。
