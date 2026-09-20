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
ON CONFLICT (delivery_id) DO NOTHING
RETURNING *;

-- name: GetPopoBridgeCommandByDeliveryID :one
SELECT * FROM popo_bridge_command WHERE delivery_id = $1;

-- name: CancelRevokedPopoBridgeCommands :exec
UPDATE popo_bridge_command AS cmd
SET status = 'cancelled', lease_expires_at = NULL, updated_at = now()
WHERE cmd.bridge_id = $1 AND cmd.status IN ('pending', 'leased')
  AND (NOT EXISTS (SELECT 1 FROM popo_bridge b WHERE b.id = cmd.bridge_id AND b.status = 'active')
    OR (cmd.type = 'send' AND NOT EXISTS (
      SELECT 1 FROM channel_installation ci
      WHERE ci.id = cmd.installation_id AND ci.status = 'active'
        AND ci.channel_type = 'popo' AND ci.workspace_id = cmd.workspace_id
        AND ci.config->>'bridge_id' = cmd.bridge_id::text)));

-- name: CancelPopoInstallationCommands :exec
UPDATE popo_bridge_command SET status = 'cancelled', lease_expires_at = NULL, updated_at = now()
WHERE installation_id = $1 AND status IN ('pending', 'leased');

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
      AND EXISTS (SELECT 1 FROM popo_bridge b WHERE b.id = cmd.bridge_id AND b.status = 'active')
      AND (cmd.type <> 'send' OR EXISTS (
        SELECT 1 FROM channel_installation ci
        WHERE ci.id = cmd.installation_id AND ci.status = 'active'
          AND ci.workspace_id = cmd.workspace_id AND ci.channel_type = 'popo'
          AND ci.config->>'bridge_id' = cmd.bridge_id::text))
    ORDER BY cmd.created_at ASC
    LIMIT sqlc.arg('max_n')
    FOR UPDATE SKIP LOCKED
)
UPDATE popo_bridge_command AS c
SET status = 'leased',
    lease_expires_at = CASE WHEN c.type = 'register_qr'
      THEN sqlc.arg('registration_lease_expires_at')::timestamptz
      ELSE sqlc.arg('lease_expires_at')::timestamptz END,
    updated_at = now()
FROM picked
WHERE c.id = picked.id
RETURNING c.*;

-- name: GetPopoBridgeCommandForBridge :one
SELECT * FROM popo_bridge_command
WHERE id = $1 AND bridge_id = $2;

-- name: LockPopoBridgeCommandForReceipt :one
SELECT * FROM popo_bridge_command WHERE id = $1 AND bridge_id = $2 FOR UPDATE;

-- name: CanDeliverPopoBridgeCommand :one
WITH renewed AS (
  UPDATE popo_bridge_command cmd SET status = 'leased',
    lease_expires_at = now() + interval '60 seconds', updated_at = now()
  WHERE cmd.id = $1 AND cmd.bridge_id = $2 AND cmd.status IN ('pending', 'leased')
    AND cmd.type = 'send'
    AND EXISTS (SELECT 1 FROM popo_bridge b WHERE b.id = cmd.bridge_id AND b.status = 'active')
    AND EXISTS (SELECT 1 FROM channel_installation ci
      WHERE ci.id = cmd.installation_id AND ci.status = 'active'
        AND ci.channel_type = 'popo' AND ci.workspace_id = cmd.workspace_id
        AND ci.config->>'bridge_id' = cmd.bridge_id::text)
  RETURNING cmd.id
)
SELECT EXISTS (SELECT 1 FROM renewed) AS allowed;

-- name: SetPopoBridgeCommandReceipt :one
UPDATE popo_bridge_command
SET status = $3,
    remote_message_id = $4,
    last_error = $5,
    lease_expires_at = NULL,
    updated_at = now()
WHERE id = $1 AND bridge_id = $2
RETURNING *;

-- name: CancelPopoBridgeOpenCommands :execrows
UPDATE popo_bridge_command
SET status = 'cancelled',
    lease_expires_at = NULL,
    updated_at = now()
