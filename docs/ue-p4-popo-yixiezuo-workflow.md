# DJ01Bot、易协作与 P4 UE 项目工作流

一张易协作工单对应同一工作区内的一条 Multica 原生任务；这条任务可以有多次运行。用户明确导入、选择项目并分配执行，验收后再确认回写结果。

## 准备

| 组件 | 配置与职责 |
| --- | --- |
| Multica | 使用合并后的服务端、CLI 和 Web/Desktop，应用全部迁移；启用 `MULTICA_POPO_ENABLED=true`。 |
| DJ01Bot | Windows 通道完成配对，机器人绑定智能体，发送者绑定同一工作区的 Multica 成员。负责 POPO 消息、任务评论、运行结果与控制。 |
| 易协作本机桥接 | 在已登录 `popo-cli` 的 Windows 上，以发送者绑定的 Multica 成员账号运行 `multica yixiezuo bridge --workspace-id <workspace-uuid>`。负责该成员明确请求的来源读取和回写。 |
| UE 项目与运行时 | 项目资源配置 `perforce_depot` 和一条 `in_place` 的 `local_directory`，指向这台机器上的 P4 工作区根目录（放着 `p4config.txt` 的那一层，如 `D:\DJ`）。Windows 守护进程上的 P4 登录、UE 安装、构建工具和项目验证步骤须可用。 |

来源访问权限属于运行易协作本机桥接的成员。DJ01Bot 通道凭证保持工作区范围；服务端持久化操作和回执，Windows 本机负责访问来源。

## 从 POPO 导入并处理

以下命令发给绑定的机器人；群聊中需要 @ 机器人。

```text
/yixiezuo preview https://<instance>.pm.netease.com/issues/<id>
/yixiezuo show <返回的操作 ID>
/yixiezuo import <预览 ID> --confirm --project <Multica 项目 UUID>
```

预览不创建任务。确认导入后返回 Multica 任务编号，任务保持未分配且不启动智能体。重复导入复用已有任务，保留本地编辑、负责人和已建立的通知路由。若任务此前从网页导入且尚未关联聊天，在机器人中确认导入同一工单可建立通知路由。

在 Multica 中确认项目与负责人，再分配给已连接 Windows 运行时的智能体。任务被认领时携带所选项目的 P4 资源和易协作单号。

```text
/reply <Multica 任务编号> <补充说明>
/status <Multica 任务编号>
/stop <Multica 任务编号>
```

这些命令操作同一条任务。任务评论和运行结果回到关联聊天；取消运行不会冒充“任务已完成”。

## 端到端：从单号到提交

Multica 提供工作区和来源上下文，P4 命令保持原生语义。智能体与人共用真实 client，应避免触碰任务之外的文件和 changelist。

1. **绑定工作区。** 守护进程在 `local_directory` 下解析 P4 client（由 `P4CONFIG` 决定），校验 Owner、Host 和 Root，并把真实的 `P4PORT`、`P4USER`、`P4CHARSET`、`P4CLIENT` 注入任务环境。校验不通过时任务直接失败。
2. **提供单号。** 易协作导入记录、结构化属性、metadata 或标题/描述中的 `refs #<单号>` 会进入运行说明。单号用于建议在 CL 描述里写 `refs #<单号>`，不再阻止文件操作。
3. **使用原生 P4。** 智能体直接运行本机 `p4`。子命令、flag、stdin、stdout、stderr 和退出码保持原生语义；Multica 不自动选择或注入 CL，也不提供 `multica p4` 原生命令代理。
4. **按需创建和收尾 CL。** 需要时使用 `p4 change`、`p4 edit -c`、`p4 shelve -c` 等原生命令。Agent 收尾前检查 `p4 opened`，只清理自己在任务 CL 中打开但未修改的文件，不对整个共享 client 执行 revert。任务结束后 daemon 不再隐式 revert、shelve、submit 或建 Swarm review。
5. **验证与提交。** 在 UE 里验证，通过原生 P4、P4V / safesubmit 或明确要求的智能体命令提交。Multica 不再提供额外的 depot 级开关。
6. **关联与状态回流。** Agent 用 `multica p4 changelist link --changelist <编号>` 显式关联 CL；`multica p4 swarm create/link` 会同时关联 CL 和 review。也可以在任务详情按编号手动关联。守护进程轮询已关联 CL，识别提交和改号；全部 CL 已提交且没有关联 Pull Request 时，按现有设置推进任务。
7. **回写易协作。** 下一节的 `publish` 回执带上已关联的 CL 号和 Swarm review。

## 刷新来源与回写

```text
/yixiezuo refresh <Multica 任务编号>
/yixiezuo show <返回的操作 ID>
```

刷新只替换来源快照，保留本地标题、描述、状态和负责人。

验收后发出结果和证据；省略 `--status` 时保持来源状态：

```text
/yixiezuo publish <Multica 任务编号> --status <来源流程允许的状态>
修复内容、changelist、验证步骤、实际结果和验收结论。
```

机器人先返回待复核内容，此时未排队执行外部写入。核对后在同一聊天确认：

```text
/yixiezuo confirm <复核 ID>
/yixiezuo show <返回的回写操作 ID>
```

复核记录持久化，15 分钟内有效，只允许原成员在原机器人和聊天中确认。任务或来源版本已变化时需重新复核；重复确认同一记录不会再次执行回写。来源写入受 `lock_version` 和写后读回保护，失败、冲突和结果不确定分别展示。结果不确定时先检查原工单并刷新来源。

## 验证范围

数据库集成测试覆盖 POPO 入站、确认导入、原生任务评论双向传递、明确分配后的 P4 资源认领、停止运行、复核确认、重复确认、过期与跨聊天拒绝。Windows P4 测试使用测试构建的可执行文件，覆盖 client 解析、Host / Owner / Root 校验、真实 client 环境、不自动创建或收尾 CL、Multica 专用 CLI 命名空间，以及 Swarm 创建不隐式 shelve。

这些检查使用模拟的来源快照和 P4 可执行文件。真实 POPO 账号收发、易协作工单写入、P4 服务端和 UE 编辑器/构建必须在配置好的工作区另行验收；代码合并不等于完成这些实机验收。原生 P4 写命令会直接作用于共享工作区，验收时应使用专门的测试目录和 CL。
