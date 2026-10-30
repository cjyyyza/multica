# Multica × Perforce：以 changelist 为中心的流程优化计划

状态：实现方案。操作权限、任务绑定和提交前检查与 [implementation contract](p4-changelist-workflow-contract.md) 保持一致；实机验收记录另行保存。

依据：
- 三份只读调研报告：daemon 链路、服务端数据模型、前端与文档。
- 对 DJ01 Helix 服务器和 Swarm 的只读实测。
- 已和用户确认的决策。

设计背景中的 `文件:行号` 来自改造前的调研；实现细节以当前代码和 implementation contract 为准。尚需现场确认的点标为“待实测”。

---

## 0. 已确认的决策

| # | 决策 | 来源 |
|---|---|---|
| D1 | 提交描述必须带 `refs #<易协作单号>`。没有单号时由人补填，agent 不编造 | 用户确认 |
| D2 | agent 在人自己的 P4 工作区里改，结束后**不 revert 改动**（只 revert"打开了但没改"的文件，等同 P4V 的 Revert Unchanged） | 用户确认 |
| D3 | **agent 默认不提交**。只有人类 owner/admin 可在工作区 depot 目录开启 `allow_submit`、`allow_sync`、`allow_unshelve`；机器凭证不能修改目录。项目资源不能授予这些权限 | 当前实现契约 |
| D4 | CL 和 review 状态靠 daemon 轮询同步，不依赖服务器 trigger（我们没有 admin 权限） | 建议，未被否决 |
| D5 | `+l` 独占文件默认不允许 agent 通过 daemon 打开，可以按 depot 放开 | 建议，未被否决 |
| D6 | 项目配置 Git 或 Perforce depot 后不继承另一类资源；只有本地目录的项目仍可使用工作区目录兜底，`in_place` 必须保持 P4 绑定 | 当前实现契约 |

## 1. 现实约束（实测）

- **服务器**：`p4-commit.example.com:1666`，P4D 2023.1 commit-server，开了 unicode，大小写敏感，`serverCluster p4_dj01`。另有 edge：`p4d_edge_mtl` 上绑了 167 个工作区，`p4d_edge_hz` 上绑了 5 个。
- **Swarm**：`https://swarm.example.com:8080`，2024.2，API v9–v11，约 1.6 万个 review。
- **体积**：一个 Dev 工作区约 182 万个文件、328GB。其中 Content 192GB、Engine 95GB、Plugins 6GB、Source 135MB。
- **typemap**：`//DJ01_Proj` 下的 `.uasset .umap .ubulk .fbx .png .psd` 是 `binary+l`；`.cpp .h .ini .json .py .cs` 是 `text`。
- **锁**：shelve 不占 `+l` 锁。CL 794229 的 shelf 里有一个 `binary+l` 的 uasset，`p4 opened -a` 显示它没有在任何地方被打开。revert 会释放锁。
- **提交后改号**：`p4 -ztag describe` 会给出 `change 795115` 和 `oldChange 795095`。用原号查要加 `describe -O`。
- **工作区解析**：
  - `D:\DJ\p4config.txt` 里写的是正确的 client（`yujian01_HIH-D-35258Z_Dev`）。
  - 全局 `P4CLIENT` 指向绑定在另一台机器上的 client，直接用会报 "can only be used from host 'OTHER-HOST'"。
  - **所以必须在工作区目录下解析**（`p4 set -q` 加 `p4 -ztag info`），不能读全局环境变量。
- **屏蔽手段（实测有效）**：给进程设 `P4CONFIG=<不存在的文件名>` 和 `P4CLIENT=<不存在的 client>` 之后：
  - `p4 files/print/changes/describe/fstat` 照常可用；
  - `p4 opened/have/where/edit` 全部失败。
- **depot 里有 agent 配置文件**：
  - `//DJ01_Proj/Dev/Game/DJ01/` 下有 `AGENTS.md`（text），`.claude/` 下有 13 个文件，`.agents/` 下有 24 个。
  - Dev 根目录下没有这些文件。
  - `//DJ01_Proj/Dev/.p4ignore` 没有忽略 `.multica`、`.agent_context`、`CLAUDE.md`、`AGENTS.md`。
- **fstat 的冲突信息**：别人打开的文件会返回 `otherOpen0 user@client`、`otherAction0`、`otherChange0`。

## 2. 设计背景：改造前的问题

按严重程度排序：

1. **工作区模式下会伤到人的文件**。这些问题今天只作用在任务专用 client 上，所以还没出事，但上线工作区模式之前必须先堵上：
   - `revert` 不带文件时默认 `...`，而且不带 `-c`（`server/internal/daemon/p4cache/mutate.go:191-202`）。在人的 client 里，这会 revert 人所有已打开的文件。
   - `reconcile` 不带文件时默认 `...`（`mutate.go:209-217`）。这会扫描 182 万个文件，并且把 Multica 写入的辅助文件（sidecar）当成新增文件 add 进来。
   - `submit` 不带 `-c` 时提交 default CL（`mutate.go:229-236`）。
   - `shelve` / `reopen` 可以指定任意 CL 号（`mutate.go:224-245`），可能覆盖人自己的 shelf。
