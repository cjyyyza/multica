# 两个场景示范

以下人物、账号、任务号、CL 和 URL 均为虚构示范。发送时使用实际查询结果、实际任务 ID和 AGENTS.md 的部署配置。

## 1. dump 需要补充信息，询问上报人

来源工具查到 dump `DUMP-001` 的上传账号属于张三；POPO 查询唯一匹配到 `reporter@corp.netease.com`，显示名为 `张三（QA）`。当前 Requirement 关联任务为 `MUL-123`。

1. 用 `requirement get` 读取执行端的 `blockedQuestion`，例如“请补充复现版本与崩溃前操作”；保存其真实评论 ID。
2. 保留当前 `issue.revision` 和 `canReply`。本例的新任务已约定由协调成员接收外部信息，因此该成员可以在获得回答后转交；不能把上报人的 POPO UID当作 Multica UUID。
3. 根据 AGENTS.md 确定发送方式。在群模式下查询群成员，然后调用 `use_robot_send_msg`：`robotName` 是工作伙伴完整名称，`receiver` 是配置群号，`atUids` 是实际的 `reporter@corp.netease.com`。
4. 发送正文并保留真实 POPO 消息回执：

```text
[需要补充信息] project:MUL-123:Q1
DUMP-001 的分析还需要两项信息：
1. 当时运行的版本/构建号。
2. 崩溃前最后几步操作，以及是否稳定复现。
请引用这条消息回复，我会转交给对应任务继续处理。
Multica：https://multica.example/project/chat?requirement=实际需求UUID
@张三（QA）
```

POPO 入站随后提供：真实发送者为该上报人 UID，聊天范围一致，被引用消息 ID正是刚保存的回执，正文为“构建号 100，进城后连续切换两次装备就崩溃”。工作伙伴先核对关联，再重新读取 Requirement，确认当前问题未变化且 `canReply=true`。

把答复写到已有工作目录中的 UTF-8 文件，保留来源：

```text
来源：POPO 上报人 reporter@corp.netease.com；消息 ID：实际入站消息ID。
对应：DUMP-001 / project:MUL-123:Q1。
上报人原文：构建号 100，进城后连续切换两次装备就崩溃。
本答复由协调成员转交。
```

使用首次核对得到的真实问题 ID、`issue.revision`、固定答案文件及幂等键调用 `requirement reply`。成功后回告“已转交该任务”，再继续检查实际状态。规划期的问题则使用 `clarify` 和 Requirement revision。

若实际 `canReply=false`，发送 Multica 问题链接给被点名成员处理，不把这一条 POPO 答复冒充成该成员提交的答案。若没有引用、多个问题都可能匹配，则询问它对应哪个任务/编号。

## 2. 修复后，把 shelve 发给 bug 归属人

来源工单明确归属李四；POPO 查询唯一匹配到 `owner@corp.netease.com`，显示名为 `李四（客户端）`。当前需求进入 `in_review`。

1. 读取 `requirement result` 的任务、运行和评论，找到执行端记录的 shelve 交付。
2. 核对交付来自实际 P4 服务器 `ssl:p4.example:1666`、CL `123456`，并已有 shelved files 的只读核验证据。Swarm 返回的真实 review URL 是 `/reviews/98765`，不能把 CL 号当成 review ID。
3. 按 bug 归属人路由给李四。此前问过 dump 上报人张三，并不改变交付对象。
4. 保存交付 key 和正文后发送，得到实际消息 ID才标记成功：

```text
[修复待验证] MUL-123 / BUG-001
该问题的修复已 shelve，等待验证。
P4：ssl:p4.example:1666
分支：//depot/project/main
Shelve CL：123456
Swarm：https://swarm.example/reviews/98765
修复：避免装备切换过程中访问已经释放的对象。
验证：定向回归通过；完整游戏内验证尚未完成。
请在原复现环境确认，结果验收在 Multica 完成。
Multica：https://multica.example/project/chat?requirement=实际需求UUID
@李四（客户端）
```

再次周期检查仍是同一份交付时不重复发送。稍后 Multica 变为 `done`，记录人工验收通过，也不再次发送相同 CL。若返工产生新 CL 或实际更新了 shelve 内容，再按新的交付证据通知。

若只有“修好了”的文本，缺少 CL 或 shelved files 核验信息，先向执行端补齐交付资料，不编造 shelve 或链接。用户只授权 shelve 时，不执行 submit、自动验收或发布。

## 3. 新轮次继续检查

服务器现有调度器把下面的提示交给工作伙伴时，skill 从持久记录继续，而不是再创建一条需求：

```text
读取 skills/multica-popo-workflow/SKILL.md。
检查 .workpartner/tasks/ 下尚未结束的任务。
只处理新出现的具体问题、已关联的真实人的答复或新的 shelve 交付。
检查已保存的发送回执；sending/unknown 先核实，不自动重发。
完成本轮检查后报告有意义的变化，不在无变化时刷屏。
```

这个提示是调度器的输入示例，不是已经创建的定时任务；安装 skill 后仍需在服务器上接好实际触发入口。
