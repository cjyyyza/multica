# P4 changelist workflow — implementation contract

Read with [p4-changelist-workflow-plan.md](p4-changelist-workflow-plan.md). This document describes the current implementation and shared wire contracts; update it when those boundaries change.

Agents submit only when the workspace's admin-managed depot catalog enables `allow_submit`; `allow_sync` and `allow_unshelve` are also default-off. Machine credentials cannot update `p4_depots`. Project resources do not persist these action grants; claim overlays them from an exact workspace catalog match, and an unmatched entry grants none. The CL description must contain `refs #<易协作单号>`; the agent works inside the human's own client and does not revert changed files at the end.

An exact catalog match fills an omitted project `charset`; an explicit project charset remains. Swarm destinations always come from the exact admin-managed catalog entry, and an unmatched project resource receives no Swarm URL. The daemon checks the current workspace catalog before sending a ticket, including automatic review requests; a conflicting explicit URL is rejected. Task-selected resource metadata cannot grant a credential destination. Parent depot paths and stream ancestry do not inherit action permissions.

Project `local_directory` bindings, including P4 client directories, are private to their `created_by` member. Resource lists omit other members' bindings and ownerless legacy rows; update/delete return not found for those rows. The frontend applies the same ownership rule before rendering or selecting a directory, including cached responses. The same member's bindings on other machines remain visible. Shared `perforce_depot` and `github_repo` configuration stays workspace-visible.

Workspace resource events carry only project/resource IDs for local directories, including bundled project creation; neither local paths nor labels are broadcast. Clients refetch their authorized resource list. Task claims expose local bindings only to their executing runtime's owner, regardless of the task initiator or shared agent visibility. Ownerless runtimes and depot-only server lookups receive no local bindings. An explicit new binding replaces ownerless rows for that project/daemon in the same transaction as creation, using only the newly supplied path and metadata. It never inherits an old path, label or owner. Owned rows still conflict; concurrent rebinds serialize on the project, and a failed creation rolls back legacy-row removal.

The project resource panel shows a red binding reminder when P4 file work has no in-place directory on the current user's machine, and a mode-repair reminder for an explicit P4 project using a Git worktree. A workspace P4 fallback does not mark a Git-only project or an ordinary Git worktree as unusable. Missing/incompatible P4 bindings and unusable P4 clients fail preparation with the existing `local_directory_error` reason; Chat and agent activity show localized directory-binding, read/write-access and P4 workspace/sign-in recovery guidance, with the raw error retained in run details. These hints do not change readonly Chat or projectless task exemptions, create bindings, or change directory modes automatically.

## Shared types

- `p4depot.Ref`, `handler.P4DepotData`, and `daemon.P4DepotData` gain `AllowExclusive bool` (`allow_exclusive`) and `NoAutoReview bool` (`no_auto_review`). The three structs must stay field-for-field identical because the code converts between them directly.
- `p4depot/description.go`: `TicketFromText`, `ValidTicket`, `ChangeDescription(ticket, title, issueKey)`, `DescriptionHasTaskTrailer`, `TaskTrailerPrefix`.
- `handler.AgentTaskResponse.P4Ticket` / `daemon.Task.P4Ticket` (`p4_ticket`): sent only when `p4_depots` is non-empty.
- `execenv.P4DepotForEnv.Charset`, `execenv.P4WorkspaceForEnv`, `execenv.TaskContextForEnv.P4Workspace`.
- `packages/core/types/p4.ts` (`P4Changelist` and related types), plus `allow_exclusive` and `no_auto_review` on the depot ref types.

## Daemon local HTTP

The agent reaches these only through the `multica` CLI; requests carry the task token as today.