2. **现有方案用不了 DJ01**：每个任务新建 client 并全量 sync（`p4depot.go:49-51`、`p4cache/cache.go:61-117`）。
3. **运行说明可能悄悄丢失**：
   - 如果本地目录资源指向 `D:\DJ\Game\DJ01`，Multica 会把运行说明追加到 depot 里的 `AGENTS.md`。
   - 由于 `noallwrite`，这个文件是只读的，写入会失败（`execenv/runtime_config.go:272-301`）。
   - 这个失败只记一条 warn（`daemon.go:8588-8590`），agent 会在**没有 Multica 运行说明**的情况下照常运行。
4. **CL 不是一等对象**：
   - 服务端只有 `swarm_review.changelist` 这一个字符串。
   - 任务结果里没有 CL 字段（`daemon/types.go:305-314`）。
   - CL 状态不会同步；合入后推进 issue 的逻辑和 `--until-pr` 都不认 Swarm（`issue_wakeup_condition.go:266`）。
5. **agent 指令偏 git**：
   - 只配了 P4 的任务照样注入 `repo checkout` 和 git 身份段落（`runtime_config_sections.go:271-274`）。
   - brief 里漏了 `reopen` 和 charset（`daemon.go:10202-10218`、`execenv/execenv.go:26-35`）。
6. **fallback 过宽**：只配了 git 的项目也会继承 workspace 级的 UE depot（`handler/project_resource.go:1020-1021`）。
7. **超时不一致**：CLI 的 sync 是 5 分钟（`cmd/multica/cmd_p4.go:293`），daemon 侧是 10 分钟；swarm 只有 1 分钟（`cmd_p4_task.go:357`），而创建 review 前还要先 shelve。

## 3. 目标流程（工作区模式）

1. 人给项目挂上两个资源：`local_directory`（in_place，指向自己的工作区根目录，如 `D:\DJ`）和 `perforce_depot`（`//DJ01_Proj/Dev`）。
2. 人把任务指派给 agent。claim 里带上易协作单号 `p4_ticket`。
3. daemon 在本地目录下识别 P4 工作区，做一系列校验，然后给这个 client 加锁。同一个工作区同时只跑一个 agent 任务；本地目录本身也有路径锁。
4. agent 第一次调用 `multica p4 edit` 时，daemon 创建任务 CL，描述为：

   ```
   refs #<单号> <任务标题>

   Multica: <DJ-123>
   ```

   之后 agent 打开的文件都只进这个 CL。只读分析类的任务不会产生 CL。
5. agent 用 `multica p4 edit` / `add`（`add-files` 相同）/ `delete` 修改文件。raw `p4` 写操作已被环境变量屏蔽，只读查询仍然可用。历史用 `multica p4 filelog`。
6. 任务结束（成功、失败或取消都执行）时：
   - `revert -a -c <CL>`，只 revert 打开了但没改的文件；
   - 如果 CL 变空了，先用 `shelve -d -c <CL>` 清理旧 shelf，再用 `change -d` 删掉它；
   - 否则 `shelve -r -c <CL>`，edge 工作区加 `-p`；
   - 自动建 Swarm review 并关联到 issue；
   - **文件保持打开状态**。
7. 默认由人在 UE 里验证，用 P4V / safesubmit 提交。管理员开启 `allow_submit` 后，agent 可通过提交前检查提交本任务 CL；关闭开关不能撤回已提交的修改。
8. 第二阶段起：daemon 轮询到 CL 已提交（包括改号后的新号），自动推进 issue；易协作回写自动带上 CL 和 review。
9. 返工：同一个 issue 再次指派、落在同一个 client 上时，复用原来的 pending CL（按描述里的 `Multica: <DJ-123>` 查找）。

## 4. daemon 强制执行的不变量（工作区模式）

| # | 不变量 | 违反时的行为 |
|---|---|---|
| I1 | 所有写操作都绑定在任务 CL 上。agent 传入的 `--changelist` 只能是任务 CL，不传时由 daemon 填 | 拒绝 |
| I2 | 已在人的 CL（含 default CL）里打开的文件，agent 不能碰 | 拒绝："已在你的 CL N 里打开" |
| I3 | 被别人独占、或是 `+l` 文件且 depot 没开 `allow_exclusive` | 拒绝，并给出占用者 `user@client` |
| I4 | `revert` / `reconcile` 必须带 `-c <任务CL>`，`reconcile` 还必须给明确路径；永远不对整个 client 执行 | 拒绝 |
| I5 | `submit`、`sync`、`unshelve` 默认拒绝，仅在工作区目录的对应权限开启后允许；没有绑定的写操作始终拒绝 | 409 `action_not_allowed` |
| I6 | 本次任务写入的 sidecar 路径永远不会被 add 或 reconcile 进 CL | 从文件列表里过滤掉 |
| I7 | 没有单号（`p4_ticket` 为空）时不创建 CL、不打开文件 | 返回 409 `ticket_required` |
| I8 | 只操作本机、本人的 client：Host 为空或等于本机，Owner 等于 P4USER，本地目录在 client Root 之内 | 任务失败，并说明原因 |

