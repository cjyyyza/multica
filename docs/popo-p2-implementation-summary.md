# P2 implementation summary

POPO issue loop on the shared Channel Engine: create → run → comment thread → continue → result back to the originating chat. No second approval state machine.

## Product behavior

- `/issue title` plus following lines as description is unchanged. Empty `/issue` still returns usage. The replier returns identifier + web link.
- Quote a task/issue message and send text → member comment on that issue, not a second Chat run. `/reply` `/status` `/stop` take the same exclusive follow path.
- `/reply ISSUE-123 body` comments and runs existing comment-trigger rules.
- `/status ISSUE-123` reports real issue status, current run status, and link.
- `/stop ISSUE-123` cancels the current cancellable run and does **not** set the issue to cancelled. Quote + `/stop` cancels that run; ambiguous quotes get guidance.
- POPO-origin issues (`origin_type=popo_chat`) get a frozen `channel_issue_source` route. Visible comments, run failures/cancels, and status changes enqueue bridge `send` commands. Web/Desktop comments on that issue use the same path.
- Member comments that originated from POPO are acked only (inbound write ledger), not echoed back.
- After reassignment, the old bot tells the new owner and does not keep executing.
- External quotes validate installation (robot) + chat + message id. There is no “latest issue” fallback.

## Schema (517–526)

No FKs or cascades. Concurrent indexes are their own migrations.

- `channel_outbound_message.issue_id` / `comment_id` (nullable) so a quoted POPO message can be resolved.
- `channel_issue_source` — one originating chat per issue.
- `channel_inbound_write` — issue/comment/cancel idempotency keyed by `(installation_id, message_id, kind)`, written in the same transaction as the business row.

Workspace teardown deletes the new tables. `make sqlc` was run.

## Engine and actor entry points

Shared parsers: `/reply`, `/status`, `/stop` (same token rules as `/issue`). Router consumes follow-ups **before** chat append, so one inbound message cannot start both a comment and a Chat run.

`engine.IssueFollow` is implemented by `Handler` with an explicit member actor:

- `CreateMemberComment`
- `CancelRun` → existing `TaskService.CancelTaskByUser`
- `ResolveIssue` / `ListActiveRuns` / `LookupQuote`

The channel path does not simulate HTTP or forge user headers. HTTP `CreateComment` is unchanged and still publishes `comment:created`, which POPO follow outbound consumes.

Issue create accepts `ChannelIdempotency` + `ChannelSource` inside the create transaction. Deduped chat message is not treated as “issue created”: a replay with a missing write retries create; a completed write is silent.

## POPO transport

- Bridge quote JSON accepts `message_id`, `msgId`, `uuid`, `messageId`.
- Send payload carries optional issue/comment/task/binding metadata. A `delivered` receipt records `channel_outbound_message` with the remote POPO id so later quotes resolve.
- `popo.Outbound` also subscribes to `comment:created`, `task:failed`, `task:cancelled`, `issue:updated` (status changes only).

Windows (`G:\DJ01Bot\dj01bot-multica-bridge`): `normalize_bridge_quote` maps POPO `quote_info` onto `{message_id}`. `G:\DJ01Bot\dj01bot` was not modified.

## Tests

Go DB tests (`server/internal/handler/popo_p2_test.go` and engine parsers):

- `/issue` creates one issue (idempotent replay)
- quote + text creates a comment, not a chat run
- `/reply` `/status` `/stop` (stop does not cancel the issue)
- POPO-origin comment is not re-delivered
- Web comment on a POPO-origin issue enqueues a send command
- reassignment stops the old bot

Also ran: full `channel/engine` package, `integrations/popo`, focused existing POPO bridge tests, workspace deletion manifest. No real POPO. Feishu engine tests in the shared engine package passed.

## Not in P2

Group @, QR, media, `/new` routing, Feishu ack cards for `/reply`. Those stay P3.
