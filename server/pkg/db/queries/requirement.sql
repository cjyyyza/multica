-- name: CreateRequirement :one
INSERT INTO requirement (
    workspace_id, source_host, external_id, source_url, source_snapshot,
    source_digest, source_lock_version, requester_id, agent_id, project_id,
    intake_operation_id
) VALUES (
    @workspace_id, @source_host, @external_id, @source_url, @source_snapshot::jsonb,
    @source_digest, sqlc.narg('source_lock_version'), @requester_id, @agent_id,
    sqlc.narg('project_id'), sqlc.narg('intake_operation_id')
)
ON CONFLICT (workspace_id, source_host, external_id) DO NOTHING
RETURNING *;

-- name: GetRequirement :one
SELECT * FROM requirement
WHERE id = @id AND workspace_id = @workspace_id;

-- name: LockRequirement :one
SELECT * FROM requirement
WHERE id = @id AND workspace_id = @workspace_id
FOR UPDATE;

-- name: ListRequirementsForParticipant :many
SELECT r.id, r.workspace_id, r.revision, r.intake_state, r.source_host, r.external_id,
       COALESCE(r.source_snapshot->>'title', '')::text AS title,
       COALESCE(r.source_snapshot->>'kind', '')::text AS source_kind,
       COALESCE(r.source_snapshot->>'squadId', '')::text AS squad_id,
       r.requester_id, r.agent_id, r.project_id, r.chat_session_id, r.issue_id,
       r.cancelled_at, r.created_at, r.updated_at,
       COALESCE(i.status, '')::text AS issue_status,
       COALESCE(s.category, '')::text AS issue_status_category,
       (r.issue_id IS NOT NULL AND i.id IS NULL)::boolean AS issue_deleted
FROM requirement r
LEFT JOIN issue i ON i.id = r.issue_id AND i.workspace_id = r.workspace_id
LEFT JOIN issue_status s ON s.workspace_id = i.workspace_id AND s.key = i.status
WHERE r.workspace_id = @workspace_id
  AND EXISTS (
      SELECT 1 FROM requirement_participant p
      WHERE p.requirement_id = r.id AND p.workspace_id = r.workspace_id AND p.user_id = @user_id
  )
  AND (sqlc.narg('external_ref')::text IS NULL OR (r.source_host = 'multica' AND r.external_id = sqlc.narg('external_ref')::text))
ORDER BY r.created_at DESC, r.id DESC
LIMIT @page_limit OFFSET @page_offset;

-- name: GetRequirementBySource :one
SELECT * FROM requirement
WHERE workspace_id = @workspace_id
  AND source_host = @source_host
  AND external_id = @external_id;

-- name: GetRequirementByChatSession :one
SELECT * FROM requirement
WHERE workspace_id = @workspace_id AND chat_session_id = @chat_session_id;

-- name: LockRequirementByChatSession :one
SELECT * FROM requirement
WHERE workspace_id = @workspace_id AND chat_session_id = @chat_session_id
FOR UPDATE;

-- name: ListRequirementIDsByChatSessions :many
-- Batch lookup for the chat session list/detail projections. Every requirement
-- row carries a chat session once opened; the unique (chat_session_id) index
-- makes this the authoritative chat-session -> requirement mapping.
SELECT chat_session_id, id FROM requirement
WHERE workspace_id = @workspace_id
  AND chat_session_id = ANY(@chat_session_ids::uuid[])
  AND chat_session_id IS NOT NULL;

-- name: GetRequirementByIssue :one
SELECT * FROM requirement
WHERE workspace_id = @workspace_id AND issue_id = @issue_id;

-- name: UpdateRequirementIntakeState :one
UPDATE requirement
SET intake_state = @intake_state,
    revision = revision + 1,
    last_error = sqlc.narg('last_error'),
    updated_at = now()
WHERE id = @id
  AND workspace_id = @workspace_id
  AND revision = @expected_revision
  AND issue_id IS NULL
  AND cancelled_at IS NULL
RETURNING *;

-- name: BindRequirementChatSession :one
UPDATE requirement
SET chat_session_id = @chat_session_id,
    intake_state = 'clarifying',
    revision = revision + 1,
    last_error = NULL,
    updated_at = now()
WHERE id = @id
  AND workspace_id = @workspace_id
  AND revision = @expected_revision
  AND chat_session_id IS NULL
  AND issue_id IS NULL
  AND cancelled_at IS NULL
RETURNING *;

-- name: BindRequirementIssue :one
UPDATE requirement
SET issue_id = @issue_id,
    revision = revision + 1,
    last_error = NULL,
    updated_at = now()
WHERE id = @id
  AND workspace_id = @workspace_id
  AND revision = @expected_revision
  AND chat_session_id = @chat_session_id
  AND issue_id IS NULL
  AND cancelled_at IS NULL
RETURNING *;

-- name: BindRequirementDelegationIssue :one
UPDATE requirement
SET issue_id = @issue_id,
    revision = revision + 1,
    last_error = NULL,
    updated_at = now()
WHERE id = @id
  AND workspace_id = @workspace_id
  AND revision = @expected_revision
  AND source_host = 'multica'
  AND issue_id IS NULL
  AND cancelled_at IS NULL
RETURNING *;

-- name: CancelRequirement :one
UPDATE requirement
SET cancelled_at = now(),
    cancel_reason = sqlc.narg('cancel_reason'),
    revision = revision + 1,
    updated_at = now()
WHERE id = @id
  AND workspace_id = @workspace_id
  AND revision = @expected_revision
  AND cancelled_at IS NULL
RETURNING *;

-- name: AdvanceRequirementRevision :one
UPDATE requirement
SET revision = revision + 1, updated_at = now()
WHERE id = @id
  AND workspace_id = @workspace_id
  AND revision = @expected_revision
  AND cancelled_at IS NULL
RETURNING *;

-- name: LockIssueForRequirement :one
SELECT * FROM issue
WHERE id = @id AND workspace_id = @workspace_id
FOR UPDATE;

-- name: GetRequirementInitialChatTask :one
SELECT * FROM agent_task_queue
WHERE requirement_id = @requirement_id
  AND chat_session_id = @chat_session_id
  AND mode = 'plan'
ORDER BY created_at, id
LIMIT 1;

-- name: GetLatestRequirementPlanTask :one
SELECT * FROM agent_task_queue
WHERE requirement_id = @requirement_id AND chat_session_id = @chat_session_id AND mode = 'plan'
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: GetLatestRequirementTaskForIssue :one
SELECT * FROM agent_task_queue
WHERE requirement_id = @requirement_id
  AND issue_id = @issue_id
  AND mode = 'execute'
ORDER BY created_at DESC, id DESC
LIMIT 1;