说明：环境变量屏蔽是护栏，不是安全边界（agent 可以自己改环境变量）。真正的约束是 daemon 路由上的这些检查，再加上 brief 里的明确禁止。

---

## 5. 分阶段计划

### 阶段 0：先堵口子（小 PR，基本只改 fork 自有文件）

| 改动 | 文件 / 函数 |
|---|---|
| 提交权限与检查 | submit 仅作用于绑定的任务 CL。提交前回读 pending spec，核对 client、user、`refs #` 和任务标记；执行 `revert -a` 并拒绝空 CL；再次核对描述，再整份 `shelve -d -c`，最后 `submit -c`。成功后通过 `describe -s -O` 回读，不因回读失败重放 submit |
| brief 补 `reopen` 和 charset | `execenv/execenv.go` 的 `P4DepotForEnv` 加 `Charset`；`daemon.go` 的 `convertP4DepotsForEnv` 把它带上；`writeP4Depots` 输出 charset；命令行里补 `reopen` |
| 超时对齐 | 新增 `MULTICA_P4_TIMEOUT`（默认 10m，读法同 `daemon/config.go:326` 的 `durationFromEnv`）；`p4cache.p4Timeout` 改读这个配置；CLI 的 sync / run 超时设为 daemon 超时加 1 分钟；swarm 超时改为 3 分钟 |
| 删除任务专用 client 模式 | 不再创建 client 或下载 depot 副本，也不保留体积预检、旧 checkout mutation 或拒绝用的 Sync shim。无绑定的 `/p4/run` 和 `/p4/sync` 统一返回 409；quick-create 和无项目的 autopilot 不因工作区目录兜底而要求本地目录 |
| 收紧 fallback | `handler/project_resource.go:1020-1021`：`needGitFallback := len(out.Repos)==0 && len(out.P4Depots)==0`，`needP4Fallback := len(out.P4Depots)==0 && len(out.Repos)==0`；同步更新 `handler/daemon_test.go:2269-2369` 的预期 |
| 只配 P4 的任务不再注入 git 段落 | `runtime_config_sections.go`：当 `len(ctx.Repos)==0 && len(ctx.P4Depots)>0` 时省略 271 行（`repo checkout`）和 274 行（git 身份）；`writeProjectContext` 里 537 行改为只在确实存在 `github_repo` 资源时输出。103 行的 CI 段落是 review-locked 文本，**不动** |

**验收**
- P4 定向测试：机器凭证不能修改工作区 depot；`in_place` 兜底保持绑定；quick-create 和无项目 autopilot 不被误拦；submit 前检查、整份 shelf 清理和失败停止；sync 包含 import，明确拒绝 `--changelist` / `--fresh`。
- `handler` 测试：fallback 新规则下 4 种组合（无资源 / 只有 git / 只有 P4 / 两者都有）。
- brief 快照测试：只配 P4 的任务不再出现 `repo checkout`。

### 阶段 1：工作区模式（核心）

#### 1.1 启用条件（不新增配置项）

- 项目同时挂了 `local_directory`（in_place，`daemon_id` 是这台机器）和 `perforce_depot`。
- daemon 能在 `local_path` 下识别出 P4 工作区，并且通过 I8 的校验；该 client 的 Stream（或 View）落在某个 `perforce_depot` 的 depot/stream 之下。
- 如果两个资源都在、但识别或校验失败，任务**直接失败**并说明原因，不静默退回到"直接改只读文件"。
- worktree 模式的 `local_directory` 是 git 专用的，不参与。

#### 1.2 新文件 `server/internal/daemon/p4cache/workspace.go`

```go
// Workspace is a human-owned Helix client resolved from a local directory.
type Workspace struct {
    Port, User, Client, Charset string
    Root, Stream                string
    Edge                        bool // p4 info serverServices == edge-server
}

// DetectWorkspace runs `p4 set -q` and `p4 -ztag info` with cwd=dir (so P4CONFIG
// resolves), then `p4 -ztag client -o`; enforces I8.
func (c *Cache) DetectWorkspace(ctx context.Context, dir string) (*Workspace, error)

type FileState struct {
    DepotFile, LocalPath, HeadType string
    HaveRev, HeadRev               int
    OpenChange                     string   // "" not opened here; "default" or a number
    OtherOpens                     []string // user@client
    OtherLock                      bool
}
func (c *Cache) FileStates(ctx context.Context, ws *Workspace, files []string) ([]FileState, error) // p4 -ztag fstat on the given files only

func (c *Cache) CreateTaskChange(ctx context.Context, ws *Workspace, desc string) (string, error)
func (c *Cache) FindTaskChange(ctx context.Context, ws *Workspace, issueKey string) (string, error) // changes -s pending -c <client> -l, match "Multica: <key>"
func (c *Cache) SetChangeDescription(ctx context.Context, ws *Workspace, change, desc string) error
func (c *Cache) RunInChange(ctx context.Context, ws *Workspace, change, verb string, files []string, flags ...string) (string, error) // always -c change

type FinishResult struct {
    Change                 string
    Files                  []FileState
    Deleted, Shelved       bool
    ExclusiveHeld, NotHead []string
}
// FinishChange: revert -a -c N; opened -c N; empty -> shelve -d -c N, change -d N; else shelve -r -c N [-p].
func (c *Cache) FinishChange(ctx context.Context, ws *Workspace, change string) (*FinishResult, error)
```