`POST /p4/run`: existing request shape. When the task is bound to a workspace (`p4Binding(taskID) != nil`):
- Optional fields: `port`, `depot`, `stream`, `workdir`. The daemon fills them from the binding.
- `changelist` must be empty or equal to the task CL.
- File arguments must name individual files, without wildcards, revision specifiers, or directories. Local and depot paths are resolved with `p4 where` before checking the local root and sidecar paths, including files not added to the depot yet. Exclusion records are ignored; multiple distinct effective mappings are rejected. Each effective depot file must also be covered by a configured task resource. Mapping into the client does not grant access to another depot.
- Root and sidecar checks compare resolved filesystem locations, following symlinks and Windows junctions before applying `..`. Missing file tails retain their resolved parent; an unresolvable location is rejected. P4 keeps its original client-root and working-directory spellings.
- A new description must retain the bound ticket. A foreign or malformed `refs #...` marker is rejected before creating or updating a CL.
- Allowed actions: `edit`, `add`, `delete`, `move`, `reopen`, `revert`, `reconcile`, `resolve`, `shelve`, `change`, `opened`, `describe`, `filelog`, plus the forwarded reads `changes`, `fstat`, `files`, `dirs`, `grep`, `print`, `diff`, `diff2`, `annotate`, `where`, `have`, `sizes`, `integrated`, `interchanges`, and `resolved`.
- Revert never targets opens present when this run bound a reused CL, even if the run later edited them. `--unchanged` without file arguments enumerates only current opens touched by this run; an empty selection performs no P4 command. Submit and close-out use the same explicit cleanup selection, separately from files selected for shelving. Snapshot comparison includes removed opens; their disappearance fails shelving/close-out visibly and leaves the CL available for reconciliation.
- Additional actions: `submit`, `sync`, and `unshelve` require their corresponding workspace depot permission. Submit and unshelve use only this task's changelist. Sync with no files updates the full bound client view, including stream imports. Named sync arguments must be individual files in an authorized depot; directories and wildcards are rejected. Sync rejects `-c`/`--changelist` and `--fresh` instead of silently ignoring them.
- Imported files use the most specific configured depot's exclusive/submit/unshelve grants. Submit checks both opened and shelved files before changing the shelf. A primary depot's grant does not authorize an imported depot.
- Stream metadata in `describe -S` is valid shelf content, including a shelf containing only a stream spec. Close-out retains it instead of treating the CL as empty. File shelving and unshelving use `-Af` to preserve the stream spec. A numbered CL containing an open or shelved stream spec is rejected explicitly before submit changes anything: a person must move or submit the spec first; agents do not submit stream-spec changes.
- `filelog` is read-only. It requires individual files in authorized depots and returns at most `max` revisions (default 20, hard cap 200) with changelist descriptions (`-L`, or full text when `long` is true). It does not accept a changelist and does not create or touch a task CL. Allowed for read-only bindings, after submit, and while registration is pending. Response adds `"revisions": [{"depot_file", "rev", "change", "action", "type"?, "user"?, "client"?, "time"?, "description"?}]`.
- `describe` with an empty changelist still describes the task CL. A different number is a history read: the description and file list are returned only when every listed file is in an authorized depot. An unparsed body, a changelist with no files, or a file outside those depots is rejected without returning the description or the other depot's paths. Response may include `"description"` and `"files"`.
- CLI spellings that map onto these fields, and are not a p4 passthrough: `add <files>` (same as `add-files`; `add` with no files still registers a depot), `-c`/`--changelist`, `revert -a`, `shelve -f`, `describe <number>`, `move <source> <dest>`, and `sync <file>`. Native aliases `changelist`, `changelists`, `rename`, and `update` map to the same guarded commands. `reconcile -n` previews without creating or touching a task CL. Other p4 options on those write commands (`opened -a`, `-x`, revision specifiers) stay rejected. A passthrough would also accept those.
- `resolve` accepts the same `args` argv channel as forwarded reads, but it is a write unless `-n` or `-N` is present. It always injects the task CL, rejects caller `-c` and stream-spec resolves, and requires an automatic mode (`-as`, `-am`, `-af`, `-at`, or `-ay`) unless previewing. Only the documented non-interactive resolve, automatic, diff, and preview flags are allowlisted; unknown flags are rejected rather than forwarded. With no file arguments, the injected CL still prevents it from touching another changelist. Previews do not create or touch a task CL.
- Forwarded reads (`changes`, `fstat`, `files`, `dirs`, `grep`, `print`, `diff`, `diff2`, `annotate`, `where`, `have`, `sizes`, `integrated`, `interchanges`, `resolved`) run in the bound client and return p4's own stdout. `args` is the p4 argv after the verb, in order. `-ztag` is lifted in front of the verb. Each command declares its value-taking and rejected flags; attached values such as `-du3` and `--tab=8` are preserved. Attached forms of rejected values (`-ooutfile`, `-bbranch`, `-Pparent`, `-Sstream`) are recognized and rejected too, not misclassified as boolean flags. Connection flags (`-p`, `-x`, `-Mj`) are rejected, while command-local flags such as `annotate -u`, `sizes -H`, and `resolved -o` remain available. `print -o`, stream-spec diffs (`diff -As` and `diff2 -As`), and branch/stream views (`diff2 -b/-P/-S`, and their counterparts on integration reads) stay rejected. The Multica `-c`/`--changelist` field is rejected; a p4 `-c` in `args` is a command flag and is forwarded where the native read supports it. These reads do not create or touch a task CL and are allowed for read-only bindings, after submit, and while registration is pending. The raw `p4` process cannot see this client (`P4CLIENT` is a missing name), which is why `fstat` and client-scoped `changes` go through this route instead of a parsed subset of flags.
- `changes` with no filespec is limited to each authorized depot (`scope/...`). `-m` defaults to 50 and cannot exceed 200.
- `fstat` and `files` require a filespec. A wildcard must be a subdirectory of an authorized depot, not the depot or stream root, and must include `-m` of at most 200. `dirs`, `grep`, `integrated`, and `interchanges` allow bounded subdirectory wildcards. `print`, `diff`, `diff2`, `annotate`, `where`, `have`, `sizes`, and `resolved` require individual files and reject wildcards. Output is refused, without returning the foreign path, when a scanned `depotFile` or listing path is outside the authorized depots. Fstat also scans `movedFile` and the numbered integration fields `resolveFromFile<N>` and `resolveBaseFile<N>`; `integrated`, `interchanges`, and `resolved` scan every depot token, and stderr is scanned as well as stdout. `RunRead` uses bounded stdout/stderr writers and cancels p4 as soon as either stream exceeds 1 MiB rather than buffering the whole stream first. A non-zero p4 exit is still HTTP 200 with `stderr` and `exit_code`; the CLI prints both streams only after those checks pass.
- Success response: the existing `MutateResult` plus `"warnings": [{"kind": string, "files": [string]}]` (omitted when empty). `opened` also returns `"files": [{"depot_file", "client_file"?, "action", "change", "type"?}]` when the task changelist has opens. `change` is the number or `default`. The CLI `-F`/`--format` flag formats those rows with p4 -ztag names; it is not forwarded to `p4`.
- Rejection: HTTP 409 with JSON body `{"code": string, "message": string, "files": [string]?, "holders": [string]?}`. `code` is one of:

| `code` | Meaning |
|---|---|
| `ticket_required` | the task has no 易协作 ticket |
| `opened_by_you` | the file is already in one of the human's changelists, including default |
| `exclusive_locked` | someone else holds the +l lock |
| `exclusive_not_allowed` | +l file, and the depot does not set `allow_exclusive` |
| `outside_task_change` | revert/reconcile/reopen target outside the task CL |
| `sidecar_path` | the path is a Multica-written file |
| `action_not_allowed` | submit/sync/unshelve without permission, or writes after submission |
| `not_in_workspace` | the path is outside the client root |

- The CLI prints `message`, then `files` / `holders` one per line, and exits non-zero.

`GET /p4/status` (new): with no binding, returns 404 `{"code":"no_workspace","message":...}`. With a binding, returns:

```json
{"port":"","user":"","client":"","root":"","stream":"","edge":false,"ticket":"","change":"","files":[{"depot_file":"","action":"","type":""}],"warnings":[{"kind":"","files":[]}]}
```

`POST /p4/sync`: rejected with 409 `action_not_allowed` unless a binding exists and its depot enables `allow_sync`. Without a binding, Multica never creates a task client or syncs a depot copy, because that downloads the depot.

`POST /p4/swarm`: a request to shelve before creating a review requires the bound task workspace and its changelist. Requests without a binding must use `--no-shelve`. Submitted task changelists can create post-commit reviews without shelving.