WHERE bridge_id = $1
  AND workspace_id = $2
  AND status IN ('pending', 'leased');

-- name: CountPopoBridgeCommandStatuses :many
SELECT bridge_id, status, count(*)::bigint AS n
FROM popo_bridge_command
WHERE workspace_id = $1
  AND (
      (type = 'send' AND status IN ('pending', 'leased'))
      OR status = 'unknown'
  )
GROUP BY bridge_id, status;

-- name: CountPendingPopoMediaStagingByBridge :many
-- Inbound backlog is pending media uploads. Engine Handle is synchronous on
-- the inbound HTTP request, and popo_inbound_event has no processed column.
SELECT bridge_id, count(*)::bigint AS n
FROM popo_media_staging
WHERE workspace_id = $1
  AND status = 'pending'
  AND expires_at > now()
GROUP BY bridge_id;

-- name: PopoBoundAgentRuntimeOnline :one
-- True when a live POPO install is bound to an unarchived agent whose
-- runtime row is currently online. Reuses agent_runtime.status, the same
-- presence signal the sweeper maintains.
SELECT EXISTS (
    SELECT 1
    FROM channel_installation ci
    JOIN agent a ON a.id = ci.agent_id
    JOIN agent_runtime r ON r.id = a.runtime_id
    WHERE ci.workspace_id = $1
      AND ci.channel_type = 'popo'
      AND ci.status = 'active'
      AND a.archived_at IS NULL
      AND r.status = 'online'
) AS online;

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
SELECT g.* FROM popo_outbound_media_grant g
JOIN channel_installation ci ON ci.id = g.installation_id AND ci.status = 'active'
JOIN popo_bridge_command cmd ON cmd.id = g.command_id AND cmd.status IN ('pending', 'leased')
WHERE g.bridge_id = $1
  AND g.attachment_id = $2
  AND g.expires_at > now()
ORDER BY g.created_at DESC
LIMIT 1;

-- name: InsertPopoRegistration :one
INSERT INTO popo_registration (
    workspace_id, agent_id, initiator_id, bridge_id, status, expires_at
) VALUES (
    $1, $2, $3, $4, 'pending', $5
)
RETURNING *;

-- name: GetPopoRegistration :one
SELECT * FROM popo_registration
WHERE id = $1;

-- name: GetPopoRegistrationForUpdate :one
SELECT * FROM popo_registration
WHERE id = $1
FOR UPDATE;

-- name: GetPopoRegistrationInWorkspace :one
SELECT * FROM popo_registration
WHERE id = $1 AND workspace_id = $2;

-- name: ExpirePopoRegistrationIfStale :one
UPDATE popo_registration
SET status = 'expired',
    updated_at = now()
WHERE id = $1
  AND status IN ('pending', 'awaiting_scan')
  AND expires_at <= $2
RETURNING *;

-- name: UpdatePopoRegistration :one
UPDATE popo_registration
SET status = sqlc.arg('status'),
    qr_url = sqlc.arg('qr_url'),
    robot_id = sqlc.arg('robot_id'),
    robot_name = sqlc.arg('robot_name'),
    installation_id = sqlc.narg('installation_id'),
    error_reason = sqlc.arg('error_reason'),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND status IN ('pending', 'awaiting_scan')
RETURNING *;