所有命令都显式传 `-p -u -c -C`，并在 `cmd.Dir = local_path` 下执行，这样相对路径能正确解析、`P4IGNORE` 也能生效。注意现有 `commandAt` 在没传 dir 时会落到临时目录所在盘的根目录（`p4cache/cache.go:253-272`）。

#### 1.3 新文件 `server/internal/daemon/p4_workspace.go`（package daemon）

```go
type p4TaskBinding struct {
    ws              *p4cache.Workspace
    ref             p4depot.Ref
    ticket          string // empty => I7
    issueKey, title string // task.IssueIdentifier (types.go:92), task.ThreadName (types.go:113)
    change          string // created lazily, or reused via FindTaskChange
    sidecars        []string
    defaultSnapshot []string // files already in the default CL when the task started
    release         func()   // client lock
}

func (d *Daemon) bindP4Workspace(ctx context.Context, task Task, a *localDirectoryAssignment, provider string) (*p4TaskBinding, error)
func (d *Daemon) finishP4Workspace(ctx context.Context, task Task, b *p4TaskBinding)
func (d *Daemon) p4Binding(taskID string) *p4TaskBinding
```

- **挂载位置**：在 `daemon.go` 的 `runTask` 里，本地目录分配解析完成（8020 行附近）、并拿到路径锁之后，`execenv.Prepare` 写 sidecar 之前，调用 `bindP4Workspace`；然后注册 `defer d.finishP4Workspace(...)`。这个 defer 会在 runTask 返回、也就是终态上报之前执行。
- **client 锁**：在 Daemon 结构体里新增 `p4ClientLocks *LocalPathLocker`（复用现有类型，`daemon.go:696` 同款），key 为 `port|client`。这样即使同一个 client 下挂了两个不同的本地目录，也只会串行执行。
- **sidecar 检查**：在 `bindP4Workspace` 里，对 Multica 将要写入的路径执行一次 `p4 -ztag fstat`：运行说明文件（为 `execenv/runtime_config.go:224` 的 `runtimeConfigPath` 加一个导出包装）、`.multica/`、`.agent_context/`，以及本次要写的 skill 文件（为 `execenv/context.go:332-362` 中决定 skill 路径的函数加导出包装）。任何一个是 depot 文件，任务就失败，并提示："把本地目录资源指到工作区根目录（如 `D:\DJ`）"。检查通过的路径记入 `sidecars`，供 I6 过滤用。
- **default CL 快照**：执行 `p4 -ztag opened -c default`，结果留给 finish 报告"任务期间新进入 default CL 的文件"（UE 编辑器自动 checkout 的文件会落在这里）。

#### 1.4 路由改造：`server/internal/daemon/p4_handlers.go`

`p4RunHandler` 和 `p4SyncHandler` 检查 `d.p4Binding(taskID)`：有绑定就走工作区模式，没有则返回 409。`p4SwarmHandler` 的 shelve 操作同样要求绑定；不触及工作区文件的 review 查询、关联和 `--no-shelve` 创建仍通过已授权的 Swarm 地址执行。

| 动作 | 工作区模式下的行为 |
|---|---|
| `edit` / `add` / `delete` / `move` | 先过 I7 → 执行 `FileStates` → 依次检查 I2、I3、I6 → 按需创建或复用任务 CL → 用 `-c CL` 执行。响应里带上 warnings：不在 head（haveRev ≠ headRev）、同时被别人打开 |
| `reopen` | 只允许把**任务期间新进 default CL** 的文件（与快照对比）移进任务 CL |
| `revert` | 必须带 `-c CL`，文件必须在任务 CL 里；`--unchanged` 对应 `revert -a -c CL` |
| `reconcile` | 必须给明确路径，带 `-c CL`，过滤 sidecar |
| `shelve` | 只能 shelve 任务 CL；edge 工作区加 `-p` |
| `change` | 只能改任务 CL 的描述：daemon 固定加上 `refs #<单号>` 前缀和 `Multica: <key>` 尾注 |
| `sync` / `unshelve` | 默认拒绝；分别需要 `allow_sync` / `allow_unshelve`。sync 使用 `//<client>/...` 更新完整视图；unshelve 仅恢复本任务自己的 shelf |
| `opened` / `describe` | 只读，允许 |
| 新增 `/p4/status` | 返回 client、根目录、stream、单号、任务 CL、CL 里的文件和 warnings（路由注册在 `health.go:419-421` 旁边，加一行） |

