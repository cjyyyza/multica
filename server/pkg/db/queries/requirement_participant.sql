-- name: CreateRequirementParticipant :one
INSERT INTO requirement_participant (workspace_id, requirement_id, user_id, role)
VALUES (@workspace_id, @requirement_id, @user_id, @role)
ON CONFLICT (requirement_id, user_id, role) DO NOTHING
RETURNING *;

-- name: ListRequirementParticipants :many
SELECT * FROM requirement_participant
WHERE workspace_id = @workspace_id AND requirement_id = @requirement_id
ORDER BY user_id, role;

-- name: ListRequirementParticipantRoles :many
SELECT * FROM requirement_participant
WHERE workspace_id = @workspace_id
  AND requirement_id = @requirement_id
  AND user_id = @user_id
ORDER BY role;

-- name: DeleteRequirementParticipants :exec
-- The caller must hold the requirement lock and restore a valid full list.
DELETE FROM requirement_participant
WHERE workspace_id = @workspace_id
  AND requirement_id = @requirement_id
  AND role <> 'requester';

-- name: ListRequirementAgentIDsForUser :many
SELECT DISTINCT r.agent_id
FROM requirement r
JOIN requirement_participant p
  ON p.requirement_id = r.id AND p.workspace_id = r.workspace_id
WHERE r.workspace_id = @workspace_id
  AND p.user_id = @user_id;