Before submit, read the pending changelist spec from the bound workspace and verify its client, owner, ticket (`refs #<bound-ticket>`) and Multica issue trailer. Revert unchanged opens only through this run's explicit cleanup selection, refuse an empty changelist, and verify that every shelved file is either still open in this CL or confirmed identical in content and type to its have revision. If a shelf-only file has changes, reject submit without deleting its shelf; a person must restore or explicitly remove that file first. An unreadable shelf also prevents submit. Re-read the spec before deleting the whole shelf with `shelve -d -c <CL>` (no file arguments). Any preflight failure prevents submit. Whole-shelf deletion preserves the associated Swarm review's files, including when shelf-file deletion processing is enabled; see [Swarm shelf deletion](https://workshop.perforce.com/docs/Content/Swarm/code_reviews.activities.html#Deleting). Keep the existing review link and recorded review state; submission does not imply review approval. Do not approve, archive or recreate the review. Swarm remains authoritative for its review state. If submit fails, normal close-out re-shelves the remaining work. Operations and close-out serialize on the binding.

An `in_place` local directory keeps its binding when its depot comes from the workspace catalog. Quick-create and projectless autopilot tasks do not require a P4 directory merely because they inherited that catalog. Unbound `/p4/run` requests return 409, including when an old checkout directory exists.

A workspace depot catalog alone does not make a projectless issue a P4 task. An explicit project depot still requires an in-place directory. For an inherited catalog, skip binding only when the server explicitly reports an unknown client or a verified client root does not contain the directory. Authentication, connection, ownership, and unverified client-root failures remain errors.

When an inherited catalog has no workspace binding, omit it from the task's registered resources and runtime brief. Explicit project depots retain their setup guidance. Bound workspace guidance is the same for fresh and reused environments.

Binding recognizes quoted classic View paths, overlay/ditto mappings, and broad client views covering a narrower configured resource. Wildcards retain their full matching semantics, including literal prefixes and suffixes. Stream identity matches are exact. Stream clients also inspect their generated View, so virtual/derived streams can bind through an authorized mapped namespace, but a catalog ancestor of a mapped namespace is not accepted as that namespace's resource. The client's own stream takes priority over imported resources when choosing the primary binding; longer imported paths cannot replace its task-level switches. Only matched resources enter the binding's file-authorization set. Neither a common stream depot nor a parent relationship alone authorizes another namespace.

Endpoint comparison preserves TLS and explicit address-family choices. Different endpoint spellings need a positive matching cluster identity or a server ID paired with its canonical address; a reused server name alone does not prove that two installations are related. A local edge can also bind through its configured upstream when that upstream's server registry confirms the edge's ID, service and address. Unverifiable relationships are rejected. Alias verification never adds a depot or grants an action. Client locks use verified server identity and the physical client root, so aliases or two clients sharing a folder cannot bypass serialization.

Bound P4 tasks write Multica runtime instructions and managed skills into a unique task-private directory under the execution environment. The runtime brief and explicit skill paths are included in the agent's prompt for fresh, resumed and cold-retry runs. Repository `CLAUDE.md`, `AGENTS.md`, and provider skill directories remain repository-owned and can be edited through normal P4 rules. Only `.multica` and `.agent_context` are protected sidecars; tracked content there rejects binding. This avoids overwriting readonly depot files or hiding shared repository rules behind task-generated files.

Chat tasks and tasks without an issue receive a read-only workspace binding: they do not acquire the P4 client lock, reuse a task CL, finalize a CL, or execute P4 writes. An unassigned task CL's `opened` response is empty. Issue tasks wait for a shared P4 client before the environment preparation deadline begins; contention reports the normal waiting state, renews the prepare lease, and observes cancellation. Prepare-lease renewal also covers the earlier workspace detection and endpoint-verification calls, and stops when that phase returns.

When a task report uses a registered edge's verified commit identity, polling the original configured edge revalidates the same upstream registry relationship before accepting its observations. Only current configured upstreams may be contacted; a replica address alone, a different identity, or a transport downgrade cannot authorize the relationship. The canonical identity remains unchanged in status updates, and observations of missing or pending changes through another endpoint remain insufficient.

For a reused CL, a missing ticket may be added on its first write only when it was already absent at binding and the description is unchanged. Preserve multiline human content and review markers. Conflicting tickets or issue trailers, malformed markers, and markers removed after binding are errors; never rewrite a description to authorize submit.

Creating or reusing a task CL immediately registers a pending receipt through the durable report outbox. Registration must be acknowledged before file writes proceed; a failed registration also gates subsequent shelve, revert, submit and unshelve requests. Startup replays persisted registrations without needing an in-memory binding. Pending registration is acknowledged before final delivery so an older queued pending payload cannot overwrite a later receipt.

After submit, read the receipt with `p4 describe -s -O <original-number>` in the bound workspace. The CLI/status/report use the submitted number and file list; the report also keeps `original_number`. A failed read must not replay submit or declare a terminal receipt verified. Close-out retries the read; if it still fails, report a pending reference for reconciliation and fail delivery.

Warning kinds: `not_head`, `opened_elsewhere`, `exclusive_held`, `default_changelist_files`, `unexpected_opens`.

## Server API

All JSON uses snake_case. A changelist row serializes exactly as `packages/core/types/p4.ts` `P4Changelist`. Unknown `status` values parse as `pending` on the client.

Daemon routes (DaemonAuth group):

| Route | Body | Response |
|---|---|---|
| `POST /api/daemon/tasks/{taskId}/p4/changelists` | see below | 200 with one `P4Changelist`; creates or updates the row and links it to the task's issue (`link_source="task"`) |
| `GET /api/daemon/p4/changelists/open?workspace_id=<uuid>` | — | `{"changelists":[{"id","port","server_key","client","daemon_id","poll_generation","p4_user","charset","number","original_number","status","swarm_review_number","swarm_url","p4_depots":[P4DepotData...]}],"p4_depots":[P4DepotData...],"lease_required":true}`; resources are resolved separately for each row |
| `POST /api/daemon/p4/changelists/{changelistId}/claim` | `{"workspace_id":"<uuid>","poll_generation":int}` | `{"poll_generation":int}`; CAS claim/renewal, 409 if ownership or generation changed |
| `POST /api/daemon/p4/changelists/status` | see below | `{"updated": int}` |

The task-report body:

```
{"port","server_key","charset","number","original_number","status","client","p4_user","description","ticket","files_count","exclusive_files":[],"warnings":[{"kind","files":[]}],"fence_polls":true,"submitted_at":"<RFC3339, submitted receipts only>","swarm_review":{"number","url","state","author","description"}?}
```

Polling first selects a currently authorized resource, then uses the changelist's saved `p4_user` and `charset` only for fields that resource leaves blank. A saved row cannot authorize a port or Swarm origin. Submitted status uses the `time` reported by `p4 describe`; missing or invalid timestamps leave the row for a later poll rather than substituting the polling time. Direct task submission receipts carry that same timestamp through durable delivery and retries. Servers retain the receipt-time default only for older daemons that omit the optional `submitted_at` field.

Lease-aware daemons claim before observing and renew with CAS before reporting. Leases last two minutes. Another daemon may take over only after expiry and when no runtime for the original owner is online with a heartbeat in the last 90 seconds. Claims and status writes require the expected generation. A new task report with `fence_polls` advances the generation and clears the lease so an earlier observation cannot overwrite new work. Omitted feature fields preserve generation-zero behavior for installed older daemons. Once the row has a newer generation, a task report without `fence_polls: true` receives HTTP 409 and cannot alter its contents, owner, generation or lease, including when it comes from the same daemon.

An alternate configured endpoint must positively verify the stored server identity before observation. A submitted receipt can be confirmed through that endpoint; missing or pending results on a different edge cannot prove the state of an edge-local changelist. Polling never connects to a historical row's unconfigured port.

The status body:

```
{"workspace_id":"<uuid>","updates":[{"id","status","number","original_number","server_key","submitted_at","swarm_review_state","poll_generation"}]}
```

Rules for the task-report route:
- Ownership is checked with `requireDaemonTaskAccessWithWorkspace`.
- The row is keyed by `(workspace_id, server_key, original_number)`.
- Task reports, manual links, identity merges, status updates, and unlinks serialize identity writes within the workspace. Manual rows are adopted or merged once the cluster and original number are known; all issue links and manual attribution survive. Delayed receipts cannot revert a submitted/deleted status or a submitted number.
- When `swarm_review` is present, upsert `swarm_review` and attach it to the changelist (`swarm_review_id`). That review must not also be returned in the issue's pull-request list.

Polling only uses the returned configured depots. Unconfigured ports and `rsh:` are excluded on both server and daemon; review URLs must match a configured Swarm origin before credentials are sent. Project resources are resolved even after a task ends or the daemon restarts.

At close-out, read the existing shelf before making changes. Shelve explicit opened files with `shelve -f`, with `-p` on an edge, retaining earlier shelf-only files and including them in the receipt. Delete a CL only when both its opens and confirmed shelf are empty; never delete a shelf as empty-CL cleanup. Missing, malformed, or failed shelf readback prevents cleanup.

Binding a reused CL snapshots only that CL's opened files, including content hashes. A read-only continuation whose open state and contents are unchanged skips finalization and delivery. Direct edits to already opened files still trigger finalization, even without a P4 command or a modification-time change. Metadata-only or no-op operations may report the existing shelf but do not reshelve the human's pre-existing opens or create a review. New CLs receive normal close-out. A reused CL reverts and shelves only opens added or changed during this run, retaining the human's pre-existing opens and shelf contents. Finalization errors fail the task and retain an unverified receipt as pending where applicable. CL receipts use a separate delivery key in the durable terminal-report outbox so they survive HTTP failures/restarts without replacing the task result. A report is acknowledged only after the server accepts it, including when a server lacks the route.

User routes (authenticated issue group):

| Route | Body | Response |
|---|---|---|
| `GET /api/issues/{id}/p4-changelists` | — | `{"changelists":[P4Changelist...]}`, newest first |
| `POST /api/issues/{id}/p4-changelists` | `{"number": int, "port"?: string}` | 201 with one `P4Changelist`, `link_source="manual"`, status `pending` until the daemon's next poll |
| `DELETE /api/issues/{id}/p4-changelists/{changelistId}` | — | 204 |

For the manual link:
- `port` defaults to the only `perforce_depot` on the issue's project; with zero or several depots, a missing `port` returns 422.
- A manual row has no `daemon_id`. The open-changelists route also returns manual rows whose port matches a configured depot, and a lease-aware daemon claims them before observing.
- Manual links reuse existing rows by either the current or original number. Port aliases converge on the canonical cluster identity during polling; merging a duplicate does not transfer the canonical row to another daemon.

Realtime event `p4_changelist:updated`, payload `{"issue_id": "<uuid>", "changelist_id": "<uuid>"}`. Published after each create, update, link, or unlink.

Issue auto-advance:
- Runs when every changelist linked to an issue is `submitted` and the issue has no GitHub or VCS pull request linked.
- The issue moves to `resolvePRMergeTarget`, and the per-issue `GetIssuePRAutoCompleteDisabled` opt-out is respected.

## Migrations

- Fork migrations use the reserved range **9001–9099**, so they cannot collide with upstream numbering.
- One concurrent index per file. Every concurrent index gets an entry in `cmd/migrate/main.go` `concurrentIndexCleanups`.
- No foreign keys. Deletes are swept in `issue.sql` / `workspace_delete.sql`, and the manifest test lists the new tables.

## Verification

Use the checkout's managed environment and database from `CONTRIBUTING.md`: `make up`, `make status`, and `make test` from the repository root. Do not copy another checkout's `.env` or manually create a database through an assumed PostgreSQL instance. Apply migration `9092_p4_poll_lease` before running the newer backend.

For narrow native Windows checks, load this checkout's managed environment and use `scripts/go-test-with-agent-cli-guard.ps1` with `-CommandArgs @('go','-C','server','test',<packages>,'-count=1')`. Cover `internal/daemon`, `internal/daemon/p4cache`, `internal/daemon/execenv`, `internal/p4depot`, `internal/handler`, and `cmd/multica`. If database fixtures skip because the DB is unreachable, report the missing coverage.

The opt-in `p4integration` suite uses explicitly supplied `MULTICA_TEST_P4_BIN` and `MULTICA_TEST_P4D_BIN` to start a disposable loopback P4D. It verifies existing shelf/human-open preservation, submit preflight, imported files, real submitted time, server aliases, and tracked repository rule preservation. It does not prove a real team's multi-edge topology, Swarm deployment, or agent-client acceptance.

For frontend changes, run the checks in `AGENTS.md`. Never use the production P4 server or user-installed agent CLIs as default test fixtures.
