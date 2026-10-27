-- Requirement questions live in issue_wakeup (see wakeup.sql). The queries here
-- cover only the run and comment reads that recovery needs; the requirement_wakeup
-- logical ledger is no longer read by application code.

-- name: ListRequirementWakeupRecoveryCandidates :many
-- A triggered question wakeup whose run ended without rearming is recovered by
-- the next tick, provided the issue is still blocked and no run is active.
SELECT w.* FROM issue_wakeup w
JOIN requirement r ON r.id = w.requirement_id AND r.workspace_id = w.workspace_id
JOIN issue i ON i.id = w.issue_id AND i.workspace_id = w.workspace_id AND i.id = r.issue_id
WHERE w.requirement_id IS NOT NULL
  AND w.parent_comment_id IS NOT NULL
  AND w.disabled_at IS NULL
  AND NOT w.enabled
  AND r.cancelled_at IS NULL
  AND i.status = 'blocked'
  AND (sqlc.narg('workspace_ids')::uuid[] IS NULL OR w.workspace_id = ANY(sqlc.narg('workspace_ids')::uuid[]))
  AND NOT EXISTS (
      SELECT 1 FROM agent_task_queue t
      WHERE t.issue_id = i.id AND t.status IN ('queued', 'deferred', 'dispatched', 'running', 'waiting_local_directory')
  )
ORDER BY w.updated_at, w.id
LIMIT @row_limit;

-- name: HasActiveRequirementRun :one
SELECT count(*) > 0 AS has_active FROM agent_task_queue
WHERE issue_id = @issue_id
  AND status IN ('queued', 'deferred', 'dispatched', 'running', 'waiting_local_directory');

-- name: ListRequirementTargetComments :many
SELECT c.* FROM comment c
JOIN comment question ON question.id = @question_comment_id
WHERE c.workspace_id = @workspace_id
  AND c.issue_id = @issue_id
  AND c.author_type = 'member'
  AND c.author_id = @target_user_id
  AND c.deleted_at IS NULL
  AND (c.created_at, c.id) > (question.created_at, question.id)
ORDER BY c.created_at, c.id;

-- name: ListRequirementCommentCoverage :many
SELECT id, trigger_comment_id, coalesced_comment_ids, delivered_comment_ids
FROM agent_task_queue
WHERE issue_id = @issue_id
  AND requirement_id = @requirement_id
  AND context->>'requirement_question_comment_id' = @question_comment_id::text
ORDER BY created_at, id;
