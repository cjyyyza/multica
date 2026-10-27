# POPO integration hardening

## Current TypeScript bridge contract

The supported bridge is DJ01Bot's `feat/ts-popo-engine` implementation, entered
with `node ts/dist/multica-bridge.js` from its repository root. Build that checkout
before running the cross-repository tests:

```powershell
npm --prefix <DJ01Bot-checkout>/ts run build
$env:MULTICA_POPO_TEST_NODE = (Get-Command node).Source
$env:MULTICA_POPO_TEST_DJ01BOT_ROOT = '<DJ01Bot-checkout>'
# From the Multica root, with its managed DATABASE_URL and migrations loaded:
./scripts/go-test-with-agent-cli-guard.ps1 -CommandArgs @('go', '-C', 'server', 'test', './internal/handler', '-run', 'TestPopoGoHandlerTypeScriptDeliveryContract|TestPopoTypeScriptQuoteCreatesOnlyCurrentComment', '-count=1', '-v')
```

These tests pass actual Go command envelopes through the built TypeScript bridge
and return its receipts to Go. They also feed TypeScript-normalized POPO quotes
into the real Go handler and verify the stored comment and replay deduplication.
POPO delivery is faked; no agent or real POPO account is contacted. The tests skip
unless both environment variables are explicitly configured.

Inbound and media staging require the installation's `bridge_id` to match the
authenticated bridge. `text` contains readable context; `command_text` contains
the user's instruction. `has_selected_context` means the bridge already expanded
the selected quote/forward in `text`, so Go preserves it and uses `quote` only
for routing. Multica task cards own execution state; the dedicated TS channel
does not attach an ING sticker at transport ingress.

## Earlier Python bridge hardening

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

## Transport v2 recovery contract (2026-10-25)

Requirement notifications use the native v2 attempt/lease/receipt model described
in the "Transport v2" section of
[popo-dj01bot-integration-plan.md](popo-dj01bot-integration-plan.md) (§11).
These constraints are part of the hardening contract and must not regress:

- v2 does not reuse v1 reclaim: an expired lease never means "not sent". A
  `pending` (never dispatched) lease may be re-armed with the same attempt and
  frozen payload; a `leased` lease that expired without a receipt is projected
  as `unknown` and must not create a new attempt automatically.
- Only a finalized, retryable, not-sent receipt with complete required-component
  evidence may start the next attempt, and only within the five-attempt budget.
  `unknown`, partial delivery, a remote message id or delivered evidence blocks
  resending until new definitive evidence arrives.
- A delivery whose own `expires_at` elapsed is projected `expired`, and its
  outbox stops waiting as `failed`; no further lease is issued for automatic
  dispatch.
- `accepted` outbox rows are recovered from durable delivery/lease state (not
  only from the `needs_reconcile` hint): delivery expiry, an expired lease
  without an active replacement, and two minutes without progress all nominate
  a row for the restart-safe scan. `requirementSyncMetrics` exposes `accepted`,
  `stale` and `oldest_accepted_seconds` for this path.
- Automatic recovery must converge so completed history cannot starve fresh
  work: durable-scan candidates are only rows that still need a durable action
  (decided-delivery projection mismatch, non-terminal delivery at expiry/stale
  lease, unknown delivery at expiry). Already projected terminal rows are
  excluded, and `Reconciled` counts only passes that persisted a write. The
  subsequent `needs_reconcile` sweep also excludes a v2 `unknown` row when its
  delivery is already `unknown` at the same attempt; the operator hint remains
  stored, v1 and v2 conflict recovery remain eligible, and manual reconcile
  still invokes recovery directly. A single dirty row is logged, counted in
  `RecoveryFailed` and skipped; it must not abort the scan or skip the claim
  phase of the same outbox pass.
- The v2 poll envelope carries `attempt` (the lease attempt). A bridge must echo
  it in the receipt instead of inferring it locally; this is what makes
  attempt>1 receipts match the fenced lease.
- Late and duplicate receipts stay fenced by `(delivery_id, attempt)` plus
  lease/payload-hash identity. Recovery never rewrites receipt rows, so a late
  `delivered` receipt can still repair an `unknown` delivery.
- Revoking a bridge or installation fences every unfinished v2 delivery in the
  same transaction (delivery `cancelled`, outbox `failed` with a visible reason).
  The fence repeatedly drains the bounded first page (200 rows by default) until
  no candidates remain instead of stopping after one page. Delivered/unknown
  deliveries are left untouched so a late receipt remains authoritative; rows
  that become terminal or disappear during a racing fence are skipped and do not
  keep the page loop alive.
- Lock order for v2 writers is requirement outbox -> delivery -> receipt/lease.
  The claim path, receipts, fencing, recovery and poll all follow it; duplicate
  workers are separated by `SKIP LOCKED` claims and row locks.
- A workspace that selected v2 but still has a v1-only bridge keeps requirement
  notifications pending/backlog with a visible `last_error`; it never emits a
  fallback v1 command, and the same notification resumes in v2 after upgrade.
  `receipt_attempt_v1` and the v1 "lease timeout returns to pending" rule remain
  v1-only and are not required from a v2 bridge.
- `notification_v2` only gates new notifications. Existing v2 deliveries keep
  recovering after the flag is turned off; rollback must not demote them to v1.

Crash windows are covered by DB-backed Go tests in
`server/internal/handler/requirement_outbox_v2_recovery_test.go` (crash before
dispatch, crash after dispatch without receipt, uncertain remote outcome, server
restart scanning accepted+stale, duplicate/late receipts during recovery, and
the confirmed-not-sent retry boundary). This validation uses a local PostgreSQL
database and fake bridge HTTP, not a real POPO account or a real Windows bridge.

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
