# Agent Note: OpenCode 2 以原生结果确认完成

Status: implemented
Kind: architecture
Protocol: agent-notes/v1

## 问题

OpenCode 2.2.1 的 `run --format json` 兼容流会发送 step_start、text 和 tool_use，但成功结束时可以没有 step_finish。仅沿用旧流协议会把实际成功的协调调用报成失败；只凭文本或进程退出码又会放过中断和未完成的工具续轮。

## 决策

仅对内置 OpenCode 2，在进程正常退出、提示完整写入、没有取消且流缺少完成信号时，使用同一命令、环境、目录和服务连接执行原生 `session export` 只读回查。独立服务保持 standalone，不访问或重启共享服务。回查受本轮取消及二十秒上限约束。

结果必须属于流中的同一 session，所有已观测到的 assistant message ID 都必须存在且已完成，最后一条 assistant 必须属于本轮并以 stop 结束，末尾必须为 idle/succeeded，同时包含实际 usage。缺失、失败、取消、旧轮次、工具续轮和空回执继续失败关闭。恢复会话只累计本轮观测到的消息计数，不能重复累计历史 token。

旧版本和自定义运行时不改变原有流判定，进程非零退出、取消、超时及显式错误不通过结果回查转成成功。外部控制器的 MULTICA 凭证显式加入本轮环境；继承的 MULTICA 命名空间仍被过滤，不能把宿主账号配置泄漏到普通任务。

## 替代方案

把 EOF、退出码零或最终文本视为完成：缺少服务端成功证据，不能采用。

读取最近会话或累计整个导出的 usage：会混入其他会话、恢复前的轮次或重复计费，因此按本轮消息身份核对。

## 后果

没有流式完成事件的原生运行增加一次只读结果查询；查询失败或身份不完整时保留失败结果，不以退出码冒充成功。此协议改变完成确认，不授予规划、批准或验收权限。

## 验证与限制

[原生结果回归](../../../server/pkg/agent/opencode_v2_result_test.go) 覆盖终态、消息身份、恢复历史、独立与指定服务连接，以及 Execute 实际接线。默认回归使用测试二进制作为假 CLI，不读取安装的 OpenCode 或账号。真实调用通过显式开启的 [协调驱动](../../../server/pkg/agent/opencode_multica_integration_test.go) 和 [Requirement 链路验收](../../../e2e/requirement-opencode-live.spec.ts) 执行。该完成协议不代表 OpenCode provider 已支持 Requirement 原生 Plan。
