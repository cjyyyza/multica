# Agent Note: CL 文件与差异按需读取

Status: implemented
Kind: feature
Protocol: agent-notes/v1

## 问题

任务上的 CL 卡片只有状态和文件数。Web 或桌面界面需要读取实际文件和差异，但 P4 连接、凭证及未搁置文件在 daemon 机器上。卡片中保存的 port 也不能代替项目当前配置授权新的服务器连接。

## 决策

共享界面点击 CL 编号或文件数，打开文件列表及单文件 unified diff。读取经现有 runtime pending-work 通知和心跳认领机制交给 daemon；新增 `p4-content-v1` capability，缺少可见且在线的新 daemon 时明确报错。

请求和结果使用十分钟的临时存储，配置 Redis 时各 API 节点共用队列，否则使用单节点内存存储；不把源码或 diff 写入产品数据库。请求绑定成员、工作区、任务、CL 和 runtime，轮询复查任务访问及 CL 关联。daemon 取上下文时重新解析当前 P4 项目资源，并沿用轮询的服务器授权规则。未被认领的轮询会再次通知 daemon，避免连续读文件的通知落在 daemon 一秒限流内后等待下一轮十五秒心跳。

版本选择按当前 P4 实际状态：有 shelf 时只列出 shelf 文件并与其基线比较；已提交时比较该次提交与上一版；未搁置时可读所属机器所有者的本地副本。共享 runtime 的其他成员不能读取私有工作文件。单文件读取先验证路径属于对应 CL 快照；本地文件还需匹配 client 的 Owner、Host、Root 和映射。二进制仅显示文件信息，单文件预览最多 512 KiB / 6000 行并显示截断提示。

这是产品的只读查看能力，不提供任意原生 argv 代理，也不创建、搁置或提交 CL；[P4 仅原生执行](2026-11-08-p4-native-direct-only.md) 的任务执行边界继续适用。

## 替代方案

API 服务器直接运行 P4：服务器未必能连接成员的 P4 网络，且不应持有机器本地 client 和凭证，因此使用 daemon。

随 CL 状态轮询持久化全部 diff：会在无人查看时反复读取源码，还需要管理源码快照的数据库生命周期，因此采用按需读取和临时结果。

## 后果

Web 和桌面共用 core 查询和 views 面板。打开面板及切换文件需要 daemon 在线；该功能发布时需一起更新服务器、界面和 daemon。移动端没有接入此查看入口。

读取不会修改 P4。连续选择文件会产生短期请求，取消界面查询会停止客户端轮询；已认领的只读请求仍在 daemon 的一分钟执行预算内结束。

## 验证与限制

2026-11-12：全仓 `pnpm typecheck`、`pnpm lint` 通过，lint 有未改动文件的既有警告。core 的 schema 与查询定向测试共 238 项通过，CL 组件测试 21 项通过。Windows Go 检查使用 `scripts/go-test-with-agent-cli-guard.ps1`；新 P4 内容测试、daemon 读取接线、handler 请求回读/隔离、心跳相关回归及真实 Redis 的跨节点队列测试通过。

只读访问真实 CL 831760，得到 4 个 shelf 文件及 4 份非空 diff。浏览器使用实际共享组件并注入这份读取结果，验证文件切换、Esc 和焦点恢复，记录浅色、深色及 390px 窄窗口截图；产物在忽略目录 `test-results/p4-content-preview/`。这是组件和真实 P4 数据的分层验证，未完成生产服务器—daemon—安装客户端整条链路验收，未发布。

全量 `pnpm test` 在未修改的 `packages/ui/icons/icons.test.ts` 中有两项图标生成一致性检查失败，未把全量测试记为通过。未执行真实 agent smoke test。
