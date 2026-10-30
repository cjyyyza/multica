# Agent Note: 来源渠道与 Multica 人工决策分离

Status: implemented
Kind: architecture
Protocol: agent-notes/v1

## 问题

POPO 是需求的发起和通知渠道，批准属于 Multica Chat。把 POPO 会话绑定作为批准前置，会阻断来自可验证易协作预览的原生 Chat；普通 Issue 通知再镜像同一需求的状态、评论和任务进度，还会重复打扰用户。

## 决策

批准始终要求可验证的来源快照、当前参与者权限、最新候选及 revision/digest。预览携带 POPO 渠道时严格验证其工作区、机器人和当前绑定；未携带渠道时不制造绑定，也不阻止 Chat 批准、Issue 创建或验收回写。

机器人可以配置易协作默认模板；成员按个人会话选择需求模式后，普通想法进入需求草稿流程，`/new` 发起独立需求。入口只创建来源草稿和 Requirement，不进入普通 POPO Chat 执行管道。新草稿的渠道元数据不制造普通 Chat 绑定；批准验证来源操作、机器人和 Agent，继续使用原生 Requirement Chat。个人模式、命令和群聊隔离由[成员会话路由记录](2026-10-27-popo-member-conversation-routing.md)细化；本记录的来源、批准与验收边界继续适用。

来源草稿复用成员专属的 `yixiezuo_operation` 和 Windows bridge，使用独立 `draft` kind。新建前重新校验成员、Agent、来源机器人和写开关；读取模板的项目、类型及新建表单，只补齐必填值，正文使用转义后的新想法并标注待澄清，不复制原单正文、附件、关注人或父子关系。缺省指派给当前 bridge 的易协作用户。

创建调用只发送一次。响应、回读或租约结果不明时进入 unknown，不再发送创建；成员提供新单链接后，通过只读预览核对操作 marker、标题、来源和编辑版本，再恢复原操作。来源成功与 Requirement 交接采用固定幂等键；回执重放或 bridge 后续轮询恢复遗漏的交接，不创建第二个需求。

一个 Requirement Chat 最多绑定一个执行 Issue。批准后的补充与调整回到原 Issue，独立新需求另开 Chat。POPO 不提供 Requirement 批准入口；已有单工单导入、来源发布确认和只读 pm-sync 保持各自边界。

Requirement 自己的投递账本负责入口链接、待批准、具体阻塞问题、待验收与完成通知。打开 Chat、批准开始执行、返工和取消不再额外推送常规状态；普通 Issue 的评论、进度和状态流不再镜像 Requirement。业务事件与实时更新仍保留。

验收时在 Multica 审阅回写摘要、交付链接和可选来源状态。服务端验证当前来源工作流，把已审阅内容和原 Issue 链接写入同一发布操作；状态留空时不修改来源状态。验收、发布入队和通知保持同一事务及幂等重放。源站后续写入仍通过现有 Windows 易协作桥接与 lock_version 检查。

2026-10-29 新增原生 Squad 委派入口 `POST /api/workspaces/{id}/requirement-delegations`。调用方提交外部引用、Squad 和结构化 Candidate，服务端以来源快照固定 Squad 及其队长，不开放执行 Agent 或 Run 选择。候选直接处于 `awaiting_approval`，不创建 Plan task、Requirement Chat 或 Issue；`createdByTaskId` 因此为 null，原生记录禁止 open。参与者仍在 Multica 按 revision/digest 批准，批准后事务内创建唯一 `requirement_delegation` Issue 和 Squad leader deferred Run，提交后再 promote。Squad 与队长由来源 digest 和 Candidate canonical 共同约束，队长变化或 Squad 归档会阻止批准。

从想法启动的原生入口为 `POST /api/workspaces/{id}/requirement-requests`，使用 `agentId` 或 `squadId` 二选一。来源分别为 `agent_request` 和 `squad_request`；固定智能体或小队队长，原子创建 Requirement、Chat 和真实只读 Plan，候选带实际 `createdByTaskId`。批准前没有 Issue 或 Execute。旧 `requirement-delegations` 保留外部已完成候选的含义，不能把它的空 task ID 当作 Agent 规划。

外部调用方通过个人 PAT 复用正式 Requirement 动作账本。参与者列表接口支持 externalRef 和分页以查回丢失回执；需求消息入口按幂等键和 expectedRevision 原子发送、排队 Plan 并推进 revision，补充输入使旧候选失效。普通 direct Chat 的入队实现提取成事务内 helper，保留原有输入批次与运行时锁，需求回复按 Chat、Requirement 的顺序加锁。批准或取消后不接受规划输入。

原生委派验收只完成 Requirement 和 Issue，不制造易协作 import、预览或发布操作。单 Agent 的批准、执行和返工保持该 Agent 绑定；Squad 的返工重新创建当前操作者的队长运行。索引替换通过 9099 先并发创建新名称的宽索引，再由 9100 退休旧索引；回滚先恢复旧索引再删除新索引。9097/9098 文件名与历史记录保留，runner 对已有有效索引跳过破坏性步骤；已中断且缺少索引时先恢复保护。deferred Run 使用独立的 `requirement_delegation` 上下文标记，并与 media-pending 一样参与 pending 唯一索引、评论合并、去重、promotion occupant 和 overlay 恢复，避免提交到 promote 的窗口产生第二条 Run。

