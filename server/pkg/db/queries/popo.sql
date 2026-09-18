-- POPO outbound queue: Multica never calls dj01bot. The Windows CLI polls
-- these rows and POSTs them to the local gateway /outbound.

-- name: EnqueuePopoOutbound :one
INSERT INTO popo_outbound_queue (
    workspace_id, installation_id, chat_id, robot_id, content
) VALUES (
    $1, $2, $3, $4, $5
)
RETURNING *;

-- name: ListPendingPopoOutbound :many
SELECT * FROM popo_outbound_queue
WHERE workspace_id = $1 AND delivered_at IS NULL
ORDER BY created_at ASC
LIMIT $2;

-- name: AckPopoOutbound :execrows
UPDATE popo_outbound_queue
SET delivered_at = now(),
    last_error = NULL
WHERE id = $1
  AND workspace_id = $2
  AND delivered_at IS NULL;

-- name: FailPopoOutbound :execrows
UPDATE popo_outbound_queue
SET last_error = $3
WHERE id = $1
  AND workspace_id = $2
  AND delivered_at IS NULL;
