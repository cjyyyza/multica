# POPO 工作伙伴 AGENTS.md 与 skill 示例

本例由工作伙伴决定找谁、问什么、如何转交回复和交付结果；Multica 提供规划、任务状态、阻塞问题、答复和结果接口。POPO 消息直接调用 `use_robot_send_msg`，每次传入本次收件人，支持私聊或在指定群 @ 人。

## 文件与安装位置

```text
workpartner/
├── AGENTS.md                         # Copy AGENTS.example.md here.
└── skills/
    └── multica-popo-workflow/
        ├── SKILL.md
        └── references/
            ├── commands.md
            ├── state.md
            └── scenarios.md
```

把 [AGENTS.example.md](AGENTS.example.md) 复制到**服务器上工作伙伴自己的工作目录**，命名为 `AGENTS.md`，并复制本目录的 `skills/`。示例通过 AGENTS.md 明确要求读取相对路径中的 skill，不假定某个 DSH 版本的自动发现目录。若部署环境已有原生 skill 注册方式，也可按该环境的配置注册。

在实际工作伙伴会话中确认能读取这份 AGENTS.md 和相对的 skill 文件，再启用协调流程；不要仅因文件已复制就声称 DSH 已加载。

这是部署样例，不替换 Multica 仓库根的 AGENTS.md，也不安装到当前用户的全局 skill 目录。所有 `REPLACE_WITH_*` 都要换成实际值；场景中的人物、任务号、CL 和链接均为虚构数据。

## 部署前填写

| 项目 | 放在哪里 |
| --- | --- |
| 工作伙伴完整名称、默认通知方式和群号 | 工作伙伴的 AGENTS.md |
| 如何从 dump/工单认定上报人和 bug 归属人 | 工作伙伴的 AGENTS.md |
| Multica Agent 或 Squad UUID | 工作伙伴的 AGENTS.md；用 `requirement targets` 核实 |
| `MULTICA_SERVER_URL`、`MULTICA_WORKSPACE_ID`、成员凭证 `MULTICA_TOKEN`、`MULTICA_APP_URL` | 服务器进程环境；令牌不写进 Markdown |
| POPO 登录 | 与工作伙伴相同的系统账号和配置环境 |
| 工单、dump 元信息和 P4/Swarm 的读取工具 | 工作伙伴已有的相应 skill 或连接器 |

Multica、工作伙伴和通知处理进程可以同处服务器。工作伙伴使用成员凭证，执行智能体使用自己运行的任务凭证；两者不交换凭证。POPO UID 与 Multica 用户 UUID 是不同标识。

协调进程不应继承某个 Multica 执行运行的任务上下文环境，例如该运行的 `MULTICA_TASK_ID`、`MULTICA_TASK_MODE` 和 `MULTICA_MANAGED_BY`。

## 两个示范

1. **dump 需要补充信息**：读取 Multica 的具体问题；按 AGENTS.md 的规则找上报人；在 POPO 私聊或群内 @ 他；保存问题与消息 ID；收到对应人的回复后，以有权限的协调成员身份调用 `clarify` 或 `reply`。
2. **bug 已实现，发送 shelve**：读取 `in_review` 的交付结果；核对已有 shelve CL、P4 服务器及真实评审链接；按 AGENTS.md 的规则找到 bug 归属人；发送“待验证”的交付说明。人工验收后的 `done` 不重复发送同一份 shelve。

完整消息和回复示例见 [scenarios.md](skills/multica-popo-workflow/references/scenarios.md)。

## 对话结束后的触发

AGENTS.md 和 skill 是操作规程，本身不会创建定时器或启动新的 DSH 轮次。部署时需要由工作伙伴已有的事件入口或服务器调度器触发以下三类调用：

| 触发 | 交给工作伙伴的输入 |
| --- | --- |
| 用户提交 dump/bug | 原始请求、来源标识，以及通道提供的真实发送者和聊天元数据 |
| 周期检查未完成任务 | `读取 skills/multica-popo-workflow/SKILL.md，检查 .workpartner/tasks/ 下未结束的记录，只处理新增问题或新的 shelve 交付，不重复发送。` |
| POPO 收到人的回复 | 原消息 ID、发送者 UID、聊天 ID、被引用消息 ID和正文；加载相同 skill 做关联与回填 |

同一任务串行处理。生产调度器负责同任务互斥及重启后重新扫描持久记录；不能仅靠模型写 JSON 声称具有并发去重保证。

`multica requirement wait` 可在当前调用内有界等待；`multica notify watch` 可发送已配置的状态通知。它们都不会自动执行这份 skill 的找人和交付整理流程。本例由 skill 负责业务通知，同一任务和目标不要再并行启用另一条状态通知流。

## 当前接口边界

- 本例使用现有 Requirement 问答流程；`reply` 必须满足 `blockedQuestion.canReply`，并绑定当前问题和任务 revision。
- 默认由执行端把外部信息问题交给需求的协调成员 `requesterId`；工作伙伴在 POPO 询问实际上报人，再以协调成员身份转交原文，不冒充对方的 Multica 身份。既有问题点名其他成员时，遵守其现有权限。
- shelve 的创建和修复由 Multica 执行端完成；工作伙伴读取和转交交付物。
- 发送超时可能已产生消息；POPO 没有此示例可用的服务端幂等键，未知结果要核实，不能自动重发。
- 本例不依赖 `multica notify send` 的静态 `--target`，也没有给 POPO CLI 添加不存在的参数。

本示例未安装到真实工作伙伴、未配置服务器调度器，也未发送实际 POPO 消息。
