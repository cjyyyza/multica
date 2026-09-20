# P1a implementation summary

Windows-bridge protocol on the Multica server (plan §8). Channel Engine (resolvers, binding tokens, `/popo/bind`, `/issue` `/new` `/clear`, Chat runs) is unchanged. P2/P3 (QR, media, group @, issue subscriptions, `/reply` `/status` `/stop`, settings UI) are not in this change.

## Files changed

### Migrations (504–516)

- `server/migrations/504_popo_bridge_pairing.{up,down}.sql`
- `server/migrations/505_popo_bridge_pairing_code_hash_index.{up,down}.sql`
- `server/migrations/506_popo_bridge_pairing_workspace_index.{up,down}.sql`
- `server/migrations/507_popo_bridge.{up,down}.sql`
- `server/migrations/508_popo_bridge_token_hash_index.{up,down}.sql`
- `server/migrations/509_popo_bridge_workspace_index.{up,down}.sql`
- `server/migrations/510_popo_bridge_command.{up,down}.sql`
- `server/migrations/511_popo_bridge_command_delivery_id_index.{up,down}.sql`
- `server/migrations/512_popo_bridge_command_lease_index.{up,down}.sql`
- `server/migrations/513_popo_bridge_command_workspace_index.{up,down}.sql`
- `server/migrations/514_popo_inbound_event.{up,down}.sql`
- `server/migrations/515_popo_inbound_event_unique_index.{up,down}.sql`
- `server/migrations/516_popo_inbound_event_workspace_index.{up,down}.sql`

No FKs, no `ON DELETE CASCADE`. Each extra index is its own `CREATE INDEX CONCURRENTLY` file, registered in `server/cmd/migrate/main.go`.

### Queries / generated

- `server/pkg/db/queries/popo.sql` — pairing, bridge, command, inbound (outbound-queue writes removed)
- `server/pkg/db/queries/workspace_delete.sql` — delete new tables with the existing outbound queue
- `server/pkg/db/generated/{popo.sql.go,models.go,workspace_delete.sql.go}`

### Server

- `server/internal/integrations/popo/bridge.go` — mint/redeem pairing, register, heartbeat, auth
- `server/internal/integrations/popo/command.go` — enqueue `send`, long-poll lease, receipts
- `server/internal/integrations/popo/inbound_bridge.go` — §8.5 JSON → `channel.InboundMessage`; persist before engine
- `server/internal/integrations/popo/{channel,config,install,outbound,queue,replier}.go` — install `{bridge_id,robot_id}`, enqueue commands
- `server/internal/handler/popo.go` — install body change, `canManageAgent`
- `server/internal/handler/popo_bridge.go` — workspace pairing/bridges + `/api/popo/bridge/*`
- `server/cmd/server/router.go` — `MULTICA_POPO_ENABLED=true`; drop member inbound/outbound/ack; public bridge routes
- `server/cmd/multica/cmd_popo.go` — `gateway` stub errors toward `python -m nanobot.multica_bridge`

### Tests

- `server/internal/handler/popo_test.go`, `popo_bridge_test.go`
- `server/internal/integrations/popo/{inbound_test,occupancy_test}.go`
- `server/cmd/multica/cmd_popo_test.go`
- `server/internal/handler/workspace_delete_manifest_test.go`

### Docs / env

- `apps/docs/content/docs/popo-bot-integration.mdx` (+ zh/ja/ko)
- `apps/docs/content/docs/channels.mdx` (+ zh)
- `apps/docs/content/docs/environment-variables.mdx` (+ zh/ja/ko)
- `.env.example`, `docker-compose.selfhost.yml`

## Decisions

- Enablement is exactly `MULTICA_POPO_ENABLED=true`. `MULTICA_POPO_SECRET_KEY` is no longer required (and is not used).
- Bridge bearer tokens and pairing codes are 32-byte `base64url`; only SHA-256 hex is stored.
- Workspace is taken from the bridge token. Installation is `(channel_type=popo, app_id=robot_id)` and must match that workspace. Body workspace/agent/user is ignored.
- Inbound persist happens before `ChannelRouter.Handle`. Duplicate `(installation_id, event_id)` returns `200 {accepted:true, duplicate:true}` and does not enter the engine.
- P1 inbound: `chat.type=p2p`, `addressed_to_bot=true`, non-empty text, empty media. Others `200 {accepted:false}` with no persist.
- `popoChannel.Send` and `OutboundReplier.post` enqueue `popo_bridge_command` type=`send`. `unknown` receipts stay `unknown` and are not re-leased.
- Install requires an active workspace bridge and a robot that is idle on a heartbeat ≤45s old (`connected` and empty `occupied_by`), or `occupied_by=multica` already owned by this agent. One live bot per agent is unchanged.

## Deviations

- Unconfigured **management mutations** (install, pairing, redeem) still use `writeFeatureDisabled` **403**. Unconfigured **bridge** routes are **503** `popo_not_configured`. List endpoints return `configured: false`.
- Install/revoke sit on member routes and use `canManageAgent` (agent owner or workspace owner/admin), matching plan §8.1 rather than the old admin-only router split.
- `popo_outbound_queue` is not dropped; it is no longer written or polled.
- `multica popo gateway` remains as a cobra stub that returns an error (no dual transport).
- `GatewayClient` (loopback dj01bot HTTP) is kept for existing unit tests; the CLI no longer calls it.
- Frontend/core API client is unchanged (P1c). Install response still includes `webhook_url` (empty for new rows) for desktop parse compatibility.
- `wait_ms=0` is allowed so tests and clients can poll without blocking; default remains 25000, max 30000.

## What was not implemented

DJ01Bot Python, QR scan, media, group chat, issue subscriptions, `/reply` `/status` `/stop`, settings UI rewrite.