-- name: GetLatestPopoIssueStatus :one
SELECT COALESCE(payload->>'issue_status', '')::text AS issue_status
FROM popo_bridge_command
WHERE installation_id = $1 AND payload->>'issue_id' = $2::text
  AND payload->>'outbound_kind' IN ('issue_status', 'issue_created')
ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: ListPopoRecoveryCandidates :many
-- The business rows survive a crash before event-bus publication. A stable
-- delivery id fences both the event fast path and this bounded recovery pass.
WITH sources AS (
    SELECT d.installation_id, 'task_reply:' || t.id::text AS source_key,
           'chat_done'::text AS kind, t.id AS entity_id, m.created_at AS occurred_at
    FROM channel_task_delivery d
    JOIN agent_task_queue t ON t.id = d.task_id
    JOIN chat_message m ON m.task_id = t.id AND m.role = 'assistant'
    WHERE d.channel_type = 'popo' AND t.status = 'completed'
      AND (btrim(m.content) <> '' OR EXISTS (SELECT 1 FROM attachment a WHERE a.chat_message_id = m.id))
    UNION ALL
    SELECT s.installation_id, 'issue_created:' || s.issue_id::text,
           'issue_created', s.issue_id, s.created_at
    FROM channel_issue_source s JOIN issue i ON i.id = s.issue_id
    WHERE s.channel_type = 'popo'
    UNION ALL
    SELECT s.installation_id, 'issue_comment:' || c.id::text,
           'issue_comment', c.id, c.created_at
    FROM channel_issue_source s JOIN comment c ON c.issue_id = s.issue_id
    JOIN issue i ON i.id = s.issue_id
    WHERE s.channel_type = 'popo' AND c.created_at >= s.created_at AND c.deleted_at IS NULL
      AND (btrim(c.content) <> '' OR EXISTS (SELECT 1 FROM attachment a WHERE a.comment_id = c.id))
      AND NOT EXISTS (SELECT 1 FROM channel_inbound_write w WHERE w.comment_id = c.id AND w.channel_type = 'popo')
    UNION ALL
    SELECT COALESCE(d.installation_id, s.installation_id),
           CASE WHEN t.status IN ('failed', 'cancelled') THEN 'task_' || t.status || ':' || t.id::text
             ELSE 'task_card:task:' || t.id::text || ':attempt:' || t.attempt::text || ':' ||
               CASE WHEN t.status = 'completed' THEN '9007199254740991'
                 ELSE GREATEST(1, COALESCE((SELECT max(m.seq)::bigint + 2 FROM task_message m WHERE m.task_id = t.id), 1))::text END
           END,
           CASE WHEN t.status = 'running' THEN 'task_progress' ELSE 'task_' || t.status END,
           t.id, COALESCE(t.completed_at, t.created_at)
    FROM agent_task_queue t
    LEFT JOIN channel_task_delivery d ON d.task_id = t.id
    LEFT JOIN channel_issue_source s ON s.issue_id = t.issue_id AND t.created_at >= s.created_at
    WHERE COALESCE(d.channel_type, s.channel_type) = 'popo'
      AND (t.status IN ('running', 'failed', 'cancelled') OR (t.status = 'completed' AND t.chat_session_id IS NULL))
    UNION ALL
    SELECT s.installation_id, 'issue_status:' || i.id::text || ':' || i.revision::text,
           'issue_status', i.id, i.updated_at
    FROM channel_issue_source s JOIN issue i ON i.id = s.issue_id
    WHERE s.channel_type = 'popo' AND i.status IS DISTINCT FROM COALESCE((
      SELECT cmd.payload->>'issue_status' FROM popo_bridge_command cmd
      WHERE cmd.installation_id = s.installation_id AND cmd.payload->>'issue_id' = i.id::text
        AND cmd.payload->>'outbound_kind' IN ('issue_status', 'issue_created')
      ORDER BY cmd.created_at DESC, cmd.id DESC LIMIT 1
    ), 'todo')
)
SELECT sources.installation_id, sources.source_key::text AS source_key,
       sources.kind::text AS kind, sources.entity_id, sources.occurred_at
FROM sources
JOIN channel_installation ci ON ci.id = sources.installation_id
  AND ci.channel_type = 'popo' AND ci.status = 'active'
JOIN popo_bridge b ON b.id::text = ci.config->>'bridge_id' AND b.status = 'active'
WHERE NOT EXISTS (
  SELECT 1 FROM popo_bridge_command cmd
  WHERE cmd.delivery_id = md5(sources.installation_id::text || ':' || sources.source_key)::uuid
)
ORDER BY sources.occurred_at, sources.source_key LIMIT $1;
