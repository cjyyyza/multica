-- name: CreateRequirementCandidate :one
INSERT INTO requirement_candidate (
    workspace_id, requirement_id, revision, snapshot, digest, created_by_task_id
) VALUES (
    @workspace_id, @requirement_id, @revision, @snapshot::jsonb, @digest, @created_by_task_id
)
ON CONFLICT (requirement_id, revision) DO NOTHING
RETURNING *;

-- name: GetLatestRequirementCandidate :one
SELECT * FROM requirement_candidate
WHERE workspace_id = @workspace_id AND requirement_id = @requirement_id
ORDER BY revision DESC
LIMIT 1;

-- name: LockLatestRequirementCandidate :one
SELECT * FROM requirement_candidate
WHERE workspace_id = @workspace_id AND requirement_id = @requirement_id
ORDER BY revision DESC
LIMIT 1
FOR UPDATE;