执行任务仍绑定已批准的同一 Requirement 和 Issue。Squad 成员及有真实队长委派血缘的 guest worker 可以领取、读取需求和回报；其他 Squad、无委派的外部 Agent、伪造 leader 角色和错绑 Issue 不获得这项权限。单 Agent Requirement 与 Plan task 保留原来的 Agent 绑定。入口先验证所有参与者仍是工作区成员，不能把唯一 approver 指向外部用户。

Squad 的一轮完成可能只是派工或部分交付，因此不自动进入待验收。固定 Squad 的运行中队长在汇总后通过既有 Issue status 命令显式设为 `in_review`；同 Issue 及后代 Issue 仍有待领取或执行中的任务时拒绝送审，`done` 仍只属于人工验收。原生 Issue 和共享 Web/Desktop 卡片提供同一需求验收面板，批准展示 Squad 与队长，验收不提供来源发布控件。尚未绑定评论线程的原生预领取 Run 接收第一条评论后采用其线程，后续继续遵守线程隔离。评论合并在 UPDATE 目标行上重新检查线程及可合并状态，不能依赖等待行锁前的子查询快照。返工 Run 在创建时绑定拒绝评论，确定交付拒绝原因并固定线程。

外部编排通过通用 `multica requirement --strict` CLI 或 HTTP API 完成目标发现、原生 Plan、澄清、执行答复和结果读取。严格模式要求显式 server、token 和 workspace，缺失时不回退本机 profile；人工 CLI 保留 profile 用法。列表使用单次查询生成轻量摘要，Candidate、参与者、运行及交付由详情接口读取。成员凭证的规划澄清与执行期答复分开命名；任务凭证仍仅能读取自身需求、交接 Plan 候选或上报执行阻塞，不能调用成员写入口。批准与验收继续由人在 Multica 决定。个人 PAT 继承成员角色；同一用户同时为 approver 时该 PAT 技术上也能批准。协调 Agent 不自批是操作约定，不是 PAT 的权限隔离；强制职责分离需要独立的凭证权限边界。

执行答复必须绑定当前阻塞问题、被点名的当前参与者及 Issue revision。评论、恢复回执和幂等账本在同一事务内落盘；已启用的唤醒复用评论触发器，恢复运行中的停用唤醒补记回执，不叠加第二套事件入口。幂等重放重新验证当前成员及参与者权限，但不因 Issue 后续状态推进再次写入或恢复。恢复任务从已验证的来源任务保留 Squad 与队长身份，避免队长恢复后丢失派工和送审权限，也不把成员提升为队长。

## 替代方案

要求所有预览补造 POPO 会话：会把来源元数据变成人工批准权限，也无法表达原生入口，因此不采用。

同时保留通用 Issue 和 Requirement 的通知流：同一进度会发送多条消息，违背只在需要用户时通知的目标，因此按业务归属保留一条通知流。

自动选择来源“完成”状态或发布全部聊天与附件：源站工作流和公开范围需要人确认，因此只发布已审阅摘要及显式选择的状态。

为草稿再建一套任务与投递表，或在 POPO 入口启动通用执行 Agent：前者重复已有操作账本，后者绕过只读澄清与 Chat 批准，因此不采用。

原生委派改用普通 backlog Issue 或伪造 Plan task：前者不进入正式 Requirement 批准与验收账本，后者把外部输入伪装成规划运行产物，因此不采用。Squad deferred Run 复用 media-pending 标记：会把评论合并和 overlay 恢复误认为附件等待，因此使用独立标记并同步并发规则。

## 后果

ChannelScope 是可选来源信息，不是批准凭证。通知是否已送达、易协作是否已回写与 Issue 是否完成仍分别展示，不能用入队成功冒充外部成功。原生委派没有来源发布状态，验收只表示 Multica 内的人工决定。

外部 Candidate 的空 `createdByTaskId` 保留真实来源。读取端以 `requirement-candidate-provenance-v1` 声明兼容；未声明的成员读取返回 `426 requirement_client_upgrade_required`，由旧客户端已有错误面板提示升级或使用当前网页，不发送会被静默丢弃的候选。真实 Plan Candidate 仍保留 task ID，既有流程不受影响。

## 验证与限制

2026-10-27，[独立来源预览测试](../../../server/internal/handler/requirement_approve_test.go) 覆盖无渠道的批准与验收；[通知测试](../../../server/internal/handler/requirement_notification_test.go) 覆盖实时与恢复扫描的归属；[回写测试](../../../server/internal/handler/requirement_accept_result_test.go) 覆盖摘要、链接、来源状态、事务回滚及幂等。共享 Web/Desktop 卡片的确认、失败重试和版本变化回归通过。

