# P4 changelist workflow — implementation contract

Read with [p4-changelist-workflow-plan.md](p4-changelist-workflow-plan.md). The plan says *what* and *why*; this file fixes the shared names and wire shapes so parallel work streams stay compatible. Do not change anything here inside a work stream. If something here is wrong, report it in your final summary and implement against the contract as written.

The plan's §0 decisions are binding: agents never submit; the CL description must contain `refs #<易协作单号>`; the agent works inside the human's own client and does not revert changed files at the end.

## Already landed in the contract commit

- `p4depot.Ref`, `handler.P4DepotData`, and `daemon.P4DepotData` gain `AllowExclusive bool` (`allow_exclusive`) and `NoAutoReview bool` (`no_auto_review`). The three structs must stay field-for-field identical because the code converts between them directly.
- `p4depot/description.go`: `TicketFromText`, `ValidTicket`, `ChangeDescription(ticket, title, issueKey)`, `DescriptionHasTaskTrailer`, `TaskTrailerPrefix`.
- `handler.AgentTaskResponse.P4Ticket` / `daemon.Task.P4Ticket` (`p4_ticket`): sent only when `p4_depots` is non-empty.
- `execenv.P4DepotForEnv.Charset`, `execenv.P4WorkspaceForEnv`, `execenv.TaskContextForEnv.P4Workspace`.
- `packages/core/types/p4.ts` (`P4Changelist` and related types), plus `allow_exclusive` and `no_auto_review` on the depot ref types.

## Ownership by work stream

Files outside your stream are read-only for you. Every new file belongs to the stream that creates it.

| Stream | May edit |
|---|---|
| **A**: daemon P4 core | `server/internal/daemon/p4cache/**`, `server/internal/daemon/p4_handlers*.go`, `server/internal/daemon/p4_workspace*.go` (new), `server/internal/daemon/p4_status_loop*.go` (new), `server/internal/daemon/p4_api*.go` (new), `server/internal/daemon/health.go` (P4 route registration only), `server/internal/daemon/daemon.go` (P4 hooks only: Daemon fields, `New`, `runTask` hook, `taskMulticaEnvironment`, loop start, `convertP4DepotsForEnv`), `server/internal/daemon/config.go` + `helpers.go` (P4 env vars only), `server/internal/daemon/client.go` (new P4 report methods only), `server/internal/daemon/execenv/runtime_config.go` + `context.go` (exported path wrappers only), `server/cmd/multica/cmd_p4*.go` |
| **B**: server data and API | `server/migrations/9001_*`–`9099_*` (new), `server/pkg/db/queries/p4_changelist.sql` (new), `server/pkg/db/generated/**` (sqlc output only), `server/pkg/db/queries/issue.sql` + `workspace_delete.sql` (cleanup lines only), `server/internal/handler/p4_ticket*.go` (new), `server/internal/handler/p4_changelist*.go` (new), `server/internal/handler/daemon.go` (P4Ticket fill only), `server/internal/handler/project_resource.go` (fallback rule only), `server/internal/handler/workspace_delete_manifest_test.go`, `server/internal/handler/daemon_test.go` (fallback tests only), `server/cmd/server/router.go` (P4 routes only), `server/pkg/protocol/events.go` (one event), `server/cmd/migrate/main.go` (`concurrentIndexCleanups` entries only), `CONTRIBUTING.md` (migration numbering note) |
| **C**: brief, skills, docs | `server/internal/daemon/execenv/runtime_config_sections.go` and its tests, `server/internal/service/builtin_skills/multica-platform/**`, `server/internal/service/builtin_skills_test.go`, `apps/docs/content/docs/perforce*.mdx`, `apps/docs/content/docs/project-resources*.mdx`, `docs/ue-p4-popo-yixiezuo-workflow.md` |
| **D**: frontend | `packages/core/api/schemas.ts` (+ test), `packages/core/api/client.ts` (P4 methods only), `packages/core/p4/**` (new), `packages/core/realtime/use-realtime-sync.ts` (P4 invalidation only), `packages/core/types/events.ts` (one event), `packages/views/issues/components/p4-changelists-section*.tsx` (new), `packages/views/issues/components/issue-detail.tsx` (mount and visibility only), `packages/views/issues/components/pull-request-list.tsx` (+ test), `packages/views/issues/components/yixiezuo-source.tsx`, `packages/views/locales/*/issues.json`, `packages/views/locales/*/settings.json` |

## Daemon local HTTP (A implements, C documents)

The agent reaches these only through the `multica` CLI; requests carry the task token as today.

`POST /p4/run`: existing request shape. When the task is bound to a workspace (`p4Binding(taskID) != nil`):
- Optional fields: `port`, `depot`, `stream`, `workdir`. The daemon fills them from the binding.
- `changelist` must be empty or equal to the task CL.
- Allowed actions: `edit`, `add`, `delete`, `move`, `reopen`, `revert`, `reconcile`, `shelve`, `change`, `opened`, `describe`.
- Rejected actions: `submit` (removed in every mode), `sync`, `unshelve`.
- Success response: the existing `MutateResult` plus `"warnings": [{"kind": string, "files": [string]}]` (omitted when empty).
- Rejection: HTTP 409 with JSON body `{"code": string, "message": string, "files": [string]?, "holders": [string]?}`. `code` is one of:

