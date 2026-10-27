-- name: AppendRequirementEvent :one
INSERT INTO requirement_event (
    workspace_id, requirement_id, actor_type, actor_id, action,
    from_state, to_state, entity_refs, request_id
) VALUES (
    @workspace_id, @requirement_id, @actor_type, sqlc.narg('actor_id'), @action,
    sqlc.narg('from_state'), sqlc.narg('to_state'), @entity_refs::jsonb,
    sqlc.narg('request_id')
)
RETURNING *;

-- name: GetRequirementEvent :one
SELECT * FROM requirement_event
WHERE id = @id AND workspace_id = @workspace_id;

-- name: CountRequirementParticipantRevisionEvents :one
SELECT count(DISTINCT entity_refs->>'revision')::bigint
FROM requirement_event
WHERE workspace_id = @workspace_id
  AND requirement_id = @requirement_id
  AND action = 'participants_updated'
  AND (entity_refs->>'revision')::bigint > @after_revision::bigint
  AND (entity_refs->>'revision')::bigint <= @current_revision::bigint;
