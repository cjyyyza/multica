-- name: LockYixiezuoImportSource :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg('source_key')::text, 0));

-- name: GetYixiezuoImportBySource :one
SELECT * FROM yixiezuo_import WHERE workspace_id = $1 AND source_host = $2 AND external_id = $3;

-- name: GetYixiezuoImportByIssue :one
SELECT * FROM yixiezuo_import WHERE workspace_id = $1 AND issue_id = $2;

-- name: DeleteYixiezuoImportForMissingIssue :exec
DELETE FROM yixiezuo_import link
WHERE link.workspace_id = $1 AND link.issue_id = $2
  AND NOT EXISTS (SELECT 1 FROM issue i WHERE i.workspace_id = link.workspace_id AND i.id = link.issue_id);

-- name: ListYixiezuoImportStates :many
SELECT link.issue_id, link.external_id, link.source_url,
       COALESCE(link.snapshot->>'status', '')::text AS source_status,
       CASE WHEN latest.state IN ('pending', 'running', 'failed', 'conflict', 'unknown') THEN latest.state
            WHEN link.published_revision = 0 THEN 'imported'
            WHEN link.published_revision = i.revision THEN 'published'
            ELSE 'needs_review' END::text AS state
FROM yixiezuo_import link
JOIN issue i ON i.id = link.issue_id AND i.workspace_id = link.workspace_id
LEFT JOIN LATERAL (
    SELECT op.state FROM yixiezuo_operation op
    WHERE op.workspace_id = link.workspace_id AND op.issue_id = link.issue_id AND op.kind <> 'review'
    ORDER BY op.created_at DESC, op.id DESC LIMIT 1
) latest ON true
WHERE link.workspace_id = $1;

-- name: CreateYixiezuoImport :one
INSERT INTO yixiezuo_import (workspace_id, issue_id, source_host, external_id, source_url, snapshot, imported_by)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING *;

-- name: UpdateYixiezuoImportSnapshot :exec
UPDATE yixiezuo_import SET snapshot = $3 WHERE workspace_id = $1 AND issue_id = $2;

-- name: RecordYixiezuoPublication :exec
UPDATE yixiezuo_import SET snapshot = $3, published_revision = $4, published_at = now()
WHERE workspace_id = $1 AND issue_id = $2;

-- name: CreateYixiezuoOperation :one
INSERT INTO yixiezuo_operation (
    workspace_id, requested_by, kind, issue_id, payload, request_key,
    requirement_id, target_operation_id, request_hash, error
)
VALUES (
    $1, $2, $3, sqlc.narg('issue_id'), $4, sqlc.narg('request_key'),
    sqlc.narg('requirement_id'), sqlc.narg('target_operation_id'),
    sqlc.narg('request_hash'), COALESCE(sqlc.narg('error'), '')
)
ON CONFLICT (workspace_id, requested_by, request_key) WHERE request_key IS NOT NULL
DO UPDATE SET request_key = EXCLUDED.request_key RETURNING *;

-- name: CreateYixiezuoReview :one
INSERT INTO yixiezuo_operation (workspace_id, requested_by, kind, issue_id, payload, result, state, request_key, requirement_id)
VALUES ($1, $2, 'review', $3, $4, $5, 'succeeded', $6, sqlc.narg('requirement_id'))
ON CONFLICT (workspace_id, requested_by, request_key) WHERE request_key IS NOT NULL
DO UPDATE SET request_key = EXCLUDED.request_key RETURNING *;

-- name: GetYixiezuoOperationByRequestKey :one
SELECT * FROM yixiezuo_operation WHERE workspace_id = $1 AND requested_by = $2 AND request_key = $3;

-- name: GetYixiezuoOperation :one
SELECT * FROM yixiezuo_operation WHERE workspace_id = $1 AND requested_by = $2 AND id = $3;

-- name: GetYixiezuoOperationInWorkspace :one
SELECT * FROM yixiezuo_operation WHERE workspace_id = $1 AND id = $2;

-- name: LockYixiezuoOperation :one
SELECT * FROM yixiezuo_operation WHERE workspace_id = $1 AND id = $2 FOR UPDATE;

-- name: ReconcileYixiezuoOperationState :one
UPDATE yixiezuo_operation
SET state = sqlc.arg('state'),
    result = sqlc.arg('result')::jsonb,
    error = sqlc.arg('error'),
    completed_at = COALESCE(completed_at, now()),
    lease_token = NULL
WHERE workspace_id = sqlc.arg('workspace_id')
  AND id = sqlc.arg('target_operation_id')
  AND (
      state = sqlc.arg('state')
      OR (
          state IN ('pending', 'running', 'unknown', 'conflict', 'needs_reconcile')
          AND sqlc.arg('state') IN ('succeeded', 'failed', 'needs_reconcile')
      )
      OR (state = 'failed' AND sqlc.arg('state') = 'succeeded')
  )
RETURNING *;