#### 1.5 屏蔽 agent 进程里的 raw p4 写操作

在 `daemon.go:181-198` 的 `taskMulticaEnvironment` 中，任务处于工作区模式时注入：

```
P4CONFIG=.multica-p4-disabled
P4CLIENT=multica-blocked
```

实测效果：depot 只读查询可用，工作区写操作全部失败。daemon 进程自己的环境不受影响。

#### 1.6 单号解析（服务端）

新文件 `server/internal/handler/p4_ticket.go`：

```go
var refsTicketRe = regexp.MustCompile(`(?i)\brefs\s*#\s*(\d+)\b`)

// resolveIssueP4Ticket: 1) yixiezuo_import.external_id (GetYixiezuoImportByIssue,
// pkg/db/queries/yixiezuo_import.sql:7-8); 2) refsTicketRe in the issue title, then the description.
func (h *Handler) resolveIssueP4Ticket(ctx context.Context, issue db.Issue) string
```

- `handler/agent.go` 的 `AgentTaskResponse` 增加 `P4Ticket string \`json:"p4_ticket,omitempty"\``。
- `handler/daemon.go` 在 issue 分支 `resolveClaimProjectContext` 之后（2792 行附近），仅当 `len(resp.P4Depots) > 0` 时填写这个字段。
- daemon 侧 `daemon/types.go` 的 Task 增加 `P4Ticket`。
- 不是从易协作导入的任务：由人在任务描述里写 `refs #<单号>`。

#### 1.7 结束收尾：`finishP4Workspace`

1. 没有任务 CL → 直接结束。
2. 执行 `FinishChange`：`revert -a -c N` → `opened -c N` → 如果为空就先 `shelve -d -c N`，再 `change -d N`；否则 `shelve -r -c N`（edge 工作区加 `-p`）。**改过的文件保持打开。**
3. depot 的 `auto_review` 开着（默认开）时：
   - 这个 CL 还没有 review → 调现有的 `Cache.Swarm` 创建（已经 shelve 过，`Shelve=false`），再用该任务的 token 调 `POST /api/issues/{id}/swarm-reviews` 关联到 issue。这段逻辑和 CLI 的 `reportSwarmReviewToIssue`（`cmd_p4_task.go:373-424`）一致，在 daemon 侧新文件 `p4_api.go` 里实现。
   - 已经有 review → 重新 shelve 即可更新它（Swarm 的标准行为，**待实测**）。
4. 写日志，并在 `/p4/status` 里保留结果。第一阶段中，人通过两处看到 CL：Swarm 卡片（`cl/N`），以及 agent 最终回复里写的 CL 号。

#### 1.8 agent 指令：`runtime_config_sections.go`

当 `ctx.P4Workspace != nil` 时，用新的段落替换 `writeP4Depots`（488-512 行）的内容。需要新增 `execenv.P4WorkspaceForEnv{Client, Root, Stream, Ticket, Change, AllowExclusive}`。文案用英文，和现有 brief 保持一致，要点如下：
- 你在 `<user>` 的工作区里干活，给出 client、根目录、stream。
- 改文件前先 `multica p4 edit`；文件默认只读，**不要改只读属性**；新建文件用 `multica p4 add`（`add-files` 相同）。所有改动都会进本任务的 CL。`-c` 就是 `--changelist`，写操作不能指向别的 CL。
- 查文件历史用 `multica p4 filelog`，查某条 CL 描述用 `multica p4 describe -c <编号>`。看这个 client 的文件状态用 `multica p4 fstat`，列 changelist 用 `multica p4 changes`（原样转发 p4 输出）。不要用 raw `p4` 写工作区；depot 只读查询仍可用，但原生 `fstat` 看不到绑定的 client。
- submit、sync、unshelve 只在对应权限开启时可用，不碰别的 CL。遇到“已在你的 CL 里打开 / 被独占 / 不在 head”之类的提示，停下并在任务里说明。
- 结束后 daemon 会自动 shelve 并发起 Swarm 评审。最终回复里写清：改了什么、CL 号（用 `multica p4 status` 查）、在 UE 里怎么验证。
- 没有单号时：本任务不能打开文件；需要改文件就留言请人在描述里补 `refs #<单号>`，然后结束。
- 项目规范在 `Game/DJ01/AGENTS.md`，开始前先读。

#### 1.9 CLI：`cmd_p4_task.go`

- 工作区模式下 `--port` / `--depot` 可以省略（`requireP4PortDepot` 改为可选，由 daemon 从绑定信息里补齐）。
- 新增 `multica p4 status`。

#### 1.10 文档和内置技能

- `apps/docs/content/docs/perforce.mdx` 和 `.zh.mdx`：新增工作区模式一节；把 "Settings → Repositories" 改为 "Settings → Code"。
- `references/runtimes.md`、`projects.md`、`SKILL.md` 的 P4 部分，以及 `builtin_skills_test.go`。
- `docs/ue-p4-popo-yixiezuo-workflow.md`。

#### 阶段 1 验收

