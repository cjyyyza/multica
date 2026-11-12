# 命令参考

所有 `REPLACE_WITH_*` 和示范账号都需替换。正文文件放在工作伙伴当前工作目录内；以下命令对应现有 CLI，并不增加 Multica 或 POPO 的参数。执行前读取 AGENTS.md 中的实际角色与路由。

## Multica

成员环境变量由服务器注入：`MULTICA_SERVER_URL`、`MULTICA_WORKSPACE_ID`、`MULTICA_TOKEN`、`MULTICA_APP_URL`。不使用执行运行的 `mat_` 令牌作为协调成员凭证。

发现目标与创建只读规划：

```bash
multica requirement --strict targets
multica requirement --strict create --agent REPLACE_WITH_AGENT_UUID --external-ref dump:source-record-001 --idea-file .workpartner/request.md --idempotency-key dump-source-record-001-plan
```

使用 Squad 时将 `--agent ...` 换成 `--squad ...`，两者二选一。请求正文至少包含下面的约定；具体问题和业务目标由用户请求确定：

```text
来源：实际 dump/工单链接，附执行端可访问的日志或附件标识。
先只读分析并形成规划，等待人在 Multica 批准。
执行中需要外部信息时，读取当前 Requirement 的 requesterId，
使用现有 requirement block 将问题交给该协调成员，明确问题原文和目标角色。
由工作伙伴询问实际 POPO 上报人，再以协调成员身份回填答案。
修复交付为 shelve：记录 P4 服务器/分支、实际 CL、shelved files 核验结果、
真实 Swarm 链接（如果有）、测试结果和已知限制；不自动 submit。
```

恢复创建回执、读取状态和结果：

```bash
multica requirement --strict list --external-ref dump:source-record-001
multica requirement --strict get REPLACE_WITH_REQUIREMENT_UUID
multica requirement --strict messages REPLACE_WITH_REQUIREMENT_UUID
multica requirement --strict result REPLACE_WITH_REQUIREMENT_UUID
multica requirement --strict wait REPLACE_WITH_REQUIREMENT_UUID --until awaiting_approval,blocked,in_review,done,cancelled --timeout 2m --interval 10s
```

`get` 保留 `blockedQuestion.commentId/content/targetUserId/canReply` 等实际响应字段；`result` 聚合 Requirement、关联任务、运行和评论。任务链接使用 `MULTICA_APP_URL/{workspaceSlug}/chat?requirement={requirementId}`，`workspaceSlug` 从真实工作区结果中取得，不用工作区 UUID 代替 slug。

等待超时表示本次等待结束，不表示任务失败；再次检查时读取当前状态。规划期需要读取消息才能知道具体问题，不能仅凭 `clarifying` 状态自动发问。消息有分页时使用返回游标读取，不声称已读完未读取的消息。

首次规划补充的 revision、正文和 key 固定后执行：

```bash
multica requirement --strict clarify REPLACE_WITH_REQUIREMENT_UUID --expected-revision 5 --content-file .workpartner/answer.txt --idempotency-key answer-source-message-001
```

首次执行答复先核对 `blockedQuestion.canReply`、当前 `commentId` 和关联任务的 `issue.revision`，再固定这些值执行：

```bash
multica requirement --strict reply REPLACE_WITH_REQUIREMENT_UUID --question-comment REPLACE_WITH_QUESTION_COMMENT_UUID --expected-issue-revision 8 --content-file .workpartner/answer.txt --idempotency-key answer-source-message-001
```

数字 5、8 只是演示，必须用真实响应。丢失回执的重试保留同一正文、revision、问题 ID和 key。409/权限错误不通过换 key、换成员或猜新 revision 规避。`canReply=false` 时由真正有权限的参与者在 Multica 答复。

已有普通任务可使用下列读取接口，继续跟踪同一条任务：

```bash
multica issue get REPLACE_WITH_ISSUE_ID --output json
multica issue runs REPLACE_WITH_ISSUE_ID --output json
multica issue comment list REPLACE_WITH_ISSUE_ID --output json
```

普通任务转交回复需绑定实际评论线程，`issue comment add --parent ... --content-file ...`；是否唤醒执行端取决于已有评论派发/唤醒规则，不保证任意评论都恢复运行。

## POPO 查人和群成员

使用来源 skill 返回的实际姓名或账号查询，不根据拼音猜 UID：

