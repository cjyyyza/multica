# Agent Note: 同机 Windows Docker 发码

Status: implemented
Kind: architecture
Protocol: agent-notes/v1

## 问题

Windows 服务器同时运行 Docker API 和 Multica 桌面端，POPO CLI 登录态已在该 Windows 用户会话中。用户要求仅服务器这台 Windows 自动接入，免去部署时填写转发地址、共享密钥和证书。

## 决策

Windows 桌面主进程在登录前用内置 CLI 启动发码进程。进程先校验现有 API 地址属于当前主机，再经本机 Docker 命名管道查找有镜像标识且发布了同一 API 端口的唯一容器。选定后用固定的 `docker exec -i` 子命令接入容器内私有 Unix socket。访问 Docker 的 Windows 用户同时持有 POPO CLI 登录态。验证码请求与业务受理回执走同一连接，桌面退出时发码进程随之结束。

用户确认升级版 Docker 镜像默认提供同机能力。显式关闭 POPO、容器内已有 CLI、已配置 [Windows HTTPS 转发](2026-11-19-popo-windows-verification-relay.md) 的入口沿用各自选择。自动路径采用既有开关和镜像内部标识。连接未建立或结果不明时返回发送错误。

## 替代方案

复用工作区 daemon 的登录凭证会依赖先完成登录。沿用显式 HTTPS 转发需要在同机部署中维护额外的地址、密钥和证书。

## 后果

服务器桌面端需要保持运行，已有 API 地址应指向 Windows 本机与发布的 API 端口。遇到远端地址或存在歧义的容器映射时，客户端跳过相应发码入口。Windows 端重连时只接后续请求。部署方法见 [Windows 验证码部署说明](../../../docs/popo-windows-verification-relay.md)。

## 验证与限制

[CLI 检测测试](../../../server/cmd/multica/cmd_popo_docker_test.go) 覆盖本机地址、容器端口和本机 Docker 连接。[发码连接测试](../../../server/internal/notify/popo_verification_docker_test.go) 使用模拟 POPO CLI，覆盖受理、失败和断线重连。真实 Windows Docker Desktop 与 POPO 账号属于部署验收。