**单元测试**（fake p4）：
- `DetectWorkspace`：`set -q` / `info` / `client -o` 的解析；host 不匹配、owner 不匹配、目录不在 root 内、edge 识别。
- 每条不变量 I1–I8 一个表驱动用例。
- `FinishChange` 的两种序列：CL 变空 → `shelve -d` → `change -d`；有改动 → `shelve -r`，edge 加 `-p`。
- sidecar 是 depot 文件时任务失败。
- `ticket_required`。

**handler 测试**：
- `resolveIssueP4Ticket` 的三种来源：易协作导入 / 描述里的 `refs #` / 都没有。
- claim 只在有 depot 时才带 `p4_ticket`。

**实机验收**（需要你授权，因为会在共享服务器上产生一个 pending CL 和一个 shelf，可能还有一个 Swarm review；测试完删除 CL、归档 review）：
1. 在测试项目上挂 `D:\DJ` 和 `//DJ01_Proj/Dev`，新建一个描述里带 `refs #<测试单号>` 的任务，让 agent 改一个无害的文本文件。
2. 预期结果：
   - CL 的描述以 `refs #` 开头；
   - 文件在这个 CL 里；
   - CL 已 shelve，Swarm review 已关联到 issue，文件仍处于打开状态；
   - agent shell 里 raw `p4 edit` 失败；
   - 不带参数的 `multica p4 revert` 被拒；submit、sync、unshelve 默认被拒，管理员开启后按绑定范围执行；
   - 任务前后对比 `p4 opened`，你自己的 CL 完全没变。
3. 再验证一遍不带单号的任务：返回 `ticket_required`，不产生 CL。

### 阶段 2：CL 成为一等对象

#### 2.1 迁移（使用 fork 专用号段，见第 6 节）

- **`p4_changelist`** 表，字段：
  - 标识：`id`、`workspace_id`、`daemon_id`、`server_key`（取 `serverCluster`，没有时用小写 port）、`port`、`charset`；
  - CL 号：`number`（当前号）、`original_number`（pending 时的号）；
  - 归属：`client`、`p4_user`；
  - 状态：`status`，CHECK 只允许 `pending`、`shelved`、`submitted`、`deleted`；
  - 内容：`description`、`ticket`、`files_count`、`exclusive_files jsonb`、`warnings jsonb`；
  - 关联：`swarm_review_id`、`task_id`；
  - 时间：`submitted_at`、`created_at`、`updated_at`。
- **`issue_p4_changelist`** 表：`issue_id`、`changelist_id`、`workspace_id`、`link_source`（`task` / `manual`）、`linked_at`。
- 唯一索引 `(workspace_id, server_key, original_number)` 和 `(issue_id, changelist_id)`，以及查询用索引。每个索引都是 `CREATE ... INDEX CONCURRENTLY`，各自单独一个迁移文件。不加外键。
- 删除清理：在 `pkg/db/queries/issue.sql`（501 行附近的 issue 删除清理）和 `workspace_delete.sql` 里补上这两张表；同时更新 `handler/workspace_delete_manifest_test.go`。

#### 2.2 SQL 和 handler

- `pkg/db/queries/p4_changelist.sql`（新文件）：`UpsertP4Changelist`、`LinkIssueP4Changelist`、`ListP4ChangelistsForIssue`、`ListOpenP4ChangelistsForDaemon`、`UpdateP4ChangelistStatus`、`MoveIssueFromP4Changelists`。改完执行 `make sqlc`。
- `handler/p4_changelist.go`（新文件）：
  - daemon 路由：`POST /api/daemon/tasks/{taskId}/p4/changelists`（`ReportTaskP4Changelist`，校验任务归属本 daemon 的方式同 `/tasks/{taskId}/progress`）；`GET /api/daemon/p4/changelists/open`；`POST /api/daemon/p4/changelists/status`。
  - 用户路由：`GET /api/issues/{id}/p4-changelists`；`POST`（按 CL 号手动关联，状态先记为未知，等轮询补齐）；`DELETE .../{clId}`（取消关联）。
  - 推送新事件 `p4_changelist:updated`（在 `pkg/protocol/events.go` 加一行）。
- 自动推进 issue：`maybeAutoCompleteIssueFromP4`。
  - 只在 issue 没有 GitHub/VCS PR 关联时生效。
  - 关联的 CL 全部提交后，移到 `resolvePRMergeTarget`（`pr_auto_complete.go:155`）给出的目标状态。
  - 尊重 issue 级的开关 `GetIssuePRAutoCompleteDisabled`。
  - 不修改上游 `ListIssueLinkedPullRequestStates` 里的 union。
- Swarm review 通过 `swarm_review_id` 挂到 CL 上。

#### 2.3 daemon 轮询：`server/internal/daemon/p4_status_loop.go`（新文件）

- `func (d *Daemon) p4StatusLoop(ctx context.Context)`，间隔由 `MULTICA_P4_POLL_INTERVAL` 控制（默认 5m）。
- 每轮：
  - 领取本 daemon 名下未结束的 CL；
  - 对每个 CL 执行 `p4 -ztag describe -s -O <original_number>`，判断是否已提交，并拿到改号后的新号；查不到就记为 `deleted`；
  - 调 Swarm `GET /api/v11/reviews/{id}` 拿 review 状态；
  - 把结果上报服务端。
