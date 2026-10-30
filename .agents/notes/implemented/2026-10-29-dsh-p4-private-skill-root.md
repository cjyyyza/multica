# Agent Note: P4 托管 skill 的原生发现

Status: implemented
Kind: bug-fix
Protocol: agent-notes/v1

## 问题

运行时只把原生扫描范围内的 `SKILL.md` 收进目录。P4 运行将托管 skill 写到 `p4-context-*/skills`，保护人的 Helix client；仅在提示里提供路径不能让运行时发现或调用这些 skill。

## 决策

托管 skill 保留在运行私有目录，通过已核实的原生入口加入目录。工作目录仍是 Helix client，仓库规则和仓库 skill 继续由运行时读取。只有已接线的入口才在 brief 中声明自动发现；未接入的运行时保留绝对路径，不能把提供文件误称为已注册。

| 运行时 | 接线 |
| --- | --- |
| dsh | `--patch` 在 `--stdio` 前，设置 `skill-filesystem.customSkillDirs` 并保留 `includeDefaultRoots: true` |
| Claude | 私有 `.claude/skills` 配合 `--add-dir` |
| Pi、omp | 对每个 skill 目录追加 `--skill` |
| Codex、Hermes、QwenPaw | 沿用已有的运行 home 或 workspace |
| CodeBuddy、Copilot、Cursor、Grok、Qoder、Qoder CN | 私有 `multica-task` 插件，追加 `--plugin-dir`；保留用户的插件参数，brief 提示使用原生目录中的命名空间 |
| OpenClaw | 在现有运行配置中追加 `skills.load.extraDirs`；通过 CLI 的配置加载器读取用户原来的数组，保留 JSON5、include 和变量解析语义 |
| Kimi Code、Qwen、Kiro、Reasonix、MiniMax Code | 分别使用 `KIMI_CODE_HOME`、`QWEN_HOME`、`KIRO_HOME`、`REASONIX_HOME`、`MINIMAX_DATA_DIR` 指向私有 home |
| Antigravity | 私有 `.agents/skills` 配合额外 `--add-dir`，保留主要工作目录 |
| OpenCode 1.x、DevEco | 分别使用 `OPENCODE_CONFIG_DIR`、`DEVECO_CONFIG_DIR` 的私有 `skills/`；保留原有配置目录的输入，不覆盖配置文件或 inline config 中的 `skills.paths` 数组 |

Kiro 的 home 入口要求已检测到 2.3.0 或更新版本；旧版和未知版本仍给文件路径。OpenCode 的能力判断与实际后端版本分支一致，内置 2.x 不声明支持旧版配置环境变量。

私有 home 复制根配置文件，保留原生子目录链接，合并托管和个人 skill，同目录冲突时托管副本优先；托管文件不会安装到用户 home。已有认证文件和状态目录保持链接，首次会话的原生目录提前建立链接，避免清理私有目录后丢失会话。Windows 目录使用 junction；普通状态文件在符号链接不可用时使用同卷硬链接，不能共享时明确报错，不悄悄复制可变认证状态。Reasonix 仍使用已有的智能体状态目录。进程的 `HOME`、`USERPROFILE` 和 `XDG_*` 保持原值。

以下入口尚未接入，继续提供绝对 `SKILL.md` 路径：

- CodeArts 的发布包包含 OpenCode 代码，但 `SCENARIO=codeartsdoer` 的场景加载器另行选择 skill 根，不能仅凭 `skills.paths` 字段推断它生效。
- TRAE 2 文档列出 `TRAE_HOME` 和 ACP 配置覆盖，但附加 skill 根的字段及旧版兼容范围尚未核实。
- Muse 的用户根依赖进程 `XDG_CONFIG_HOME`，插件安装状态由宿主共享；当前没有实现独立的运行附加根。
- Dim 的发布包可通过 `DIMCODE_HOME/skills` 发现 skill，但该变量也移动根目录中的 SQLite 与认证状态；不能仅改 home 且链接数据库文件来宣称会话安全。
- ZeroClaw 使用按智能体引用的 `skill_bundles` 和安装目录下的共享根，需要单独处理配置、智能体选择及持久状态。
- OpenCode 2.x 不读取已有的私有配置环境通道，当前适配器没有实现 client 外的附加根。

## 替代方案

把托管文件写到 client 的原生目录会污染工作副本；安装到用户的共享 skill 根会导致并发运行互相覆盖。整体修改 `HOME` 或 `XDG_*` 会改变认证、会话及子工具的配置范围。额外工作目录只有在该运行时明确扫描其中的 skill 时才可采用。

dsh 的 `DSH_BUNDLED_SKILL_DIR` 优先级低于同名个人 skill；采用 `customSkillDirs` 后保留仓库优先、托管副本高于个人副本的原生顺序。

## 后果

原生发现的接线按运行时区分，不能靠统一布尔开关覆盖。缺失必需的插件清单、私有 skill 或不可保留的配置会拒绝启动。OpenClaw 增加一次读取原有附加目录的 CLI 调用，准备预算按最坏四个独立 deadline 计算。

dsh 的 patch 会整段替换 `skill-filesystem` 的 config。若 profile 自己也给该行写 config，该运行进程使用托管 overlay，profile 文件本身不变。

## 验证与限制

回归覆盖私有文件落盘、插件参数与用户参数共存、版本对应的 brief、原有 JSONC 配置与 skill/tool 输入保留、子进程实际收到的环境、认证文件共享、首个会话在清理后恢复，以及托管 skill 不被写入 client。原生入口依据官方文档、只读 help 和发布包源码核对；这些测试不代表真实模型、远程 OpenClaw gateway 或运行中的 daemon 已验收。

入口实现见 [私有目录与插件](../../../server/internal/daemon/execenv/private_skill_catalog.go)、[运行 home](../../../server/internal/daemon/execenv/private_skill_home.go)、[附加配置目录](../../../server/internal/daemon/execenv/private_skill_config.go)、[版本判断](../../../server/pkg/agent/private_skill_config.go)。主要来源为 [Kimi Code](https://moonshotai.github.io/kimi-code/en/configuration/env-vars)、[Kiro](https://kiro.dev/docs/cli/reference/settings/)、[OpenClaw](https://docs.openclaw.ai/tools/skills-config)、[Cursor 插件](https://cursor.com/docs/reference/plugins)、[OpenCode 配置目录](https://opencode.ai/docs/config/#custom-directory) 和 [DevEco 发布包](https://www.npmjs.com/package/@deveco/deveco-code)。