```bash
popo-cli popo userinfo_search query=REPLACE_WITH_SOURCE_ACCOUNT type=2 page=0 pageSize=10 searchRange=1 includeRobot=true
popo-cli popo team_info_query tid=REPLACE_WITH_GROUP_ID type=2
```

读取查询结果业务层 `search_results.person[].content` 中的 `uid`、`showName`、`name`。发件人的完整工作伙伴名直接取自 AGENTS.md，不通过该查询搜索工作伙伴。

群成员结果有 UID/邮箱时核对 UID；仅有姓名时结合已查回的显示名核对，无法可靠匹配就报告缺口。不要假定所有 POPO 版本都返回邮箱。多条同名结果不能直接取第一条。

## Linux 服务器发送

先由文件写入工具准备 UTF-8 正文 `.workpartner/outbound.txt`。群消息正文需含查询得到的 `@显示名`，并把实际 UID 数组写入 `.workpartner/at-uids.json`，例如 `["reporter@corp.netease.com"]`。该账号仅是示例，不作为默认接收人。

```bash
# Direct message: receiver is the resolved person's actual UID.
popo-cli popo use_robot_send_msg "robotName=DeepSeek Harness" receiver=REPLACE_WITH_RECIPIENT_UID message=@text:.workpartner/outbound.txt msgType=text

# Group message: receiver is the configured group, atUids contains actual person UIDs.
popo-cli popo use_robot_send_msg "robotName=DeepSeek Harness" receiver=REPLACE_WITH_GROUP_ID message=@text:.workpartner/outbound.txt msgType=text isAtAll=false atUids=@file:.workpartner/at-uids.json
```

两条命令按发送方式选择一条；工作伙伴名称也必须与部署配置一致。草稿保留用于同一消息的核验，不在命令行展开正文或拼接 JSON 参数。`localDeliveryKey` 只记在本地记录，不能当作 POPO CLI 的参数。

## Windows 服务器群消息

以下变量由前面的来源查询与 POPO 查询填入，正文草稿已包含实际 `@显示名`。临时消息和数组文件用 UTF-8 无 BOM；若保存成 `.ps1`，脚本本身按 PowerShell 5.1 的要求保存为 UTF-8 BOM。

```powershell
$robotName = "DeepSeek Harness"
$groupId = "REPLACE_WITH_GROUP_ID"
$recipientUid = "REPLACE_WITH_RESOLVED_POPO_UID"
$bodyPath = Join-Path (Get-Location).Path ".workpartner/outbound.txt"
$messagePath = Join-Path ([IO.Path]::GetTempPath()) ("popo-msg-" + [guid]::NewGuid().ToString("N") + ".txt")
$atPath = Join-Path ([IO.Path]::GetTempPath()) ("popo-at-" + [guid]::NewGuid().ToString("N") + ".json")
try {
    $body = [IO.File]::ReadAllText($bodyPath, [Text.Encoding]::UTF8)
    [IO.File]::WriteAllText($messagePath, $body, [Text.UTF8Encoding]::new($false))
    $atUids = @($recipientUid)
    $atJson = ConvertTo-Json -InputObject $atUids -Compress
    [IO.File]::WriteAllText($atPath, $atJson, [Text.UTF8Encoding]::new($false))
    popo-cli popo use_robot_send_msg "robotName=$robotName" "receiver=$groupId" "message=@text:$messagePath" msgType=text isAtAll=false "atUids=@file:$atPath"
    if ($LASTEXITCODE -ne 0) { throw "POPO invocation failed; reconcile the delivery outcome before retrying" }
} finally {
    Remove-Item -LiteralPath $messagePath, $atPath -ErrorAction SilentlyContinue
}
```

Windows 私聊同样写 UTF-8 临时正文，用 `receiver=<实际用户UID>`，省略 `atUids` 和群 @ 文本；仍用 `try/finally` 清理临时文件。

## 回执和重复发送

发送前保存正文、路由和本地 key，记录 `sending`。检查完整 POPO CLI 响应，业务状态成功并得到实际 `msgId` 才改为 `sent`。仅有最外层 `ok=true` 或进程退出码为零还不够。

超时、崩溃、返回结构异常或缺少 `msgId` 时记录 `unknown`。先用已安装 POPO 消息查询能力核实同一目标、工作伙伴身份、时间和正文；按其查询 skill 同时检查远程和 PC 来源，缺失的一方如实记录。没有找到不等于确定未发送。不能自动重发或改用个人身份发送。
