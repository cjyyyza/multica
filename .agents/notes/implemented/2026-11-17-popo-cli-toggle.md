# Agent Note: POPO CLI 布尔开关

Status: implemented
Kind: architecture
Protocol: agent-notes/v1

## 问题

本机已经能够从 `PATH` 直接使用已登录的 `popo-cli`，产品配置不需要再暴露可执行文件路径。`MULTICA_POPO_CLI` 同时被验证码和评论成员提及通知使用，若只改其中一处，布尔值会被其他调用方误当成可执行文件。本记录部分取代 [POPO CLI 验证码默认启用](2026-11-17-popo-verification-default.md) 的路径值和显式空值关闭规则，并更新 [POPO CLI 注册验证码投递](2026-11-17-popo-verification-code-delivery.md) 的启用条件。

## 决策

`MULTICA_POPO_CLI` 是纯布尔开关，默认 `true`；未设置、空值和 `true` 都启用，只有大小写不敏感的 `false` 关闭。启用时固定执行 `PATH` 中的 `popo-cli`，不再接受产品配置中的自定义路径。关闭验证码投递时继续使用邮件服务或开发日志；关闭评论通知时跳过发送且不影响评论。

Compose 和配置示例默认写入 `true`。需要显式外部投递的验收测试使用独立的 `MULTICA_ACCEPTANCE_POPO_CLI` 可选路径，默认同样为 `popo-cli`，避免与产品开关复用。

## 替代方案

同时接受布尔值和路径：同一变量含义含糊，且无法可靠区分自定义命令与开关，因此不采用。

新增独立开关并保留原路径变量：会增加一套部署配置，不符合本次简化目标。

显式空值继续关闭：不符合默认开启的布尔开关语义，因此空值与未设置保持一致。

## 后果

部署只需保证 API 或 Windows 执行环境能够从 `PATH` 调用已登录的 `popo-cli`。产品运行时不支持自定义可执行文件路径；需要关闭 POPO 时显式设置 `MULTICA_POPO_CLI=false`。机器人通道仍由 `MULTICA_POPO_ENABLED` 独立控制，CLI 失败仍不会自动回退或重试。

## 验证与限制

2026-11-17，[验证码服务测试](../../../server/internal/service/popo_verification_code_test.go)覆盖未设置、空值、`true` 和大小写不同的 `false`；[评论通知测试](../../../server/cmd/multica/cmd_issue_popo_test.go)覆盖 CLI 失败不影响评论及 `false` 跳过通知；[守护进程测试](../../../server/internal/daemon/task_cli_test.go)覆盖开关透传。相关 Go 聚焦测试、`cmd/server` 测试、前端类型检查和文档 362 项测试通过。本机 `popo-cli` 版本和认证状态已确认可用，未发送真实测试消息，也未进行 Docker 部署验收。
