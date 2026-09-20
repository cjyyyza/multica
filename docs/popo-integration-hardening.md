# POPO integration hardening

This change addresses the eight findings from the cross-repository review of
Multica `40ca8f609` and DJ01Bot `df34489`. It preserves Multica as the task owner
and DJ01Bot as the Windows POPO transport.

## Implementation order

1. Match the actual Go command envelope and media fields in the Python client;
   exercise a shared wire fixture and delivery receipt contract.
2. Support starting with no robots and hot-loading newly registered robots.
   Keep active registration commands alive, cancel them explicitly, and separate
   registration completion from remote message delivery receipts.
3. Persist inbound attachment work before upload; retry transient failures and
   report permanent missing attachments instead of accepting incomplete input.
4. Recover outbound notifications from persisted business records with stable
   source identities, and avoid duplicate delivery across process restarts.
5. Fence command leasing/sending and media access after installation revocation;
   commit delivery receipts and outbound quote mappings atomically.
6. Reject conflicting explicit issue targets and quoted targets before cancellation.

## Verification

- Reproduce each regression with fake POPO/Runtime fixtures before fixing it.
- Run Python bridge, registration, robot-lock and channel regression tests.
- Run Go integration, channel-engine, handler and recovery tests, including a
  managed isolated database when the repository environment can be started.
- Run the cross-repository protocol check against Go-produced JSON.
- Record actual test outcomes separately from live POPO or Runtime acceptance;
  no real-agent smoke test or production rollout is part of this repair.

## Implemented behavior

| Finding | Repair and evidence |
| --- | --- |
| Outbound wire format did not match Go | Python reads the nested `payload`, `chat_type`, `download_path`, and quote message id. `TestPopoGoHandlerPythonDeliveryContract` passes actual handler JSON through the real Python delivery path with a fake POPO sender, then records its receipt in Go. |
| No first-robot bootstrap or transport reload | Empty inventory starts normally. First scan creates the config, keeps credentials disabled on disk, and starts the transport before reporting registration success. Existing gateway and Sparse robots remain excluded. |
| Transient attachment failure became accepted input | Input bytes and descriptors persist locally. Upload failure leaves the event pending; retry reopens idempotent staging sessions, including after expiry. Permanent failures retain their index with zero size. |
| Commit-before-publication lost notifications | A five-second recovery worker reads durable chat replies, comments, failed/cancelled runs, issue creation and current issue status. Deterministic source identities fence event/recovery races; migration 537 backfills older command identities. Recovery never starts another agent run. |
| Revocation left deliverable commands | Revocation cancels pending/leased commands transactionally. Leasing, media access and send authorization check active ownership. Windows checks authorization again after attachment preparation. |
| Explicit stop target conflicted with quote | Conflicting issue/quote targets and multiple ambiguous runs are rejected before cancellation. |
| Registration outlived its lease | Registration leases cover the ten-minute QR wait. Active redelivery is ignored, cancellation stops its scanner, and delivered control receipts do not require a remote message id. |
| Receipt could commit without quote mapping | Receipt and outbound correlation commit in one transaction. A lock-induced mapping failure rolls back the receipt; matching replay repairs an older missing mapping. |

Preparation errors before a POPO call can retry safely; a started send without a
confirmed remote id remains `unknown` and is never automatically resent. Recovery
skips deleted comments, carries attachment-only comments and deduplicates concurrent
media command insertion.

## Local validation — 2026-09-19

- DJ01Bot full suite: **2,101 passed** in 825.67 seconds. The subsequently added
  expired-staging regression and final bridge edits were checked separately:
  bridge/registration/robot-lock suite **67 passed**, final bridge suite **49 passed**.
- Expanded Python POPO channel/routing/rich-text suite: **379 passed**.
- Multica focused tests: `go test ./internal/handler ./internal/integrations/popo
  ./internal/integrations/channel/engine -run 'TestPopo|TestFollowStop' -count=1`
  passed against the checkout's managed isolated PostgreSQL database.
- The cross-repository contract test ran with explicitly selected Python and
  DJ01Bot worktree paths; it was not skipped. POPO and agent execution were faked.
- Expanded Go run: channel engine, POPO, Lark, DingTalk, Slack, Telegram, Composio,
  GitHub snapshot, VCS and server packages passed. The complete handler and WeCom
  suites were **not green** (details below).
- `go vet` for POPO, channel engine and handlers passed. sqlc 1.31.1 regeneration
  and both repositories' `git diff --check` passed. Ruff passed with the two
  pre-existing N818 exception-name warnings excluded.
- Race tests were not run (`CGO_ENABLED=0`, no local GCC). Frontend/mobile/E2E
  tests were not rerun because this repair changes Go/Python behavior only.

The expanded Go failures were checked using a Go build overlay of the original
`40ca8f609` source, without reverting the worktree. Ten failures also reproduced
on that baseline: six WeCom temporary-file tests on Windows, Unix path formatting,
unsafe archive path handling, seat-capacity lock timeout and task-message clock
skew. Two other failures were not reproduced by the isolated baseline rerun:
`TestListIssuesPropertyFilterAndSort` passed when rerun on the changed code, while
`TestClaimTasksByRuntime_ClaimPollHintSchedulesNextDeferredTask` fluctuated around
its five-second database-versus-host clock bound. These results are not a claim
that the complete backend suite passes.

## Delivery and acceptance

Changes are in the existing integration worktrees, uncommitted and undeployed.
Before rollout, stop the old Windows bridge, update Multica and apply migration
537, then update/restart the bridge. The new bridge requires the authorization
endpoint; do not deploy it against the older server.

Local verification does not establish real POPO delivery, successful account QR
binding, or Runtime execution. Accept those with an actual private/group message,
file round trip, task creation, quoted comment, stop command, revocation and
disconnect/restart exercise. Current-state recovery does not recreate every
intermediate status change. POPO automatic ACK still leaves a pre-persistence
crash gap, and revocation cannot recall a request already sent to POPO.
