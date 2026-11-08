# Agent Note: P4 仅原生执行

Status: implemented
Kind: simplification
Protocol: agent-notes/v1

## 问题

在已经向任务注入真实 P4 client 环境后，再保留 `multica p4` → daemon → `p4` 的薄代理会多维护一条 HTTP 协议、capability、参数与 stdin 序列化、输出限制和版本偏斜路径。运行说明和 skill 同时教授两种等价写法，却没有增加原生执行能力。

## 决策

所有 Helix 原生命令都由调用方直接运行本机 `p4`。`multica p4` 仅作为 Multica 自有 `registry`、`changelist` 和 `swarm` 命令的命名空间，不接受原生 P4 子命令。

删除 CLI 原生代理、`POST /p4/native`、p4cache 原生执行实现及 `perforce-native-v1` capability。旧 `/p4/run`、`/p4/status`、`/p4/sync` 继续返回 HTTP 410 升级提示，不恢复参数转换。运行 brief、内置 `multica-platform` skill 和用户文档只指导直接 `p4`。

本记录部分取代 [P4 原生命令透传](2026-11-05-p4-native-passthrough.md) 中保留薄代理的选择；原记录关于真实 client 绑定、不自动创建或收尾任务 CL、显式 Swarm、手动关联与轮询、移除动作开关的决策仍然有效。

## 替代方案

保留薄代理作为可选入口：需要永久维护一条没有额外执行能力的 daemon 协议，并继续处理 HTTP 边界和版本偏斜，因此删除。

恢复受控子命令和参数白名单：仍需跟随各 P4 版本重建 argv，也会恢复本次已经取消的隐式任务 CL 行为，因此不采用。

## 后果

绑定任务内仍自动获得真实的 `P4PORT`、`P4USER`、`P4CHARSET`、`P4CLIENT` 和工作目录；任务外直接运行 `p4` 时，调用方自行保证本机 P4 环境正确。原生命令的交互、stdin/stdout、退出码和运行时间不再受代理的 10 分钟及每路 1 MiB 输出限制；daemon 自己执行的绑定、轮询和 Swarm 辅助命令仍沿用现有超时。

可信 agent 的共享工作区边界不变。`multica p4 changelist link` 让 agent 显式关联原生 CL，daemon 校验其属于绑定 client 后复用既有上报与轮询；Swarm create/link 在 review 关联成功后走同一 CL 关联路径。Agent 按运行说明用原生命令完成限定范围的收尾，daemon 不隐式 revert、shelve 或 submit。

## 验证与限制

2026-11-08：`go -C server build ./...` 通过。`cmd/multica` 的 P4 测试、daemon 的 P4 与 capability 定向测试、p4cache/service 测试，以及 execenv 的 P4 和相关 brief 定向测试通过。`multica p4 --help` 仅列出 `registry`、`changelist` 和 `swarm`。显式 CL 关联覆盖 client 校验、shelved 状态与文件数、现有上报接口，以及 Swarm 成功后的自动 CL 关联。

这些检查使用编译、单元测试和 fake P4，不证明真实 P4 服务器、UE、Swarm 或生产共享工作区已验收；未运行真实 agent smoke test。