-- name: LatestYixiezuoOperation :one
SELECT * FROM yixiezuo_operation WHERE workspace_id = $1 AND issue_id = $2 AND kind <> 'review'
ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: ExpireYixiezuoOperations :many
UPDATE yixiezuo_operation SET
    state = CASE WHEN kind IN ('publish', 'draft') AND state = 'running' THEN 'unknown' ELSE 'failed' END,
    error = CASE WHEN kind = 'draft' AND state = 'running' THEN 'Draft creation is unconfirmed. Do not create it again. Inspect the source for [Multica draft ' || id::text || '] and reconcile its URL.' WHEN state = 'running' THEN 'The local bridge stopped before reporting a result. Refresh the source before retrying.' ELSE 'The local bridge did not pick up this request. Start multica yixiezuo bridge, then retry.' END,
    completed_at = now()
WHERE workspace_id = $1 AND
    ((state = 'pending' AND created_at < now() - interval '5 minutes') OR
     (state = 'running' AND started_at < now() - interval '3 minutes'))
RETURNING *;

-- name: ClaimYixiezuoOperation :one
WITH next AS (
    SELECT candidate.id FROM yixiezuo_operation candidate
    WHERE candidate.workspace_id = $1 AND candidate.requested_by = $2 AND candidate.state = 'pending'
      AND (
          candidate.requirement_id IS NULL
          OR EXISTS (
              SELECT 1 FROM requirement bound
              JOIN member current_member
                ON current_member.workspace_id = bound.workspace_id
               AND current_member.user_id = $2
              JOIN requirement_participant approver
                ON approver.workspace_id = bound.workspace_id
               AND approver.requirement_id = bound.id
               AND approver.user_id = $2
               AND approver.role = 'approver'
              WHERE bound.id = candidate.requirement_id
                AND bound.workspace_id = candidate.workspace_id
                AND bound.issue_id = candidate.issue_id
          )
      )
      AND (
          sqlc.arg('allow_requirement_write')::boolean
          OR (candidate.kind <> 'draft' AND (candidate.kind <> 'publish' OR candidate.requirement_id IS NULL))
      )
    ORDER BY candidate.created_at, candidate.id FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE yixiezuo_operation op SET state = 'running', lease_token = gen_random_uuid(), started_at = now()
FROM next WHERE op.id = next.id RETURNING op.*;

-- name: ReleaseYixiezuoOperation :exec
UPDATE yixiezuo_operation
SET state = 'pending', lease_token = NULL, started_at = NULL,
    error = 'pmmcp_write is disabled; requirement publication remains pending'
WHERE workspace_id = $1 AND requested_by = $2 AND id = $3 AND state = 'running';

-- name: CompleteYixiezuoOperation :one
UPDATE yixiezuo_operation SET state = $5, result = $6, error = $7, completed_at = now(),
    payload = CASE WHEN kind = 'draft' AND $5 = 'succeeded'
        THEN jsonb_set(payload, '{source}', $6::jsonb->'source') ELSE payload END
WHERE workspace_id = $1 AND requested_by = $2 AND id = $3 AND lease_token = $4
  AND state = 'running' AND started_at >= now() - interval '3 minutes'
RETURNING *;

-- name: LatestYixiezuoDraftByChannel :one
SELECT * FROM yixiezuo_operation
WHERE workspace_id = @workspace_id AND requested_by = @requested_by AND kind = 'draft'
  AND payload->'channel'->>'installation_id' = @installation_id::text
  AND payload->'channel'->>'chat_id' = @chat_id::text
ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: ListUnsettledYixiezuoDrafts :many
SELECT * FROM yixiezuo_operation
WHERE workspace_id = @workspace_id AND requested_by = @requested_by
  AND kind = 'draft' AND state = 'succeeded' AND requirement_id IS NULL
ORDER BY created_at, id LIMIT 20;

-- name: BindYixiezuoDraftRequirement :one
UPDATE yixiezuo_operation SET requirement_id = @requirement_id::uuid
WHERE workspace_id = @workspace_id AND requested_by = @requested_by AND id = @operation_id
  AND kind = 'draft' AND state = 'succeeded'
  AND (requirement_id IS NULL OR requirement_id = @requirement_id::uuid)
RETURNING *;

-- name: ResolveYixiezuoDraft :one
UPDATE yixiezuo_operation SET state = 'succeeded', result = @snapshot::jsonb,
    payload = jsonb_set(payload, '{source}', @snapshot::jsonb->'source'),
    error = '', completed_at = now(), lease_token = NULL
WHERE workspace_id = @workspace_id AND requested_by = @requested_by AND id = @operation_id
  AND kind = 'draft' AND state IN ('unknown', 'needs_reconcile')
RETURNING *;

-- name: ListYixiezuoOperationsByIssue :many
SELECT * FROM yixiezuo_operation
WHERE workspace_id = @workspace_id AND issue_id = @issue_id
ORDER BY created_at, id;

-- name: FencePendingYixiezuoOperation :exec
UPDATE yixiezuo_operation
SET state = 'failed',
    error = 'Requirement was cancelled before the operation started',
    completed_at = now(),
    lease_token = NULL
WHERE workspace_id = @workspace_id AND id = @operation_id AND state = 'pending';

-- name: FencePendingYixiezuoOperationsForIssue :exec
UPDATE yixiezuo_operation
SET state = 'failed',
    error = 'Requirement was cancelled before writeback started',
    completed_at = now(),
    lease_token = NULL
WHERE workspace_id = @workspace_id AND issue_id = @issue_id AND state = 'pending';
