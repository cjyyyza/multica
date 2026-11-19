# Agent Note: POPO CLI 注册验证码投递

Status: implemented
Kind: architecture
Protocol: agent-notes/v1

## 问题

自部署内部用户的 POPO 账号就是登录邮箱。用户明确要求通过 `popo-cli` 投递随机注册和登录验证码，不依赖邮件服务，也不要求配置机器人通道。CLI 调用可能失败，或在进程异常后无法确认对端是否收到消息，不能把进程启动或入队当成送达。

## 决策

本记录的本地 CLI 投递路径继续适用。[Windows POPO 验证码转发](2026-11-19-popo-windows-verification-relay.md) 增加跨主机的投递路径，并更新私聊回执的受理条件。

`MULTICA_POPO_CLI` 启用时，`notify.Popo` 执行 `message_send_p2p`，以规范化登录邮箱作为 `receiver`，并传入 `source=AGENT`。发送身份来自部署的 POPO CLI 登录账号。当前默认为启用，设为 `false` 时保留 SMTP、Resend 和开发日志行为，开关语义见 [POPO CLI 布尔开关](2026-11-17-popo-cli-toggle.md)。

本地模式在 API 进程的运行环境调用 CLI。消息通过权限为 0600 的临时 UTF-8 文本文件传给 CLI，避免命令行转义问题。以 POPO 明确业务受理作为用户进入填码的条件。拒绝、调用失败、缺失回执、结果不明和超时均返回发送错误。邀请邮件仍使用 EmailService，生产部署文档要求清空固定开发验证码。

`/api/config` 只暴露 `popo_verification_enabled`，不暴露 CLI 路径或登录账号。Web/Desktop 共享登录页和移动端根据该标志说明验证码会进入 POPO 私聊；旧服务端缺少字段时保持邮箱文案。POPO 消息明确标注为用于注册或登录的 Multica 验证码及 10 分钟有效期，页面不向用户暴露 CLI 实现细节。

## 替代方案

复用 Windows dj01bot 机器人通道：需要部署级桥接 ID 和机器人 ID，且不是用户要求的直接 POPO CLI 路径，因此不采用。

API 调用 `use_robot_send_msg`：会引入工作伙伴或机器人发送身份，与直接按 POPO 账号私聊的目标不同，因此使用 `message_send_p2p`。

只要 CLI 进程退出就返回成功：无法区分明确送达与结果不明，因此要求结构化成功结果和消息 ID；不确定结果不自动重发，避免重复验证码。

## 后果

内部部署可以使用 API 运行环境中已登录的 POPO CLI，或使用 Windows 上的专用转发进程发送随机验证码。本地模式的 Node.js、CLI 和认证配置都由 API 容器提供。验证码投递与工作区机器人通道各有独立配置。

## 验证与限制

2026-11-17，[POPO CLI 适配测试](../../../server/internal/notify/popo_test.go)覆盖真实 `message_send_p2p` 命令参数、邮箱接收人、UTF-8 临时文件、消息 ID、拒绝和不确定回执；[验证码服务测试](../../../server/internal/service/popo_verification_code_test.go)覆盖环境开关和消息内容，两个包的聚焦测试通过。[共享登录页](../../../packages/views/auth/login-page.test.tsx) 41 项、Core 配置 179 项、移动端 377 项及文档 362 项测试通过，相关类型检查、lint、Go vet 和编译检查通过。浏览器以模拟公开配置和发送响应验证了 POPO 输入页及私聊提示页，截图保存在未跟踪的 `test-results/popo-login/`。

[数据库用例](../../../server/internal/handler/auth_popo_test.go)覆盖规范化邮箱、随机码、成功登录和 CLI 失败不返回成功。本机没有 Docker 或本地 PostgreSQL，该数据库用例尚未实际运行，也未调用真实 POPO 或完成生产部署验收。
