-- name: CreateRequirementApproval :one
INSERT INTO requirement_approval (
    workspace_id, requirement_id, candidate_id, candidate_digest,
    approved_by, issue_id, initial_task_id
) VALUES (
    @workspace_id, @requirement_id, @candidate_id, @candidate_digest,
    @approved_by, @issue_id, @initial_task_id
)
RETURNING *;

-- name: GetRequirementApproval :one
SELECT * FROM requirement_approval
WHERE workspace_id = @workspace_id AND requirement_id = @requirement_id;