草稿行为由 [本地 driver 回归](../../../server/internal/integrations/yixiezuo/manual_draft_test.go)、[服务端入口与恢复测试](../../../server/internal/handler/requirement_intake_test.go) 和 [共享配置 UI 测试](../../../packages/views/requirements/components/requirement-intake-settings.test.tsx) 覆盖。真实表单要求有效用户 ID；缺少默认指派人时使用新建表单的正数 author_id。批量创建按实际数组回执解析，明确的字段校验失败报告 failed；缺失回执保持 unknown，不自动重复创建。

2026-10-27，[真实链路验收](../../../e2e/requirement-nine-live.spec.ts) 在 WSL Docker 隔离服务完成 POPO 入站、新草稿 110156、Chat 批准、临时文件及哈希、人工审阅和易协作评论 593383 回读；用户确认四类 POPO 通知均收到且无重复。旧验收单 110152 的阻塞线程回复、同 Issue 恢复及回写也完成。成员、只读参与者、非参与者与外部用户的 API 和浏览器边界通过。

[原生桌面验收](../../../e2e/requirement-nine-desktop-live.spec.ts) 覆盖标签重开、Issue 标签复用、两个不同工作区切换，以及隔离 Docker 后端重启后的 WebSocket 重连和 Requirement 重读。任务生命周期通过现有 WebSocket 失效 Requirement 查询，流式消息及进度不触发这次重读。账号绑定仍为测试夹具；正式服务器和主桌面 daemon 未替换，原 POPO bridge 与配置已恢复。

2026-10-29，托管数据库实际应用 9093–9098 迁移。[原生 Squad 委派测试](../../../server/internal/handler/requirement_delegation_test.go) 覆盖批准前零 Run、禁止 open、幂等、Squad Issue 与 leader Run、deferred 评论合并和 promotion；[链路与权限回归](../../../server/internal/handler/requirement_delegation_lifecycle_test.go) 使用真实数据库覆盖批准、评论派工、成员领取、队长汇总送审、人工返工和验收，并验证参与者成员资格、跨 Squad/Issue 拒绝、guest 委派、子任务待完成及派工轮次不提前送审。Requirement、Squad、P4 describe/filelog 的相关 Go 测试通过；三个依赖 symlink 的 Windows 用例跳过。

[浏览器与 API 联测](../../../e2e/requirement-squad-delegation.spec.ts) 通过个人 PAT 模拟外部提交，实际点击批准与验收，使用服务端签发的任务 Token 完成队长和成员的 HTTP 领取、评论、状态变更及完成回调，回读唯一 Issue、唯一验收事件和零易协作写回。该用例的执行端是测试 runtime。

[原生 Plan 入口回归](../../../server/internal/handler/requirement_request_test.go) 覆盖单 Agent 和 Squad 的 Plan、批准、执行及验收，真实 Plan 任务来源，幂等回复、输入批次、旧候选失效、分页可见性与非法目标。外部 OpenCode 使用 [协调示例](../../../examples/opencode/agents/multica.md) 中的正式 CLI；[HTTP helper](../../../examples/opencode/multica.mjs) 保留独立示例语义。此前显式开启的真实 OpenCode 用例调用 HTTP helper，不能作为官方成员 CLI 的真实验收。当前 [OpenCode 原生链路用例](../../../e2e/requirement-opencode-live.spec.ts) 已改为官方 CLI 的严格模式，并将 Agent 与 Squad 分成独立用例；本次尚未重跑真实用例。

[执行答复回归](../../../server/internal/handler/requirement_reply_test.go) 覆盖原生单 Agent 与 Squad 从 Plan、批准、阻塞答复、恢复到验收，两个并发相同请求只写一条评论和一次恢复，事务回滚、已提交但响应丢失后的重放、点名对象与参与者撤销权限。[正式 CLI 回归](../../../server/cmd/multica/cmd_requirement_member_test.go) 覆盖中文文件、成员与任务凭证隔离、幂等键、分页、结果读取、人工决策状态等待、取消、超时和未知状态；[API 契约回归](../../../packages/core/api/client-requirements.test.ts) 拒绝 malformed 答复响应，并对旧后端缺失的阻塞字段默认关闭答复权限。这些测试使用测试 runtime，不代表本次重新运行真实 OpenCode 或生产部署。

此前 HTTP helper 真实用例使用 OpenCode 2.2.1 协调、Claude CLI 执行 Multica Agent，单 Agent 与 Squad 均通过原生 Plan、浏览器批准、实际修复、回归测试及浏览器验收到 done。Squad 包含队长、成员和回报后的队长复核三个执行轮次；批准前文件哈希不变、原失败用例转绿、测试文件未改、批准与验收各一条、易协作回写零条。测试使用隔离账号、工作区和 daemon profile，不证明 POPO 工作伙伴收发或生产部署成功。

本轮针对性回归覆盖索引逐步升级与回滚、历史索引替换中断恢复、行锁等待后的线程和派发状态复核、单 Agent 与 Squad 返工输入、旧客户端明确升级响应、CLI 凭证来源、列表摘要及审批来源展示。该范围不包含真实 OpenCode 模型或 POPO 工作伙伴收发。来源快照当前仍按已知结构及摘要严格校验；新增语义字段需要先定义版本和 canonical/digest 协议并部署可读取该版本的服务，不通过忽略字段绕开完整性校验。
