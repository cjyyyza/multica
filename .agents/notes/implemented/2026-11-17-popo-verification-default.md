# Agent Note: POPO CLI 验证码默认启用

Status: implemented
Kind: architecture
Protocol: agent-notes/v1

## 问题

内部部署希望无需重复配置即可通过 POPO CLI 投递验证码，同时仍需为使用 SMTP、Resend 或本地开发日志的部署保留明确的退出方式。本记录部分取代 [POPO CLI 注册验证码投递](2026-11-17-popo-verification-code-delivery.md) 中“只有部署者设置变量才启用”的默认行为，其余投递、回执和独立通道约束继续适用。后续 [POPO CLI 布尔开关](2026-11-17-popo-cli-toggle.md) 将变量改为纯布尔语义；本记录的自定义路径和显式空值关闭规则不再适用，默认启用结论仍保留。

## 决策

未设置 `MULTICA_POPO_CLI` 时，API 默认使用 `PATH` 中的 `popo-cli`。部署者可设置自定义 CLI 或 `bin/popo.js` 路径；显式设置为空或纯空白时禁用 POPO 投递，继续使用邮件服务或开发日志。自部署 Compose 使用未设置才生效的默认值，显式空值不会被模板重新替换为默认 CLI。

## 替代方案

未设置和显式空值都强制启用：会移除邮件服务和本地日志模式的退出方式，因此不采用。

只修改 `.env.example` 和 Compose 默认值：直接启动 API 时行为仍不同，因此在服务构造入口统一默认值。

## 后果

默认部署需要 API 运行环境提供 Node.js、可执行的 `popo-cli` 和已登录配置；缺失或发送失败仍不会自动回退邮件。不需要 POPO 的部署必须显式将 `MULTICA_POPO_CLI` 留空。机器人通道仍由独立的 `MULTICA_POPO_ENABLED` 控制。

## 验证与限制

2026-11-17，[验证码服务测试](../../../server/internal/service/popo_verification_code_test.go)覆盖未设置时的默认 CLI、显式空值和空白关闭、自定义路径及空接口语义，聚焦 Go 测试通过。配置示例、Compose、启动日志和六种语言的认证及环境变量文档同步更新。未调用真实 POPO，也未进行 Docker 部署验收。
