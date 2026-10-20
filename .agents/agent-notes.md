# Agent Notes protocol v1

本文件是可随项目分发的完整约定。共享 `agent-notes` skill 提供工作流程；未安装该 skill 时也按本文执行。项目适配由同目录的 `agent-notes.json` 声明，本文与 `check_agent_notes.py` 是共享技能 v1 的固定版本副本，升级时一起审查。

## 查找与写入范围

先读项目入口规则和 `agent-notes.json`，再搜索其中的 `notes_root`。项目可能只是 Git 仓库的子目录，所有路径以这个项目根目录为基准。

用 `rg -n --hidden -g '!**/archived/**' '<关键词>' '<notes_root>'` 查活跃记录；关键词包括业务概念、符号和备选方案。需要历史时再显式搜索 `archived`。结合当前代码核对命中内容；历史记录、提案和静态说明不证明当前实现或部署状态。

只有现有代码、测试和文档未解释、且可能影响未来选择的决策理由才需要记录。只读评审不写文件。局部文案、格式、机械性修改无需新增记录。已有记录拥有同一决策时优先更新它。不得为了满足模板编造当时考虑过的替代方案。

## 布局与文件格式

`layout: lifecycle` 使用 `{notes_root}/{proposed|implemented|rejected|archived}/YYYY-MM-DD-topic.md`；可在状态下按主题分子目录。`layout: flat` 在已有 ADR 目录直接新增日期命名的记录，状态由头部声明；归档仍使用该目录下的 `archived/`。

日期是首次记录本主题的日期，不伪装成未知的历史决策时间。正文可以使用中文或英文，头部固定：

```markdown
# Agent Note: 标题

Status: implemented
Kind: architecture
Protocol: agent-notes/v1

## 问题
说明值得保存的约束。

## 决策
描述当前已实现的选择。

## 替代方案
记录实际考虑的方案和放弃原因；无法重建的历史明确写未知。

## 后果
说明收益、代价和适用范围。

## 验证与限制
列出具体证据、验证日期与尚未验证的范围。
```

`Kind` 为 `feature`、`bug-fix`、`simplification`、`architecture`、`process` 或 `testing`。分类表达决策性质，不要求创建六套空目录。

| 状态 | 必需章节，中文 / English |
| --- | --- |
| proposed | 问题 / Problem；方案 / Proposal；替代方案 / Alternatives considered；验收标准 / Acceptance criteria；风险 / Risks |
| implemented | 问题 / Problem；决策 / Decision；替代方案 / Alternatives considered；后果 / Consequences；验证与限制 / Verification |
| rejected | 问题 / Problem；方案 / Proposal；替代方案 / Alternatives considered；头部写 `Status: rejected — 原因` |

必需章节有实际内容。implemented 不保留 `方案/Proposal`、`计划/Plan`、`验收标准/Acceptance criteria` 或迁移计划章节；可在验证与限制中说明已知覆盖缺口。提案达到其验收条件后再迁移并改写为实际交付内容；本地实现、远端部署与真实客户端验收分别说明。

## 更新、取代与归档

同一决策的路径、符号或默认值变化，更新原记录中的事实。选择或理由发生逆转时新增记录。部分取代保留两份活跃记录，通过正文相对链接说明各自仍适用的范围。

完全取代可在新记录头部使用 `Supersedes: [旧记录](相对路径)`，旧记录使用 `Superseded-by: [新记录](相对路径)`；这两个字段是互相对应的单条链接。多条或部分关系写在正文中，清楚标明范围。不得仅凭文本相似或记录年龄自动归档。

只有已完成且不再指导当前工作的 implemented 记录可以归档。移动到 `archived/`，保留 `Status: implemented`，增加 `Archived: YYYY-MM-DD`，修复入站链接并写好替代关系后，显式运行：

```text
python .agents/check_agent_notes.py --seal-archives
```

这会在 `.agents/notes-archive.json` 中仅追加新归档文件的 SHA-256；哈希统一 CRLF 为 LF，以便跨平台检出。已封存文件不能修改、删除、移动或重新封存。归档正文允许历史出站链接失效，不能作为当前实现依据。

归档目录应在项目 `.rgignore` 排除。记录仍由 Git/P4 跟踪；搜索排除不等于版本控制排除。规则只约束遵守该搜索方式的客户端，不承诺所有检索器自动过滤历史。

## 旧 ADR 与项目适配

`legacy_files` 是项目初次接入时逐项登记的旧记录/支持文件相对路径。它们保留原有格式和权威等级，校验器会报告数量但不会要求批量改写。新文件不能通过通配符逃过格式检查。旧 ADR 的“历史快照”约定继续有效，不因接入而变成当前实现权威。

`current_sources` 列出项目已有的当前状态/架构入口。阅读相关旧记录时仍需核对这些文件和代码。`project_rules` 记录本项目的适配约束；不得用个人机器绝对路径作为团队运行依赖。

## 检查

校验器只使用 Python 3.10+ 标准库。从项目根目录运行 `python .agents/check_agent_notes.py`；Windows 可用 `py -3 .agents/check_agent_notes.py`，Linux/macOS 可用 `python3`。共享技能脚本可以用 `--project <项目根目录>` 检查指定项目。

默认只读，检查配置路径范围、文件名、状态、章节、相对链接目标、双向取代关系和归档哈希。不访问网络、不启动产品服务、不提交代码。Markdown 链接检查验证文件目标，不验证标题锚点或远端网页。

Git 项目可附加 `--base-ref <可信基线>`，以阻止通过改写 manifest 来重封存旧档案；支持位于 Git 子目录的项目。P4 项目按实际 changelist 审查 manifest 的追加性质；未提供 Git 基线时不声称已验证历史 manifest 未被改写。

校验不能判断方案是否正确、记录是否真实、功能是否部署，也不会自动证明验收标准已经满足。这些判断必须有与改动相称的代码、测试或运行证据。
