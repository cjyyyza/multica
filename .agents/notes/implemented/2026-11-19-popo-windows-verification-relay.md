# Agent Note: Windows POPO 验证码转发

Status: implemented
Kind: architecture
Protocol: agent-notes/v1

## 问题

API 部署在 Linux 容器，登录态保存在 Windows 的 POPO CLI 用户配置。部署要求继续用该 Windows 用户发验证码。工作区机器人凭证的授权范围是工作区内的会话，全站登录投递需要单独的发送权限。[POPO CLI 注册验证码投递](2026-11-17-popo-verification-code-delivery.md) 的本地运行假设和消息 ID 要求需补充。

## 决策

Docker 与 Windows 桌面端同机的部署采用[同机 Docker 发码](2026-11-19-popo-local-docker-verification.md)。本记录说明显式配置的 HTTPS 投递路径。

用已登录 POPO CLI 的 Windows 用户运行 `multica popo verification-relay`。API 配置 HTTPS 地址和专用共享密钥后，将邮箱和验证码交给该进程。转发端只接受受认证的验证码请求，调用固定的 `message_send_p2p`，在 POPO 确认受理后返回 HTTP 204。API 根据该结果推进填写验证码。验证码保持原来的生成、有效期和校验逻辑。

`message_send_p2p` 的明确业务受理允许空数据和缺省消息 ID，见[已有 POPO 私聊验收样例](../../../server/internal/handler/requirement_external_integration_test.go)。机器人通知仍由其发送路径校验正式回执。转发配置有误、连接失败或受理未确认时，API 返回发送错误。两个转发配置均留空时，API 使用本地 CLI 路径。

## 替代方案

为 Linux 容器配置独立 CLI 登录，改动集中在部署配置，但需要单独认证。用户选择沿用 Windows 登录态。工作区机器人桥接使用不同的授权范围，保留给工作区会话。

## 后果

Windows 侧持有 CLI 登录态。API 需要能访问 Windows 的内网 HTTPS 地址，并信任其证书。投递依赖 Windows 用户进程和有效的 POPO 登录态。部署命令及错误分类见 [Windows 转发部署说明](../../../docs/popo-windows-verification-relay.md)。

## 验证与限制

[转发测试](../../../server/internal/notify/popo_verification_relay_test.go) 使用隔离 HTTPS 服务和假 CLI，覆盖已授权发码、密钥拒绝和非法请求。[POPO CLI 适配测试](../../../server/internal/notify/popo_test.go) 覆盖空私聊受理回执与机器人回执。实际 Windows 到 POPO 的发码以及线上 Docker 到 Windows 的连通性属于部署验收。
