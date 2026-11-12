---
name: multica-popo-workflow
description: "Coordinates Multica engineering tasks through a POPO work partner, including dump clarification, human answers and shelved fix delivery. Use when a user reports a dump or bug, asks for Multica progress, answers a pending question, or needs a shelve sent to its owner."
---

# Multica 与 POPO 协调

按工作伙伴 AGENTS.md 中的业务角色和路由组织流程。Multica 保存需求、任务、问题和交付结果，POPO 用于联系具体的人。

## 读取输入

1. 读取工作伙伴 AGENTS.md，确认工作伙伴名、发送方式、目标群和 Multica 目标均已配置。
2. 核对显式 Multica 环境变量及 POPO 登录环境；不打印令牌，不替换为其他 profile 的凭证。
3. 区分入口：新 dump/bug、周期检查、收到 POPO 回复。读取对应持久记录；不存在才创建新记录。
4. 保存可信通道提供的原消息 ID、发送者 UID、聊天 ID和引用消息 ID。身份来自元数据，不从“我是某某”的正文推断。
5. 工具命令见 [commands.md](references/commands.md)，记录约定见 [state.md](references/state.md)，完整示范见 [scenarios.md](references/scenarios.md)。

## 新 dump 或 bug

1. 读取来源资料，按 AGENTS.md 分别确定上报人和交付对象，核实 POPO UID，保存角色和依据。
2. 已有 Requirement 时接续原记录；已有关联任务时先检查其归属，不为同一修复重复创建任务。
3. 新需求使用稳定 `externalRef` 和幂等键调用 `requirement --strict create`；只选择一个 Agent 或 Squad。
4. 请求正文包含来源链接/附件、缺少的信息、目标和交付约定：执行端上报具体问题并向协调成员请求输入，修复结果包含实际 shelve 信息。
5. 保留 Requirement ID。创建回执丢失时使用原请求重放或按 `externalRef` 查回。
6. 待批准时给出 Multica 链接，由人在 Multica 批准；不自动批准、验收或并行实施。

## 检查待处理事项

1. 按任务串行读取 `requirement get`；需要规划问答时读取 `messages`，需要交付时读取 `result`。
2. 规划阶段从实际消息判断是否有具体待回答问题；`clarifying` 状态本身不等于出现了新问题。
   到达 `awaiting_approval` 时展示候选和 Multica 链接，记录已提示的候选 ID，由人在 Multica 决定。
3. 执行阶段读取 `blockedQuestion`，用其 `commentId` 标识问题，并保存当前任务 revision。
4. 新问题按角色找到接收人，发出包含问题原文、所需补充信息、任务链接和关联编号的 POPO 消息。
5. 动态调用 `use_robot_send_msg`；群聊同时带实际 `atUids` 和可见的 `@显示名`。保存实际发送回执。
6. 已发送、发送中或结果未知的同一通知不重复发送；问题已失效则不再追问旧问题。
7. `in_review` 时执行下面的交付流程；`done` 记录人工验收结果，不重复投递相同 shelve；取消时结束跟踪。
8. 运行失败时读取实际错误并报告，不把失败解释成修复完成。

## 收到 POPO 答复

1. 按引用消息 ID或关联编号查找待回答问题，并核对真实发送者 UID 和聊天范围。
2. 多条任务均可能匹配、回复来自其他人或没有可靠关联时，先澄清，不猜测答案属于哪条任务。
3. 重新读取 Requirement，确认问题仍有效；保存这条答案对应的原始 POPO 消息 ID。
4. 规划期用 `clarify`；执行期仅在当前 `blockedQuestion` 匹配且 `canReply=true` 时用 `reply`。
5. 答案标明真实来源，转交人的原意，不补造结论。首次写入时固定正文、revision 和幂等键。
6. 回执丢失时以完全相同的请求重试；409 时重读并处理冲突，不更新 revision 后盲目重发。
   已有待核实的 Multica 答复操作先重放其固定请求取得回执；不要把已成功引起的状态变化当成另一个新答复。
7. 记录 Multica 接收回执后再告知已转交；“已转交”不代表执行已成功，后续继续读取实际状态。

## 交付 shelve

1. 从 `result`、运行记录或评论读取执行端记录的 CL、P4 服务器/分支、Swarm 链接和验证结果。
2. 用现有 P4/Swarm 只读工具或明确的执行端核验记录确认有 shelved files；只有“完成”二字不算交付证据。
3. 从来源和本地绑定读取 bug 归属人，按 AGENTS.md 核实接收 UID；不默认使用刚才回答问题的人。
4. 发送“修复已 shelve，待验证”，附实际 CL、真实评审链接、测试结论、已知限制和 Multica 链接。
5. 以服务器、任务、P4 服务器、CL、交付内容和接收人为依据识别同一交付，保存回执；同一交付不重复发送。
6. CL 更新或交付内容确有变化时可作为新交付；正常验收不重复发送上一份交付。

## 运行边界

- 本 skill 不会自行调度下一轮；由服务器调度器、工作伙伴事件入口或当前轮次的有界等待触发。
- 同任务串行处理，并跨轮次持久保存状态；持久化失败时不继续外部发送。
- 不自动邀请群成员、不切换发送身份、不提交 P4、不调用规划批准或结果验收接口。
- 普通任务使用其实际 `issue` 接口；没有 Requirement 绑定时不伪造 ID 调用 `reply`，评论也不保证自动续跑。
