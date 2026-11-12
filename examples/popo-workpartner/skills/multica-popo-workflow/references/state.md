# 持久记录与结果约定

下面是工作伙伴自己的本地 JSON 示例，不是 Multica API 新增的响应字段，也不是凭证文件。默认保存为工作伙伴目录下 `.workpartner/tasks/<requirement-id>/context.json`。服务器要持久保存该目录，并串行调度同一任务。

## 任务记录

```json
{
  "schemaVersion": 1,
  "multica": {
    "serverUrl": "https://multica.example",
    "workspaceId": "REPLACE_WITH_WORKSPACE_UUID",
    "requirementId": "REPLACE_WITH_REQUIREMENT_UUID",
    "issueId": "REPLACE_WITH_ISSUE_UUID",
    "externalRef": "dump:source-record-001",
    "lastObservedState": "blocked"
  },
  "source": {
    "kind": "dump",
    "id": "source-record-001",
    "url": "https://source.example/dumps/source-record-001"
  },
  "people": {
    "reporter": {
      "uid": "reporter@corp.netease.com",
      "displayName": "上报人显示名",
      "evidence": "来源工具返回的 dump 上传人账号"
    },
    "owner": {
      "uid": "owner@corp.netease.com",
      "displayName": "归属人显示名",
      "evidence": "来源工单明确的业务负责人"
    }
  },
  "pendingQuestion": {
    "phase": "execution",
    "questionId": "REPLACE_WITH_COMMENT_UUID",
    "visibleRef": "project:MUL-123:Q1",
    "recipientUid": "reporter@corp.netease.com",
    "chatType": "group",
    "chatId": "REPLACE_WITH_GROUP_ID",
    "sentMessageId": "REPLACE_WITH_CONFIRMED_POPO_MESSAGE_ID"
  },
  "deliveries": [],
  "processedInboundMessageIds": []
}
```

未创建关联任务时 `issueId` 为空；未找到人时不伪造 UID；没有待回答问题时 `pendingQuestion` 为空。一次存在多个待回答问题时保存列表，不能覆盖尚未解决的问题。

问题记录来自真实 `blockedQuestion.commentId`，或规划消息的实际消息 ID；`visibleRef` 只帮助人引用，调用 Multica 仍使用完整真实 ID。收到 POPO 答复后先按被引用消息或明确编号关联，再核对发送者 UID、聊天范围和当前问题。关联存在歧义时先澄清。

## 通知记录

每次外部发送前持久保存以下信息；写入失败就不发送：

```json
{
  "localDeliveryKey": "server-workspace-requirement-question-comment-recipient-chat",
  "event": "question",
  "robotName": "DeepSeek Harness",
  "recipientUid": "reporter@corp.netease.com",
  "receiver": "REPLACE_WITH_GROUP_ID",
  "atUids": ["reporter@corp.netease.com"],
  "bodyFile": ".workpartner/tasks/REQUIREMENT_UUID/question-Q1.txt",
  "bodyDigest": "REPLACE_WITH_DIGEST",
  "status": "sending",
  "messageId": null
}
```

key 必须包含服务器、工作区、Requirement、事件、真实问题/交付标识和目的地；不能只用“question-1”。已确认发送用 `sent` 并保存实际 `messageId`；进入发送后无法确认结果用 `unknown`。重启后 `sending` 也视为待核实，不直接重新发送。

本地 key 不会让 POPO 接口变成幂等接口。多轮次靠记录避免重复计划；并发场景还需要宿主提供同任务互斥，不能通过两个进程各读写 JSON 获得发送原子性。

对 Multica 的答复另存首次写入的 `questionCommentId`、`expectedIssueRevision`（规划期为 `expectedRevision`）、正文文件和 `idempotencyKey`。相同 POPO 入站消息只回填一次；回执丢失的重试保持这些值不变。

## 执行端的交付正文约定

要求执行端在 Multica 结果或评论里提供以下资料。这是业务正文示例，skill 从实际结果中读取；不是假定 `requirement result` 原生就有这些字段。

```json
{
  "deliveryType": "p4_shelve",
  "p4Server": "ssl:p4.example:1666",
  "branch": "//depot/project/main",
  "changelist": "123456",
  "shelved": true,
  "shelveEvidence": "已通过配置的只读 P4 工具核对该服务器上 CL 的 shelved files",
  "swarmUrl": "https://swarm.example/reviews/98765",
  "summary": "修复了导致该 dump 的无效对象访问",
  "verification": "定向回归通过；完整游戏内验证尚未完成",
  "limitations": "需要上报人在原复现环境确认"
}
```

没有 Swarm 评审时留空，不用 CL 号拼造 review ID。CL 号与 P4 服务器一起识别交付物；不同服务器相同 CL 不是同一交付。相同 CL 的 shelve 内容可能更新，重新交付需有真实的交付版本或内容证据，不能仅因任务 revision 改变就重复发送。
