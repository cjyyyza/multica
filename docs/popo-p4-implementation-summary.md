# P4 operations implementation summary

Multica diagnostics for the POPO Windows bridge. Windows start/stop/autostart is DJ01Bot work and was not implemented here. No single `healthy` flag stands in for the path.

## Status API

`GET /api/workspaces/{id}/popo/status` (workspace member):

```json
{
  "configured": true,
  "protocol_version": 1,
  "bridges": [{
    "id": "...",
    "hostname": "...",
    "online": true,
    "last_heartbeat_at": "...",
    "popo_connected": true,
    "robots": [],
    "inbound_backlog": 0,
    "outbound_backlog": 0,
    "unknown_deliveries": 0
  }],
  "runtime_online": true
}
```

Derived:

- `online` — last heartbeat within 45s and bridge status `active`
- `popo_connected` — any robot on that heartbeat has `connected=true`
- `outbound_backlog` — `send` commands in `pending` or `leased`
- `unknown_deliveries` — commands with `status=unknown` (must not be resent)
- `inbound_backlog` — pending, unexpired `popo_media_staging` rows for the bridge. Engine `Handle` runs on the inbound HTTP request, and `popo_inbound_event` has no processed column, so this is the measurable wait. It is not a fake zero.
- `runtime_online` — at least one active POPO install is bound to an unarchived agent whose `agent_runtime.status` is `online` (same presence signal the sweeper maintains)

`MULTICA_POPO_ENABLED` off returns `configured: false`, `protocol_version: 1`, empty bridges, `runtime_online: false`.

zod + `parseWithFallback` on the web/desktop client. Settings → Integrations → POPO shows the counters separately (host online, POPO connected, runtime, inbound backlog, outbound backlog, unknown). All locales.

## Revoke

`DELETE /api/workspaces/{id}/popo/bridges/{bridgeId}` already existed. It now cancels `pending` and `leased` commands for that bridge (`status=cancelled`) in the same transaction as the token revoke, so they cannot be leased.

## Logs

Enqueue, inbound accept, and command receipt log `event_id`, `installation_id`, `chat_id`, `issue_id`, `task_id`, `delivery_id`, `remote_message_id`. Token, pairing_code, message body, and secrets are not logged.

## Tests

- Status counts pending/leased send commands, unknown receipts, pending media, and runtime presence
- Revoke cancels pending and leased commands; subsequent command poll is 401
- Schema fallback for malformed status
- UI renders the six counters without a combined health badge

## What was not implemented

Windows Task Scheduler autostart, DJ01Bot process management, edits to `G:\DJ01Bot\dj01bot`.

## Verification

Ran from the worktree after `sqlc generate`:

```
go test ./internal/handler/ -count=1 -run 'TestGetPopoStatusNotConfiguredReturnsEmpty|TestPopoStatusCountsPendingUnknownAndRuntime|TestPopoRevokeCancelsPendingCommands|TestPopoRevokedBridgeToken401sInbound'
go test ./internal/integrations/popo/ -count=1
pnpm --filter @multica/core exec node ./node_modules/vitest/vitest.mjs run api/schemas.test.ts api/schema.test.ts
pnpm --filter @multica/views exec node ./node_modules/vitest/vitest.mjs run settings/components/popo-tab.test.tsx locales/parity.test.ts
```

Handler tests passed (DB-backed). POPO integration package passed. 285 core tests and 227 views tests passed (2 files each). `pnpm typecheck`, `pnpm lint`, `make test`, and Playwright were not run. Windows Task Scheduler autostart was not implemented.
