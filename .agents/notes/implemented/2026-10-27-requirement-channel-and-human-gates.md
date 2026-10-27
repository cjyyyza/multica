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

## 替代方案

要求所有预览补造 POPO 会话：会把来源元数据变成人工批准权限，也无法表达原生入口，因此不采用。

同时保留通用 Issue 和 Requirement 的通知流：同一进度会发送多条消息，违背只在需要用户时通知的目标，因此按业务归属保留一条通知流。

自动选择来源“完成”状态或发布全部聊天与附件：源站工作流和公开范围需要人确认，因此只发布已审阅摘要及显式选择的状态。

为草稿再建一套任务与投递表，或在 POPO 入口启动通用执行 Agent：前者重复已有操作账本，后者绕过只读澄清与 Chat 批准，因此不采用。

## 后果

ChannelScope 是可选来源信息，不是批准凭证。通知是否已送达、易协作是否已回写与 Issue 是否完成仍分别展示，不能用入队成功冒充外部成功。

## 验证与限制

2026-10-27，[独立来源预览测试](../../../server/internal/handler/requirement_approve_test.go) 覆盖无渠道的批准与验收；[通知测试](../../../server/internal/handler/requirement_notification_test.go) 覆盖实时与恢复扫描的归属；[回写测试](../../../server/internal/handler/requirement_accept_result_test.go) 覆盖摘要、链接、来源状态、事务回滚及幂等。共享 Web/Desktop 卡片的确认、失败重试和版本变化回归通过。

草稿行为由 [本地 driver 回归](../../../server/internal/integrations/yixiezuo/manual_draft_test.go)、[服务端入口与恢复测试](../../../server/internal/handler/requirement_intake_test.go) 和 [共享配置 UI 测试](../../../packages/views/requirements/components/requirement-intake-settings.test.tsx) 覆盖。真实表单要求有效用户 ID；缺少默认指派人时使用新建表单的正数 author_id。批量创建按实际数组回执解析，明确的字段校验失败报告 failed；缺失回执保持 unknown，不自动重复创建。

2026-10-27，[真实链路验收](../../../e2e/requirement-nine-live.spec.ts) 在 WSL Docker 隔离服务完成 POPO 入站、新草稿 110156、Chat 批准、临时文件及哈希、人工审阅和易协作评论 593383 回读；用户确认四类 POPO 通知均收到且无重复。旧验收单 110152 的阻塞线程回复、同 Issue 恢复及回写也完成。成员、只读参与者、非参与者与外部用户的 API 和浏览器边界通过。

[原生桌面验收](../../../e2e/requirement-nine-desktop-live.spec.ts) 覆盖标签重开、Issue 标签复用、两个不同工作区切换，以及隔离 Docker 后端重启后的 WebSocket 重连和 Requirement 重读。任务生命周期通过现有 WebSocket 失效 Requirement 查询，流式消息及进度不触发这次重读。账号绑定仍为测试夹具；正式服务器和主桌面 daemon 未替换，原 POPO bridge 与配置已恢复。
