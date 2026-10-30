# Runtimes and repos

A runtime is the execution target behind an agent. A daemon owns local runtime
processes and claims queued tasks from the server.

- [Core model](#core-model)
- [CLI](#cli)
- [Task CLI boundary](#task-cli-boundary)
- [Debugging an agent that did not run](#debugging-an-agent-that-did-not-run)
- [Repos](#repos)

## Core model

The chain is:

1. a user action creates or updates a queued task;
2. the task points at an agent and runtime;
3. the server wakes the runtime over the daemon websocket when possible;
4. the daemon polls and claims the task;
5. the server returns task context, repos, project resources, prior
   session/workdir hints, and a task token;
6. the daemon prepares a workdir and launches the provider CLI;
7. `multica repo checkout` talks to the local daemon, not directly to GitHub.
   Perforce edits go through the local daemon, in the person's existing Helix workspace. The daemon does not create a task client or sync a separate depot copy.

## CLI

```bash
multica runtime list --output json
multica runtime usage <runtime-id> --output json
multica runtime activity <runtime-id> --output json
multica runtime update <runtime-id> --target-version <version> --output json
multica runtime delete <runtime-id>
multica repo checkout <url>
multica repo checkout <url> --ref <branch-or-sha>
multica repo checkout <url> --fresh
multica p4 edit -- <files>
multica p4 add -- <files>
multica p4 add-files -- <files>
multica p4 filelog -- <files>
multica p4 describe -c <number>
multica p4 fstat -- <files>
multica p4 changes
multica p4 grep -e <pattern> -- <files>
multica p4 dirs -- <directories>
multica p4 resolved -- <files>
multica p4 resolve -am -- <files>
multica p4 reconcile -n -- <files>
multica p4 status --output json
multica p4 swarm create --output json
multica p4 swarm link --review <id>
```

Runtime and repo commands affect active agent execution. Do not restart daemons,
update runtimes, or check out arbitrary repos just to test.

`runtime usage` reports what the provider CLI reported. Claude and CodeBuddy
usage prefers the CLI's final per-model totals. If a run ends without usable
final usage, Multica can recover only main-loop input and cache tokens: split
assistant events with the same response ID count once. Output tokens stay zero
when no final count is available; that does not establish that the model
produced no output. Subagent totals require final per-model usage. Streams that
omit response IDs retain best-effort per-event input/cache accounting. These
fallback figures can be incomplete; use the provider's billing records for
actual charges. This correction applies to new runs, not historical usage rows.

`runtime update` and `runtime delete` are writes. Starting a runtime update is
limited to its owner or a workspace owner/admin; the original initiator may keep
polling that specific in-flight request if their admin role changes.

`runtime delete` removes a runtime registration; if active agents are still
bound, it refuses unless the user explicitly passes `--cascade`, which unbinds
those agents and cancels their queued/running tasks before deleting the runtime.
Unbinding keeps the agents and everything they own — instructions, skills,
chats, labels, channel installations, autopilots and task history — and only
clears the agent's runtime binding; an unbound agent cannot run until it is
bound again (`multica agent update <id> --runtime-id <runtime-id>`), and every
trigger path refuses it with `agent_runtime_required`.

`repo checkout` creates a dedicated branch in the task working directory. Most
runtimes use a linked worktree; Linux and Windows Codex use task-local Git
metadata so a task can stage and commit without making the shared repository
cache writable.

Running `repo checkout` again where the repository is already checked out
(the same task, a follow-up turn, or a reused workdir) never silently discards
work:

- a checkout with uncommitted changes, untracked files, or commits that no
  remote ref reaches is kept exactly as it is — no reset, clean, branch switch,
  or branch deletion — and only its remote refs are fetched;
- a checkout already on the current task's branch is treated as done, and only
  its remote refs are fetched;
- a clean checkout with nothing unpushed on some other branch still moves to a
  new branch from the latest default branch (or `--ref`).

When a checkout is kept, the command says so and reports its branch and how
many uncommitted files and unpushed commits it holds. `--fresh` discards the
existing checkout's uncommitted changes and untracked files and starts over on
a new branch from the latest default branch (or `--ref`). It deletes no branch
holding unpushed commits, so those commits stay on the old branch:

- with task-local Git metadata, the old branch stays in the checkout, including
  when `--fresh` replaces a linked worktree left by an older daemon;
- with a linked worktree, the old branch lives in the daemon's shared
  repository cache, whose periodic cleanup drops `agent/*` branches that no
  checkout has checked out.

Push any commits you still need before using `--fresh`. This needs a daemon
that includes the change; older daemons always start over.

`repo checkout` requires both `MULTICA_DAEMON_PORT` and the injected task-scoped
`MULTICA_TOKEN`; it is intended to run inside the active daemon task and from
that task's workdir (or a descendant). The local daemon authenticates the token
against its active-task registry, derives workspace/task/agent identity itself,
and rejects a caller-supplied workdir outside that task. If either variable is
absent, you are not in the normal agent checkout path. When a project
`github_repo` resource has `resource_ref.ref`, `repo checkout <url>` uses that
ref by default for the current task; an explicit
`repo checkout <url> --ref <branch-or-sha>` overrides it.

Perforce work runs in the requesting person's own Helix client when the
project carries both an `in_place` `local_directory` on this daemon and a
`perforce_depot`, and the daemon resolves a client under that directory. The
runtime brief names that client. There is no task-client mode: the daemon
never creates a client or downloads a separate depot copy into the task directory.

An agent shelves by default; the person who owns the files verifies in the
editor and submits unless the workspace settings explicitly allow agent submission.

### Workspace mode

The daemon creates one pending changelist for the task and every file the
agent opens goes into it. Its description carries `refs #<ticket>` — the
易协作 ticket number, resolved from the issue's import record or from a
`refs #<ticket>` in the issue title or description. **Without a ticket no file
can be opened**: `p4 edit` returns `ticket_required`, and the fix is to ask a
human to put `refs #<ticket>` in the issue description, not to invent a
number.

Do not pass `--port` or `--depot`, and do not look up P4PORT from Project
Context, `p4 set`, or `p4 info`. The daemon fills both from the binding.
Passing either flag is optional and, if you do pass it, must match that
binding; a spelling that only looks equivalent is rejected. `-c` is
`--changelist`. On a write it may only name the task changelist, so omit it.
`multica p4 describe -c <number>` is the exception: it only reads that
changelist when every file is in this task's depots. `multica p4
status --output json` reports the client, root, stream, ticket, changelist
number, its files, and any warnings. `multica p4 opened -F "%depotFile% %change%"`
prints each file's depot path and changelist (`%change%` is the number or
`default`, not the words `change N`). The same rows are `files` in
`multica p4 opened --output json`. `multica p4` does not pass other p4 flags
through: `opened -a` and `-x` are rejected, and `p4 opened -a` is not a
substitute. `revert -a` and `shelve -f` are the native short forms of
`--unchanged` and `--force`. Submit, sync, and unshelve are off
unless that depot turns on `allow_submit`, `allow_sync`, or `allow_unshelve`
in the admin-managed Settings → Code catalog (each defaults to off, and a
disabled one answers `action_not_allowed`). Project resources cannot grant
these permissions; unmatched catalog entries keep all three disabled. Only
run `multica p4 submit`, `multica p4 sync`, or `multica p4 unshelve` when the
runtime brief explicitly enables that action. Sync never creates a client. The daemon also
rejects a write that would touch a file already open
in one of the human's changelists (`opened_by_you`), a file someone else has
locked (`exclusive_locked`), an exclusive-lock file on a depot that does not
set `allow_exclusive` (`exclusive_not_allowed`), a revert or reconcile outside
the task changelist (`outside_task_change`), or a path outside the client root
(`not_in_workspace`). Raw `p4` writes are blocked in the task environment
through `P4CONFIG` / `P4CLIENT`. Read history with `multica p4 filelog -- <files>`
(each revision and its changelist description) and `multica p4 describe -c <number>`.
File state in this client is `multica p4 fstat -- <files>`; changelist lists are
`multica p4 changes`. Both print p4's own output and forward flags after the verb.
Other scoped reads are `files`, `dirs`, `grep`, `print`, `diff`, `diff2`,
`annotate`, `where`, `have`, `sizes`, `integrated`, `interchanges`, and `resolved`.
Native long options and attached values such as `diff -du3` and `annotate --tab=8`
are preserved; connection flags, `print -o` (including `-ooutfile`), and unscoped
branch/stream views are rejected in both separated and attached forms. `multica p4
resolve` injects the task changelist and needs an automatic mode (`-as`, `-am`,
`-af`, `-at`, or `-ay`) unless `-n` or `-N` previews it; unknown flags are rejected.
Use
`multica p4 reconcile -n -- <files>` for a non-mutating reconcile preview. Raw
depot reads still work, but raw `p4 fstat` cannot see this client.

When the task ends with a pending changelist, the daemon reverts the files that were opened but not
changed, shelves the rest, and — unless the depot sets `no_auto_review` —
opens a Helix Swarm review and links it to the issue. Changed files stay open
for the human. A submitted changelist is read back with its actual submitted
number and file list, and is not shelved again. If the submission receipt
cannot be read, report the failure; never repeat the submit operation.

Do not create a Helix client for the task.
If the brief has no `## Perforce workspace` section, the task is not bound to
an existing workspace: ask a person to attach an `in_place` local directory at
the workspace root, then stop. Credentials stay on the host (`P4USER`,
`P4TICKETS`, `P4CONFIG`); Multica does not store a Perforce password.

Open files with `multica p4 edit` (alias `checkout`) before modifying them;
exclusive-lock files will fail otherwise. `multica p4 add <files>` is `p4 add`
(`add-files` is the same command). `multica p4 add` with no files still
registers a workspace depot. Shelve through `multica p4 shelve`.

### Swarm

Helix Swarm reviews go through `multica p4 swarm …` using the depot's
`swarm_url` and the host Helix ticket. `swarm create` and `swarm link` attach
the review to the current issue (`MULTICA_ISSUE_ID`) and to any issue
identifier in the review description, so it shows in the issue sidebar next to
GitHub pull requests. Do not point a host `p4` client at another UE workspace.

## Task CLI boundary

The daemon injects a task-scoped `mat_` credential for Multica API commands and
a private task-local Multica configuration root. Inside that managed task
context:

- API commands such as `issue list`, `issue get`, and `issue runs` use the
  injected task identity and never fall back to the daemon Owner's saved Multica
  profile.
- `config show` and `config set` operate only on task-local Multica state. A
  missing task config root fails closed.
- `auth status` may verify the task identity but omits all token material from
  its output.
- `daemon status` and `daemon disk-usage` report on the runtime hosting this
  task: `status` probes the daemon-injected health port, and `disk-usage` scans
  the daemon-injected workspaces root. Both refuse `--profile`, `disk-usage`
  also refuses `--all-profiles` and `--workspaces-root`, and its STATUS column
  stays blank because filling it would spend the Owner's credential.
- Human/local profile and daemon commands — including `login`, `logout`,
  `setup`, `workspace switch`, local runtime profile path mutation,
  `daemon start` / `stop` / `restart`, `daemon logs`, and
  `daemon probe-runtimes` — are unavailable. `daemon stop` in particular would
  terminate the daemon running this task and every sibling task on it.

`MULTICA_DAEMON_PORT` alone is a weak, defense-in-depth signal for task-safe API
and profile resolution, not task identity for human/local command rejection.
Genuine tasks also carry `MULTICA_AGENT_ID` / `MULTICA_TASK_ID`,
`MULTICA_TASK_CONFIG_ROOT`, or a daemon-managed workdir marker. When the port is
the only signal, `daemon status` uses the selected host profile's derived health
port; ordinary API and profile-resolving commands still fail closed. Host
operators should not export `MULTICA_DAEMON_PORT`; the daemon derives its host
health port from the selected profile and injects the variable into tasks
itself.

The daemon still preserves the real `HOME` and XDG variables for provider tools
such as `gh`, `aws`, `kubectl`, and npm. This is CLI resolution hardening, not
hard filesystem confidentiality: a process under the same OS user can still open
an explicitly known Owner path. Dedicated Unix users, containers, VMs, or an
equivalent OS boundary are required for that stronger isolation.

## Debugging an agent that did not run

Check in this order:

1. Was a task supposed to be created? Inspect issue/comment/autopilot context.
2. Is the assignee an agent or squad? A squad routes to its leader.
3. Is the agent archived or bound to a runtime the actor cannot use?
4. Is the runtime online? `multica runtime list --output json`.
5. Did the daemon heartbeat recently? Runtime `last_seen_at` is the visible clue.
6. Did the task get claimed or is it stuck pending/running/waiting for local directory?
7. If repo checkout failed, or a Perforce command was refused because the task
   is not bound to an existing workspace, classify it after checking whether
   repo / Perforce context was present in the task/project context.

## Repos

The runtime brief lists repos available to this task. Treat that list as the
authority for agent checkout unless the user explicitly asks to bind a new
project resource.

Workspace repos and project resources are not the same thing:

- workspace repo metadata can appear in workspace context;
- `github_repo` project resources are durable project context and can affect
  future tasks; optional `resource_ref.ref` pins the default checkout ref for
  tasks in that project;
- `perforce_depot` project resources are durable Helix context. The daemon
  edits `resource_ref.port` and `depot` only inside the person's existing
  workspace; it does not sync a task copy;
- `local_directory` resources point at a path owned by a daemon and carry
  local-machine assumptions.

Do not add a project resource just because `repo checkout` failed. First
determine whether the user asked for durable project context or just a task
checkout.

For Perforce work, check the brief for the mode first. In workspace mode you
are already inside the human's client — open files with `multica p4 edit`,
add new ones with `multica p4 add` (or `add-files`), and read the changelist
number from `multica p4 status`. Read file history with `multica p4 filelog`.
Read client file state with `multica p4 fstat` and list changelists with `multica p4 changes`.
Submit, sync, and unshelve require the corresponding
permission in the runtime brief. Open and add through
`multica p4 edit` / `add` and never through a host `p4` client, which may
belong to another UE workspace. Record the changelist, Swarm review
id, and the actual editor/build validation in the task; without submit permission,
a human submits.
An imported 易协作 issue remains unassigned until a member chooses its owner.
Result publication is a separate member-confirmed source operation.

## Qoder Cloud Agents bridge (PoC)

A server-hosted `qoder-runtime` service can register the `qoder_cloud` provider.
Bind a Multica agent to the runtime and set its `runtime_config.qoder_agent_id` to the ID of the selected agent in Qoder Cloud Agents. The Web/Desktop agent creation form and runtime configuration tab provide a name-based agent picker.
The bridge executes issue runs through Qoder's managed session API; it does not
run a local coding-agent CLI. Model, tools, skills and credentials are configured
on Qoder. Local-directory resources and Multica execution overrides are rejected.

Each run's session ID is pinned to the Multica run. A follow-up run on the same
issue continues the agent's previous session when it is idle; otherwise a fresh
session starts. The prompt asks the agent to push a `multica/<identifier>-<slug>`
branch and prefix the pull request title with the issue identifier, so the GitHub
integration links the pull request. The service needs persistent state for
restart recovery. Cancel from Multica to stop remote
work; stopping the bridge process alone leaves remote execution running. Do not
use `runtime update` to upgrade the bridge: deploy its binary through its service
manager. For setup and recovery, consult the Qoder Cloud Agents section of the
Multica runtime documentation or ask the deployment administrator.

The bridge registers `runtime_mode=cloud`. Other daemons may omit
`runtimes[].runtime_mode` to retain local registration; custom CLI profiles
remain local. `qoder-runtime --config <private-config.json> --check` verifies
workspace and Qoder resource access without starting execution.

Daemon `/tasks/{taskId}/messages` requests may include an `idempotency_key`
(maximum 512 bytes) per message. Keys are scoped to a run: repeated keys retain
the first persisted payload and do not rebroadcast it. Omitted keys preserve
append-only behavior. The Qoder bridge uses its remote event IDs as these keys.

Qoder Cloud bridge repository runs require `github_token_files` in its private JSON configuration, mapping each GitHub repository URL to a 0600 token file. Credentials are injected only into session creation requests, never into prompts or recovery state. Managed connections also support repository credential configuration in the runtime dialog.

Workspace admins may also configure one managed Qoder Cloud Agents connection in **Runtimes →
Add → Cloud services → Qoder Cloud Agents** (web and desktop). The server prerequisite is
`MULTICA_VCS_SECRET_KEY`. The server
supervises the bridge, encrypts credentials in the database, and supports idle
stop/restart. Active tasks must finish or be cancelled before configuration
changes. `GET/PUT /api/workspaces/{id}/qoder`, `POST .../qoder/check`, and
`POST .../qoder/stop` expose this flow; writes/checks are admin-only and no
credential plaintext is returned. Blank secrets retain saved values; an empty
`github_tokens` object removes all repository credentials. `max_concurrency`
(1–10, default 3) bounds simultaneous runs, with extra tasks queued in Multica;
`run_timeout_minutes` (5–240, default 60) bounds each run. `GET` also reports
`active_runs` and `queued_runs`.


Managed runtimes store a Qoder personal access token (PAT) and the selected environment. Entering a PAT loads
available environments automatically; a single environment is selected by default.
`POST .../qoder/environments` lists environments using an entered or saved PAT
(admin-only). `GET .../qoder/agents` lists names and IDs using the saved PAT for
workspace members. Lists exclude archived resources. Each Multica agent stores its
own `runtime_config.qoder_agent_id`; each run uses that association and the
shared runtime's environment. An optional `runtime_config.repository`
(`{"url": "https://github.com/owner/repo", "ref": "main"}`) is mounted when the
issue's project binds no repository; the workspace's only repository is the last
fallback when the connection holds its credential. Qoder Cloud Agents model/tool/skill
management remains in Qoder.
