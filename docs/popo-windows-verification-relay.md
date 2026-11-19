# 用 Windows 上的 POPO CLI 发送 Multica 验证码

Docker 中的 Multica API 通过 HTTPS 请求 Windows 上的 `multica popo verification-relay`。转发进程以已经登录 POPO CLI 的 Windows 用户运行。部署需要包含此功能的 Multica 服务端镜像和 Windows `multica.exe`。

## 启动 Windows 转发

在已登录 POPO CLI 的 Windows 用户下打开 PowerShell。用只读命令确认发送身份。

```powershell
popo-cli popo team_current_user_info
```

为本次部署生成一个随机密钥，例如用 `openssl rand -hex 32`。准备 Windows 转发端的 PEM 证书和私钥。证书的主机名需匹配 Docker 中配置的 URL，签发机构需受 API 容器信任。使用 Docker 可以访问的 Windows 内网地址监听，防火墙只允许 API 所在机器访问该端口。

```powershell
$env:MULTICA_POPO_VERIFICATION_RELAY_TOKEN = '<部署密钥>'
.\multica.exe popo verification-relay --listen '192.0.2.10:9443' --cert 'C:\certs\relay.crt' --key 'C:\certs\relay.key'
```

将示例地址和文件路径换成部署值。保持进程运行。如果用任务计划程序常驻，使用上述 Windows 用户，并确保其运行环境能从 `PATH` 找到 `popo-cli` 和 Node.js。

## 配置 Docker API

将下面的配置写到部署的 `.env`。URL 写 Windows 转发的 HTTPS 地址，密钥使用与 Windows 相同的值。

```dotenv
MULTICA_POPO_CLI=true
MULTICA_POPO_VERIFICATION_RELAY_URL=https://popo-relay.example.com:9443
MULTICA_POPO_VERIFICATION_RELAY_TOKEN=<部署密钥>
```

一体包使用 `docker compose up -d --no-deps app` 重建应用容器。独立后端部署使用 `docker compose up -d --no-deps backend`。使用测试账号从登录页请求一次验证码，确认页面进入填码步骤，并验证 POPO 私聊收到的验证码可登录。

## 发送失败时

- `dial` 或超时意味着 API 到 Windows 的连接或响应出现问题。检查监听地址、防火墙与 Windows 进程。
- `x509` 表示证书主机名或信任链校验失败。使用与 URL 匹配的证书，并让 API 容器信任签发机构。
- `HTTP 401` 表示两个进程加载的密钥不一致。
- `HTTP 502` 表示 Windows 转发未能确认 POPO 受理。查看 Windows 转发日志，并在同一 Windows 账号下检查 POPO 登录状态。
