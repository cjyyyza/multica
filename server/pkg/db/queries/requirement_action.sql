-- name: BeginRequirementAction :one
-- The insert, business writes and CompleteRequirementAction belong to one caller transaction.
INSERT INTO requirement_action (
    workspace_id, requirement_id, actor_type, actor_id, action,
    idempotency_key, request_hash
) VALUES (
    @workspace_id, sqlc.narg('requirement_id'), @actor_type, @actor_id, @action,
    @idempotency_key, @request_hash
)
ON CONFLICT (workspace_id, actor_type, actor_id, action, idempotency_key) DO NOTHING
RETURNING *;

-- name: GetRequirementAction :one
SELECT * FROM requirement_action
WHERE workspace_id = @workspace_id
  AND actor_type = @actor_type
  AND actor_id = @actor_id
  AND action = @action
  AND idempotency_key = @idempotency_key;

-- name: LockRequirementAction :one
SELECT * FROM requirement_action
WHERE workspace_id = @workspace_id
  AND actor_type = @actor_type
  AND actor_id = @actor_id
  AND action = @action
  AND idempotency_key = @idempotency_key
FOR UPDATE;

-- name: CompleteRequirementAction :one
UPDATE requirement_action
SET requirement_id = COALESCE(requirement_id, sqlc.narg('requirement_id')),
    response_status = @response_status,
    response_body = @response_body::jsonb
WHERE id = @id
  AND workspace_id = @workspace_id
  AND request_hash = @request_hash
  AND response_status IS NULL
RETURNING *;
