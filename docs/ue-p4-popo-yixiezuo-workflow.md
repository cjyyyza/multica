# DJ01Bot、易协作与 P4 UE 项目工作流

一张易协作工单对应同一工作区内的一条 Multica 原生任务；这条任务可以有多次运行。用户明确导入、选择项目并分配执行，验收后再确认回写结果。

## 准备

| 组件 | 配置与职责 |
| --- | --- |
| Multica | 使用合并后的服务端、CLI 和 Web/Desktop，应用全部迁移；启用 `MULTICA_POPO_ENABLED=true`。 |
| DJ01Bot | Windows 通道完成配对，机器人绑定智能体，发送者绑定同一工作区的 Multica 成员。负责 POPO 消息、任务评论、运行结果与控制。 |
| 易协作本机桥接 | 在已登录 `popo-cli` 的 Windows 上，以发送者绑定的 Multica 成员账号运行 `multica yixiezuo bridge --workspace-id <workspace-uuid>`。负责该成员明确请求的来源读取和回写。 |
| UE 项目与运行时 | 项目资源配置 `perforce_depot`（port、depot、stream、可选 changelist）和一条 `in_place` 的 `local_directory`，指向这台机器上的 P4 工作区根目录（放着 `p4config.txt` 的那一层，如 `D:\DJ`）。Windows 守护进程上的 P4 登录、UE 安装、构建工具和项目验证步骤须可用。 |

来源访问权限属于运行易协作本机桥接的成员。DJ01Bot 通道凭证保持工作区范围；服务端持久化操作和回执，Windows 本机负责访问来源。

## 从 POPO 导入并处理

以下命令发给绑定的机器人；群聊中需要 @ 机器人。

```text
/yixiezuo preview https://<instance>.pm.netease.com/issues/<id>
/yixiezuo show <返回的操作 ID>
/yixiezuo import <预览 ID> --confirm --project <Multica 项目 UUID>
```

预览不创建任务。确认导入后返回 Multica 任务编号，任务保持未分配且不启动智能体。重复导入复用已有任务，保留本地编辑、负责人和已建立的通知路由。若任务此前从网页导入且尚未关联聊天，在机器人中确认导入同一工单可建立通知路由。

在 Multica 中确认项目与负责人，再分配给已连接 Windows 运行时的智能体。任务被认领时携带所选项目的 P4 资源和易协作单号。改动流程见下一节。

```text
/reply <Multica 任务编号> <补充说明>
/status <Multica 任务编号>
/stop <Multica 任务编号>
```

这些命令操作同一条任务。任务评论和运行结果回到关联聊天；取消运行不会冒充“任务已完成”。

## 端到端：从单号到提交

全流程只有一个 changelist，它从创建到提交都带着易协作单号，所以任何一环都能回溯到工单。

1. **绑定工作区。** 守护进程在 `local_directory` 下解析出你的 P4 client（`p4 set -q` + `p4 info`，由 `P4CONFIG` 决定），校验 Owner 是本人、Host 是本机、本地路径在 client Root 之内，然后给 `port|client` 加锁 —— 同一个工作区同时只跑一条智能体任务。校验不通过时任务直接失败并说明原因，不会退回到“直接写只读文件”。
2. **解析单号。** 从易协作导入的任务用导入记录里的工单号；手工建的任务在标题或描述里写 `refs #<单号>`。**没有单号就打不开文件**：`multica p4 edit` 返回 `ticket_required`，智能体会留言请人补单号然后结束，不会编造一个号。
3. **创建任务 CL。** 智能体第一次 `multica p4 edit` 时创建，描述为 `refs #<单号> <任务标题>` 加 `Multica: <任务编号>` 尾注。同一条任务返工时按尾注找回原来的 pending CL 继续用。
4. **改文件。** `multica p4 edit` 打开、`multica p4 add`（`add-files` 相同）新增，全部自动进这个 CL。文件默认只读，不要改只读属性。查历史用 `multica p4 filelog` 和 `multica p4 describe -c <编号>`；`-c` 就是 `--changelist`。智能体进程里的原生 `p4` 写操作已被 `P4CONFIG` / `P4CLIENT` 屏蔽，只读查询（`files`、`print`、`filelog`、`describe`、`changes`）照常可用。不同步、不提交、不碰别的 CL；遇到 `opened_by_you`、`exclusive_locked`、`exclusive_not_allowed`、`not_head` 就停下并在任务里说明。
5. **收尾。** 任务成功、失败、取消都执行：`revert -a` 退回“打开了但没改”的文件 → CL 空了就删掉 → 否则 shelve（edge 工作区加 `-p`）→ 除非 depot 设了 `no_auto_review`，自动建 Swarm review 并关联到任务。**改过的文件保持打开状态。**
6. **人工验证并提交。** 用 `multica p4 status` 或任务上的 changelist 卡片拿到 CL 号，在 UE 里验证，再用 P4V / safesubmit 提交。智能体不提交。
7. **状态回流。** 守护进程轮询这条 CL，提交后（包括 commit server 改号后的新号）把状态写回服务端；关联的 CL 全部提交、且任务上没有关联 Pull Request 时，任务状态自动推进。
8. **回写易协作。** 下一节的 `publish` 回执里带上 CL 号和 Swarm review。

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

数据库集成测试覆盖 POPO 入站、确认导入、原生任务评论双向传递、明确分配后的 P4 资源认领、停止运行、复核确认、重复确认、过期与跨聊天拒绝。Windows P4 测试使用测试构建的可执行文件，覆盖客户端创建、stream、fresh 同步和服务器目录隔离。工作区模式的单元测试同样使用假 `p4`，覆盖 client 解析与 Host / Owner / Root 校验、每条不变量的拒绝路径、没有单号时的 `ticket_required`、sidecar 落在 depot 里时任务失败，以及收尾的两条分支（CL 变空删掉 / 有改动则 shelve）。

这些检查使用模拟的来源快照和 P4 可执行文件。真实 POPO 账号收发、易协作工单写入、P4 服务端和 UE 编辑器/构建必须在配置好的工作区另行验收；本次代码合并不会自动部署、执行真实智能体、提交 P4 changelist 或改写生产工单。实机验收需要单独授权：它会在共享服务器上产生一条 pending CL、一个 shelf，可能还有一个 Swarm review，验收后需要删除 CL、归档 review。
