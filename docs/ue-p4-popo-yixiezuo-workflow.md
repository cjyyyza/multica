# DJ01Bot、易协作与 P4 UE 项目工作流

一张易协作工单对应同一工作区内的一条 Multica 原生任务；这条任务可以有多次运行。用户明确导入、选择项目并分配执行，验收后再确认回写结果。

## 准备

| 组件 | 配置与职责 |
| --- | --- |
| Multica | 使用合并后的服务端、CLI 和 Web/Desktop，应用全部迁移；启用 `MULTICA_POPO_ENABLED=true`。 |
| DJ01Bot | Windows 通道完成配对，机器人绑定智能体，发送者绑定同一工作区的 Multica 成员。负责 POPO 消息、任务评论、运行结果与控制。 |
| 易协作本机桥接 | 在已登录 `popo-cli` 的 Windows 上，以发送者绑定的 Multica 成员账号运行 `multica yixiezuo bridge --workspace-id <workspace-uuid>`。负责该成员明确请求的来源读取和回写。 |
| UE 项目与运行时 | 项目资源配置 `perforce_depot` 的 port、depot、stream 和可选 changelist。Windows 守护进程上的 P4 登录、UE 安装、构建工具和项目验证步骤须可用。 |

来源访问权限属于运行易协作本机桥接的成员。DJ01Bot 通道凭证保持工作区范围；服务端持久化操作和回执，Windows 本机负责访问来源。

## 从 POPO 导入并处理

以下命令发给绑定的机器人；群聊中需要 @ 机器人。

```text
/yixiezuo preview https://<instance>.pm.netease.com/issues/<id>
/yixiezuo show <返回的操作 ID>
/yixiezuo import <预览 ID> --confirm --project <Multica 项目 UUID>
```

预览不创建任务。确认导入后返回 Multica 任务编号，任务保持未分配且不启动智能体。重复导入复用已有任务，保留本地编辑、负责人和已建立的通知路由。若任务此前从网页导入且尚未关联聊天，在机器人中确认导入同一工单可建立通知路由。

在 Multica 中确认项目与负责人，再分配给已连接 Windows 运行时的智能体。任务被认领时携带所选项目的 P4 资源；智能体通过 `multica p4 sync ... --output json` 按需取得独立 client 和目录。打开文件用 `multica p4 edit` / `checkout`，新增文件用 `add-files`，提交前用 `shelve` 并可用 `multica p4 swarm create --changelist <n>` 开 Helix Swarm review。不要使用本机默认 P4 client。任务中应保留 changelist、Swarm review、构建结果及实际 UE 验证证据。

```text
/reply <Multica 任务编号> <补充说明>
/status <Multica 任务编号>
/stop <Multica 任务编号>
```

这些命令操作同一条任务。任务评论和运行结果回到关联聊天；取消运行不会冒充“任务已完成”。

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

数据库集成测试覆盖 POPO 入站、确认导入、原生任务评论双向传递、明确分配后的 P4 资源认领、停止运行、复核确认、重复确认、过期与跨聊天拒绝。Windows P4 测试使用测试构建的可执行文件，覆盖客户端创建、stream、fresh 同步和服务器目录隔离。

这些检查使用模拟的来源快照和 P4 可执行文件。真实 POPO 账号收发、易协作工单写入、P4 服务端和 UE 编辑器/构建必须在配置好的工作区另行验收；本次代码合并不会自动部署、执行真实智能体、提交 P4 changelist 或改写生产工单。
