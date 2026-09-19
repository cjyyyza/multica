-- POPO Windows-bridge protocol. The API never opens POPO or dj01bot.
-- popo_outbound_queue is left in place but is no longer written.

-- name: CreatePopoBridgePairing :one
INSERT INTO popo_bridge_pairing (
    workspace_id, code_hash, created_by, hostname, expires_at
) VALUES (
    $1, $2, $3, $4, $5
)
RETURNING *;

-- name: GetPopoBridgePairingByCodeHashForUpdate :one
SELECT * FROM popo_bridge_pairing
WHERE code_hash = $1
FOR UPDATE;

-- name: ConsumePopoBridgePairing :one
UPDATE popo_bridge_pairing
SET consumed_at = now(),
    bridge_id = $2
WHERE id = $1
  AND consumed_at IS NULL
  AND expires_at > now()
RETURNING *;

-- name: InsertPopoBridge :one
INSERT INTO popo_bridge (
    workspace_id, token_hash, hostname, status
) VALUES (
    $1, $2, $3, 'active'
)
RETURNING *;

-- name: GetPopoBridgeByTokenHash :one
SELECT * FROM popo_bridge
WHERE token_hash = $1;

-- name: GetPopoBridgeInWorkspace :one
SELECT * FROM popo_bridge
WHERE id = $1 AND workspace_id = $2;

-- name: ListPopoBridgesByWorkspace :many
SELECT * FROM popo_bridge
WHERE workspace_id = $1
ORDER BY created_at ASC;

-- name: RevokePopoBridge :one
UPDATE popo_bridge
SET status = 'revoked',
    revoked_at = now(),
    revoked_by = $3
WHERE id = $1
  AND workspace_id = $2
  AND status = 'active'
RETURNING *;

-- name: HeartbeatPopoBridge :one
UPDATE popo_bridge
SET last_heartbeat_at = now(),
    robots_json = $2
WHERE id = $1
  AND status = 'active'
RETURNING *;

-- name: EnqueuePopoBridgeCommand :one
INSERT INTO popo_bridge_command (
    workspace_id, bridge_id, installation_id, type, delivery_id, payload, status
) VALUES (
    $1, $2, $3, $4, $5, $6, 'pending'
)
RETURNING *;

-- name: ReclaimExpiredPopoBridgeCommandLeases :exec
UPDATE popo_bridge_command
SET status = 'pending',
    lease_expires_at = NULL,
    updated_at = now()
WHERE bridge_id = $1
  AND status = 'leased'
  AND lease_expires_at IS NOT NULL
  AND lease_expires_at < now();

-- name: LeasePopoBridgeCommands :many
WITH picked AS (
    SELECT cmd.id
    FROM popo_bridge_command AS cmd
    WHERE cmd.bridge_id = sqlc.arg('lease_bridge_id')
      AND cmd.status = 'pending'
    ORDER BY cmd.created_at ASC
    LIMIT sqlc.arg('max_n')
    FOR UPDATE SKIP LOCKED
)
UPDATE popo_bridge_command AS c
SET status = 'leased',
    lease_expires_at = sqlc.arg('lease_expires_at'),
    updated_at = now()
FROM picked
WHERE c.id = picked.id
RETURNING c.*;

-- name: GetPopoBridgeCommandForBridge :one
SELECT * FROM popo_bridge_command
WHERE id = $1 AND bridge_id = $2;

-- name: SetPopoBridgeCommandReceipt :one
UPDATE popo_bridge_command
SET status = $3,
    remote_message_id = $4,
    last_error = $5,
    lease_expires_at = NULL,
    updated_at = now()
WHERE id = $1 AND bridge_id = $2
RETURNING *;

-- name: InsertPopoInboundEvent :one
INSERT INTO popo_inbound_event (
    workspace_id, bridge_id, installation_id, event_id, robot_id, accepted, duplicate
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
RETURNING *;

-- name: GetPopoInboundEvent :one
SELECT * FROM popo_inbound_event
WHERE installation_id = $1 AND event_id = $2;

-- name: InsertPopoMediaStaging :one
INSERT INTO popo_media_staging (
    id, workspace_id, bridge_id, installation_id, robot_id,
    event_id, media_index, filename, mime_type, size_bytes, kind, status, expires_at
) VALUES (
    COALESCE(sqlc.narg('id')::uuid, gen_random_uuid()),
    sqlc.arg('workspace_id'),
    sqlc.arg('bridge_id'),
    sqlc.narg('installation_id'),
    sqlc.arg('robot_id'),
    sqlc.arg('event_id'),
    sqlc.arg('media_index'),
    sqlc.arg('filename'),
    sqlc.arg('mime_type'),
    sqlc.arg('size_bytes'),
    sqlc.arg('kind'),
    'pending',
    sqlc.arg('expires_at')
)
RETURNING *;

-- name: GetPopoMediaStagingForBridge :one
SELECT * FROM popo_media_staging
WHERE id = $1 AND bridge_id = $2;

-- name: GetPopoMediaStagingByEventIndex :one
SELECT * FROM popo_media_staging
WHERE bridge_id = $1 AND event_id = $2 AND media_index = $3;

-- name: ListPopoMediaStagingByBridgeEvent :many
SELECT * FROM popo_media_staging
WHERE bridge_id = $1 AND event_id = $2
ORDER BY media_index ASC;

-- name: ResetPopoMediaStaging :one
UPDATE popo_media_staging
SET installation_id = sqlc.narg('installation_id'),
    robot_id = sqlc.arg('robot_id'),
    filename = sqlc.arg('filename'),
    mime_type = sqlc.arg('mime_type'),
    size_bytes = sqlc.arg('size_bytes'),
    kind = sqlc.arg('kind'),
    status = 'pending',
    storage_key = NULL,
    storage_url = NULL,
    error = NULL,
    uploaded_at = NULL,
    expires_at = sqlc.arg('expires_at')
WHERE id = sqlc.arg('id')
  AND bridge_id = sqlc.arg('bridge_id')
RETURNING *;

-- name: MarkPopoMediaStagingUploaded :one
UPDATE popo_media_staging
SET status = 'uploaded',
    storage_key = $3,
    storage_url = $4,
    size_bytes = $5,
    error = NULL,
    uploaded_at = now()
WHERE id = $1
  AND bridge_id = $2
  AND status = 'pending'
  AND expires_at > now()
RETURNING *;

-- name: MarkPopoMediaStagingFailed :one
UPDATE popo_media_staging
SET status = 'failed',
    error = $3
WHERE id = $1
  AND bridge_id = $2
  AND status = 'pending'
RETURNING *;

-- name: InsertPopoOutboundMediaGrant :one
INSERT INTO popo_outbound_media_grant (
    id, workspace_id, bridge_id, installation_id, command_id, attachment_id, expires_at
) VALUES (
    COALESCE(sqlc.narg('id')::uuid, gen_random_uuid()),
    sqlc.arg('workspace_id'),
    sqlc.arg('bridge_id'),
    sqlc.arg('installation_id'),
    sqlc.arg('command_id'),
    sqlc.arg('attachment_id'),
    sqlc.arg('expires_at')
)
RETURNING *;

-- name: GetPopoOutboundMediaGrant :one
SELECT * FROM popo_outbound_media_grant
WHERE bridge_id = $1
  AND attachment_id = $2
  AND expires_at > now()
ORDER BY created_at DESC
LIMIT 1;