- 在 `daemon.go:2206-2219` 那组 `go d.xxxLoop(ctx)` 里加一行启动。
- 阶段 1 的 finish 也改为调用 `ReportTaskP4Changelist`，把 CL 写进服务端。

#### 2.4 前端

- core 层：
  - `packages/core/types/p4.ts`（新文件）；
  - `packages/core/api/schemas.ts` 加 `P4ChangelistSchema`，通过 `parseWithFallback` 解析，未知状态有兜底；补畸形响应测试；
  - `packages/core/p4/queries.ts`（新文件），query key 为 `["p4","changelists",wsId,issueId]`；
  - `packages/core/realtime/use-realtime-sync.ts` 收到事件后 invalidate。
- views 层：`packages/views/issues/components/p4-changelists-section.tsx`（新文件）。
  - 每张 CL 卡片显示：CL 号（改号后显示新号）、状态、文件数、Swarm review 状态。
  - 提醒：占用的 `+l` 文件、不在 head 的文件、缺单号。
  - 支持复制 CL 号。
  - 挂在 `issue-detail.tsx` 中。显示条件改为"任务所属项目有 `perforce_depot`，或这个任务已有 CL"，不再只看 workspace 级的 `p4_depots`（`issue-detail.tsx:1369-1370`）。
- `pull-request-list.tsx`：已挂在某个 CL 上的 Swarm 行不再在 PR 列表里重复出现；"已提交"不再显示为 "Merged"。
- 文案：en、zh-Hans、ja、ko、fr 五套 locale 都要补。
- 易协作发布对话框（`yixiezuo-source.tsx`）：自动预填"CL xxxx（已提交 @yyyy）· Swarm #n"。

#### 阶段 2 验收

- DB 测试（`testutil` / `dbfx`）：
  - upsert 幂等，改号处理正确；
  - 状态流转 pending → shelved → submitted / deleted；
  - 自动推进的各种开关组合；
  - issue 删除、workspace 删除时清理干净，manifest 测试覆盖新表。
- 前端：schema 的畸形响应测试；CL 卡片的各状态组件测试。
- 实机：人提交测试 CL 后 5 分钟内，issue 上显示"已提交 @新号"，并按设置推进状态。

### 阶段 3：按需增强

- **评审类任务**：让 agent 审人的 CL（`p4 describe -S -du`、`p4 print`、`multica p4 swarm comment`）。raw p4 只读查询已经可用，只需补 brief 和技能说明。
- **收编 UE 编辑器的 checkout**：在 UI 上一键把"任务期间新进 default CL"的文件移进任务 CL。默认不自动搬，避免误搬人自己的改动。
- **跨机器续做**：在另一台机器上 `unshelve -s 旧CL -c 新CL`。
- **专用 agent 机**：多 slot；首次用 `p4 flush` 只登记 have list；只同步 Source、Config、Plugins。
- **`--until-cl submitted` 唤醒条件**：需要改上游的 `issue_wakeup_condition.go`。
- **旧模式结束时删 client**：没有打开文件时执行 `client -d`。

---

## 6. 上游同步策略

本仓库会定期同步上游 main。为了降低冲突：

- **新逻辑尽量放在 fork 自有文件里**：
  - daemon 侧：`p4cache/*`、`p4_handlers.go`、`p4_workspace.go`（新）、`p4_status_loop.go`（新）、`p4_api.go`（新）；
  - 服务端：`handler/p4_ticket.go`（新）、`handler/p4_changelist.go`（新）、`handler/swarm.go`、`p4depot/p4depot.go`、`pkg/db/queries/p4_changelist.sql`（新）；
  - CLI：`cmd/multica/cmd_p4*.go`；
  - 前端：`packages/core/p4/*`（新）、`p4-changelists-section.tsx`（新）、`yixiezuo-source.tsx`；
  - 文档：perforce 相关文档。
- **上游文件只加挂钩点**（括号里是预估改动行数）：
  - daemon 侧：`daemon/daemon.go`（约 15）、`daemon/types.go`（3）、`daemon/health.go`（1）；
  - execenv：`execenv/execenv.go`（10）、`runtime_config.go` / `context.go` 的导出包装（10）、`runtime_config_sections.go`（约 40）；
  - 服务端：`handler/daemon.go`（5）、`handler/agent.go`（2）、`handler/project_resource.go`（4）、`cmd/server/router.go`（6）、`pkg/protocol/events.go`（1）、`issue.sql` / `workspace_delete.sql`（4）；
  - 前端：`core/api/schemas.ts`（30）、`use-realtime-sync.ts`（3）、`issue-detail.tsx`（5）、`pull-request-list.tsx`（5）、各 locale 文件。