| `code` | Meaning |
|---|---|
| `ticket_required` | the task has no 易协作 ticket |
| `opened_by_you` | the file is already in one of the human's changelists, including default |
| `exclusive_locked` | someone else holds the +l lock |
| `exclusive_not_allowed` | +l file, and the depot does not set `allow_exclusive` |
| `outside_task_change` | revert/reconcile/reopen target outside the task CL |
| `sidecar_path` | the path is a Multica-written file |
| `action_not_allowed` | submit/sync/unshelve |
| `not_in_workspace` | the path is outside the client root |

- The CLI prints `message`, then `files` / `holders` one per line, and exits non-zero.

`GET /p4/status` (new): with no binding, returns 404 `{"code":"no_workspace","message":...}`. With a binding, returns:

```json
{"port":"","user":"","client":"","root":"","stream":"","edge":false,"ticket":"","change":"","files":[{"depot_file":"","action":"","type":""}],"warnings":[{"kind":"","files":[]}]}
```

`POST /p4/sync`: rejected with 409 `action_not_allowed` while a binding exists. Otherwise it keeps the legacy task-client behavior, plus the size pre-check.

`POST /p4/swarm`: unchanged.

Warning kinds: `not_head`, `opened_elsewhere`, `exclusive_held`, `default_changelist_files`, `unexpected_opens`.

## Server API (B implements; A and D consume)

All JSON uses snake_case. A changelist row serializes exactly as `packages/core/types/p4.ts` `P4Changelist`. Unknown `status` values parse as `pending` on the client.

Daemon routes (DaemonAuth group):

| Route | Body | Response |
|---|---|---|
| `POST /api/daemon/tasks/{taskId}/p4/changelists` | see below | 200 with one `P4Changelist`; creates or updates the row and links it to the task's issue (`link_source="task"`) |
| `GET /api/daemon/p4/changelists/open?workspace_id=<uuid>` | — | `{"changelists":[{"id","port","number","original_number","status","swarm_review_number","swarm_url"}]}`; rows with status `pending` or `shelved` that this daemon reported, for workspaces it can access |
| `POST /api/daemon/p4/changelists/status` | see below | `{"updated": int}` |

The task-report body:

```
{"port","server_key","charset","number","original_number","status","client","p4_user","description","ticket","files_count","exclusive_files":[],"warnings":[{"kind","files":[]}],"swarm_review":{"number","url","state","author","description"}?}
```

The status body:

```
{"workspace_id":"<uuid>","updates":[{"id","status","number","submitted_at","swarm_review_state"}]}
```

Rules for the task-report route:
- Ownership is checked with `requireDaemonTaskAccessWithWorkspace`.
- The row is keyed by `(workspace_id, server_key, original_number)`.
- When `swarm_review` is present, upsert `swarm_review` and attach it to the changelist (`swarm_review_id`). That review must not also be returned in the issue's pull-request list.

User routes (authenticated issue group):

| Route | Body | Response |
|---|---|---|
| `GET /api/issues/{id}/p4-changelists` | — | `{"changelists":[P4Changelist...]}`, newest first |
| `POST /api/issues/{id}/p4-changelists` | `{"number": int, "port"?: string}` | 201 with one `P4Changelist`, `link_source="manual"`, status `pending` until the daemon's next poll |
| `DELETE /api/issues/{id}/p4-changelists/{changelistId}` | — | 204 |

For the manual link:
- `port` defaults to the only `perforce_depot` on the issue's project; with zero or several depots, a missing `port` returns 422.
- A manual row has no `daemon_id`. The open-changelists route also returns manual rows whose port matches a depot that daemon serves, and the first daemon to report them claims them.

Realtime event `p4_changelist:updated`, payload `{"issue_id": "<uuid>", "changelist_id": "<uuid>"}`. Published after each create, update, link, or unlink.

Issue auto-advance:
- Runs when every changelist linked to an issue is `submitted` and the issue has no GitHub or VCS pull request linked.
- The issue moves to `resolvePRMergeTarget`, and the per-issue `GetIssuePRAutoCompleteDisabled` opt-out is respected.

## Migrations

- Fork migrations use the reserved range **9001–9099**, so they cannot collide with upstream numbering.
- One concurrent index per file. Every concurrent index gets an entry in `cmd/migrate/main.go` `concurrentIndexCleanups`.
- No foreign keys. Deletes are swept in `issue.sql` / `workspace_delete.sql`, and the manifest test lists the new tables.

## Verification each stream runs before finishing

Go, from `server/`:

```powershell
$env:DATABASE_URL = "postgres://multica:multica@localhost:5432/multica_p4_<stream>?sslmode=disable"
pwsh ../scripts/go-test-with-agent-cli-guard.ps1 go -C . test <your packages> -count=1
```

Create that database once per stream: `go run ./cmd/migrate up` against the same `DATABASE_URL`. If the DB is unreachable, DB-backed suites exit green with a "Skipping" line; say so in the summary instead of claiming coverage.

Frontend, from the repository root: `pnpm typecheck`, `pnpm lint`, `pnpm --filter @multica/core test`, `pnpm --filter @multica/views test`.

Never run `p4` against the real server, never start a real agent CLI, never push.