- **迁移号段**：
  - 上游最新是 572，fork 也是 572。fork 新迁移如果用 573 及以后的号，下次同步上游时就会撞号，`TestMigrationNumericPrefixesAreUnique` 会失败；而已发布的迁移不允许改名。
  - 建议 fork 使用专用高位号段（如 9001 起），并把这个约定写进 CONTRIBUTING。
  - 迁移按文件名字典序执行（`internal/migrations/migrations.go:66-70`），新表不依赖上游的后续迁移，所以执行顺序不影响结果。

## 7. 配置项

| 配置 | 默认值 | 位置 |
|---|---|---|
| `MULTICA_P4_TIMEOUT` | 10m | daemon 环境变量（读法同 `durationFromEnv`） |
| `MULTICA_P4_POLL_INTERVAL` | 5m | daemon 环境变量（阶段 2） |
| `allow_exclusive` | false | `p4depot.Ref`，按 depot 配置（CLI `project resource add/update` 加参数；设置页在阶段 2 补） |
| `auto_review` | true | `p4depot.Ref`，按 depot 配置 |

## 8. 检查命令

- **Go**：Windows 上用 `scripts/go-test-with-agent-cli-guard.ps1` 跑 `./internal/daemon/... ./internal/handler/... ./cmd/multica/...`；改了 SQL 之后执行 `make sqlc`。
- **隔离 P4 集成测试**：设置 `MULTICA_TEST_P4_BIN` 和 `MULTICA_TEST_P4D_BIN` 为本机可执行文件的绝对路径，再通过同一 guard 运行 `go -C server test -tags=p4integration ./internal/daemon -run '^TestP4IsolatedServerSubmitAndImportedSync$' -count=1 -v`。测试自行创建临时目录和 loopback P4D，验证 shelf 提交、未改动文件排除和 stream import 同步；不连接共享服务器或使用已有工作区。
- **前端**：`pnpm typecheck`、`pnpm lint`、`pnpm test`。
- **合并前**：`make check`。移动端不涉及。
- 每个阶段都必须做实机验收：静态测试全部通过，不等于功能验证通过。

## 9. 风险与对策

| 风险 | 对策 |
|---|---|
| agent 绕过环境变量屏蔽，直接执行 `p4 -c <client> edit` | brief 明令禁止。finish 时检查任务期间新出现、但不在任务 CL 里的打开文件，并在报告里列出 |
| 人和 agent 同时改同一个文件 | edit 前先 fstat；人已经打开的文件直接拒绝 |
| UE 编辑器自动 checkout 的文件进了 default CL | 任务开始时做快照，结束时报告；可以用 `reopen` 收编；阶段 3 在 UI 上做一键收编 |
| 运行中途，人提交或删除了任务 CL | 后续写操作会报错；daemon 直接报出来，不偷偷新建 CL |
| 构建脚本需要读工作区的 P4 信息 | 被环境变量屏蔽后会失败；真遇到时再给 depot 加一个开关 |
| 文件不在 head | edit 返回警告，提交时由人 resolve |
| fstat 的开销 | 只 fstat agent 传入的文件，永远不扫描整个 client |
| 重新 shelve 能否更新 Swarm review | 待实测；如果不能，就改为调用 Swarm API 更新 |
| edge 工作区 | shelve 时加 `-p`；Swarm 只能读到 commit server 上的 shelf |

## 10. 暂不做 / 撤回的想法

| 想法 | 原因 | 何时重新考虑 |
|---|---|---|
| 每个任务独立 client 并全量 sync | DJ01 一份工作区 328GB，不可行 | 已删除；P4 文件任务只使用既有 `in_place` 工作区 |
| 专用 agent 机、多 slot 并行 | 磁盘和首次同步成本太高；工作区模式已能满足"每人一个 agent" | 并发需求明确之后（阶段 3） |
| 服务器 trigger / Swarm webhook | 没有 admin 权限 | 能拿到 Swarm 管理员支持时，作为轮询的加速通道 |
| agent 无授权提交 | 默认拒绝提交；权限只由人类工作区管理员授予 | 不支持；不提供项目级授权或机器凭证自授权 |
| task stream / sparse stream | 团队目前没用 task stream | 有分支隔离需求时 |
| P4 Proxy | 只对专用机有意义 | 随专用 agent 机一起考虑 |
| Swarm 卡片继续当作 PR 展示 | 语义错误（committed 显示成 "Merged"） | 阶段 2 改为 CL 卡片 |
| 自动把 default CL 里的新文件搬进任务 CL | 可能把人自己的改动搬走 | 只报告，由人手动或一键收编 |

## 11. 待确认

1. **迁移号段**（阶段 2 才用到）：fork 专用高位号段是否可行，需要和负责同步上游的同学确认。
2. **实机验收的授权**：会在共享服务器上产生一个 pending CL 和一个 shelf，可能还有一个 Swarm review，测试后删除或归档。
3. 不是从易协作导入的任务，**单号写在任务描述里**（`refs #<单号>`）。
4. **默认值**：自动建 Swarm review 默认开；`+l` 文件默认不让 agent 通过 daemon 打开。两者都可以按 depot 修改。
